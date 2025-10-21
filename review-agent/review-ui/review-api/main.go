package main

import (
	"context"
	"encoding/base64"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	redis "github.com/go-redis/redis/v8"
	"github.com/google/go-github/v39/github"
	"golang.org/x/oauth2"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
)

var (
	rdb       *redis.Client
	k8sClient dynamic.Interface
	namespace string
)

// PR represents a pull request
type PR struct {
	ID             string `json:"id"`
	Title          string `json:"title"`
	Draft          string `json:"draft,omitempty"`
	Sandbox        string `json:"sandbox,omitempty"`
	SandboxReplica string `json:"sandboxReplica,omitempty"`
	Review         string `json:"review,omitempty"`
	HTMLURL        string `json:"htmlURL,omitempty"`
}

// Repo represents a repository
type Repo struct {
	Name          string `json:"name"`
	URL           string `json:"url"`
	SomeOtherInfo string `json:"someOtherInfo,omitempty"`
}

func main() {
	// Redis client
	namespace = os.Getenv("NAMESPACE")
	if namespace == "" {
		namespace = "review-agent-system"
	}
	redisAddr := os.Getenv("REDIS_ADDR")
	if redisAddr == "" {
		redisAddr = "localhost:6379"
	}
	rdb = redis.NewClient(&redis.Options{
		Addr: redisAddr,
	})

	// Kubernetes client
	config, err := rest.InClusterConfig()
	if err != nil {
		// Fallback to local config for local development
		log.Printf("Failed to get in-cluster config, trying local config: %v", err)
		config, err = rest.InClusterConfig()
		if err != nil {
			log.Fatalf("Failed to get in-cluster config: %v", err)
		}
	}
	k8sClient, err = dynamic.NewForConfig(config)
	if err != nil {
		log.Fatalf("Failed to create kubernetes client: %v", err)
	}

	// Ping redis to ensure connection
	_, err = rdb.Ping(context.Background()).Result()
	if err != nil {
		log.Fatalf("Failed to connect to Redis: %v", err)
	}

	// Pre-populate mock data in Redis
	populateMockData()

	// Gin router
	router := gin.Default()
	api := router.Group("/api")
	{
		api.GET("/repos", getRepos)
		api.GET("/repo/:repo/prs", getPRs)
		api.POST("/repo/:repo/prs/:id/draft", saveDraft)
		api.POST("/repo/:repo/prs/:id/submitreview", submitReview)
		api.DELETE("/repo/:repo/prs/:id", deletePR)
	}

	err = router.Run(":8080")
	if err != nil {
		log.Fatalf("Failed to start router: %v", err)
	}
}

func populateMockData() {
	ctx := context.Background()
	mockRepos := []Repo{
		{Name: "redis", URL: "https://github.com/redis/redis"},
		{Name: "linux", URL: "https://github.com/linux/linux"},
	}

	mockPRs := map[string][]PR{
		"redis": {
			{ID: "123", Title: "Feat: Add awesome feature", Draft: "This is a draft review."},
			{ID: "124", Title: "Fix: A really bad bug", Sandbox: "redis-pr-124", Review: "LGTM!"},
		},
		"linux": {
			{ID: "1", Title: "Docs: Update README", Sandbox: "linux-pr-1", Draft: "Few spelling mistakes. s/Nort/North/"},
			{ID: "2", Title: "Refactor: Improve performance"},
		},
	}

	for _, repo := range mockRepos {
		// Store repo URL
		if err := rdb.HSet(ctx, fmt.Sprintf("repo:%s", repo.Name), "url", repo.URL).Err(); err != nil {
			log.Printf("Failed to set repo URL in Redis: %v", err)
		}

		// Store PRs for the repo
		for _, pr := range mockPRs[repo.Name] {
			prKey := fmt.Sprintf("pr:repo:%s:pr:%s", repo.Name, pr.ID)
			if err := rdb.HSet(ctx, prKey, "title", pr.Title, "draft", pr.Draft, "sandbox", pr.Sandbox, "review", pr.Review).Err(); err != nil {
				log.Printf("Failed to set PR info in Redis: %v", err)
			}
		}
	}
}

