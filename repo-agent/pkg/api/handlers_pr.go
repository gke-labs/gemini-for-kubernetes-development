package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httputil"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/clients"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/models"
	"github.com/google/go-github/v39/github"
	yaml "go.yaml.in/yaml/v3"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/klog/v2"
)

func (s *Server) getPRs(c *gin.Context) {
	log := klog.FromContext(c.Request.Context())
	namespace := s.Auth.GetNamespaceFromContext(c)
	repo := c.Param("repo")

	prs, err := s.listPRsFromK8s(c.Request.Context(), namespace, repo)
	if err != nil {
		log.Info("Error listing PRs", "err", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to list PRs"})
		return
	}

	c.JSON(http.StatusOK, prs)
}

func (s *Server) getPRTasks(c *gin.Context) {
	namespace := s.Auth.GetNamespaceFromContext(c)
	repo := c.Param("repo")
	prID := c.Param("id")

	sandboxName := fmt.Sprintf("%s-pr-%s", repo, prID)

	taskList, err := s.K8sManager.ListSandboxTasks(c.Request.Context(), namespace, sandboxName)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to list tasks", "details": err.Error()})
		return
	}

	var tasks []models.Task
	for _, taskItem := range taskList.Items {
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

		tasks = append(tasks, models.Task{
			Name:              taskItem.GetName(),
			Type:              taskType,
			TaskState:         taskState,
			Result:            result,
			CreationTimestamp: taskItem.GetCreationTimestamp().Format(time.RFC3339),
			AgentDraft:        tAgentDraft,
			AgentDraftType:    tAgentDraftType,
			UserDraft:         tUserDraft,
			AgentState:        tAgentState,
			AgentStateMessage: tAgentStateMessage,
		})
	}
	// Sort tasks by creation timestamp (newest first)
	sort.Slice(tasks, func(i, j int) bool {
		return tasks[i].CreationTimestamp > tasks[j].CreationTimestamp
	})

	c.JSON(http.StatusOK, tasks)
}

func (s *Server) listPRsFromK8s(ctx context.Context, namespace, repo string) ([]models.PR, error) {
	log := klog.FromContext(ctx)
	gvr := schema.GroupVersionResource{
		Group:    "agents.x-k8s.io",
		Version:  "v1alpha1",
		Resource: "sandboxes",
	}
	list, err := s.K8sManager.Client.Resource(gvr).Namespace(namespace).List(context.Background(),
		v1.ListOptions{
			LabelSelector: fmt.Sprintf("review.gemini.google.com/repowatch=%s", repo),
		})
	if err != nil {
		return nil, fmt.Errorf("failed to list Sandbox CRs: %w", err)
	}

	var prs []models.PR
	for _, item := range list.Items {
		if item.GetDeletionTimestamp() != nil {
			continue
		}

		// Get replicas and if it scaled down skip
		replicas, found, err := unstructured.NestedInt64(item.Object, "spec", "replicas")
		if err != nil || !found {
			log.Info("Replicas (.spec.replicas) not found in Sandbox", "name", item.GetName())
			continue
		}

		annotations := item.GetAnnotations()
		if annotations == nil {
			continue
		}

		prID := annotations["pr"]
		title := annotations["title"]
		htmlurl := annotations["htmlURL"]
		diffurl := annotations["diffURL"]

		if prID == "" {
			log.Info("PR ID not found in Sandbox annotations", "name", item.GetName())
			continue
		}

		// get draft from annotation[agentDraft]
		draft := ""
		agentState := ""
		agentStateMessage := ""
		reviewState := ""
		var labels []string

		if val, ok := annotations["userDraft"]; ok {
			draft = val
		} else if val, ok := annotations["agentDraft"]; ok {
			draft = val
		}

		if val, ok := annotations["agentState"]; ok {
			agentState = val
		}
		if val, ok := annotations["agentStateMessage"]; ok {
			agentStateMessage = val
		}
		if val, ok := annotations["reviewState"]; ok {
			reviewState = val
		}
		if val, ok := annotations["agentLabels"]; ok {
			_ = json.Unmarshal([]byte(val), &labels)
		}

		pr := models.PR{
			ID:                prID,
			Title:             title,
			Sandbox:           item.GetName(),
			HTMLURL:           htmlurl,
			DiffURL:           diffurl,
			SandboxReplica:    fmt.Sprintf("%d", replicas),
			Draft:             draft,
			AgentDraft:        annotations["agentDraft"], // Explicitly set AgentDraft
			AgentState:        agentState,
			AgentStateMessage: agentStateMessage,
			ReviewState:       reviewState,
			Labels:            labels,
		}
		prs = append(prs, pr)
	}
	return prs, nil
}

