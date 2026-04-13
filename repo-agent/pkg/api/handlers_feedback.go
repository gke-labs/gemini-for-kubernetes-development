package api

import (
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/tasks/metadata"
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/clients"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/k8s"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/tasks"
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
		Title *string `json:"title"`
		Text  *string `json:"text"`
		Image *string `json:"image"` // base64
	}
	if err := c.ShouldBindJSON(&payload); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	titleText := ""
	if payload.Title != nil {
		titleText = *payload.Title
	}
	bodyText := ""
	if payload.Text != nil {
		bodyText = *payload.Text
	}
	imageText := ""
	if payload.Image != nil {
		imageText = *payload.Image
	}

	if titleText == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Title is required"})
		return
	}

	if bodyText == "" {
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
	title := fmt.Sprintf("[repo-agent] %s", titleText)
	body := fmt.Sprintf("User: %s\n\n%s", namespace, bodyText)
	if s.TraceabilityMetadataEnabled {
		if !strings.Contains(body, "<!-- repo-agent-metadata") {
			footer := tasks.GenerateMetadataFooter(metadata.Metadata{
				SandboxTask:    "n/a",
				SandboxTaskUID: "n/a",
				Sandbox:        "n/a",
				RepoWatch:      "n/a",
				TaskType:       metadata.TaskTypeFeedback,
				Timestamp:      time.Now().UTC().Format(time.RFC3339),
			})
			// GitHub limit is 65536. Leave some room.
			body = truncateString(strings.TrimSpace(body), 65000-len(footer))
			body += footer
		}
	} else {
		log.V(4).Info("Traceability metadata is disabled, skipping footer")
	}
	labels := []string{"feedback"}

	if imageText != "" {
		body += "\n\n[Screenshot attached in request but ignored due to missing image host configuration]"
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
