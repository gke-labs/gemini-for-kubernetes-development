/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package api

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/clients"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/models"
	"github.com/google/go-github/v39/github"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/klog/v2"
)

// Issue-fix tasks run through the factory CLI since the factory migration:
// the repowatch controller launches `factory fix` per matched issue, and
// factory owns the sandbox, named fix-<githubRepo>-<issueNumber> with the
// factory.gemini.google.com/managed label and
// sandbox.gemini.google.com/last-task-state task annotations. Once the fix
// opens a PR, factory aliases the sandbox (pr label + htmlURL annotation).

var sandboxGVR = schema.GroupVersionResource{
	Group:    "agents.x-k8s.io",
	Version:  "v1alpha1",
	Resource: "sandboxes",
}

// issueRepoInfo resolves the GitHub owner/repo behind a RepoWatch, needed to
// derive factory fix sandbox names and issue URLs.
func (s *Server) issueRepoInfo(ctx context.Context, namespace, repo string) (string, string, error) {
	repoWatch, err := s.K8sManager.GetRepoWatch(ctx, namespace, repo)
	if err != nil {
		return "", "", fmt.Errorf("getting RepoWatch %s: %w", repo, err)
	}
	repoURL, found, err := unstructured.NestedString(repoWatch.Object, "spec", "repoURL")
	if err != nil || !found {
		return "", "", fmt.Errorf("repoURL not found in RepoWatch %s", repo)
	}
	return parseRepoURL(repoURL)
}

// resolveFactoryIssueSandbox returns the factory fix sandbox for an issue,
// plus its expected name (valid even when the sandbox does not exist yet).
func (s *Server) resolveFactoryIssueSandbox(c *gin.Context) (*unstructured.Unstructured, string, error) {
	namespace := s.Auth.GetNamespaceFromContext(c)
	repo := c.Param("repo")
	issueID := c.Param("issue_id")

	_, ghRepo, err := s.issueRepoInfo(c.Request.Context(), namespace, repo)
	if err != nil {
		return nil, "", err
	}
	name := fmt.Sprintf("fix-%s-%s", ghRepo, issueID)
	sb, err := s.K8sManager.Client.Resource(sandboxGVR).Namespace(namespace).Get(c.Request.Context(), name, v1.GetOptions{})
	if err != nil {
		return nil, name, err
	}
	return sb, name, nil
}

// issueAgentState maps factory's task state onto the UI-facing agentState.
func issueAgentState(taskState string) string {
	switch taskState {
	case "Running":
		return "fixing"
	case "Completed":
		return "fix ready"
	case "Failed":
		return "error: fix failed"
	default:
		return "provisioning"
	}
}

func (s *Server) getIssues(c *gin.Context) {
	log := klog.FromContext(c.Request.Context())
	namespace := s.Auth.GetNamespaceFromContext(c)
	repo := c.Param("repo")

	issues, err := s.listIssuesFromK8s(c.Request.Context(), namespace, repo)
	if err != nil {
		log.Info("Error listing issues", "err", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to list issues"})
		return
	}

	c.JSON(http.StatusOK, issues)
}

func (s *Server) listIssuesFromK8s(ctx context.Context, namespace, repo string) ([]models.Issue, error) {
	owner, ghRepo, err := s.issueRepoInfo(ctx, namespace, repo)
	if err != nil {
		return nil, err
	}

	list, err := s.K8sManager.Client.Resource(sandboxGVR).Namespace(namespace).List(ctx,
		v1.ListOptions{LabelSelector: labelFactoryManaged + "=true"})
	if err != nil {
		return nil, fmt.Errorf("failed to list Sandbox CRs: %w", err)
	}

	prefix := fmt.Sprintf("fix-%s-", ghRepo)
	var issues []models.Issue
	for _, item := range list.Items {
		if item.GetDeletionTimestamp() != nil {
			continue
		}
		issueID := strings.TrimPrefix(item.GetName(), prefix)
		if issueID == item.GetName() {
			continue
		}
		if _, err := strconv.Atoi(issueID); err != nil {
			continue
		}

		replicas, _, _ := unstructured.NestedInt64(item.Object, "spec", "replicas")
		annotations := item.GetAnnotations()
		if annotations == nil {
			annotations = map[string]string{}
		}

		// After factory opens a PR it aliases the sandbox: htmlURL then
		// points at the PR. The issue URL itself is derived.
		prURL := ""
		if u := annotations["htmlURL"]; strings.Contains(u, "/pull/") {
			prURL = u
		}

		issues = append(issues, models.Issue{
			ID:                issueID,
			Title:             annotations["title"],
			Sandbox:           item.GetName(),
			HTMLURL:           fmt.Sprintf("https://github.com/%s/%s/issues/%s", owner, ghRepo, issueID),
			SandboxReplica:    fmt.Sprintf("%d", replicas),
			BranchURL:         prURL,
			Draft:             annotations["userDraft"],
			AgentState:        issueAgentState(annotations[annoTaskState]),
			AgentStateMessage: annotations["agentStateMessage"],
			SandboxStatus:     annotations["sandbox.gemini.google.com/pod-status"],
		})
	}
	return issues, nil
}

// getIssueTasks synthesizes the single UI task entry for a factory-run fix
// from the sandbox annotations (no SandboxTask CRs exist for issues).
func (s *Server) getIssueTasks(c *gin.Context) {
	sb, _, err := s.resolveFactoryIssueSandbox(c)
	if err != nil {
		c.JSON(http.StatusOK, []models.Task{})
		return
	}

	annotations := sb.GetAnnotations()
	if annotations == nil {
		annotations = map[string]string{}
	}
	state := annotations[annoTaskState]
	if state == "" {
		state = "Pending"
	}
	createdAt := sb.GetCreationTimestamp().Format("2006-01-02T15:04:05Z07:00")
	if completion := annotations[annoCompletionTime]; completion != "" {
		createdAt = completion
	}

	c.JSON(http.StatusOK, []models.Task{{
		Name:              sb.GetName(),
		Type:              "fix",
		TaskState:         state,
		CreationTimestamp: createdAt,
		AgentDraft:        annotations["agentDraft"],
		UserDraft:         annotations["userDraft"],
		AgentState:        issueAgentState(state),
		AgentStateMessage: annotations["agentStateMessage"],
	}})
}

func (s *Server) saveIssueDraft(c *gin.Context) {
	namespace := s.Auth.GetNamespaceFromContext(c)
	var payload struct {
		Draft string
	}
	if err := c.ShouldBindJSON(&payload); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	_, name, err := s.resolveFactoryIssueSandbox(c)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Failed to find fix sandbox", "details": err.Error()})
		return
	}
	if err := s.K8sManager.UpdateSandboxUserDraft(c.Request.Context(), namespace, name, payload.Draft); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to save draft", "details": err.Error()})
		return
	}

	c.Status(http.StatusOK)
}

