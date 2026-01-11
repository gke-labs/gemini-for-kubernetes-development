package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/auth"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/models"
	"github.com/google/go-github/v39/github"
	yaml "go.yaml.in/yaml/v3"
	"golang.org/x/oauth2"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/klog/v2"
)

func (s *Server) getPRs(c *gin.Context) {
	log := klog.FromContext(c.Request.Context())
	namespace := c.MustGet(auth.UserKey).(string)
	repo := c.Param("repo")
	s.fetchAndPopulatePRs(c.Request.Context(), namespace, repo)

	prs, err := s.Store.ListPRs(c.Request.Context(), namespace, repo)
	if err != nil {
		log.Info("Error listing PRs", "err", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to list PRs"})
		return
	}

	c.JSON(http.StatusOK, prs)
}

func (s *Server) fetchAndPopulatePRs(ctx context.Context, namespace, repo string) {
	log := klog.FromContext(ctx)
	gvr := schema.GroupVersionResource{
		Group:    "custom.agents.x-k8s.io",
		Version:  "v1alpha1",
		Resource: "reviewsandboxes",
	}
	list, err := s.K8sManager.Client.Resource(gvr).Namespace(namespace).List(context.Background(),
		v1.ListOptions{
			LabelSelector: fmt.Sprintf("review.gemini.google.com/repowatch=%s", repo),
		})
	if err != nil {
		log.Info("Failed to list ReviewSandbox CRs. Serving mock data.", "err", err)
		return
	}

	log.Info("Populating PRs", "reviewsandbox_count", len(list.Items), "repo", repo)

	activePRs := make(map[string]bool)
	for _, item := range list.Items {
		log.Info("Creating PR entry for ReviewSandbox", "namespace", item.GetNamespace(), "name", item.GetName())
		// Get replicas and if it scaled down skip
		replicas, found, err := unstructured.NestedInt64(item.Object, "spec", "replicas")
		if err != nil || !found {
			log.Info("Replicas (.spec.replicas) not found in ReviewSandbox", "name", item.GetName())
			continue
		}

		if item.GetDeletionTimestamp() != nil {
			log.Info("Skipping terminating ReviewSandbox", "name", item.GetName())
			continue
		}

		prID, found, err := unstructured.NestedString(item.Object, "spec", "source", "pr")
		if err != nil || !found {
			log.Info("PR ID (.spec.source.pr) not found in ReviewSandbox", "name", item.GetName())
			continue
		}

		title, found, err := unstructured.NestedString(item.Object, "spec", "source", "title")
		if err != nil || !found {
			log.Info("Title (.spec.source.title) not found in ReviewSandbox", "name", item.GetName())
			continue
		}

		activePRs[prID] = true

		htmlurl, found, err := unstructured.NestedString(item.Object, "spec", "source", "htmlURL")
		if err != nil || !found {
			log.Info("Title (.spec.source.htmlURL) not found in ReviewSandbox", "name", item.GetName())
		}
		diffurl, found, err := unstructured.NestedString(item.Object, "spec", "source", "diffURL")
		if err != nil || !found {
			log.Info("diffURL (.spec.source.diffURL) not found in ReviewSandbox", "name", item.GetName())
		}

		// get draft from annotation[agentDraft]
		draft := ""
		agentState := ""
		agentStateMessage := ""
		reviewState := ""
		var labels []string
		annotations := item.GetAnnotations()
		if annotations == nil {
			log.Info("annotations (annotations=nil) not found in ReviewSandbox", "name", item.GetName())
		} else {
			if val, ok := annotations["agentDraft"]; ok {
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
		}

		pr := models.PR{
			ID:                prID,
			Title:             title,
			Sandbox:           item.GetName(),
			HTMLURL:           htmlurl,
			DiffURL:           diffurl,
			SandboxReplica:    fmt.Sprintf("%d", replicas),
			Draft:             draft,
			AgentDraft:        draft,
			AgentState:        agentState,
			AgentStateMessage: agentStateMessage,
			ReviewState:       reviewState,
			Labels:            labels,
		}

		if err := s.Store.SavePR(ctx, namespace, repo, pr); err != nil {
			log.Info("Failed to cache PR", "prID", pr.ID, "repo", repo, "err", err)
		}
	}

	// Cleanup stale entries
	storedPRs, err := s.Store.ListPRs(ctx, namespace, repo)
	if err != nil {
		log.Info("Failed to list PRs for cleanup", "err", err)
		return
	}

	for _, pr := range storedPRs {
		if !activePRs[pr.ID] {
			log.Info("Removing stale PR from store", "prID", pr.ID)
			if err := s.Store.DeletePR(ctx, namespace, repo, pr.ID); err != nil {
				log.Info("Failed to delete stale PR", "prID", pr.ID, "err", err)
			}
		}
	}
}

func (s *Server) saveDraft(c *gin.Context) {
	namespace := c.MustGet(auth.UserKey).(string)
	repo := c.Param("repo")
	prID := c.Param("id")
	var payload struct {
		Draft string
	}
	if err := c.ShouldBindJSON(&payload); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	err := s.Store.UpdatePRDraft(c.Request.Context(), namespace, repo, prID, payload.Draft)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to save draft"})
		return
	}

	c.Status(http.StatusOK)
}

func (s *Server) submitReview(c *gin.Context) {
	log := klog.FromContext(c.Request.Context())
	namespace := c.MustGet(auth.UserKey).(string)
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

	// Get draft and agentDraft from Redis
	pr, err := s.Store.GetPR(ctx, namespace, repo, prID)
	if err != nil {
		log.Info("Failed to get PR from Store for repo", "prID", prID, "repo", repo, "err", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to get PR data from Store"})
		return
	}

	draft := payload.Review
	agentDraft := pr.AgentDraft
	sandboxName := pr.Sandbox

	// Get RepoWatch to get repoURL and secret ref
	repoWatch, err := s.K8sManager.GetRepoWatch(ctx, namespace, repo)
	if err != nil {
		log.Info("Failed to get repowatch", "repo", repo, "err", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to get repowatch config"})
		return
	}

	if draft != agentDraft {
		// Store feedback for fine-tuning
		prompt, _, _ := unstructured.NestedString(repoWatch.Object, "spec", "review", "gemini", "prompt")
		configdir, _, _ := unstructured.NestedString(repoWatch.Object, "spec", "review", "gemini", "configdirRef")
		repoURL, _, _ := unstructured.NestedString(repoWatch.Object, "spec", "repoURL")
		owner, _, _ := parseRepoURL(repoURL)

		if err := s.Store.SavePRFeedback(ctx, owner, repo, prID, draft, agentDraft, prompt, configdir); err != nil {
			log.Info("Failed to store feedback for PR", "prID", prID, "repo", repo, "err", err)
			// Continue without failing the review submission
		}

		if sandboxName != "" {
			if err := s.K8sManager.UpdateReviewSandboxUserDraft(ctx, namespace, sandboxName, draft); err != nil {
				log.Info("Failed to update reviewsandbox userDraft for PR", "prID", prID, "repo", repo, "err", err)
				// Not failing the request for this, just logging.
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
	ts := oauth2.StaticTokenSource(
		&oauth2.Token{AccessToken: token},
	)
	tc := oauth2.NewClient(ctx, ts)
	client := github.NewClient(tc)

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
		reviewRequest = agentOutput.Review
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
	// Set review in Redis
	err = s.Store.UpdatePRReview(c.Request.Context(), namespace, repo, prID, payload.Review)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to save review", "details": err.Error()})
		return
	}

	// scale down sandbox
	err = s.K8sManager.ScaledownSandbox(ctx, namespace, repo, prID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to scaledown Sandbox after review submission", "details": err.Error()})
		return
	}

	if sandboxName != "" {
		if err := s.K8sManager.UpdateReviewSandboxAnnotation(ctx, namespace, sandboxName, "reviewState", "submitted"); err != nil {
			log.Info("Failed to update reviewState annotation for PR", "prID", prID, "repo", repo, "err", err)
		}
	}

	c.Status(http.StatusOK)
}

func (s *Server) deletePR(c *gin.Context) {
	log := klog.FromContext(c.Request.Context())
	namespace := c.MustGet(auth.UserKey).(string)
	repo := c.Param("repo")
	prID := c.Param("id")
	ctx := c.Request.Context()

	if err := s.K8sManager.ScaledownSandbox(ctx, namespace, repo, prID); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to delete sandbox", "details": err.Error()})
		return
	}

	// Clean up Redis keys
	if err := s.Store.DeletePR(c.Request.Context(), namespace, repo, prID); err != nil {
		log.Info("Failed to DEL PR data from Redis", "err", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to DEL PR data from Redis"})
		return
	}

	c.Status(http.StatusOK)
}

func (s *Server) scaleUpPR(c *gin.Context) {
	namespace := c.MustGet(auth.UserKey).(string)
	repo := c.Param("repo")
	prID := c.Param("id")
	ctx := c.Request.Context()

	if err := s.K8sManager.ScaleupSandbox(ctx, namespace, repo, prID); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to scale up sandbox", "details": err.Error()})
		return
	}
	c.Status(http.StatusOK)
}

func (s *Server) scaleDownPR(c *gin.Context) {
	namespace := c.MustGet(auth.UserKey).(string)
	repo := c.Param("repo")
	prID := c.Param("id")
	ctx := c.Request.Context()

	if err := s.K8sManager.ScaledownSandbox(ctx, namespace, repo, prID); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to scale down sandbox", "details": err.Error()})
		return
	}
	c.Status(http.StatusOK)
}

//nolint:unused
func (s *Server) deleteSandbox(ctx context.Context, namespace, repo, prID string) error {
	log := klog.FromContext(ctx)
	pr, err := s.Store.GetPR(ctx, namespace, repo, prID)
	if err != nil {
		// If sandbox is not in Store, we can assume it's already deleted or never existed.
		log.Info("Sandbox for repo and PR not found in Store. Assuming it's already deleted.", "repo", repo, "prID", prID)
		return nil
	}
	sandboxName := pr.Sandbox

	gvr := schema.GroupVersionResource{
		Group:    "custom.agents.x-k8s.io",
		Version:  "v1alpha1",
		Resource: "reviewsandboxes",
	}
	log.Info("Deleting sandbox", "name", sandboxName)
	err = s.K8sManager.Client.Resource(gvr).Namespace(namespace).Delete(ctx, sandboxName, v1.DeleteOptions{})
	if err != nil {
		// We can choose to not return an error if it's already gone.
		return fmt.Errorf("failed to delete sandbox: %w", err)
	}
	return nil
}
