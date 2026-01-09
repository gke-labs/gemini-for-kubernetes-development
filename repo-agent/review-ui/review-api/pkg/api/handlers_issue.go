package api

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/review-ui/review-api/pkg/auth"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/review-ui/review-api/pkg/models"
	"github.com/google/go-github/v39/github"
	"golang.org/x/oauth2"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

func (s *Server) getIssues(c *gin.Context) {
	namespace := c.MustGet(auth.UserKey).(string)
	repo := c.Param("repo")
	handler := c.Param("handler")
	s.fetchAndPopulateIssues(c.Request.Context(), namespace, repo, handler)

	issues, err := s.Store.ListIssues(c.Request.Context(), namespace, repo, handler)
	if err != nil {
		log.Printf("Error listing issues: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to list issues"})
		return
	}

	c.JSON(http.StatusOK, issues)
}

func (s *Server) fetchAndPopulateIssues(ctx context.Context, namespace, repo, handler string) {
	gvr := schema.GroupVersionResource{
		Group:    "custom.agents.x-k8s.io",
		Version:  "v1alpha1",
		Resource: "issuesandboxes",
	}
	list, err := s.K8sManager.Client.Resource(gvr).Namespace(namespace).List(context.Background(),
		v1.ListOptions{
			LabelSelector: fmt.Sprintf("review.gemini.google.com/repowatch=%s,review.gemini.google.com/handler=%s", repo, handler),
		})
	if err != nil {
		log.Printf("Failed to list IssueSandbox CRs: %v.", err)
		return
	}

	log.Printf("Populating Issues: Found %d issuesandboxes for Repo: %s Handler: %s", len(list.Items), repo, handler)
	for _, item := range list.Items {
		log.Printf("Creating Issue entry for IssueSandbox: %s/%s", item.GetNamespace(), item.GetName())
		replicas, found, err := unstructured.NestedInt64(item.Object, "spec", "replicas")
		if err != nil || !found {
			log.Printf("Replicas (.spec.replicas) not found in IssueSandbox %s", item.GetName())
			continue
		}

		issueID, found, err := unstructured.NestedString(item.Object, "spec", "source", "issue")
		if err != nil || !found {
			log.Printf("Issue ID (.spec.source.issue) not found in IssueSandbox %s", item.GetName())
			continue
		}

		title, found, err := unstructured.NestedString(item.Object, "spec", "source", "title")
		if err != nil || !found {
			log.Printf("Title (.spec.source.title) not found in IssueSandbox %s", item.GetName())
			continue
		}
		htmlurl, found, err := unstructured.NestedString(item.Object, "spec", "source", "htmlURL")
		if err != nil || !found {
			log.Printf("htmlURL (.spec.source.htmlURL) not found in IssueSandbox %s", item.GetName())
		}

		// https://github.com/barney-s/kro/tree/issue-753-bugfix
		// https://github.com/ + .user.login + source.cloneURL repo name + /tree/ + .destination.branch
		// https://github.com/kubernetes-sigs/kro/compare/main...barney-s:kro:issue-753-bugfix
		// .source.cloneURL - .git + /compare/main... + .user.login + : + source.cloneURL repo name  + : + .destination.branch

		cloneURL, found, err := unstructured.NestedString(item.Object, "spec", "source", "cloneURL")
		if err != nil || !found {
			log.Printf("branchURL (.spec.source.cloneURL) not found in IssueSandbox %s", item.GetName())
			cloneURL = "https://github.com/noorg/norepo.git"
		}
		login, found, err := unstructured.NestedString(item.Object, "spec", "destination", "user", "login")
		if err != nil || !found {
			log.Printf("branchURL (.spec.destination.user.login) not found in IssueSandbox %s", item.GetName())
			login = "nouser"
		}
		branch, found, err := unstructured.NestedString(item.Object, "spec", "destination", "branch")
		if err != nil || !found {
			log.Printf("branchURL (.spec.destination.branch) not found in IssueSandbox %s", item.GetName())
			branch = "nobranch"
		}

		repoParts := strings.Split(strings.TrimSuffix(cloneURL, ".git"), "/")
		repoName := repoParts[len(repoParts)-1]

		branchURL := fmt.Sprintf("https://github.com/%s/%s/tree/%s", login, repoName, branch)

		pushBranch, found, err := unstructured.NestedBool(item.Object, "spec", "destination", "pushEnabled")
		if err != nil || !found {
			log.Printf("pushBranch (.spec.source.pushBranch) not found in IssueSandbox %s", item.GetName())
		}

		// get draft from annotation[agentDraft]
		draft := ""
		agentState := ""
		agentStateMessage := ""
		var labels []string
		annotations := item.GetAnnotations()
		if annotations == nil {
			log.Printf("annotations (annotations=nil) not found in IssueSandbox %s", item.GetName())
		} else {
			if val, ok := annotations["agentDraft"]; ok {
				draft = val
			} else {
				log.Printf("agentDraft (annotations[agentDraft]) not found in IssueSandbox %s", item.GetName())
			}
			if val, ok := annotations["agentState"]; ok {
				agentState = val
			}
			if val, ok := annotations["agentStateMessage"]; ok {
				agentStateMessage = val
			}
			if val, ok := annotations["agentLabels"]; ok {
				_ = json.Unmarshal([]byte(val), &labels)
			}
		}

		issue := models.Issue{
			ID:                issueID,
			Title:             title,
			Sandbox:           item.GetName(),
			HTMLURL:           htmlurl,
			SandboxReplica:    fmt.Sprintf("%d", replicas),
			BranchURL:         branchURL,
			Draft:             draft,
			PushBranch:        pushBranch,
			AgentState:        agentState,
			AgentStateMessage: agentStateMessage,
			Labels:            labels,
		}
		if err := s.Store.SaveIssue(ctx, namespace, repo, handler, issue); err != nil {
			log.Printf("Failed to cache Issue %s for repo %s handler %s: %v", issueID, repo, handler, err)
		}
	}
}

