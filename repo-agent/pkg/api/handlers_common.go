package api

import (
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	sandboxtaskv1alpha1 "github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/api/sandboxtask/v1alpha1"
	pkgk8s "github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/k8s"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/models"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
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

func (s *Server) ensureLLMKeySet(c *gin.Context, namespace string) bool {
	geminiSet := false
	claudeSet := false

	if sec, err := s.K8sManager.Clientset.CoreV1().Secrets(namespace).Get(c.Request.Context(), pkgk8s.GeminiSecretName, v1.GetOptions{}); err == nil {
		if val, ok := sec.Data["gemini"]; ok && len(val) > 0 {
			geminiSet = true
		}
	}
	if sec, err := s.K8sManager.Clientset.CoreV1().Secrets(namespace).Get(c.Request.Context(), pkgk8s.ClaudeSecretName, v1.GetOptions{}); err == nil {
		if val, ok := sec.Data["claude"]; ok && len(val) > 0 {
			claudeSet = true
		}
	}

	if !geminiSet && !claudeSet {
		c.JSON(http.StatusForbidden, gin.H{"error": "Neither Gemini nor Claude API Key is configured. Please set at least one in Settings."})
		return false
	}

	return true
}
