package api

import (
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
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