func (s *Server) saveIssueDraft(c *gin.Context) {
	namespace := c.MustGet(auth.UserKey).(string)
	repo := c.Param("repo")
	issueID := c.Param("issue_id")
	handler := c.Param("handler")
	var payload struct {
		Draft string
	}
	if err := c.ShouldBindJSON(&payload); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	err := s.Store.UpdateIssueDraft(c.Request.Context(), namespace, repo, handler, issueID, payload.Draft)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to save draft"})
		return
	}

	c.Status(http.StatusOK)
}

func (s *Server) submitIssueComment(c *gin.Context) {
	namespace := c.MustGet(auth.UserKey).(string)
	repo := c.Param("repo")
	issueID := c.Param("issue_id")
	handler := c.Param("handler")
	var payload struct {
		Comment string
	}
	if err := c.ShouldBindJSON(&payload); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	ctx := c.Request.Context()
	log.Printf("Submitting comment for Issue %s in repo %s with comment: %s", issueID, repo, payload.Comment)

	issue, err := s.Store.GetIssue(ctx, namespace, repo, handler, issueID)
	if err != nil {
		log.Printf("Failed to get Issue %s from Store for repo %s handler %s: %v", issueID, repo, handler, err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to get Issue data from Store"})
		return
	}

	draft := payload.Comment
	agentDraft := issue.AgentDraft

	repoWatch, err := s.K8sManager.GetRepoWatch(ctx, namespace, repo)
	if err != nil {
		log.Printf("Failed to get repowatch %s: %v", repo, err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to get repowatch config"})
		return
	}

	if draft != agentDraft {
		// Store feedback for fine-tuning
		var prompt, configdir string
		if handlers, found, err := unstructured.NestedSlice(repoWatch.Object, "spec", "issueHandlers"); err == nil && found {
			for _, h := range handlers {
				handlerMap, ok := h.(map[string]interface{})
				if !ok {
					continue
				}
				name, _ := handlerMap["name"].(string)
				if name == handler {
					gemini, ok := handlerMap["gemini"].(map[string]interface{})
					if ok {
						prompt, _ = gemini["prompt"].(string)
						configdir, _ = gemini["configdirRef"].(string)
					}
					break
				}
			}
		}

		repoURL, _, _ := unstructured.NestedString(repoWatch.Object, "spec", "repoURL")
		owner, _, _ := parseRepoURL(repoURL)

		if err := s.Store.SaveIssueFeedback(ctx, owner, repo, handler, issueID, draft, agentDraft, prompt, configdir); err != nil {
			log.Printf("Failed to store feedback for Issue %s in repo %s: %v", issueID, repo, err)
			// Continue without failing the comment submission
		}
	}

	token, err := s.K8sManager.GetGitHubToken(ctx, repoWatch)
	if err != nil {
		log.Printf("Failed to get github token for repo %s: %v", repo, err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to get github token"})
		return
	}

	ts := oauth2.StaticTokenSource(&oauth2.Token{AccessToken: token})
	tc := oauth2.NewClient(ctx, ts)
	client := github.NewClient(tc)

	repoURL, found, err := unstructured.NestedString(repoWatch.Object, "spec", "repoURL")
	if err != nil || !found {
		log.Printf("repoURL not found in RepoWatch CR %s", repoWatch.GetName())
		c.JSON(http.StatusInternalServerError, gin.H{"error": "repoURL not found in RepoWatch CR"})
		return
	}
	owner, repoName, err := parseRepoURL(repoURL)
	if err != nil {
		log.Printf("Failed to parse repo url %s: %v", repoURL, err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to parse repo url"})
		return
	}

	issueNumber, err := strconv.Atoi(issueID)
	if err != nil {
		log.Printf("Failed to parse issueID %s: %v", issueID, err)
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid issue id"})
		return
	}

	comment := &github.IssueComment{Body: &payload.Comment}
	_, _, err = client.Issues.CreateComment(ctx, owner, repoName, issueNumber, comment)
	if err != nil {
		log.Printf("Failed to create comment on Issue %d: %v", issueNumber, err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create comment on github"})
		return
	}

	err = s.Store.UpdateIssueComment(c.Request.Context(), namespace, repo, handler, issueID, payload.Comment)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to save comment", "details": err.Error()})
		return
	}

	err = s.K8sManager.ScaledownIssueSandbox(ctx, namespace, repo, issueID, handler)
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
	handler := c.Param("handler")
	ctx := c.Request.Context()

	if err := s.K8sManager.ScaledownIssueSandbox(ctx, namespace, repo, issueID, handler); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to delete sandbox", "details": err.Error()})
		return
	}

	if err := s.Store.DeleteIssue(c.Request.Context(), namespace, repo, handler, issueID); err != nil {
		log.Printf("Failed to DEL Issue data from Store: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to DEL Issue data from Store"})
		return
	}

	c.Status(http.StatusOK)
}

func (s *Server) scaleUpIssue(c *gin.Context) {
	namespace := c.MustGet(auth.UserKey).(string)
	repo := c.Param("repo")
	issueID := c.Param("issue_id")
	handler := c.Param("handler")
	ctx := c.Request.Context()

	if err := s.K8sManager.ScaleupIssueSandbox(ctx, namespace, repo, issueID, handler); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to scale up issue sandbox", "details": err.Error()})
		return
	}
	c.Status(http.StatusOK)
}

func (s *Server) scaleDownIssue(c *gin.Context) {
	namespace := c.MustGet(auth.UserKey).(string)
	repo := c.Param("repo")
	issueID := c.Param("issue_id")
	handler := c.Param("handler")
	ctx := c.Request.Context()

	if err := s.K8sManager.ScaledownIssueSandbox(ctx, namespace, repo, issueID, handler); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to scale down issue sandbox", "details": err.Error()})
		return
	}
	c.Status(http.StatusOK)
}
