package api

import (
	"net/http"
	"os"

	"github.com/gin-gonic/gin"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/auth"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/k8s"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/quota"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func (s *Server) getQuota(c *gin.Context) {
	namespace := c.MustGet(auth.UserKey).(string)

	projectID := os.Getenv("GOOGLE_CLOUD_PROJECT")

	// Check if project ID is overridden in user settings
	if sec, err := s.K8sManager.Clientset.CoreV1().Secrets(namespace).Get(c.Request.Context(), k8s.GeminiSecretName, v1.GetOptions{}); err == nil {
		if val, ok := sec.Data["project_id"]; ok && len(val) > 0 {
			projectID = string(val)
		}
	}

	if projectID == "" {
		c.JSON(http.StatusPreconditionRequired, gin.H{"error": "Google Cloud Project ID is not configured. Please configure it in settings."})
		return
	}

	checker := quota.NewChecker(projectID)
	usage, err := checker.GetUsage(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to check quota usage", "details": err.Error()})
		return
	}

	c.JSON(http.StatusOK, usage)
}
