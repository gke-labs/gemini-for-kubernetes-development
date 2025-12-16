package api

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/review-ui/review-api/pkg/auth"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/review-ui/review-api/pkg/models"
	redis "github.com/go-redis/redis/v8"
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
	// SCAN Redis for PRs for repo
	prs := []models.PR{}
	repoPRKeyPrefix := fmt.Sprintf("pr:ns:%s:repo:%s:pr:", namespace, repo)
	iter := s.Redis.Scan(c.Request.Context(), 0, repoPRKeyPrefix+"*", 0).Iterator()
	for iter.Next(c.Request.Context()) {
		key := iter.Val()
		prID := key[len(repoPRKeyPrefix):]
		prData, err := s.Redis.HGetAll(c.Request.Context(), key).Result()
		if err != nil {
			log.Printf("Failed to get PR %s from Redis for repo %s: %v", prID, repo, err)
			continue
		}
		pr := models.PR{
			ID:    prID,
			Title: prData["title"],
		}

		if _, ok := prData["htmlurl"]; ok {
			pr.HTMLURL = prData["htmlurl"]
		}
		if _, ok := prData["diffurl"]; ok {
			pr.DiffURL = prData["diffurl"]
		}
		if _, ok := prData["draft"]; ok {
			pr.Draft = prData["draft"]
		}
		if _, ok := prData["sandbox"]; ok {
			pr.Sandbox = prData["sandbox"]
		}
		if _, ok := prData["sandboxReplica"]; ok {
			pr.SandboxReplica = prData["sandboxReplica"]
		}
		if _, ok := prData["review"]; ok {
			pr.Review = prData["review"]
		}
		prs = append(prs, pr)
	}
	if err := iter.Err(); err != nil {
		log.Printf("Error during Redis SCAN: %v", err)
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
		annotations := item.GetAnnotations()
		if annotations == nil {
			log.Printf("agentDraft (annotations=nil) not found in ReviewSandbox %s", item.GetName())
		} else if _, ok := annotations["agentDraft"]; !ok {
			log.Printf("agentDraft (annotations[agentDraft]) not found in ReviewSandbox %s", item.GetName())
		} else {
			draft = annotations["agentDraft"]
		}

		pr := models.PR{
			ID:             prID,
			Title:          title,
			Sandbox:        item.GetName(),
			HTMLURL:        htmlurl,
			DiffURL:        diffurl,
			SandboxReplica: fmt.Sprintf("%d", replicas),
		}

		prKey := fmt.Sprintf("pr:ns:%s:repo:%s:pr:%s", namespace, repo, prID)
		// Ensure the URL is in Redis
		if err := s.Redis.HSet(ctx, prKey,
			"title", pr.Title,
			"sandbox", pr.Sandbox,
			"htmlurl", pr.HTMLURL,
			"diffurl", pr.DiffURL,
			"sandboxReplica", pr.SandboxReplica,
			"draft", draft,
			"agentDraft", draft,
		).Err(); err != nil {
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

	prKey := fmt.Sprintf("pr:ns:%s:repo:%s:pr:%s", namespace, repo, prID)
	err := s.Redis.HSet(c.Request.Context(), prKey, "draft", payload.Draft).Err()
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
	prKey := fmt.Sprintf("pr:ns:%s:repo:%s:pr:%s", namespace, repo, prID)
	prData, err := s.Redis.HGetAll(ctx, prKey).Result()
	if err != nil {
		log.Printf("Failed to get PR %s from Redis for repo %s: %v", prID, repo, err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to get PR data from Redis"})
		return
	}

	draft := payload.Review
	agentDraft := prData["agentDraft"]
	sandboxName := prData["sandbox"]

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

		hfKey := fmt.Sprintf("hf:review:githubuser:%s:repo:%s:pr:%s", owner, repo, prID)
		if err := s.Redis.HSet(ctx, hfKey,
			"draft", draft,
			"agentDraft", agentDraft,
			"prompt", prompt,
			"configdir", configdir,
		).Err(); err != nil {
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
	agentOutput := &models.AgentOutput{}
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
	err = s.Redis.HSet(c.Request.Context(), prKey, "review", payload.Review).Err()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to save review", "details": err.Error()})
		return
	}

	// Delete draft from Redis
	err = s.Redis.HSet(c.Request.Context(), prKey, "draft", "").Err()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to clear draft", "details": err.Error()})
		return
	}

	// scale down sandbox
	err = s.K8sManager.ScaledownSandbox(ctx, namespace, repo, prID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to scaledown Sandbox after review submission", "details": err.Error()})
		return
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
	prKey := fmt.Sprintf("pr:ns:%s:repo:%s:pr:%s", namespace, repo, prID)
	if err := s.Redis.HDel(c.Request.Context(), prKey, "review", "draft", "sandbox", "htmlurl", "title").Err(); err != nil {
		log.Printf("Failed to HDEL PR data from Redis: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to HDEL PR data from Redis"})
		return
	}
	if err := s.Redis.Del(c.Request.Context(), prKey).Err(); err != nil {
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
	prKey := fmt.Sprintf("pr:ns:%s:repo:%s:pr:%s", namespace, repo, prID)
	sandboxName, err := s.Redis.HGet(ctx, prKey, "sandbox").Result()
	if err == redis.Nil {
		// If sandbox is not in Redis, we can assume it's already deleted or never existed.
		log.Printf("Sandbox for repo %s, PR %s not found in Redis. Assuming it's already deleted.", repo, prID)
		return nil
	} else if err != nil {
		return fmt.Errorf("failed to get sandbox name from Redis: %w", err)
	}

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
