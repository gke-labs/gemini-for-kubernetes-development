package api

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
	sandboxtaskv1alpha1 "github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/api/sandboxtask/v1alpha1"
	pkgk8s "github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/k8s"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/models"
	"k8s.io/apimachinery/pkg/api/errors"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/klog/v2"
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
	if s == "" {
		return "[Bot-generated content]"
	}
	if len(s) <= limit {
		return s
	}

	// Reserve some space for potential closing code block
	safeLimit := limit - 10
	if safeLimit < 0 {
		safeLimit = 0
	}

	res := ""
	for i := safeLimit; i >= 0; i-- {
		if i < len(s) && utf8.RuneStart(s[i]) {
			res = s[:i]
			break
		}
	}

	// Check for open code blocks (triple backticks)
	if strings.Count(res, "```")%2 != 0 {
		res += "\n```"
	}

	if res == "" {
		return "[Bot-generated content (truncated)]"
	}
	return res
}

func (s *Server) getTaskMetadata(ctx context.Context, namespace, sandboxName, taskName, taskUID string) (string, string) {
	if taskName != "" && taskUID != "" && taskName != "n/a" && taskUID != "n/a" {
		return taskName, taskUID
	}
	// Fallback to latest
	return s.getLatestTaskMetadata(ctx, namespace, sandboxName)
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
	repowatch := sb.GetLabels()["review.gemini.google.com/repowatch"]
	if repowatch == "" {
		return "n/a"
	}
	return repowatch
}

func (s *Server) getLatestTaskMetadata(ctx context.Context, namespace, sandboxName string) (taskName, taskUID string) {
	taskName = "n/a"
	taskUID = "n/a"
	if sandboxName == "" || sandboxName == "n/a" {
		return
	}
	taskList, err := s.K8sManager.ListSandboxTasks(ctx, namespace, sandboxName)
	if err != nil || taskList == nil || len(taskList.Items) == 0 {
		return
	}
	// Find the latest task by creation timestamp
	var latestTask *sandboxtaskv1alpha1.SandboxTask
	for i := range taskList.Items {
		if latestTask == nil || taskList.Items[i].CreationTimestamp.After(latestTask.CreationTimestamp.Time) {
			latestTask = &taskList.Items[i]
		}
	}
	if latestTask != nil {
		taskName = fmt.Sprintf("%s/%s", latestTask.Namespace, latestTask.Name)
		taskUID = string(latestTask.UID)
	}
	return
}
