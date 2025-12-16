package api

import (
	"log"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/review-ui/review-api/pkg/auth"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/review-ui/review-api/pkg/k8s"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func (s *Server) getSettings(c *gin.Context) {
	namespace := c.MustGet(auth.UserKey).(string)
	settings := gin.H{"github_pat_set": false, "gemini_api_key_set": false}

	if sec, err := s.K8sManager.Clientset.CoreV1().Secrets(namespace).Get(c.Request.Context(), k8s.GithubSecretName, v1.GetOptions{}); err == nil {
		if _, ok := sec.Data["pat"]; ok {
			settings["github_pat_set"] = true
		}
	}
	if sec, err := s.K8sManager.Clientset.CoreV1().Secrets(namespace).Get(c.Request.Context(), k8s.GeminiSecretName, v1.GetOptions{}); err == nil {
		if _, ok := sec.Data["gemini"]; ok {
			settings["gemini_api_key_set"] = true
		}
	}
	c.JSON(http.StatusOK, settings)
}

func (s *Server) updateSettings(c *gin.Context) {
	namespace := c.MustGet(auth.UserKey).(string)
	var payload struct {
		GithubPAT    string `json:"github_pat"`
		GeminiAPIKey string `json:"gemini_api_key"`
	}
	if err := c.ShouldBindJSON(&payload); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if payload.GithubPAT != "" {
		err := s.K8sManager.UpdateSecret(c.Request.Context(), namespace, k8s.GithubSecretName, map[string][]byte{"pat": []byte(payload.GithubPAT)})
		if err != nil {
			log.Printf("Failed to update GitHub PAT: %v", err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update GitHub PAT"})
			return
		}
	}

	if payload.GeminiAPIKey != "" {
		err := s.K8sManager.UpdateSecret(c.Request.Context(), namespace, k8s.GeminiSecretName, map[string][]byte{"gemini": []byte(payload.GeminiAPIKey)})
		if err != nil {
			log.Printf("Failed to update Gemini API Key: %v", err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update Gemini API Key"})
			return
		}
	}

	c.Status(http.StatusOK)
}
