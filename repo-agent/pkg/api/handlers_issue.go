package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httputil"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/clients"
	pkg_github "github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/github"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/models"
	"github.com/google/go-github/v39/github"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/klog/v2"
)

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

func (s *Server) getIssueTasks(c *gin.Context) {
	namespace := s.Auth.GetNamespaceFromContext(c)
	repo := c.Param("repo")
	issueID := c.Param("issue_id")

	sandboxName := fmt.Sprintf("%s-issue-%s", repo, issueID)

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
			Stats:             convertStats(taskItem.Status.Stats),
		})
	}
	// Sort tasks by creation timestamp (newest first)
	sort.Slice(tasks, func(i, j int) bool {
		return tasks[i].CreationTimestamp > tasks[j].CreationTimestamp
	})

	c.JSON(http.StatusOK, tasks)
}

func (s *Server) listIssuesFromK8s(ctx context.Context, namespace, repo string) ([]models.Issue, error) {
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

	var issues []models.Issue
	for _, item := range list.Items {
		// Filter out dev sandboxes
		labels := item.GetLabels()
		if labels != nil && labels["sandbox.gemini.google.com/type"] == "dev" {
			continue
		}

		replicas, found, err := unstructured.NestedInt64(item.Object, "spec", "replicas")
		if err != nil || !found {
			log.Info("Replicas (.spec.replicas) not found in Sandbox", "name", item.GetName())
			continue
		}

		annotations := item.GetAnnotations()
		if annotations == nil {
			annotations = make(map[string]string)
		}

		issueID := annotations["sandbox.gemini.google.com/issue-id"]
		title := annotations["sandbox.gemini.google.com/issue-title"]
		htmlurl := annotations["sandbox.gemini.google.com/html-url"]
		cloneURL := annotations["sandbox.gemini.google.com/clone-url"]
		login := annotations["sandbox.gemini.google.com/user-login"]
		branch := annotations["sandbox.gemini.google.com/branch"]
		pushBranchStr := annotations["sandbox.gemini.google.com/push-enabled"]
		pushBranch := false
		if pushBranchStr == "true" {
			pushBranch = true
		}

		if issueID == "" {
			// Fallback or skip? It might be an old sandbox or failed creation?
			// Try to parse from name if possible?
			// Sandbox name: repo-issue-123.
			// RepoWatch name: repo.
			// Issue ID is suffix.
			continue
		}

		if cloneURL == "" {
			cloneURL = "https://github.com/noorg/norepo.git"
		}

		repoParts := strings.Split(strings.TrimSuffix(cloneURL, ".git"), "/")
		repoName := repoParts[len(repoParts)-1]

		branchURL := fmt.Sprintf("https://github.com/%s/%s/tree/%s", login, repoName, branch)

		// get draft from annotation[agentDraft]
		draft := ""
		agentState := ""
		agentStateMessage := ""
		sandboxStatus := ""
		var agentLabels []string
		comment := ""

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
		if val, ok := annotations["sandbox.gemini.google.com/pod-status"]; ok {
			sandboxStatus = val
		}
		if val, ok := annotations["agentLabels"]; ok {
			_ = json.Unmarshal([]byte(val), &agentLabels)
		}
		if val, ok := annotations["issueCommentSubmitted"]; ok && val == "true" {
			comment = draft
		}

		name := item.GetName()

		issue := models.Issue{
			ID:                issueID,
			Title:             title,
			Sandbox:           name,
			HTMLURL:           htmlurl,
			SandboxReplica:    fmt.Sprintf("%d", replicas),
			BranchURL:         branchURL,
			Comment:           comment,
			Draft:             draft,
			AgentDraft:        annotations["agentDraft"],
			PushBranch:        pushBranch,
			AgentState:        agentState,
			AgentStateMessage: agentStateMessage,
			SandboxStatus:     sandboxStatus,
			Labels:            agentLabels,
		}
		issues = append(issues, issue)
	}
	return issues, nil
}

