package api

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
	"k8s.io/apimachinery/pkg/api/errors"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/klog/v2"

	sandboxtaskv1alpha1 "github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/api/sandboxtask/v1alpha1"
	pkgk8s "github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/k8s"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/models"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/tasks"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/tasks/metadata"
)

// --- Health Check ---
func (s *Server) healthCheckOk(c *gin.Context) {
	c.String(http.StatusOK, "OK")
}

func (s *Server) proxy(c *gin.Context) {
	proxyURL := c.Query("url")
	if proxyURL == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "url query parameter is required"})
		return
	}

	// validate the URL begins with  https://github.com/ or https://raw.githubusercontent.com/
	if !strings.HasPrefix(proxyURL, "https://github.com/") && !strings.HasPrefix(proxyURL, "https://raw.githubusercontent.com/") {
		c.JSON(http.StatusBadRequest, gin.H{"error": "url must begin with https://github.com/ or https://raw.githubusercontent.com/"})
		return
	}

	resp, err := http.Get(proxyURL)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("Failed to fetch url: %v", err)})
		return
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("Failed to read response body: %v", err)})
		return
	}

	c.String(resp.StatusCode, string(body))
}

// extractConditions converts unstructured Kubernetes conditions to the API model format.
func extractConditions(obj *unstructured.Unstructured) []models.Condition {
	conditionsSlice, found, err := unstructured.NestedSlice(obj.Object, "status", "conditions")
	if err != nil || !found {
		return nil
	}
	var conditions []models.Condition
	for _, item := range conditionsSlice {
		condMap, ok := item.(map[string]interface{})
		if !ok {
			continue
		}
		var k8sCond v1.Condition
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(condMap, &k8sCond); err != nil {
			continue
		}
		conditions = append(conditions, models.Condition{
			Type:               k8sCond.Type,
			Status:             string(k8sCond.Status),
			Reason:             k8sCond.Reason,
			Message:            k8sCond.Message,
			LastTransitionTime: k8sCond.LastTransitionTime.Format("2006-01-02T15:04:05Z07:00"),
		})
	}
	return conditions
}

// convertStats converts CRD-level Stats to the API model Stats.
func convertStats(crdStats *sandboxtaskv1alpha1.Stats) *models.Stats {
	if crdStats == nil || len(crdStats.Models) == 0 {
		return nil
	}
	stats := &models.Stats{
		Models: make(map[string]models.ModelUsage, len(crdStats.Models)),
	}
	for model, data := range crdStats.Models {
		stats.Models[model] = models.ModelUsage{
			TotalRequests:  data.TotalRequests,
			TotalErrors:    data.TotalErrors,
			TotalLatencyMs: data.TotalLatencyMs,
			InputTokens:    data.InputTokens,
			OutputTokens:   data.OutputTokens,
			TotalTokens:    data.TotalTokens,
			CachedTokens:   data.CachedTokens,
			ThoughtTokens:  data.ThoughtTokens,
		}
	}
	return stats
}