func getRepos(c *gin.Context) {
	fetchAndPopulateRepos(c.Request.Context())

	// SCAN Redis for repo URLs
	var repos []Repo
	iter := rdb.Scan(c.Request.Context(), 0, "repo:*", 0).Iterator()
	for iter.Next(c.Request.Context()) {
		key := iter.Val()
		repoName := key[len("repo:"):]
		repoURL, err := rdb.HGet(c.Request.Context(), key, "url").Result()
		if err != nil {
			log.Printf("Failed to get repo URL from Redis for key %s: %v", key, err)
			continue
		}
		repos = append(repos, Repo{Name: repoName, URL: repoURL})
	}
	if err := iter.Err(); err != nil {
		log.Printf("Error during Redis SCAN: %v", err)
	}

	c.JSON(http.StatusOK, repos)
}

func fetchAndPopulateRepos(ctx context.Context) {
	gvr := schema.GroupVersionResource{
		Group:    "review.gemini.google.com",
		Version:  "v1alpha1",
		Resource: "repowatches",
	}
	list, err := k8sClient.Resource(gvr).Namespace(namespace).List(context.Background(), v1.ListOptions{})
	if err != nil {
		log.Printf("Failed to list RepoWatch CRs: %v. Serving mock data.", err)
		return
	}

	for _, item := range list.Items {
		repoURL, found, err := unstructured.NestedString(item.Object, "spec", "repoURL")
		if err != nil || !found {
			log.Printf("repoURL not found in RepoWatch CR %s", item.GetName())
			continue
		}
		repo := Repo{
			Name: item.GetName(),
			URL:  repoURL,
		}

		// Ensure the URL is in Redis
		if err := rdb.HSet(ctx, fmt.Sprintf("repo:%s", repo.Name), "url", repo.URL).Err(); err != nil {
			log.Printf("Failed to cache repo URL for %s: %v", repo.Name, err)
		}
	}
}

