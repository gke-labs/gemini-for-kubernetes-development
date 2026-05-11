package api

import (
	"context"
	"fmt"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/clients"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/k8s"
	"github.com/google/go-github/v39/github"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/klog/v2"
)

func (s *Server) submitFeedback(c *gin.Context) {
	log := klog.FromContext(c.Request.Context())
	namespace := s.Auth.GetUserFromContext(c)
	if namespace == "" {
		namespace = "default"
	}

	var payload struct {
		Title string `json:"title"`
		Text  string `json:"text"`
		Image string `json:"image"` // base64
	}
	if err := c.ShouldBindJSON(&payload); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if payload.Title == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Title is required"})
		return
	}

	if payload.Text == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Text is required"})
		return
	}

	// Get Token

	token, err := s.getUserGitHubToken(c.Request.Context(), namespace)
	if err != nil {
		log.Info("Failed to get GitHub token", "user", namespace, "err", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to get GitHub token. Please login or set a PAT in settings."})
		return
	}

	client := clients.NewGitHubClient(c.Request.Context(), token)

	// Create Issue
	owner := "gke-labs"
	repo := "gemini-for-kubernetes-development"
	title := fmt.Sprintf("[repo-agent] %s", payload.Title)
	body := fmt.Sprintf("User: %s\n\n%s", namespace, payload.Text)
	labels := []string{"feedback"}

	if payload.Image != "" {
		body += "\n\n[Screenshot attached in request but ignored due to missing image host configuration]"
	}

	if s.TraceabilityEnabled {
		footer := s.getTraceabilityFooter(c.Request.Context(), body, namespace, "", "", "feedback")
		if footer != "" {
			body += footer
		}
	}

	req := &github.IssueRequest{
		Title:  &title,
		Body:   &body,
		Labels: &labels,
	}

	issue, _, err := client.Issues.Create(c.Request.Context(), owner, repo, req)
	if err != nil {
		log.Info("Failed to create issue", "err", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create issue on GitHub"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"issue_url": issue.GetHTMLURL()})
}

func (s *Server) getUserGitHubToken(ctx context.Context, namespace string) (string, error) {
	secretName := k8s.GithubSecretName
	secret, err := s.K8sManager.Clientset.CoreV1().Secrets(namespace).Get(ctx, secretName, v1.GetOptions{})
	if err != nil {
		return "", err
	}

	var token string
	if val, ok := secret.Data[k8s.ManualPATKey]; ok && len(val) > 0 {
		token = string(val)
	} else if val, ok := secret.Data[k8s.OAuthPATKey]; ok && len(val) > 0 {
		token = string(val)
	} else if val, ok := secret.Data["pat"]; ok && len(val) > 0 {
		token = string(val)
	} else {
		return "", fmt.Errorf("no token found")
	}

	return token, nil
}
