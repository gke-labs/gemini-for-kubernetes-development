package api

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/review-ui/review-api/pkg/auth"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/review-ui/review-api/pkg/models"
	"github.com/google/go-github/v39/github"
	yaml "go.yaml.in/yaml/v3"
	"golang.org/x/oauth2"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

func (s *Server) getPRs(c *gin.Context) {
	namespace := c.MustGet(auth.UserKey).(string)
	repo := c.Param("repo")
	s.fetchAndPopulatePRs(c.Request.Context(), namespace, repo)

	prs, err := s.Store.ListPRs(c.Request.Context(), namespace, repo)
	if err != nil {
		log.Printf("Error listing PRs: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to list PRs"})
		return
	}

	c.JSON(http.StatusOK, prs)
}

func (s *Server) fetchAndPopulatePRs(ctx context.Context, namespace, repo string) {
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
		log.Printf("Failed to list ReviewSandbox CRs: %v. Serving mock data.", err)
		return
	}

	log.Printf("Populating PRs: Found %d reviewsandboxes for Repo: %s", len(list.Items), repo)
	for _, item := range list.Items {
		log.Printf("Creating PR entry for ReviewSandbox: %s/%s", item.GetNamespace(), item.GetName())
		// Get replicas and if it scaled down skip
		replicas, found, err := unstructured.NestedInt64(item.Object, "spec", "replicas")
		if err != nil || !found {
			log.Printf("Replicas (.spec.replicas) not found in ReviewSandbox  %s", item.GetName())
			continue
		}

		if item.GetDeletionTimestamp() != nil {
			log.Printf("Skipping terminating ReviewSandbox: %s", item.GetName())
			continue
		}

		prID, found, err := unstructured.NestedString(item.Object, "spec", "source", "pr")
		if err != nil || !found {
			log.Printf("PR ID (.spec.source.pr) not found in ReviewSandbox  %s", item.GetName())
			continue
		}

		title, found, err := unstructured.NestedString(item.Object, "spec", "source", "title")
		if err != nil || !found {
			log.Printf("Title (.spec.source.title) not found in ReviewSandbox  %s", item.GetName())
			continue
		}
		htmlurl, found, err := unstructured.NestedString(item.Object, "spec", "source", "htmlURL")
		if err != nil || !found {
			log.Printf("Title (.spec.source.htmlURL) not found in ReviewSandbox  %s", item.GetName())
		}
		diffurl, found, err := unstructured.NestedString(item.Object, "spec", "source", "diffURL")
		if err != nil || !found {
			log.Printf("diffURL (.spec.source.diffURL) not found in ReviewSandbox  %s", item.GetName())
		}

		// get draft from annotation[agentDraft]
		draft := ""
		agentState := ""
		agentStateMessage := ""
		reviewState := ""
		var labels []string
		annotations := item.GetAnnotations()
		if annotations == nil {
			log.Printf("annotations (annotations=nil) not found in ReviewSandbox %s", item.GetName())
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
			log.Printf("Failed to cache PR %s for repo %s: %v", pr.ID, repo, err)
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
	log.Printf("Submitting review for PR %s in repo %s with review: %s", prID, repo, payload.Review)

	// Get draft and agentDraft from Redis
	pr, err := s.Store.GetPR(ctx, namespace, repo, prID)
	if err != nil {
		log.Printf("Failed to get PR %s from Store for repo %s: %v", prID, repo, err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to get PR data from Store"})
		return
	}

	draft := payload.Review
	agentDraft := pr.AgentDraft
	sandboxName := pr.Sandbox

	// Get RepoWatch to get repoURL and secret ref
	repoWatch, err := s.K8sManager.GetRepoWatch(ctx, namespace, repo)
	if err != nil {
		log.Printf("Failed to get repowatch %s: %v", repo, err)
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
			log.Printf("Failed to store feedback for PR %s in repo %s: %v", prID, repo, err)
			// Continue without failing the review submission
		}

		if sandboxName != "" {
			if err := s.K8sManager.UpdateReviewSandboxUserDraft(ctx, namespace, sandboxName, draft); err != nil {
				log.Printf("Failed to update reviewsandbox userDraft for PR %s in repo %s: %v", prID, repo, err)
				// Not failing the request for this, just logging.
			}
		}
	}

	// Get GitHub token from secret
	token, err := s.K8sManager.GetGitHubToken(ctx, repoWatch)
	if err != nil {
		log.Printf("Failed to get github token for repo %s: %v", repo, err)
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

	// Get PR number
	prNumber, err := strconv.Atoi(prID)
	if err != nil {
		log.Printf("Failed to parse prID %s: %v", prID, err)
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid pr id"})
		return
	}

	// Try Unmarshalling the yaml review payload into PullRequestReviewRequest
	agentOutput := &models.ReviewAgentOutput{}
	reviewRequest := &github.PullRequestReviewRequest{}
	err = yaml.Unmarshal([]byte(payload.Review), &agentOutput)
	if err != nil {
		log.Printf("Failed to unmarshal review payload: %v", err)
		reviewRequest.Body = github.String(payload.Review)
	} else {
		reviewRequest = agentOutput.Review
	}

	// Not setting event sets it as a draft
	reviewRequest.Event = nil

	log.Printf("reviewRequest being created: %v", reviewRequest)
	review, resp, err := client.PullRequests.CreateReview(ctx, owner, repoName, prNumber, reviewRequest)
	if err != nil {
		log.Printf("response: %v", resp)
		log.Printf("Failed to create review on PR %d: %v", prNumber, err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create review on github", "details": err.Error()})
		return
	}
	log.Printf("review created: %v", review)
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
			log.Printf("Failed to update reviewState annotation for PR %s in repo %s: %v", prID, repo, err)
		}
	}

	c.Status(http.StatusOK)
}

func (s *Server) deletePR(c *gin.Context) {
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
		log.Printf("Failed to DEL PR data from Redis: %v", err)
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
	pr, err := s.Store.GetPR(ctx, namespace, repo, prID)
	if err != nil {
		// If sandbox is not in Store, we can assume it's already deleted or never existed.
		log.Printf("Sandbox for repo %s, PR %s not found in Store. Assuming it's already deleted.", repo, prID)
		return nil
	}
	sandboxName := pr.Sandbox

	gvr := schema.GroupVersionResource{
		Group:    "custom.agents.x-k8s.io",
		Version:  "v1alpha1",
		Resource: "reviewsandboxes",
	}
	log.Printf("Deleting sandbox %s", sandboxName)
	err = s.K8sManager.Client.Resource(gvr).Namespace(namespace).Delete(ctx, sandboxName, v1.DeleteOptions{})
	if err != nil {
		// We can choose to not return an error if it's already gone.
		return fmt.Errorf("failed to delete sandbox: %w", err)
	}
	return nil
}