func (s *Server) saveIssueDraft(c *gin.Context) {
	namespace := s.Auth.GetNamespaceFromContext(c)
	repo := c.Param("repo")
	issueID := c.Param("issue_id")
	var payload struct {
		Draft string
	}
	if err := c.ShouldBindJSON(&payload); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	sandboxName := fmt.Sprintf("%s-issue-%s", repo, issueID)
	err := s.K8sManager.UpdateSandboxUserDraft(c.Request.Context(), namespace, sandboxName, payload.Draft)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to save draft", "details": err.Error()})
		return
	}

	c.Status(http.StatusOK)
}

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
	log.Info("Submitting comment for Issue", "issueID", issueID, "repo", repo, "comment", payload.Comment)

	sandboxName := fmt.Sprintf("%s-issue-%s", repo, issueID)
	gvr := schema.GroupVersionResource{
		Group:    "agents.x-k8s.io",
		Version:  "v1alpha1",
		Resource: "sandboxes",
	}

	// Get IssueSandbox to check agentDraft
	sandbox, err := s.K8sManager.Client.Resource(gvr).Namespace(namespace).Get(ctx, sandboxName, v1.GetOptions{})
	if err != nil {
		log.Info("Failed to get issuesandbox", "name", sandboxName, "err", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to get issuesandbox"})
		return
	}

	draft := payload.Comment
	agentDraft := ""
	if annotations := sandbox.GetAnnotations(); annotations != nil {
		if val, ok := annotations["agentDraft"]; ok {
			agentDraft = val
		}
	}

	repoWatch, err := s.K8sManager.GetRepoWatch(ctx, namespace, repo)
	if err != nil {
		log.Info("Failed to get repowatch", "repo", repo, "err", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to get repowatch config"})
		return
	}

	if draft != agentDraft {
		if sandboxName != "" {
			if err := s.K8sManager.UpdateSandboxUserDraft(ctx, namespace, sandboxName, draft); err != nil {
				log.Info("Failed to update sandbox userDraft", "issueID", issueID, "repo", repo, "err", err)
			}
		}
	}

	token, err := s.K8sManager.GetGitHubToken(ctx, repoWatch)
	if err != nil {
		log.Info("Failed to get github token for repo", "repo", repo, "err", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to get github token"})
		return
	}

	client := clients.NewGitHubClient(ctx, token)

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

	issueNumber, err := strconv.Atoi(issueID)
	if err != nil {
		log.Info("Failed to parse issueID", "issueID", issueID, "err", err)
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid issue id"})
		return
	}

	comment := &github.IssueComment{Body: &payload.Comment}
	_, _, err = client.Issues.CreateComment(ctx, owner, repoName, issueNumber, comment)
	if err != nil {
		log.Info("Failed to create comment on Issue", "issueNumber", issueNumber, "err", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create comment on github"})
		return
	}

	if err := s.K8sManager.UpdateSandboxAnnotation(ctx, namespace, sandboxName, "issueCommentSubmitted", "true"); err != nil {
		log.Info("Failed to update sandbox issueCommentSubmitted", "issueID", issueID, "repo", repo, "err", err)
	}

	err = s.K8sManager.ScaledownIssueSandbox(ctx, namespace, repo, issueID, "")
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to scaledown Sandbox after comment submission", "details": err.Error()})
		return
	}

	c.Status(http.StatusOK)
}

func (s *Server) deleteIssue(c *gin.Context) {
	namespace := s.Auth.GetNamespaceFromContext(c)
	repo := c.Param("repo")
	issueID := c.Param("issue_id")
	ctx := c.Request.Context()

	if err := s.K8sManager.ScaledownIssueSandbox(ctx, namespace, repo, issueID, ""); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to delete sandbox", "details": err.Error()})
		return
	}

	c.Status(http.StatusOK)
}

func (s *Server) scaleUpIssue(c *gin.Context) {
	namespace := s.Auth.GetNamespaceFromContext(c)
	repo := c.Param("repo")
	issueID := c.Param("issue_id")
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

	if err := s.K8sManager.ScaleupIssueSandbox(ctx, namespace, repo, issueID, "", annotationValue); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to scale up issue sandbox", "details": err.Error()})
		return
	}
	c.Status(http.StatusOK)
}

func (s *Server) scaleDownIssue(c *gin.Context) {
	namespace := s.Auth.GetNamespaceFromContext(c)
	repo := c.Param("repo")
	issueID := c.Param("issue_id")
	ctx := c.Request.Context()

	if err := s.K8sManager.ScaledownIssueSandbox(ctx, namespace, repo, issueID, ""); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to scale down issue sandbox", "details": err.Error()})
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

	issue, _, err := client.Issues.Get(c.Request.Context(), owner, repoName, issueID)
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
	repo := c.Param("repo")
	issueID := c.Param("issue_id")
	taskID := c.Param("taskID")

	sandboxName := fmt.Sprintf("%s-issue-%s", repo, issueID)
	serviceName := fmt.Sprintf("%s-lb", sandboxName)

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
	}

	// Custom error handler for proxy
	proxy.ErrorHandler = func(w http.ResponseWriter, _ *http.Request, err error) {
		log.Error(err, "Proxy error", "target", targetURL)
		// If connection refused, it might mean the pod is not ready or port not exposed yet
		http.Error(w, "Failed to connect to agent server logs (pod might be starting or scaled down)", http.StatusBadGateway)
	}

	proxy.ServeHTTP(c.Writer, c.Request)
}