// submitIssueComment posts a comment on the GitHub issue with the tenant's
// token. Sandbox bookkeeping is best-effort: the comment works even when no
// fix sandbox exists.
func (s *Server) submitIssueComment(c *gin.Context) {
	log := klog.FromContext(c.Request.Context())
	namespace := s.Auth.GetNamespaceFromContext(c)
	repo := c.Param("repo")
	issueID := c.Param("issue_id")
	var payload struct {
		Comment string
	}
	if err := c.ShouldBindJSON(&payload); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	ctx := c.Request.Context()

	issueNumber, err := strconv.Atoi(issueID)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid issue id"})
		return
	}

	repoWatch, err := s.K8sManager.GetRepoWatch(ctx, namespace, repo)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to get repowatch config"})
		return
	}
	token, err := s.K8sManager.GetGitHubToken(ctx, repoWatch)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to get github token"})
		return
	}
	owner, ghRepo, err := s.issueRepoInfo(ctx, namespace, repo)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	client := clients.NewGitHubClient(ctx, token)
	comment := &github.IssueComment{Body: &payload.Comment}
	if _, _, err := client.Issues.CreateComment(ctx, owner, ghRepo, issueNumber, comment); err != nil {
		log.Info("Failed to create comment on Issue", "issueNumber", issueNumber, "err", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create comment on github"})
		return
	}

	if _, name, err := s.resolveFactoryIssueSandbox(c); err == nil {
		if err := s.K8sManager.UpdateSandboxAnnotation(ctx, namespace, name, "issueCommentSubmitted", "true"); err != nil {
			log.Info("Failed to update sandbox issueCommentSubmitted", "sandbox", name, "err", err)
		}
	}

	c.Status(http.StatusOK)
}

func (s *Server) deleteIssue(c *gin.Context) {
	namespace := s.Auth.GetNamespaceFromContext(c)

	_, name, err := s.resolveFactoryIssueSandbox(c)
	if err != nil {
		// No sandbox to scale down; issue exclusion is handled separately.
		c.Status(http.StatusOK)
		return
	}
	if err := s.K8sManager.ScaledownSandboxByName(c.Request.Context(), namespace, name); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to scale down sandbox", "details": err.Error()})
		return
	}
	c.Status(http.StatusOK)
}

func (s *Server) scaleUpIssue(c *gin.Context) {
	namespace := s.Auth.GetNamespaceFromContext(c)

	_, name, err := s.resolveFactoryIssueSandbox(c)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Failed to find fix sandbox", "details": err.Error()})
		return
	}
	if err := s.K8sManager.ScaleupSandboxByName(c.Request.Context(), namespace, name); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to scale up sandbox", "details": err.Error()})
		return
	}
	c.Status(http.StatusOK)
}

func (s *Server) scaleDownIssue(c *gin.Context) {
	namespace := s.Auth.GetNamespaceFromContext(c)

	_, name, err := s.resolveFactoryIssueSandbox(c)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Failed to find fix sandbox", "details": err.Error()})
		return
	}
	if err := s.K8sManager.ScaledownSandboxByName(c.Request.Context(), namespace, name); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to scale down sandbox", "details": err.Error()})
		return
	}
	c.Status(http.StatusOK)
}

