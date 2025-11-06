package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	redis "github.com/go-redis/redis/v8"

	//"github.com/google/go-github/github"
	"github.com/google/go-github/v39/github"
	yaml "go.yaml.in/yaml/v3"
	"golang.org/x/oauth2"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

var (
	rdb       *redis.Client
	k8sClient dynamic.Interface
)

// AgentOutput defines the structure for the agent's YAML output.
type AgentOutput struct {
	Note   string                           `yaml:"note"`
	Review *github.PullRequestReviewRequest `yaml:"review"`
}

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

// Issue represents a GitHub issue
type Issue struct {
	ID             string `json:"id"`
	Title          string `json:"title"`
	Draft          string `json:"draft,omitempty"`
	Sandbox        string `json:"sandbox,omitempty"`
	SandboxReplica string `json:"sandboxReplica,omitempty"`
	Comment        string `json:"comment,omitempty"`
	HTMLURL        string `json:"htmlURL,omitempty"`
	BranchURL      string `json:"branchURL,omitempty"`
	PushBranch     bool   `json:"pushBranch"`
}

// Repo represents a repository with its configuration
type Repo struct {
	Name          string         `json:"name"`
	Namespace     string         `json:"namespace"`
	URL           string         `json:"url"`
	Review        *ReviewConfig  `json:"review,omitempty"`
	IssueHandlers []IssueHandler `json:"issueHandlers,omitempty"`
}

// ReviewConfig holds configuration for PR reviews
type ReviewConfig struct {
	MaxActiveSandboxes int64 `json:"maxActiveSandboxes"`
}

// IssueHandler holds configuration for an issue handler
type IssueHandler struct {
	Name               string `json:"name"`
	MaxActiveSandboxes int64  `json:"maxActiveSandboxes"`
	PushBranch         bool   `json:"pushBranch"`
}

type bodyLogWriter struct {
	gin.ResponseWriter
	body *bytes.Buffer
}

func (w bodyLogWriter) Write(b []byte) (int, error) {
	w.body.Write(b)
	return w.ResponseWriter.Write(b)
}

func RequestLoggerMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		// Read the request body
		var bodyBytes []byte
		if c.Request.Body != nil {
			bodyBytes, _ = io.ReadAll(c.Request.Body)
			// Restore the io.ReadCloser to its original state for subsequent handlers
			c.Request.Body = io.NopCloser(bytes.NewBuffer(bodyBytes))
		}

		log.Printf("Request Method: %s\n", c.Request.Method)
		log.Printf("Request URL: %s\n", c.Request.URL.String())
		//log.Printf("Request Headers: %v\n", c.Request.Header)
		log.Printf("Request Body: %s\n", string(bodyBytes))

		c.Next() // Process the request further
	}
}

func ResponseLoggerMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		blw := &bodyLogWriter{body: bytes.NewBufferString(""), ResponseWriter: c.Writer}
		c.Writer = blw

		c.Next() // Process the request and generate the response

		log.Printf("Response Status: %d\n", c.Writer.Status())
		log.Printf("Response Headers: %v\n", c.Writer.Header())
		log.Printf("Response Body: %s\n", blw.body.String())
	}
}

