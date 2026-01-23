package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/auth"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/clients"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/models"
	"github.com/google/go-github/v39/github"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/klog/v2"
)

func (s *Server) getIssues(c *gin.Context) {
	log := klog.FromContext(c.Request.Context())
	namespace := c.MustGet(auth.UserKey).(string)
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
	namespace := c.MustGet(auth.UserKey).(string)
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
		taskType, _, _ := unstructured.NestedString(taskItem.Object, "spec", "type")
		taskState, _, _ := unstructured.NestedString(taskItem.Object, "status", "taskState")
		result, _, _ := unstructured.NestedString(taskItem.Object, "status", "result")

		tAgentDraft := ""
		tUserDraft := ""
		tAgentState := ""
		tAgentStateMessage := ""

		tAnnotations := taskItem.GetAnnotations()
		if tAnnotations != nil {
			tAgentDraft = tAnnotations["agentDraft"]
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

func (s *Server) listIssuesFromK8s(ctx context.Context, namespace, repo string) ([]models.Issue, error) {
	log := klog.FromContext(ctx)
	gvr := schema.GroupVersionResource{
		Group:    "custom.agents.x-k8s.io",
		Version:  "v1alpha1",
		Resource: "issuesandboxes",
	}
	list, err := s.K8sManager.Client.Resource(gvr).Namespace(namespace).List(context.Background(),
		v1.ListOptions{
			LabelSelector: fmt.Sprintf("review.gemini.google.com/repowatch=%s", repo),
		})
	if err != nil {
		return nil, fmt.Errorf("failed to list IssueSandbox CRs: %w", err)
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
			log.Info("Replicas (.spec.replicas) not found in IssueSandbox", "name", item.GetName())
			continue
		}

		issueID, found, err := unstructured.NestedString(item.Object, "spec", "source", "issue")
		if err != nil || !found {
			log.Info("Issue ID (.spec.source.issue) not found in IssueSandbox", "name", item.GetName())
			continue
		}

		title, found, err := unstructured.NestedString(item.Object, "spec", "source", "title")
		if err != nil || !found {
			log.Info("Title (.spec.source.title) not found in IssueSandbox", "name", item.GetName())
			continue
		}

		htmlurl, found, err := unstructured.NestedString(item.Object, "spec", "source", "htmlURL")
		if err != nil || !found {
			log.Info("htmlURL (.spec.source.htmlURL) not found in IssueSandbox", "name", item.GetName())
		}

		cloneURL, found, err := unstructured.NestedString(item.Object, "spec", "source", "cloneURL")
		if err != nil || !found {
			log.Info("branchURL (.spec.source.cloneURL) not found in IssueSandbox", "name", item.GetName())
			cloneURL = "https://github.com/noorg/norepo.git"
		}
		login, found, err := unstructured.NestedString(item.Object, "spec", "destination", "user", "login")
		if err != nil || !found {
			log.Info("branchURL (.spec.destination.user.login) not found in IssueSandbox", "name", item.GetName())
			login = "nouser"
		}
		branch, found, err := unstructured.NestedString(item.Object, "spec", "destination", "branch")
		if err != nil || !found {
			log.Info("branchURL (.spec.destination.branch) not found in IssueSandbox", "name", item.GetName())
			branch = "nobranch"
		}

		repoParts := strings.Split(strings.TrimSuffix(cloneURL, ".git"), "/")
		repoName := repoParts[len(repoParts)-1]

		branchURL := fmt.Sprintf("https://github.com/%s/%s/tree/%s", login, repoName, branch)

		pushBranch, found, err := unstructured.NestedBool(item.Object, "spec", "destination", "pushEnabled")
		if err != nil || !found {
			log.Info("pushBranch (.spec.source.pushBranch) not found in IssueSandbox", "name", item.GetName())
		}

		// get draft from annotation[agentDraft]
		draft := ""
		agentState := ""
		agentStateMessage := ""
		var agentLabels []string
		comment := ""
		annotations := item.GetAnnotations()
		if annotations != nil {
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
			if val, ok := annotations["agentLabels"]; ok {
				_ = json.Unmarshal([]byte(val), &agentLabels)
			}
			if val, ok := annotations["issueCommentSubmitted"]; ok && val == "true" {
				comment = draft
			}
		}

		issue := models.Issue{
			ID:                issueID,
			Title:             title,
			Sandbox:           item.GetName(),
			HTMLURL:           htmlurl,
			SandboxReplica:    fmt.Sprintf("%d", replicas),
			BranchURL:         branchURL,
			Comment:           comment,
			Draft:             draft,
			AgentDraft:        annotations["agentDraft"],
			PushBranch:        pushBranch,
			AgentState:        agentState,
			AgentStateMessage: agentStateMessage,
			Labels:            agentLabels,
		}
		issues = append(issues, issue)
	}
	return issues, nil
}

func (s *Server) saveIssueDraft(c *gin.Context) {
	namespace := c.MustGet(auth.UserKey).(string)
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
	err := s.K8sManager.UpdateDevSandboxAnnotation(c.Request.Context(), namespace, sandboxName, "userDraft", payload.Draft)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to save draft", "details": err.Error()})
		return
	}

	c.Status(http.StatusOK)
}

func (s *Server) submitIssueComment(c *gin.Context) {
	log := klog.FromContext(c.Request.Context())
	namespace := c.MustGet(auth.UserKey).(string)
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
		Group:    "custom.agents.x-k8s.io",
		Version:  "v1alpha1",
		Resource: "issuesandboxes",
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
			if err := s.K8sManager.UpdateDevSandboxAnnotation(ctx, namespace, sandboxName, "userDraft", draft); err != nil {
				log.Info("Failed to update issuesandbox userDraft", "issueID", issueID, "repo", repo, "err", err)
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

	if err := s.K8sManager.UpdateDevSandboxAnnotation(ctx, namespace, sandboxName, "issueCommentSubmitted", "true"); err != nil {
		log.Info("Failed to update issuesandbox issueCommentSubmitted", "issueID", issueID, "repo", repo, "err", err)
	}

	err = s.K8sManager.ScaledownIssueSandbox(ctx, namespace, repo, issueID, "")
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to scaledown Sandbox after comment submission", "details": err.Error()})
		return
	}

	c.Status(http.StatusOK)
}

func (s *Server) deleteIssue(c *gin.Context) {
	namespace := c.MustGet(auth.UserKey).(string)
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
	namespace := c.MustGet(auth.UserKey).(string)
	repo := c.Param("repo")
	issueID := c.Param("issue_id")
	ctx := c.Request.Context()

	if err := s.K8sManager.ScaleupIssueSandbox(ctx, namespace, repo, issueID, ""); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to scale up issue sandbox", "details": err.Error()})
		return
	}
	c.Status(http.StatusOK)
}

func (s *Server) scaleDownIssue(c *gin.Context) {
	namespace := c.MustGet(auth.UserKey).(string)
	repo := c.Param("repo")
	issueID := c.Param("issue_id")
	ctx := c.Request.Context()

	if err := s.K8sManager.ScaledownIssueSandbox(ctx, namespace, repo, issueID, ""); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to scale down issue sandbox", "details": err.Error()})
		return
	}
	c.Status(http.StatusOK)
}