func getPRs(c *gin.Context) {
	repo := c.Param("repo")
	fetchAndPopulatePRs(c.Request.Context(), repo)
	// SCAN Redis for PRs for repo
	var prs []PR
	repoPRKeyPrefix := fmt.Sprintf("pr:repo:%s:pr:", repo)
	iter := rdb.Scan(c.Request.Context(), 0, repoPRKeyPrefix+"*", 0).Iterator()
	for iter.Next(c.Request.Context()) {
		key := iter.Val()
		prID := key[len(repoPRKeyPrefix):]
		prData, err := rdb.HGetAll(c.Request.Context(), key).Result()
		if err != nil {
			log.Printf("Failed to get PR %s from Redis for repo %s: %v", prID, repo, err)
			continue
		}
		pr := PR{
			ID:    prID,
			Title: prData["title"],
		}

		if _, ok := prData["htmlurl"]; ok {
			pr.HTMLURL = prData["htmlurl"]
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

func fetchAndPopulatePRs(ctx context.Context, repo string) {
	gvr := schema.GroupVersionResource{
		Group:    "custom.agents.x-k8s.io",
		Version:  "v1alpha1",
		Resource: "reviewsandboxes",
	}
	// In a real scenario, we would list the CRs from the cluster.
	// For this demo, we will return a mock list and ensure the URLs are in Redis.
	// This simulates the controller having populated Redis.
	list, err := k8sClient.Resource(gvr).Namespace(namespace).List(context.Background(),
		v1.ListOptions{
			LabelSelector: fmt.Sprintf("review.gemini.google.com/repowatch=%s", repo),
		})
	if err != nil {
		log.Printf("Failed to list ReviewSandbox CRs: %v. Serving mock data.", err)
		return
	}

	for _, item := range list.Items {
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
		pr := PR{
			ID:             prID,
			Title:          title,
			Sandbox:        item.GetName(),
			HTMLURL:        htmlurl,
			SandboxReplica: fmt.Sprintf("%d", replicas),
		}

		prKey := fmt.Sprintf("pr:repo:%s:pr:%s", repo, prID)
		// Ensure the URL is in Redis
		if err := rdb.HSet(ctx, prKey,
			"title", pr.Title,
			"sandbox", pr.Sandbox,
			"htmlurl", pr.HTMLURL,
			"sandboxReplica", pr.SandboxReplica,
		).Err(); err != nil {
			log.Printf("Failed to cache PR %s for repo %s: %v", pr.ID, repo, err)
		}
	}
}

func saveDraft(c *gin.Context) {
	repo := c.Param("repo")
	prID := c.Param("id")
	var payload struct {
		Draft string `json:"draft"`
	}
	if err := c.ShouldBindJSON(&payload); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	prKey := fmt.Sprintf("pr:repo:%s:pr:%s", repo, prID)
	err := rdb.HSet(c.Request.Context(), prKey, "draft", payload.Draft).Err()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to save draft"})
		return
	}

	c.Status(http.StatusOK)
}

func submitReview(c *gin.Context) {
	repo := c.Param("repo")
	prID := c.Param("id")
	var payload struct {
		Review string `json:"review"`
	}
	if err := c.ShouldBindJSON(&payload); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	ctx := c.Request.Context()
	log.Printf("Submitting review for PR %s in repo %s with review: %s", prID, repo, payload.Review)

	// Get RepoWatch to get repoURL and secret ref
	repoWatch, err := getRepoWatch(ctx, repo)
	if err != nil {
		log.Printf("Failed to get repowatch %s: %v", repo, err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to get repowatch config"})
		return
	}

	// Get GitHub token from secret
	token, err := getGitHubToken(ctx, repoWatch)
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

	// Create comment on PR
	comment := &github.IssueComment{
		Body: &payload.Review,
	}
	_, _, err = client.Issues.CreateComment(ctx, owner, repoName, prNumber, comment)
	if err != nil {
		log.Printf("Failed to create comment on PR %d: %v", prNumber, err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create comment on github"})
		return
	}

	// Set review in Redis
	prKey := fmt.Sprintf("pr:repo:%s:pr:%s", repo, prID)
	err = rdb.HSet(c.Request.Context(), prKey, "review", payload.Review).Err()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to save review", "details": err.Error()})
		return
	}

	// Delete draft from Redis
	err = rdb.HSet(c.Request.Context(), prKey, "draft", "").Err()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to clear draft", "details": err.Error()})
		return
	}

	// scale down sandbox
	err = scaledownSandbox(ctx, repo, prID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to scaledown Sandbox after review submission", "details": err.Error()})
		return
	}

	c.Status(http.StatusOK)
}

func deletePR(c *gin.Context) {
	repo := c.Param("repo")
	prID := c.Param("id")
	ctx := c.Request.Context()

	if err := scaledownSandbox(ctx, repo, prID); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to delete sandbox", "details": err.Error()})
		return
	}

	// Clean up Redis keys
	prKey := fmt.Sprintf("pr:repo:%s:pr:%s", repo, prID)
	if err := rdb.HDel(c.Request.Context(), prKey, "review", "draft", "sandbox", "htmlurl", "title").Err(); err != nil {
		log.Printf("Failed to HDEL PR data from Redis: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to HDEL PR data from Redis"})
		return
	}
	if err := rdb.Del(c.Request.Context(), prKey).Err(); err != nil {
		log.Printf("Failed to DEL PR data from Redis: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to DEL PR data from Redis"})
		return
	}

	c.Status(http.StatusOK)
}

//nolint:unused
func deleteSandbox(ctx context.Context, repo, prID string) error {
	prKey := fmt.Sprintf("pr:repo:%s:pr:%s", repo, prID)
	sandboxName, err := rdb.HGet(ctx, prKey, "sandbox").Result()
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
	err = k8sClient.Resource(gvr).Namespace(namespace).Delete(ctx, sandboxName, v1.DeleteOptions{})
	if err != nil {
		// We can choose to not return an error if it's already gone.
		return fmt.Errorf("failed to delete sandbox: %w", err)
	}
	return nil
}