func (s *Server) createIssueTask(c *gin.Context) {
	namespace := s.Auth.GetNamespaceFromContext(c)
	repo := c.Param("repo")
	issueID := c.Param("issue_id")

	var payload struct {
		Prompt   string            `json:"prompt"`
		TaskType string            `json:"taskType"`
		Params   map[string]string `json:"params"`
	}
	if err := c.ShouldBindJSON(&payload); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	sandboxName := fmt.Sprintf("%s-issue-%s", repo, issueID)

	// Fetch RepoWatch to get latest config
	rw, err := s.K8sManager.GetRepoWatch(c.Request.Context(), namespace, repo)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to get RepoWatch", "details": err.Error()})
		return
	}

	taskType := payload.TaskType
	if taskType == "" {
		taskType = "triage-issue"
	}

	params := map[string]string{}
	if payload.Prompt != "" {
		params["AGENT_PROMPT"] = payload.Prompt
	}
	for k, v := range payload.Params {
		params[k] = v
	}

	// Inject Models from RepoWatch if not already specified
	if params["model"] == "" {
		models, found, err := unstructured.NestedStringSlice(rw.Object, "spec", "issue", "models")
		if err == nil && found && len(models) > 0 {
			params["model"] = strings.Join(models, ",")
		}
	}

	params["PR_LABEL"] = "repo-agent"

	err = s.K8sManager.CreateSandboxTask(c.Request.Context(), namespace, sandboxName, "Sandbox", taskType, params)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create task", "details": err.Error()})
		return
	}

	// Scale up the sandbox so it can process the task
	if err := s.K8sManager.ScaleupIssueSandbox(c.Request.Context(), namespace, repo, issueID, "", ""); err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Failed to scale up sandbox after task creation", "details": err.Error()})
		klog.Warningf("Failed to scale up issue sandbox after task creation: %v", err)
		return
	}

	c.Status(http.StatusOK)
}