func main() {
	// Redis client
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
		log.Printf("Failed to get in-cluster config, trying local config: %v", err)
		kubeconfig := os.Getenv("KUBECONFIG")
		if kubeconfig == "" {
			kubeconfig = os.Getenv("HOME") + "/.kube/config"
		}
		config, err = clientcmd.BuildConfigFromFlags("", kubeconfig)
		if err != nil {
			log.Fatalf("Failed to get local kubeconfig: %v", err)
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

	// Add middleware to log requests and responses
	router.Use(RequestLoggerMiddleware())
	router.Use(ResponseLoggerMiddleware())

	// API routes
	api := router.Group("/api")
	{
		api.GET("/repos", getRepos)
		api.GET("/repo/:namespace/:repo/prs", getPRs)
		api.POST("/repo/:namespace/:repo/prs/:id/draft", saveDraft)
		api.POST("/repo/:namespace/:repo/prs/:id/submitreview", submitReview)
		api.DELETE("/repo/:namespace/:repo/prs/:id", deletePR)
		api.GET("/repo/:namespace/:repo/issues/:handler", getIssues)
		api.POST("/repo/:namespace/:repo/issues/:issue_id/handler/:handler/draft", saveIssueDraft)
		api.POST("/repo/:namespace/:repo/issues/:issue_id/handler/:handler/submitcomment", submitIssueComment)
		api.DELETE("/repo/:namespace/:repo/issues/:issue_id/handler/:handler", deleteIssue)
	}

	err = router.Run(":8080")
	if err != nil {
		log.Fatalf("Failed to start router: %v", err)
	}
}

func populateMockData() {
	ctx := context.Background()
	mockRepos := []struct {
		Name string
		URL  string
	}{
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

	repos := []Repo{}
	iter := rdb.Scan(c.Request.Context(), 0, "repo:*", 0).Iterator()
	for iter.Next(c.Request.Context()) {
		key := iter.Val()
		repoName := key[len("repo:"):]
		namespace, err := rdb.HGet(c.Request.Context(), key, "namespace").Result()
		if err != nil {
			log.Printf("Failed to get namespace for repo %s from Redis: %v", repoName, err)
			continue
		}

		repoWatch, err := getRepoWatch(c.Request.Context(), namespace, repoName)
		if err != nil {
			log.Printf("Failed to get RepoWatch %s/%s: %v", namespace, repoName, err)
			continue
		}

		repoURL, found, err := unstructured.NestedString(repoWatch.Object, "spec", "repoURL")
		if err != nil || !found {
			log.Printf("repoURL not found in RepoWatch CR %s", repoWatch.GetName())
			continue
		}

		repo := Repo{
			Name:      repoName,
			Namespace: namespace,
			URL:       repoURL,
		}

		// Extract review config
		if maxActiveSandboxes, found, err := unstructured.NestedInt64(repoWatch.Object, "spec", "review", "maxActiveSandboxes"); err == nil && found && maxActiveSandboxes > 0 {
			repo.Review = &ReviewConfig{MaxActiveSandboxes: maxActiveSandboxes}
		}

		// Extract issue handlers
		if handlers, found, err := unstructured.NestedSlice(repoWatch.Object, "spec", "issueHandlers"); err == nil && found {
			var issueHandlers []IssueHandler
			for _, h := range handlers {
				handlerMap, ok := h.(map[string]interface{})
				if !ok {
					continue
				}
				name, _ := handlerMap["name"].(string)
				maxActiveSandboxes, _ := handlerMap["maxActiveSandboxes"].(int64)
				pushBranch, _ := handlerMap["pushBranch"].(bool)

				if maxActiveSandboxes > 0 {
					issueHandlers = append(issueHandlers, IssueHandler{
						Name:               name,
						MaxActiveSandboxes: maxActiveSandboxes,
						PushBranch:         pushBranch,
					})
				}
			}
			repo.IssueHandlers = issueHandlers
		}

		repos = append(repos, repo)
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
	list, err := k8sClient.Resource(gvr).List(context.Background(), v1.ListOptions{})
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
		repo := struct {
			Name      string
			Namespace string
			URL       string
		}{
			Name:      item.GetName(),
			Namespace: item.GetNamespace(),
			URL:       repoURL,
		}

		// Ensure the URL is in Redis
		if err := rdb.HSet(ctx, fmt.Sprintf("repo:%s", repo.Name), "url", repo.URL, "namespace", repo.Namespace).Err(); err != nil {
			log.Printf("Failed to cache repo URL for %s: %v", repo.Name, err)
		}
	}
}

func getPRs(c *gin.Context) {
	namespace := c.Param("namespace")
	repo := c.Param("repo")
	fetchAndPopulatePRs(c.Request.Context(), namespace, repo)
	// SCAN Redis for PRs for repo
	prs := []PR{}
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

func fetchAndPopulatePRs(ctx context.Context, namespace, repo string) {
	gvr := schema.GroupVersionResource{
		Group:    "custom.agents.x-k8s.io",
		Version:  "v1alpha1",
		Resource: "reviewsandboxes",
	}
	list, err := k8sClient.Resource(gvr).Namespace(namespace).List(context.Background(),
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
			"draft", draft,
			"agentDraft", draft,
		).Err(); err != nil {
			log.Printf("Failed to cache PR %s for repo %s: %v", pr.ID, repo, err)
		}
	}
}

func saveDraft(c *gin.Context) {
	//namespace := c.Param("namespace")
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
	namespace := c.Param("namespace")
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

	// Get draft and agentDraft from Redis
	prKey := fmt.Sprintf("pr:repo:%s:pr:%s", repo, prID)
	prData, err := rdb.HGetAll(ctx, prKey).Result()
	if err != nil {
		log.Printf("Failed to get PR %s from Redis for repo %s: %v", prID, repo, err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to get PR data from Redis"})
		return
	}

	draft := payload.Review
	agentDraft := prData["agentDraft"]

	// Get RepoWatch to get repoURL and secret ref
	repoWatch, err := getRepoWatch(ctx, namespace, repo)
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
		if err := rdb.HSet(ctx, hfKey,
			"draft", draft,
			"agentDraft", agentDraft,
			"prompt", prompt,
			"configdir", configdir,
		).Err(); err != nil {
			log.Printf("Failed to store feedback for PR %s in repo %s: %v", prID, repo, err)
			// Continue without failing the review submission
		}

		// Update the userDraft in the ReviewSandbox status
		if err := updateReviewSandboxUserDraft(ctx, namespace, prData["sandbox"], draft); err != nil {
			log.Printf("Failed to update reviewsandbox userDraft for PR %s in repo %s: %v", prID, repo, err)
			// Not failing the request for this, just logging.
		}
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

	// https://docs.github.com/en/rest/pulls/reviews?apiVersion=2022-11-28#create-a-review-for-a-pull-request
	// Try Unmarshalling the yaml review payload into PullRequestReviewRequest
	agentOutput := &AgentOutput{}
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
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create review on github"})
		return
	}
	log.Printf("review created: %v", review)
	// Set review in Redis
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
	err = scaledownSandbox(ctx, namespace, repo, prID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to scaledown Sandbox after review submission", "details": err.Error()})
		return
	}

	c.Status(http.StatusOK)
}

func deletePR(c *gin.Context) {
	namespace := c.Param("namespace")
	repo := c.Param("repo")
	prID := c.Param("id")
	ctx := c.Request.Context()

	if err := scaledownSandbox(ctx, namespace, repo, prID); err != nil {
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
func deleteSandbox(ctx context.Context, namespace, repo, prID string) error {
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

func scaledownSandbox(ctx context.Context, namespace, repo, prID string) error {
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

func updateReviewSandboxUserDraft(ctx context.Context, namespace, sandboxName, userDraft string) error {
	gvr := schema.GroupVersionResource{
		Group:    "custom.agents.x-k8s.io",
		Version:  "v1alpha1",
		Resource: "reviewsandboxes",
	}

	// Get the existing resource
	sandbox, err := k8sClient.Resource(gvr).Namespace(namespace).Get(ctx, sandboxName, v1.GetOptions{})
	if err != nil {
		return fmt.Errorf("failed to get reviewsandbox %s: %w", sandboxName, err)
	}

	// update the annotation[agentDraft]
	if sandbox.GetAnnotations() == nil {
		sandbox.SetAnnotations(make(map[string]string))
	}
	annotations := sandbox.GetAnnotations()
	annotations["userDraft"] = userDraft
	sandbox.SetAnnotations(annotations)

	_, err = k8sClient.Resource(gvr).Namespace(namespace).Update(context.TODO(), sandbox, v1.UpdateOptions{})
	if err != nil {
		return fmt.Errorf("failed to update reviewsandbox annotation: %w", err)
	}

	return nil
}

func getGitHubToken(ctx context.Context, repoWatch *unstructured.Unstructured) (string, error) {
	secretName, found, err := unstructured.NestedString(repoWatch.Object, "spec", "githubSecretName")
	if err != nil || !found {
		return "", fmt.Errorf("githubSecretName not found in repowatch %s", repoWatch.GetName())
	}
	secretKey := "pat"

	secretGVR := schema.GroupVersionResource{Version: "v1", Resource: "secrets"}
	secretUnstructured, err := k8sClient.Resource(secretGVR).Namespace(repoWatch.GetNamespace()).Get(ctx, secretName, v1.GetOptions{})
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

func getRepoWatch(ctx context.Context, namespace, name string) (*unstructured.Unstructured, error) {
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

func getIssues(c *gin.Context) {
	namespace := c.Param("namespace")
	repo := c.Param("repo")
	handler := c.Param("handler")
	fetchAndPopulateIssues(c.Request.Context(), namespace, repo, handler)

	issues := []Issue{}
	issueKeyPrefix := fmt.Sprintf("issue:repo:%s:handler:%s:issue:*", repo, handler)
	iter := rdb.Scan(c.Request.Context(), 0, issueKeyPrefix, 0).Iterator()
	for iter.Next(c.Request.Context()) {
		key := iter.Val()
		// key is issue:repo:REPO:handler:HANDLER:issue:ISSUEID
		parts := strings.Split(key, ":")
		if len(parts) != 7 {
			continue
		}
		issueID := parts[6]

		issueData, err := rdb.HGetAll(c.Request.Context(), key).Result()
		if err != nil {
			log.Printf("Failed to get Issue %s from Redis for repo %s handler %s: %v", issueID, repo, handler, err)
			continue
		}
		pushBranch, _ := strconv.ParseBool(issueData["pushBranch"])
		issue := Issue{
			ID:         issueID,
			Title:      issueData["title"],
			PushBranch: pushBranch,
		}

		if val, ok := issueData["htmlurl"]; ok {
			issue.HTMLURL = val
		}
		if val, ok := issueData["draft"]; ok {
			issue.Draft = val
		}
		if val, ok := issueData["sandbox"]; ok {
			issue.Sandbox = val
		}
		if val, ok := issueData["sandboxReplica"]; ok {
			issue.SandboxReplica = val
		}
		if val, ok := issueData["comment"]; ok {
			issue.Comment = val
		}
		if val, ok := issueData["branchURL"]; ok {
			issue.BranchURL = val
		}

		issues = append(issues, issue)
	}
	if err := iter.Err(); err != nil {
		log.Printf("Error during Redis SCAN for issues: %v", err)
	}

	c.JSON(http.StatusOK, issues)
}

func fetchAndPopulateIssues(ctx context.Context, namespace, repo, handler string) {
	gvr := schema.GroupVersionResource{
		Group:    "custom.agents.x-k8s.io",
		Version:  "v1alpha1",
		Resource: "issuesandboxes",
	}
	list, err := k8sClient.Resource(gvr).Namespace(namespace).List(context.Background(),
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

		draft, found, err := unstructured.NestedString(item.Object, "status", "agentDraft")
		if err != nil || !found {
			log.Printf("pushBranch (.status.agentDraft) not found in IssueSandbox %s", item.GetName())
		}

		issueKey := fmt.Sprintf("issue:repo:%s:handler:%s:issue:%s", repo, handler, issueID)
		if err := rdb.HSet(ctx, issueKey,
			"title", title,
			"sandbox", item.GetName(),
			"htmlurl", htmlurl,
			"sandboxReplica", fmt.Sprintf("%d", replicas),
			"branchURL", branchURL,
			"draft", draft,
			"agentDraft", draft,
			"pushBranch", strconv.FormatBool(pushBranch),
		).Err(); err != nil {
			log.Printf("Failed to cache Issue %s for repo %s handler %s: %v", issueID, repo, handler, err)
		}
	}
}

func saveIssueDraft(c *gin.Context) {
	//namespace := c.Param("namespace")
	repo := c.Param("repo")
	issueID := c.Param("issue_id")
	handler := c.Param("handler")
	var payload struct {
		Draft string `json:"draft"`
	}
	if err := c.ShouldBindJSON(&payload); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	issueKey := fmt.Sprintf("issue:repo:%s:handler:%s:issue:%s", repo, handler, issueID)
	err := rdb.HSet(c.Request.Context(), issueKey, "draft", payload.Draft).Err()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to save draft"})
		return
	}

	c.Status(http.StatusOK)
}

func submitIssueComment(c *gin.Context) {
	namespace := c.Param("namespace")
	repo := c.Param("repo")
	issueID := c.Param("issue_id")
	handler := c.Param("handler")
	var payload struct {
		Comment string `json:"comment"`
	}
	if err := c.ShouldBindJSON(&payload); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	ctx := c.Request.Context()
	log.Printf("Submitting comment for Issue %s in repo %s with comment: %s", issueID, repo, payload.Comment)

	issueKey := fmt.Sprintf("issue:repo:%s:handler:%s:issue:%s", repo, handler, issueID)
	issueData, err := rdb.HGetAll(ctx, issueKey).Result()
	if err != nil {
		log.Printf("Failed to get Issue %s from Redis for repo %s handler %s: %v", issueID, repo, handler, err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to get Issue data from Redis"})
		return
	}

	draft := payload.Comment
	agentDraft := issueData["agentDraft"]

	repoWatch, err := getRepoWatch(ctx, namespace, repo)
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

		hfKey := fmt.Sprintf("hf:issue:githubuser:%s:repo:%s:handler:%s:pr:%s", owner, repo, handler, issueID)
		if err := rdb.HSet(ctx, hfKey,
			"draft", draft,
			"agentDraft", agentDraft,
			"prompt", prompt,
			"configdirname", configdir,
		).Err(); err != nil {
			log.Printf("Failed to store feedback for Issue %s in repo %s: %v", issueID, repo, err)
			// Continue without failing the comment submission
		}
	}

	token, err := getGitHubToken(ctx, repoWatch)
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

	err = rdb.HSet(c.Request.Context(), issueKey, "comment", payload.Comment).Err()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to save comment", "details": err.Error()})
		return
	}

	err = rdb.HSet(c.Request.Context(), issueKey, "draft", "").Err()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to clear draft", "details": err.Error()})
		return
	}

	err = scaledownIssueSandbox(ctx, namespace, repo, issueID, handler)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to scaledown Sandbox after comment submission", "details": err.Error()})
		return
	}

	c.Status(http.StatusOK)
}

func scaledownIssueSandbox(ctx context.Context, namespace, repo, issueID, handler string) error {
	sandboxName := fmt.Sprintf("%s-issue-%s-%s", repo, issueID, handler)

	gvr := schema.GroupVersionResource{
		Group:    "custom.agents.x-k8s.io",
		Version:  "v1alpha1",
		Resource: "issuesandboxes",
	}
	log.Printf("Scaling down issue sandbox %s", sandboxName)

	sandbox := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "custom.agents.x-k8s.io/v1alpha1",
			"kind":       "IssueSandbox",
			"metadata": map[string]interface{}{
				"name":      sandboxName,
				"namespace": namespace,
			},
			"spec": map[string]interface{}{
				"replicas": int64(0),
			},
		},
	}

	_, err := k8sClient.Resource(gvr).Namespace(namespace).Apply(ctx, sandboxName,
		sandbox, v1.ApplyOptions{FieldManager: "review-ui", Force: true})
	if err != nil {
		return fmt.Errorf("failed to scaledown issue sandbox: %w", err)
	}
	return nil
}

func deleteIssue(c *gin.Context) {
	namespace := c.Param("namespace")
	repo := c.Param("repo")
	issueID := c.Param("issue_id")
	handler := c.Param("handler")
	ctx := c.Request.Context()

	if err := scaledownIssueSandbox(ctx, namespace, repo, issueID, handler); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to delete sandbox", "details": err.Error()})
		return
	}

	issueKey := fmt.Sprintf("issue:repo:%s:handler:%s:issue:%s", repo, handler, issueID)
	if err := rdb.Del(c.Request.Context(), issueKey).Err(); err != nil {
		log.Printf("Failed to DEL Issue data from Redis: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to DEL Issue data from Redis"})
		return
	}

	c.Status(http.StatusOK)
}
