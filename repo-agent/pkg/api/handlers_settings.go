package api

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/k8s"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/klog/v2"
)

func (s *Server) getSettings(c *gin.Context) {
	namespace := s.Auth.GetNamespaceFromContext(c)
	settings := gin.H{
		"manual_pat_set":        false,
		"oauth_pat_set":         false,
		"gemini_api_key_set":    false,
		"anthropic_api_key_set": false,
		"github_pat_set":        false, // Legacy field for UI compatibility
	}

	if sec, err := s.K8sManager.Clientset.CoreV1().Secrets(namespace).Get(c.Request.Context(), k8s.GithubSecretName, v1.GetOptions{}); err == nil {
		if _, ok := sec.Data[k8s.ManualPATKey]; ok {
			settings["manual_pat_set"] = true
			settings["github_pat_set"] = true
		}
		if _, ok := sec.Data[k8s.OAuthPATKey]; ok {
			settings["oauth_pat_set"] = true
			if !settings["manual_pat_set"].(bool) {
				settings["github_pat_set"] = true
			}
		}
		// Fallback for legacy 'pat' key if neither of the new ones are set
		if !settings["manual_pat_set"].(bool) && !settings["oauth_pat_set"].(bool) {
			if _, ok := sec.Data["pat"]; ok {
				settings["github_pat_set"] = true
			}
		}
	}
	if sec, err := s.K8sManager.Clientset.CoreV1().Secrets(namespace).Get(c.Request.Context(), k8s.GeminiSecretName, v1.GetOptions{}); err == nil {
		if val, ok := sec.Data["gemini"]; ok && len(val) > 0 {
			settings["gemini_api_key_set"] = true
		}
	}
	if sec, err := s.K8sManager.Clientset.CoreV1().Secrets(namespace).Get(c.Request.Context(), k8s.ClaudeSecretName, v1.GetOptions{}); err == nil {
		if val, ok := sec.Data["claude"]; ok && len(val) > 0 {
			settings["anthropic_api_key_set"] = true
		}
	}
	c.JSON(http.StatusOK, settings)
}

func (s *Server) updateSettings(c *gin.Context) {
	namespace := s.Auth.GetNamespaceFromContext(c)
	var payload struct {
		GithubPAT       *string `json:"github_pat"`       // Use pointer to distinguish between empty string and missing field
		GeminiAPIKey    *string `json:"gemini_api_key"`    // Use pointer to distinguish between empty string and missing field
		AnthropicAPIKey *string `json:"anthropic_api_key"` // Use pointer to distinguish between empty string and missing field
	}
	if err := c.ShouldBindJSON(&payload); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if payload.GithubPAT != nil {
		patValue := strings.TrimSpace(*payload.GithubPAT)
		if patValue == "" {
			// Clear manual PAT
			data := map[string][]byte{
				k8s.ManualPATKey: nil,
			}
			err := s.K8sManager.UpdateSecret(c.Request.Context(), namespace, k8s.GithubSecretName, data, nil)
			if err != nil {
				klog.Errorf("Failed to clear GitHub PAT: %v", err)
				c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to clear GitHub PAT"})
				return
			}
		} else {
			// Set manual PAT
			data := map[string][]byte{
				k8s.ManualPATKey: []byte(patValue),
				"refresh_token":  nil,
				"expiry":         nil,
			}
			err := s.K8sManager.UpdateSecret(c.Request.Context(), namespace, k8s.GithubSecretName, data, nil)
			if err != nil {
				klog.Errorf("Failed to update GitHub PAT: %v", err)
				c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update GitHub PAT"})
				return
			}
		}
	}

	if payload.GeminiAPIKey != nil {
		geminiValue := strings.TrimSpace(*payload.GeminiAPIKey)
		var data map[string][]byte
		if geminiValue == "" {
			data = map[string][]byte{"gemini": nil}
		} else {
			data = map[string][]byte{"gemini": []byte(geminiValue)}
		}
		err := s.K8sManager.UpdateSecret(c.Request.Context(), namespace, k8s.GeminiSecretName, data, nil)
		if err != nil {
			klog.Errorf("Failed to update Gemini API Key: %v", err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update Gemini API Key"})
			return
		}
	}

	if payload.AnthropicAPIKey != nil {
		anthropicValue := strings.TrimSpace(*payload.AnthropicAPIKey)
		var data map[string][]byte
		if anthropicValue == "" {
			data = map[string][]byte{"claude": nil}
		} else {
			data = map[string][]byte{"claude": []byte(anthropicValue)}
		}
		err := s.K8sManager.UpdateSecret(c.Request.Context(), namespace, k8s.ClaudeSecretName, data, nil)
		if err != nil {
			klog.Errorf("Failed to update Anthropic API Key: %v", err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update Anthropic API Key"})
			return
		}
	}

	c.Status(http.StatusOK)
}