// createIssueTask ("Fix Again") marks the fix sandbox for re-fix; the
// repowatch controller relaunches `factory fix` on its next reconcile.
// Per-task prompt/model overrides from the old engine are not supported.
func (s *Server) createIssueTask(c *gin.Context) {
	namespace := s.Auth.GetNamespaceFromContext(c)

	var payload struct {
		TaskType string            `json:"taskType"`
		Prompt   string            `json:"prompt"`
		Params   map[string]string `json:"params"`
	}
	_ = c.ShouldBindJSON(&payload)
	if payload.Prompt != "" {
		klog.Infof("createIssueTask: per-task prompt overrides are ignored by the factory fix engine")
	}

	_, name, err := s.resolveFactoryIssueSandbox(c)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Failed to find fix sandbox", "details": err.Error()})
		return
	}
	if err := s.K8sManager.UpdateSandboxAnnotation(c.Request.Context(), namespace, name, annoRefixRequest, nowRFC3339()); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to request re-fix", "details": err.Error()})
		return
	}
	c.Status(http.StatusOK)
}

func (s *Server) getIssueDetails(c *gin.Context) {
	log := klog.FromContext(c.Request.Context())
	namespace := s.Auth.GetNamespaceFromContext(c)
	repo := c.Param("repo")
	issueIDStr := c.Param("issue_id")

	issueID, err := strconv.Atoi(issueIDStr)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid issue ID"})
		return
	}

	repoWatch, err := s.K8sManager.GetRepoWatch(c.Request.Context(), namespace, repo)
	if err != nil {
		log.Info("Failed to get RepoWatch", "namespace", namespace, "name", repo, "err", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to get RepoWatch"})
		return
	}

	token, err := s.K8sManager.GetGitHubToken(c.Request.Context(), repoWatch)
	if err != nil {
		log.Info("Failed to get github token", "err", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to get GitHub token"})
		return
	}

	client := clients.NewGitHubClient(c.Request.Context(), token)

	owner, ghRepo, err := s.issueRepoInfo(c.Request.Context(), namespace, repo)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	issue, _, err := client.Issues.Get(c.Request.Context(), owner, ghRepo, issueID)
	if err != nil {
		log.Info("Failed to get issue details", "issueID", issueID, "err", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to get issue details"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"number":  issue.GetNumber(),
		"title":   issue.GetTitle(),
		"htmlURL": issue.GetHTMLURL(),
	})
}

func (s *Server) getIssueTaskLogs(c *gin.Context) {
	log := klog.FromContext(c.Request.Context())
	namespace := s.Auth.GetNamespaceFromContext(c)
	taskID := c.Param("taskID")

	_, name, err := s.resolveFactoryIssueSandbox(c)
	if err != nil {
		c.String(http.StatusOK, "Logs are not available: no fix sandbox for this issue yet.")
		return
	}
	targetURL := fmt.Sprintf("http://%s-lb.%s.svc.cluster.local:13339", name, namespace)

	proxyURL, err := url.Parse(targetURL)
	if err != nil {
		log.Error(err, "Failed to parse target URL", "url", targetURL)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Invalid target URL"})
		return
	}

	proxy := httputil.NewSingleHostReverseProxy(proxyURL)
	originalDirector := proxy.Director
	proxy.Director = func(req *http.Request) {
		originalDirector(req)
		req.URL.Path = fmt.Sprintf("/logs/%s", taskID)
	}
	proxy.ErrorHandler = func(w http.ResponseWriter, _ *http.Request, err error) {
		log.Error(err, "Proxy error", "target", targetURL)
		http.Error(w, "Task logs are not exposed for factory-run fixes yet; see the sandbox with 'factory sandbox logs'.", http.StatusBadGateway)
	}
	proxy.ServeHTTP(c.Writer, c.Request)
}

func (s *Server) getIssueTaskTelemetry(c *gin.Context) {
	log := klog.FromContext(c.Request.Context())
	namespace := s.Auth.GetNamespaceFromContext(c)
	taskID := c.Param("taskID")

	_, name, err := s.resolveFactoryIssueSandbox(c)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{})
		return
	}
	targetURL := fmt.Sprintf("http://%s-lb.%s.svc.cluster.local:13339", name, namespace)

	proxyURL, err := url.Parse(targetURL)
	if err != nil {
		log.Error(err, "Failed to parse target URL", "url", targetURL)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Invalid target URL"})
		return
	}

	proxy := httputil.NewSingleHostReverseProxy(proxyURL)
	originalDirector := proxy.Director
	proxy.Director = func(req *http.Request) {
		originalDirector(req)
		req.URL.Path = fmt.Sprintf("/telemetry/%s", taskID)
	}
	proxy.ErrorHandler = func(w http.ResponseWriter, _ *http.Request, err error) {
		log.Error(err, "Proxy error", "target", targetURL)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte("{}"))
	}
	proxy.ServeHTTP(c.Writer, c.Request)
}