func (s *Server) ensureGeminiKeySet(c *gin.Context, namespace string) bool {
	sec, err := s.K8sManager.Clientset.CoreV1().Secrets(namespace).Get(c.Request.Context(), pkgk8s.GeminiSecretName, v1.GetOptions{})
	if err != nil {
		if errors.IsNotFound(err) {
			c.JSON(http.StatusForbidden, gin.H{"error": "Gemini API Key is not configured. Please set it in Settings."})
		} else {
			klog.Infof("Error getting Gemini secret: %v", err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to check Gemini API Key configuration"})
		}
		return false
	}

	if val, ok := sec.Data["gemini"]; !ok || len(val) == 0 {
		c.JSON(http.StatusForbidden, gin.H{"error": "Gemini API Key is empty. Please set it in Settings."})
		return false
	}

	return true
}

// truncateString safely truncates a string to a byte limit without splitting UTF-8 runes.
// It also ensures that the truncated body doesn't leave an open code block and provides
// a placeholder for empty content to avoid blank notifications.
func truncateString(s string, limit int) string {
	const fallback = "[Bot-generated content]"
	const truncatedFallback = "[Bot-generated content (truncated)]"

	if limit <= 0 {
		return ""
	}

	if s == "" {
		if len(fallback) <= limit {
			return fallback
		}
		return truncateToRuneBoundary(fallback, limit)
	}

	if len(s) <= limit {
		return s
	}

	res := truncateToRuneBoundary(s, limit)

	for {
		needsClosing := ""
		// To correctly handle nested blocks, we scan the string from the beginning.
		backtickOpen := false
		tildeOpen := false

		i := 0
		for i < len(res) {
			if !tildeOpen && strings.HasPrefix(res[i:], "```") {
				backtickOpen = !backtickOpen
				i += 3
			} else if !backtickOpen && strings.HasPrefix(res[i:], "~~~") {
				tildeOpen = !tildeOpen
				i += 3
			} else {
				i++
			}
		}

		if backtickOpen {
			needsClosing += "\n```"
		}
		if tildeOpen {
			needsClosing += "\n~~~"
		}

		if needsClosing == "" || len(res)+len(needsClosing) <= limit {
			res += needsClosing
			break
		}

		// If adding closing blocks exceeds the limit, truncate further and re-evaluate.
		// We jump to the available space minus the closing block.
		newLimit := limit - len(needsClosing)
		if newLimit <= 0 {
			// If even the closing tags don't fit, try to just return the closing tags if they fit,
			// or an empty string.
			if len(needsClosing) <= limit {
				return needsClosing
			}
			return ""
		}
		res = truncateToRuneBoundary(res, newLimit)
	}

	if res == "" || res == "\n```" || res == "\n~~~" || res == "\n```\n~~~" || res == "\n~~~\n```" {
		if len(truncatedFallback) <= limit {
			return truncatedFallback
		}
		// Final safety: if even fallback doesn't fit, just return whatever we can from the original string
		// even if it breaks markdown, because we are extremely constrained.
		return truncateToRuneBoundary(s, limit)
	}
	return res
}

func truncateToRuneBoundary(s string, limit int) string {
	if limit <= 0 {
		return ""
	}
	if len(s) <= limit {
		return s
	}

	// This loop is O(1) in practice because utf8.RuneStart will match within
	// a maximum of 4 bytes for any valid UTF-8 string.
	// We start from the byte at the limit and walk backwards to find the
	// beginning of the last (possibly multi-byte) rune.
	for i := limit; i >= 0; i-- {
		if utf8.RuneStart(s[i]) {
			return s[:i]
		}
	}
	return ""
}

func (s *Server) getTaskMetadata(ctx context.Context, namespace, sandboxName, taskName, taskUID string) (string, string) {
	if sandboxName == "" || sandboxName == "n/a" {
		// Even without a sandbox name, preserve explicitly provided metadata.
		if taskName == "" {
			taskName = "n/a"
		}
		if taskUID == "" {
			taskUID = "n/a"
		}
		return taskName, taskUID
	}

	// If BOTH are missing or explicitly n/a, we'll fall back to latest.
	// If only one is missing or n/a, we try to preserve or resolve the other.
	if (taskName == "" || taskName == "n/a") && (taskUID == "" || taskUID == "n/a") {
		return s.getLatestTaskMetadata(ctx, namespace, sandboxName)
	}

	// If we have a taskName but no UID, try to find the UID in the sandbox's tasks.
	if taskName != "" && taskName != "n/a" && (taskUID == "" || taskUID == "n/a") {
		taskList, err := s.K8sManager.ListSandboxTasks(ctx, namespace, sandboxName)
		if err == nil {
			for i := range taskList.Items {
				item := &taskList.Items[i]
				fullName := fmt.Sprintf("%s/%s", item.Namespace, item.Name)
				if item.Name == taskName || fullName == taskName {
					klog.FromContext(ctx).V(4).Info("Resolved task UID from name", "taskName", fullName, "taskUID", string(item.UID))
					return fullName, string(item.UID)
				}
			}
		} else {
			klog.FromContext(ctx).Error(err, "Failed to list tasks for sandbox to resolve UID", "sandbox", sandboxName)
		}
	}

	// If we have a taskUID but no name (or name is "n/a"), try to resolve the name.
	if taskUID != "" && taskUID != "n/a" && (taskName == "" || taskName == "n/a") {
		taskList, err := s.K8sManager.ListSandboxTasks(ctx, namespace, sandboxName)
		if err == nil {
			for i := range taskList.Items {
				item := &taskList.Items[i]
				if string(item.UID) == taskUID {
					fullName := fmt.Sprintf("%s/%s", item.Namespace, item.Name)
					return fullName, taskUID
				}
			}
		} else {
			klog.FromContext(ctx).Error(err, "Failed to list tasks for sandbox to resolve name", "sandbox", sandboxName)
		}
		// If not found, just return what we have
		return "n/a", taskUID
	}

	return taskName, taskUID
}

// applyTraceabilityMetadata appends a structured metadata footer to a body string
// if traceability is enabled and the footer is not already present.
func (s *Server) applyTraceabilityMetadata(c *gin.Context, body string, taskType string, sandboxName string, taskNameReq string, taskUIDReq string) string {
	const githubLimit = 65536
	const safetyMargin = 536
	const limit = githubLimit - safetyMargin

	body = strings.TrimSpace(body)

	// Identify and remove existing footer to avoid duplication and ensure it's not truncated
	// if it was already near the limit.
	if footerStart := strings.LastIndex(body, "<!-- repo-agent-metadata"); footerStart != -1 {
		footerEnd := strings.Index(body[footerStart:], "-->")
		contentBefore := body[:footerStart]

		// Also remove the preceding rule if present to avoid stacking them
		contentBefore = strings.TrimRight(contentBefore, " \t\n\r")
		if strings.HasSuffix(contentBefore, "---") {
			contentBefore = strings.TrimSuffix(contentBefore, "---")
			contentBefore = strings.TrimRight(contentBefore, " \t\n\r")
		}

		if footerEnd != -1 {
			// Remove the footer and any trailing whitespace
			body = strings.TrimSpace(contentBefore + body[footerStart+footerEnd+3:])
		} else {
			// Malformed footer? Just cut from footerStart
			body = strings.TrimSpace(contentBefore)
		}
	}

	if !s.TraceabilityMetadataEnabled {
		klog.FromContext(c.Request.Context()).V(4).Info("Traceability metadata is disabled, skipping footer", "taskType", taskType)
		return truncateString(body, limit)
	}

	ctx := c.Request.Context()
	namespace := s.Auth.GetNamespaceFromContext(c)
	if namespace == "" {
		namespace = "default"
	}

	taskName, taskUID := s.getTaskMetadata(ctx, namespace, sandboxName, taskNameReq, taskUIDReq)
	repowatchName := s.getRepoWatchName(ctx, namespace, sandboxName)
	footer := tasks.GenerateMetadataFooter(metadata.Metadata{
		SandboxTask:    taskName,
		SandboxTaskUID: taskUID,
		Sandbox:        sandboxName,
		RepoWatch:      repowatchName,
		TaskType:       taskType,
		Timestamp:      time.Now().UTC().Format(time.RFC3339),
	})

	return truncateString(body, limit-len(footer)) + footer
}

func (s *Server) getRepoWatchName(ctx context.Context, namespace, sandboxName string) string {
	if sandboxName == "" || sandboxName == "n/a" {
		return "n/a"
	}
	sb, err := s.K8sManager.GetSandbox(ctx, namespace, sandboxName)
	if err != nil {
		klog.V(4).Infof("failed to get sandbox %s/%s to retrieve repowatch label: %v", namespace, sandboxName, err)
		return "n/a"
	}
	labels := sb.GetLabels()
	if labels == nil {
		return "n/a"
	}
	repowatch := labels["review.gemini.google.com/repowatch"]
	if repowatch == "" {
		repowatch = labels["review.gemini.google.com/repo"]
	}
	if repowatch == "" {
		return "n/a"
	}
	return repowatch
}
func (s *Server) mapSandboxTaskToModel(taskItem sandboxtaskv1alpha1.SandboxTask) models.Task {
	taskType := taskItem.Spec.Type
	taskState := taskItem.Status.TaskState
	result := taskItem.Status.Result

	tAgentDraft := ""
	tUserDraft := ""
	tAgentState := ""
	tAgentStateMessage := ""
	tAgentDraftType := ""

	tAnnotations := taskItem.GetAnnotations()
	if tAnnotations != nil {
		tAgentDraft = tAnnotations["agentDraft"]
		tAgentDraftType = tAnnotations["agentDraftType"]
		tUserDraft = tAnnotations["userDraft"]
		tAgentState = tAnnotations["agentState"]
		tAgentStateMessage = tAnnotations["agentStateMessage"]
	}

	return models.Task{
		Name:              taskItem.GetName(),
		UID:               string(taskItem.GetUID()),
		Type:              taskType,
		TaskState:         taskState,
		Result:            result,
		CreationTimestamp: taskItem.GetCreationTimestamp().Format(time.RFC3339),
		AgentDraft:        tAgentDraft,
		AgentDraftType:    tAgentDraftType,
		UserDraft:         tUserDraft,
		AgentState:        tAgentState,
		AgentStateMessage: tAgentStateMessage,
		Stats:             convertStats(taskItem.Status.Stats),
	}
}

func (s *Server) getLatestTaskMetadata(ctx context.Context, namespace, sandboxName string) (string, string) {
	if sandboxName == "" || sandboxName == "n/a" {
		return "n/a", "n/a"
	}
	taskList, err := s.K8sManager.ListSandboxTasks(ctx, namespace, sandboxName)
	if err != nil {
		klog.V(4).Infof("failed to list tasks for sandbox %s/%s: %v", namespace, sandboxName, err)
		return "n/a", "n/a"
	}
	if taskList == nil || len(taskList.Items) == 0 {
		return "n/a", "n/a"
	}

	if len(taskList.Items) > 100 {
		klog.FromContext(ctx).V(2).Info("Large number of tasks found for sandbox, search might be slow", "sandbox", sandboxName, "count", len(taskList.Items))
	}

	// Find the latest task by creation timestamp.
	// Tie-break with name for stable results.
	var latestTask *sandboxtaskv1alpha1.SandboxTask
	for i := range taskList.Items {
		item := &taskList.Items[i]
		if latestTask == nil || item.CreationTimestamp.After(latestTask.CreationTimestamp.Time) ||
			(item.CreationTimestamp.Equal(&latestTask.CreationTimestamp) && item.Name > latestTask.Name) {
			latestTask = item
		}
	}
	if latestTask != nil {
		return fmt.Sprintf("%s/%s", latestTask.Namespace, latestTask.Name), string(latestTask.UID)
	}
	return "n/a", "n/a"
}