func (s *Server) saveDraft(c *gin.Context) {
	namespace := s.Auth.GetNamespaceFromContext(c)
	repo := c.Param("repo")
	prID := c.Param("id")
	var payload struct {
		Draft string
	}
	if err := c.ShouldBindJSON(&payload); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	sandboxName := fmt.Sprintf("%s-pr-%s", repo, prID)
	err := s.K8sManager.UpdateSandboxUserDraft(c.Request.Context(), namespace, sandboxName, payload.Draft)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to save draft", "details": err.Error()})
		return
	}

	c.Status(http.StatusOK)
}

func (s *Server) saveTaskDraft(c *gin.Context) {
	namespace := s.Auth.GetNamespaceFromContext(c)
	taskName := c.Param("taskID")
	var payload struct {
		Draft string
	}
	if err := c.ShouldBindJSON(&payload); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	err := s.K8sManager.UpdateSandboxTaskUserDraft(c.Request.Context(), namespace, taskName, payload.Draft)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to save task draft", "details": err.Error()})
		return
	}

	c.Status(http.StatusOK)
}

func (s *Server) submitReview(c *gin.Context) {
	log := klog.FromContext(c.Request.Context())
	namespace := s.Auth.GetNamespaceFromContext(c)
	repo := c.Param("repo")
	prID := c.Param("id")
	var payload struct {
		Review string
	}
	if err := c.ShouldBindJSON(&payload); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	ctx := c.Request.Context()
	log.Info("Submitting review for PR", "prID", prID, "repo", repo, "review", payload.Review)

	sandboxName := fmt.Sprintf("%s-pr-%s", repo, prID)
	gvr := schema.GroupVersionResource{
		Group:    "agents.x-k8s.io",
		Version:  "v1alpha1",
		Resource: "sandboxes",
	}

	// Get Sandbox to check agentDraft
	sandbox, err := s.K8sManager.Client.Resource(gvr).Namespace(namespace).Get(ctx, sandboxName, v1.GetOptions{})
	if err != nil {
		log.Info("Failed to get sandbox", "name", sandboxName, "err", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to get sandbox"})
		return
	}

	draft := payload.Review
	agentDraft := ""
	if annotations := sandbox.GetAnnotations(); annotations != nil {
		if val, ok := annotations["agentDraft"]; ok {
			agentDraft = val
		}
	}

	// Get RepoWatch to get repoURL and secret ref
	repoWatch, err := s.K8sManager.GetRepoWatch(ctx, namespace, repo)
	if err != nil {
		log.Info("Failed to get repowatch", "repo", repo, "err", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to get repowatch config"})
		return
	}

	if draft != agentDraft {
		if sandboxName != "" {
			if err := s.K8sManager.UpdateSandboxUserDraft(ctx, namespace, sandboxName, draft); err != nil {
				log.Info("Failed to update userDraft for PR", "prID", prID, "repo", repo, "err", err)
			}
		}
	}

	// Get GitHub token from secret
	token, err := s.K8sManager.GetGitHubToken(ctx, repoWatch)
	if err != nil {
		log.Info("Failed to get github token for repo", "repo", repo, "err", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to get github token"})
		return
	}

	// Create GitHub client
	client := clients.NewGitHubClient(ctx, token)

	// Parse repo URL
	repoURL, found, err := unstructured.NestedString(repoWatch.Object, "spec", "repoURL")
	if err != nil || !found {
		log.Info("repoURL not found in RepoWatch CR", "name", repoWatch.GetName())
		c.JSON(http.StatusInternalServerError, gin.H{"error": "repoURL not found in RepoWatch CR"})
		return
	}
	owner, repoName, err := parseRepoURL(repoURL)
	if err != nil {
		log.Info("Failed to parse repo url", "url", repoURL, "err", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to parse repo url"})
		return
	}

	// Get PR number
	prNumber, err := strconv.Atoi(prID)
	if err != nil {
		log.Info("Failed to parse prID", "prID", prID, "err", err)
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid pr id"})
		return
	}

	// Try Unmarshalling the yaml review payload into PullRequestReviewRequest
	agentOutput := &models.ReviewAgentOutput{}
	reviewRequest := &github.PullRequestReviewRequest{}
	err = yaml.Unmarshal([]byte(payload.Review), &agentOutput)
	if err != nil {
		log.Info("Failed to unmarshal review payload", "err", err)
		reviewRequest.Body = github.String(payload.Review)
	} else {
		reviewRequest = agentOutput.Review.ToGitHubReviewRequest()
	}

	// Not setting event sets it as a draft
	reviewRequest.Event = nil

	log.Info("reviewRequest being created", "request", reviewRequest)
	review, resp, err := client.PullRequests.CreateReview(ctx, owner, repoName, prNumber, reviewRequest)
	if err != nil {
		log.Info("response", "resp", resp)
		log.Info("Failed to create review on PR", "prNumber", prNumber, "err", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create review on github", "details": err.Error()})
		return
	}
	log.Info("review created", "review", review)

	if err := s.K8sManager.UpdateSandboxAnnotation(ctx, namespace, sandboxName, "reviewState", "submitted"); err != nil {
		log.Info("Failed to update sandbox reviewState", "prID", prID, "repo", repo, "err", err)
	}

	// scale down sandbox
	err = s.K8sManager.ScaledownPRSandbox(ctx, namespace, repo, prID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to scaledown Sandbox after review submission", "details": err.Error()})
		return
	}

	c.Status(http.StatusOK)
}

func (s *Server) deletePR(c *gin.Context) {
	namespace := s.Auth.GetNamespaceFromContext(c)
	repo := c.Param("repo")
	prID := c.Param("id")
	ctx := c.Request.Context()

	if err := s.K8sManager.ScaledownPRSandbox(ctx, namespace, repo, prID); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to delete sandbox", "details": err.Error()})
		return
	}

	c.Status(http.StatusOK)
}

func (s *Server) scaleUpPR(c *gin.Context) {
	namespace := s.Auth.GetNamespaceFromContext(c)
	repo := c.Param("repo")
	prID := c.Param("id")
	ctx := c.Request.Context()

	var payload struct {
		Manual bool `json:"manual"`
	}
	// Ignore error as body might be empty
	_ = c.ShouldBindJSON(&payload)

	annotationValue := ""
	if payload.Manual {
		annotationValue = "true"
	}

	if err := s.K8sManager.ScaleupPRSandbox(ctx, namespace, repo, prID, annotationValue); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to scale up sandbox", "details": err.Error()})
		return
	}
	c.Status(http.StatusOK)
}

func (s *Server) scaleDownPR(c *gin.Context) {
	namespace := s.Auth.GetNamespaceFromContext(c)
	repo := c.Param("repo")
	prID := c.Param("id")
	ctx := c.Request.Context()

	if err := s.K8sManager.ScaledownPRSandbox(ctx, namespace, repo, prID); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to scale down sandbox", "details": err.Error()})
		return
	}
	c.Status(http.StatusOK)
}

func (s *Server) createPRTask(c *gin.Context) {
	namespace := s.Auth.GetNamespaceFromContext(c)
	repo := c.Param("repo")
	prID := c.Param("id")

	var payload struct {
		Prompt           string `json:"prompt"`
		ExpectedComments int    `json:"expectedComments"`
	}
	if err := c.ShouldBindJSON(&payload); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	sandboxName := fmt.Sprintf("%s-pr-%s", repo, prID)

	// Fetch RepoWatch to get latest config
	rw, err := s.K8sManager.GetRepoWatch(c.Request.Context(), namespace, repo)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to get RepoWatch", "details": err.Error()})
		return
	}

	prompt := payload.Prompt
	if prompt == "" {
		defaultPrompt, found, err := unstructured.NestedString(rw.Object, "spec", "review", "llm", "prompt")
		if err == nil && found {
			prompt = defaultPrompt
		}
	}

	params := map[string]string{
		"AGENT_PROMPT": prompt,
	}

	// Inject MaxReviewFiles from RepoWatch
	maxReviewFiles, found, err := unstructured.NestedInt64(rw.Object, "spec", "review", "maxReviewFiles")
	if err == nil && found {
		params["MAX_REVIEW_FILES"] = strconv.FormatInt(maxReviewFiles, 10)
	}

	// Inject IgnoreFiles from RepoWatch
	ignoreFiles, found, err := unstructured.NestedStringSlice(rw.Object, "spec", "review", "ignoreFiles")
	if err == nil && found && len(ignoreFiles) > 0 {
		params["IGNORE_FILES"] = strings.Join(ignoreFiles, ",")
	}

	if payload.ExpectedComments > 0 {
		params["EXPECTED_COMMENTS"] = strconv.Itoa(payload.ExpectedComments)
	}

	err = s.K8sManager.CreateSandboxTask(c.Request.Context(), namespace, sandboxName, "Sandbox", "review", params)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create task", "details": err.Error()})
		return
	}

	// Scale up the sandbox so it can process the task
	if err := s.K8sManager.ScaleupPRSandbox(c.Request.Context(), namespace, repo, prID, ""); err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Failed to scale up sandbox after task creation", "details": err.Error()})
		klog.Warningf("Failed to scale up sandbox after task creation: %v", err)
		return
	}

	c.Status(http.StatusOK)
}