func (s *Server) getIssueCommits(c *gin.Context) {
	log := klog.FromContext(c.Request.Context())
	namespace := s.Auth.GetNamespaceFromContext(c)
	repo := c.Param("repo")
	issueID := c.Param("issue_id")

	repoWatch, err := s.K8sManager.GetRepoWatch(c.Request.Context(), namespace, repo)
	if err != nil {
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

	// Get the authenticated user (bot or user) associated with the token
	authUser, _, err := client.Users.Get(c.Request.Context(), "")
	if err != nil {
		log.Info("Failed to get authenticated user", "err", err)
	}
	var authUserLogin string
	if authUser != nil {
		authUserLogin = authUser.GetLogin()
	}

	sandboxName := fmt.Sprintf("%s-issue-%s", repo, issueID)
	gvr := schema.GroupVersionResource{
		Group:    "agents.x-k8s.io",
		Version:  "v1alpha1",
		Resource: "sandboxes",
	}
	sandbox, err := s.K8sManager.Client.Resource(gvr).Namespace(namespace).Get(c.Request.Context(), sandboxName, v1.GetOptions{})
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Issue sandbox not found"})
		return
	}

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

	// Try to find a linked PR
	var linkedPR *github.PullRequest
	prURLRegex := regexp.MustCompile(`https://github\.com/[\w-]+/[\w-]+/pull/\d+`)

	// 1. Check sandbox annotations for agentDraft
	if agentDraft, _, _ := unstructured.NestedString(sandbox.Object, "metadata", "annotations", "agentDraft"); agentDraft != "" {
		matches := prURLRegex.FindAllString(agentDraft, -1)
		for _, match := range matches {
			if prRef, err := pkg_github.ParsePullRequestURL(match); err == nil {
				if p, _, err := client.PullRequests.Get(c.Request.Context(), prRef.Repo.Owner, prRef.Repo.Name, prRef.PullRequestNumber); err == nil {
					linkedPR = p
					break
				}
			}
		}
	}

	// 2. Check SandboxTasks if not found in sandbox annotation
	if linkedPR == nil {
		if taskList, err := s.K8sManager.ListSandboxTasks(c.Request.Context(), namespace, sandboxName); err == nil {
			for _, taskItem := range taskList.Items {
				if tAgentDraft := taskItem.GetAnnotations()["agentDraft"]; tAgentDraft != "" {
					matches := prURLRegex.FindAllString(tAgentDraft, -1)
					for _, match := range matches {
						if prRef, err := pkg_github.ParsePullRequestURL(match); err == nil {
							if p, _, err := client.PullRequests.Get(c.Request.Context(), prRef.Repo.Owner, prRef.Repo.Name, prRef.PullRequestNumber); err == nil {
								linkedPR = p
								break
							}
						}
					}
				}
				if linkedPR != nil {
					break
				}
			}
		}
	}

	var commits []*github.RepositoryCommit
	var resp *github.Response

	if linkedPR != nil {
		log.Info("Found linked PR for issue, fetching PR commits", "issueID", issueID, "prNumber", linkedPR.GetNumber())
		commits, resp, err = client.PullRequests.ListCommits(c.Request.Context(), owner, repoName, linkedPR.GetNumber(), nil)
	} else {
		// Fallback to branch-based logic
		branch, found, _ := unstructured.NestedString(sandbox.Object, "metadata", "annotations", "sandbox.gemini.google.com/branch")
		if !found || branch == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "No branch found for this issue and no linked PR found"})
			return
		}

		originUser, found, _ := unstructured.NestedString(sandbox.Object, "metadata", "annotations", "sandbox.gemini.google.com/user-login")
		if !found || originUser == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "No origin user found for this issue"})
			return
		}

		// Use origin annotation if available, as it points to the actual fork used by the agent
		origin, foundOrigin, _ := unstructured.NestedString(sandbox.Object, "metadata", "annotations", "sandbox.gemini.google.com/origin")
		if foundOrigin && origin != "" {
			// Try to parse origin which might be github.com/owner/repo or https://github.com/owner/repo
			u := origin
			if !strings.Contains(u, "://") {
				u = "https://" + u
			}
			if parsedURL, err := url.Parse(u); err == nil {
				parts := strings.Split(strings.Trim(parsedURL.Path, "/"), "/")
				if len(parts) == 2 {
					originUser = parts[0]
					repoName = parts[1]
				}
			}
		}
		repoName = strings.TrimSuffix(repoName, ".git")

		commits, resp, err = client.Repositories.ListCommits(c.Request.Context(), originUser, repoName, &github.CommitsListOptions{
			SHA: branch,
		})
		if err != nil {
			if resp != nil && resp.StatusCode == http.StatusNotFound && authUserLogin != "" && authUserLogin != originUser {
				// Try again with the authenticated user (bot) if it's different
				log.Info("Retrying commit list with authUserLogin", "originUser", originUser, "authUserLogin", authUserLogin)
				commits, resp, err = client.Repositories.ListCommits(c.Request.Context(), authUserLogin, repoName, &github.CommitsListOptions{
					SHA: branch,
				})
			}
		}
	}

	if err != nil {
		if resp != nil && resp.StatusCode == http.StatusNotFound {
			c.JSON(http.StatusOK, []gin.H{})
			return
		}
		log.Info("Failed to list issue commits", "issueID", issueID, "err", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to list commits"})
		return
	}

	var result []gin.H
	for _, commit := range commits {
		sha := commit.GetSHA()
		message := ""
		if commit.Commit != nil {
			message = commit.Commit.GetMessage()
		}
		authorName := ""
		authorDate := time.Time{}
		if commit.Commit != nil && commit.Commit.Author != nil {
			authorName = commit.Commit.Author.GetName()
			authorDate = commit.Commit.Author.GetDate()
		}
		if authorName == "" && commit.Author != nil {
			authorName = commit.Author.GetLogin()
		}

		result = append(result, gin.H{
			"sha":     sha,
			"message": message,
			"author":  authorName,
			"date":    authorDate,
		})
	}

	c.JSON(http.StatusOK, result)
}

func (s *Server) rollbackIssue(c *gin.Context) {
	namespace := s.Auth.GetNamespaceFromContext(c)
	repo := c.Param("repo")
	issueID := c.Param("issue_id")

	var payload struct {
		CommitSHA     string `json:"commitSha"`
		PullRequestID string `json:"pullRequestId"`
	}
	if err := c.ShouldBindJSON(&payload); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if payload.CommitSHA == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "commitSha is required"})
		return
	}

	sandboxName := fmt.Sprintf("%s-issue-%s", repo, issueID)
	params := map[string]string{
		"COMMIT_SHA":      payload.CommitSHA,
		"PULL_REQUEST_ID": payload.PullRequestID,
	}

	err := s.K8sManager.CreateSandboxTask(c.Request.Context(), namespace, sandboxName, "Sandbox", "rollback", params)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create rollback task", "details": err.Error()})
		return
	}

	// Scale up the sandbox so it can process the task
	if err := s.K8sManager.ScaleupIssueSandbox(c.Request.Context(), namespace, repo, issueID, "", ""); err != nil {
		klog.Warningf("Failed to scale up issue sandbox after rollback task creation: %v", err)
	}

	c.Status(http.StatusOK)
}
