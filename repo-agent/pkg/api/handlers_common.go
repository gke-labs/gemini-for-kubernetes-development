// Copyright 2026 The Kubernetes Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// you may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

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
		var stack []string
		i := 0
		for i < len(res) {
			top := ""
			if len(stack) > 0 {
				top = stack[len(stack)-1]
			}

			if top == "```" {
				if strings.HasPrefix(res[i:], "```") {
					stack = stack[:len(stack)-1]
					i += 3
				} else {
					i++
				}
			} else if top == "~~~" {
				if strings.HasPrefix(res[i:], "~~~") {
					stack = stack[:len(stack)-1]
					i += 3
				} else {
					i++
				}
			} else if top != "" && top[0] == '`' {
				// Inline code. Must match exact number of backticks.
				count := 0
				for i+count < len(res) && res[i+count] == '`' {
					count++
				}
				if count > 0 {
					if count == len(top) {
						stack = stack[:len(stack)-1]
					}
					i += count
				} else {
					i++
				}
			} else {
				// Outside any block.
				if strings.HasPrefix(res[i:], "```") {
					stack = append(stack, "```")
					i += 3
				} else if strings.HasPrefix(res[i:], "~~~") {
					stack = append(stack, "~~~")
					i += 3
				} else if res[i] == '`' {
					count := 0
					for i+count < len(res) && res[i+count] == '`' {
						count++
					}
					stack = append(stack, strings.Repeat("`", count))
					i += count
				} else {
					i++
				}
			}
		}

		for j := len(stack) - 1; j >= 0; j-- {
			tag := stack[j]
			if tag == "```" || tag == "~~~" {
				needsClosing += "\n" + tag
			} else {
				needsClosing += tag
			}
		}

		if needsClosing == "" || len(res)+len(needsClosing) <= limit {
			res += needsClosing
			break
		}

		newLimit := limit - len(needsClosing)
		if newLimit <= 0 {
			// If we can't fit the closing tags, just use the original truncation
			// and let the next check handle the fallback if it's still messy.
			res = truncateToRuneBoundary(s, limit)
			break
		}
		res = truncateToRuneBoundary(res, newLimit)
	}

	// Final safety check: if we still have an effectively empty or messy result, use the fallback.
	if res == "" || res == "\n```" || res == "\n~~~" || res == "```\n" || res == "~~~\n" || res == "```" || res == "~~~" {
		if len(truncatedFallback) <= limit {
			return truncatedFallback
		}
		return truncateToRuneBoundary(truncatedFallback, limit)
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

	for i := limit; i >= 0; i-- {
		if utf8.RuneStart(s[i]) {
			return s[:i]
		}
	}
	return ""
}

func (s *Server) getTaskMetadata(ctx context.Context, namespace, sandboxName, taskName, taskUID string) (string, string) {
	if sandboxName == "" || sandboxName == "n/a" {
		if taskName == "" {
			taskName = "n/a"
		}
		if taskUID == "" {
			taskUID = "n/a"
		}
		return taskName, taskUID
	}

	if (taskName == "" || taskName == "n/a") && (taskUID == "" || taskUID == "n/a") {
		return s.getLatestTaskMetadata(ctx, namespace, sandboxName)
	}

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
		return "n/a", taskUID
	}

	return taskName, taskUID
}

// applyTraceabilityMetadata appends a structured metadata footer to a body string
// if traceability is enabled and the footer is not already present.
func (s *Server) applyTraceabilityMetadata(c *gin.Context, body string, taskType string, sandboxName string, taskNameReq string, taskUIDReq string) string {
	return s.applyTraceabilityMetadataWithSandbox(c, body, taskType, sandboxName, taskNameReq, taskUIDReq, nil)
}

func (s *Server) applyTraceabilityMetadataWithSandbox(c *gin.Context, body string, taskType string, sandboxName string, taskNameReq string, taskUIDReq string, sb *unstructured.Unstructured) string {
	const githubLimit = 65536
	const safetyMargin = 536
	const limit = githubLimit - safetyMargin

	body = strings.TrimRight(body, " \t\n\r")

	if footerStart := strings.LastIndex(body, "<!-- repo-agent-metadata"); footerStart != -1 {
		footerEnd := strings.Index(body[footerStart:], "-->")
		contentBefore := body[:footerStart]

		contentBefore = strings.TrimRight(contentBefore, " \t\n\r")
		if strings.HasSuffix(contentBefore, "---") {
			contentBefore = strings.TrimSuffix(contentBefore, "---")
			contentBefore = strings.TrimRight(contentBefore, " \t\n\r")
		}

		if footerEnd != -1 {
			body = strings.TrimRight(contentBefore+body[footerStart+footerEnd+3:], " \t\n\r")
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
	repowatchName := s.getRepoWatchNameWithSandbox(ctx, namespace, sandboxName, sb)
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
	return s.getRepoWatchNameWithSandbox(ctx, namespace, sandboxName, nil)
}

func (s *Server) getRepoWatchNameWithSandbox(ctx context.Context, namespace, sandboxName string, sb *unstructured.Unstructured) string {
	if sandboxName == "" || sandboxName == "n/a" {
		return "n/a"
	}
	if sb == nil {
		var err error
		sb, err = s.K8sManager.GetSandbox(ctx, namespace, sandboxName)
		if err != nil {
			klog.V(4).Infof("failed to get sandbox %s/%s to retrieve repowatch label: %v", namespace, sandboxName, err)
			return "n/a"
		}
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