func (s *Server) getPRDetails(c *gin.Context) {
	log := klog.FromContext(c.Request.Context())
	namespace := s.Auth.GetNamespaceFromContext(c)
	repo := c.Param("repo")
	prIDStr := c.Param("id")

	prID, err := strconv.Atoi(prIDStr)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid PR ID"})
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

	repoURL, found, _ := unstructured.NestedString(repoWatch.Object, "spec", "repoURL")
	if !found {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "RepoURL not found"})
		return
	}
	owner, repoName, err := parseRepoURL(repoURL)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Invalid RepoURL"})
		return
	}

	pr, _, err := client.PullRequests.Get(c.Request.Context(), owner, repoName, prID)
	if err != nil {
		log.Info("Failed to get PR details", "prID", prID, "err", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to get PR details"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"number":  pr.GetNumber(),
		"title":   pr.GetTitle(),
		"htmlURL": pr.GetHTMLURL(),
	})
}

func (s *Server) getTaskLogs(c *gin.Context) {
	log := klog.FromContext(c.Request.Context())
	namespace := s.Auth.GetNamespaceFromContext(c)
	repo := c.Param("repo")
	prID := c.Param("id")
	taskID := c.Param("taskID")

	sandboxName := fmt.Sprintf("%s-pr-%s", repo, prID)
	// Service name logic must match KRO's RGD: devc-${schema.metadata.name}-lb
	serviceName := fmt.Sprintf("devc-%s-lb", sandboxName)

	targetURL := fmt.Sprintf("http://%s.%s.svc.cluster.local:13339", serviceName, namespace)

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
		// Clear query params if any, or keep them if agentserver supports them?
		// agentserver just serves file, so query params might not matter.
	}

	// Custom error handler for proxy
	proxy.ErrorHandler = func(w http.ResponseWriter, _ *http.Request, err error) {
		log.Error(err, "Proxy error", "target", targetURL)
		// If connection refused, it might mean the pod is not ready or port not exposed yet
		http.Error(w, "Failed to connect to agent server logs (pod might be starting or scaled down)", http.StatusBadGateway)
	}

	proxy.ServeHTTP(c.Writer, c.Request)
}