func scaledownSandbox(ctx context.Context, repo, prID string) error {
	prKey := fmt.Sprintf("pr:repo:%s:pr:%s", repo, prID)
	sandboxName, err := rdb.HGet(ctx, prKey, "sandbox").Result()
	if err == redis.Nil {
		// If sandbox is not in Redis, we can assume it's already deleted or never existed.
		log.Printf("Sandbox for repo %s, PR %s not found in Redis. Assuming it's already deleted.", repo, prID)
		// For the demo, we'll construct the name to attempt deletion anyway.
		sandboxName = fmt.Sprintf("%s-pr-%s", repo, prID)
	} else if err != nil {
		return fmt.Errorf("failed to get sandbox name from Redis: %w", err)
	}

	gvr := schema.GroupVersionResource{
		Group:    "custom.agents.x-k8s.io",
		Version:  "v1alpha1",
		Resource: "reviewsandboxes",
	}
	log.Printf("Scaling down sandbox %s", sandboxName)

	// Set .spec.replicas to 0 and apply the sandbox object
	sandbox := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "custom.agents.x-k8s.io/v1alpha1",
			"kind":       "ReviewSandbox",
			"metadata": map[string]interface{}{
				"name":      sandboxName,
				"namespace": namespace,
			},
			"spec": map[string]interface{}{
				"replicas": int64(0),
			},
		},
	}

	_, err = k8sClient.Resource(gvr).Namespace(namespace).Apply(ctx, sandboxName,
		sandbox, v1.ApplyOptions{FieldManager: "review-ui", Force: true})
	if err != nil {
		// We can choose to not return an error if it's already gone.
		return fmt.Errorf("failed to scaledown sandbox: %w", err)
	}
	return nil
}

func getGitHubToken(ctx context.Context, repoWatch *unstructured.Unstructured) (string, error) {
	secretName, found, err := unstructured.NestedString(repoWatch.Object, "spec", "githubSecretRef", "name")
	if err != nil || !found {
		return "", fmt.Errorf("githubSecretRef.name not found in repowatch %s", repoWatch.GetName())
	}
	secretKey, found, err := unstructured.NestedString(repoWatch.Object, "spec", "githubSecretRef", "key")
	if err != nil || !found {
		return "", fmt.Errorf("githubSecretRef.key not found in repowatch %s", repoWatch.GetName())
	}

	secretGVR := schema.GroupVersionResource{Version: "v1", Resource: "secrets"}
	secretUnstructured, err := k8sClient.Resource(secretGVR).Namespace(namespace).Get(ctx, secretName, v1.GetOptions{})
	if err != nil {
		return "", err
	}

	secretData, found, err := unstructured.NestedStringMap(secretUnstructured.Object, "data")
	if err != nil || !found {
		return "", fmt.Errorf("data field not found in secret %s", secretName)
	}

	tokenBase64, ok := secretData[secretKey]
	if !ok {
		return "", fmt.Errorf("key %s not found in secret %s", secretKey, secretName)
	}

	tokenBytes, err := base64.StdEncoding.DecodeString(tokenBase64)
	if err != nil {
		return "", fmt.Errorf("failed to decode token for key %s in secret %s: %w", secretKey, secretName, err)
	}

	return string(tokenBytes), nil
}

func parseRepoURL(repoURL string) (string, string, error) {
	u, err := url.Parse(repoURL)
	if err != nil {
		return "", "", err
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) != 2 {
		return "", "", fmt.Errorf("invalid repo url: %s", repoURL)
	}
	return parts[0], parts[1], nil
}

func getRepoWatch(ctx context.Context, name string) (*unstructured.Unstructured, error) {
	gvr := schema.GroupVersionResource{
		Group:    "review.gemini.google.com",
		Version:  "v1alpha1",
		Resource: "repowatches",
	}
	repoWatch, err := k8sClient.Resource(gvr).Namespace(namespace).Get(ctx, name, v1.GetOptions{})
	if err != nil {
		return nil, err
	}
	return repoWatch, nil
}
