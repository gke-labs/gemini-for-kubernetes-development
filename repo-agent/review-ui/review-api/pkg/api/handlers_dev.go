package api

import (
	"context"
	"fmt"
	"hash/fnv"
	"log"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	pkgk8s "github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/k8s"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/sandbox"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/review-ui/review-api/pkg/auth"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/review-ui/review-api/pkg/models"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

func (s *Server) getDevSandboxes(c *gin.Context) {
	namespace := c.MustGet(auth.UserKey).(string)
	repo := c.Param("repo")
	s.fetchAndPopulateDevSandboxes(c.Request.Context(), namespace, repo)

	sandboxes, err := s.Store.ListDevSandboxes(c.Request.Context(), namespace, repo)
	if err != nil {
		log.Printf("Error listing dev sandboxes: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to list dev sandboxes"})
		return
	}

	c.JSON(http.StatusOK, sandboxes)
}

func (s *Server) createDevSandbox(c *gin.Context) {
	namespace := c.MustGet(auth.UserKey).(string)
	repo := c.Param("repo")

	var req struct {
		Branch string `json:"branch"`
		Prompt string `json:"prompt"`
	}
	if err := c.BindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request body"})
		return
	}

	if req.Branch == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Branch is required"})
		return
	}

	// Fetch RepoWatch to get defaults
	rw, err := s.K8sManager.GetRepoWatch(c.Request.Context(), namespace, repo)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to get RepoWatch", "details": err.Error()})
		return
	}

	repoURL, found, err := unstructured.NestedString(rw.Object, "spec", "repoURL")
	if err != nil || !found {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "RepoURL not found in RepoWatch"})
		return
	}

	repoParts := strings.Split(strings.TrimSuffix(repoURL, ".git"), "/")
	repoName := repoParts[len(repoParts)-1]

	forkCloneURL := fmt.Sprintf("https://github.com/%s/%s.git", namespace, repoName)
	forkHTMLURL := fmt.Sprintf("https://github.com/%s/%s", namespace, repoName)
	originURL := fmt.Sprintf("github.com/%s/%s.git", namespace, repoName)

	githubSecretName, _, _ := unstructured.NestedString(rw.Object, "spec", "githubSecretName")
	apiKeySecretRef, _, _ := unstructured.NestedString(rw.Object, "spec", "dev", "llm", "apiKeySecretRef")
	configdirRef, _, _ := unstructured.NestedString(rw.Object, "spec", "dev", "llm", "configdirRef")
	llmProvider, _, _ := unstructured.NestedString(rw.Object, "spec", "dev", "llm", "provider")

	// Fetch user info from secret
	var userName, userEmail string
	secret, err := s.K8sManager.Clientset.CoreV1().Secrets(namespace).Get(c.Request.Context(), pkgk8s.GithubSecretName, v1.GetOptions{})
	if err == nil && secret != nil {
		if name, ok := secret.Data["name"]; ok {
			userName = string(name)
		}
		if email, ok := secret.Data["email"]; ok {
			userEmail = string(email)
		}
	} else {
		log.Printf("Failed to get github secret for user %s: %v", namespace, err)
	}

	// Sanitize branch name for K8s resource name to match controller logic
	safeBranch := strings.ReplaceAll(req.Branch, "/", "-")
	safeBranch = strings.ReplaceAll(safeBranch, "_", "-")
	safeBranch = strings.ReplaceAll(safeBranch, ".", "-")
	safeBranch = strings.ToLower(safeBranch)

	fullSuffix := fmt.Sprintf("dev-%s-%s", repoName, safeBranch)
	h := fnv.New32a()
	h.Write([]byte(fullSuffix))
	hashedSuffix := fmt.Sprintf("%08x", h.Sum32())
	sandboxName := fmt.Sprintf("%s-dev", hashedSuffix)

	// Check if branch is in excludeBranches and remove it if so
	excludeBranches, found, err := unstructured.NestedStringSlice(rw.Object, "spec", "dev", "excludeBranches")
	if found && err == nil {
		newExclude := []string{}
		changed := false
		for _, b := range excludeBranches {
			if b == req.Branch {
				changed = true
			} else {
				newExclude = append(newExclude, b)
			}
		}

		if changed {
			if err := unstructured.SetNestedStringSlice(rw.Object, newExclude, "spec", "dev", "excludeBranches"); err != nil {
				log.Printf("Failed to update excludeBranches in local object: %v", err)
			} else {
				// Update RepoWatch in K8s
				_, updateErr := s.K8sManager.Client.Resource(schema.GroupVersionResource{
					Group:    "review.gemini.google.com",
					Version:  "v1alpha1",
					Resource: "repowatches",
				}).Namespace(namespace).Update(c.Request.Context(), rw, v1.UpdateOptions{})
				if updateErr != nil {
					log.Printf("Failed to update RepoWatch to remove excluded branch: %v", updateErr)
				}
			}
		}
	}

	gvr := schema.GroupVersionResource{
		Group:    "custom.agents.x-k8s.io",
		Version:  "v1alpha1",
		Resource: "devsandboxes",
	}

	opts := sandbox.DevSandboxOptions{
		Name:      sandboxName,
		Namespace: namespace,
		Labels: map[string]string{
			"review.gemini.google.com/repowatch": repo,
		},
		CloneURL: forkCloneURL,
		HTMLURL:  forkHTMLURL,

		Branch:      req.Branch,
		Origin:      originURL,
		PushEnabled: true,
		UserLogin:   namespace,
		UserName:    userName,
		UserEmail:   userEmail,

		LLMProvider:         llmProvider,
		LLMConfigdirRef:     configdirRef,
		LLMAPIKeySecretName: apiKeySecretRef,
		Prompt:              req.Prompt,

		GithubSecretName: githubSecretName,

		HTTPEnabled: true,
		Replicas:    1,
	}

	sb := sandbox.NewDevSandbox(opts)

	_, err = s.K8sManager.Client.Resource(gvr).Namespace(namespace).Create(c.Request.Context(), sb, v1.CreateOptions{})
	if err != nil {
		log.Printf("Failed to create DevSandbox: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create DevSandbox", "details": err.Error()})
		return
	}

	c.JSON(http.StatusCreated, gin.H{"status": "created", "name": sandboxName})
}

func (s *Server) fetchAndPopulateDevSandboxes(ctx context.Context, namespace, repo string) {
	gvr := schema.GroupVersionResource{
		Group:    "custom.agents.x-k8s.io",
		Version:  "v1alpha1",
		Resource: "devsandboxes",
	}
	list, err := s.K8sManager.Client.Resource(gvr).Namespace(namespace).List(context.Background(),
		v1.ListOptions{
			LabelSelector: fmt.Sprintf("review.gemini.google.com/repowatch=%s", repo),
		})
	if err != nil {
		log.Printf("Failed to list DevSandbox CRs: %v.", err)
		return
	}

	log.Printf("Populating DevSandboxes: Found %d devsandboxes for Repo: %s", len(list.Items), repo)
	for _, item := range list.Items {
		replicas, found, err := unstructured.NestedInt64(item.Object, "spec", "replicas")
		if err != nil || !found {
			log.Printf("Replicas (.spec.replicas) not found in DevSandbox %s", item.GetName())
			continue
		}

		branch, found, err := unstructured.NestedString(item.Object, "spec", "destination", "branch")
		if err != nil || !found {
			log.Printf("Branch (.spec.destination.branch) not found in DevSandbox %s", item.GetName())
			branch = "nobranch" // fallback
		}

		cloneURL, found, err := unstructured.NestedString(item.Object, "spec", "source", "cloneURL")
		if err != nil || !found {
			log.Printf("cloneURL (.spec.source.cloneURL) not found in DevSandbox %s", item.GetName())
			cloneURL = "https://github.com/noorg/norepo.git"
		}
		repoParts := strings.Split(strings.TrimSuffix(cloneURL, ".git"), "/")
		if len(repoParts) >= 2 {
			repoName := repoParts[len(repoParts)-1]
			owner := repoParts[len(repoParts)-2]
			// Construct branch URL: https://github.com/OWNER/REPO/tree/BRANCH
			branchURL := fmt.Sprintf("https://github.com/%s/%s/tree/%s", owner, repoName, branch)

			// Store in Redis
			// Use sandbox name as the identifier for now or the branch name?
			// The UI card key is `sandbox.name`. If we use branch name, it must be unique per repo.
			// Let's use the DevSandbox name as the key in Redis to match deletion logic.

			agentState := ""
			agentStateMessage := ""
			annotations := item.GetAnnotations()
			if annotations != nil {
				if val, ok := annotations["agentState"]; ok {
					agentState = val
				}
				if val, ok := annotations["agentStateMessage"]; ok {
					agentStateMessage = val
				}
			}

			sandbox := models.DevSandbox{
				Name:              item.GetName(),
				Sandbox:           item.GetName(),
				Branch:            branch,
				BranchURL:         branchURL,
				SandboxReplica:    fmt.Sprintf("%d", replicas),
				AgentState:        agentState,
				AgentStateMessage: agentStateMessage,
			}
			if err := s.Store.SaveDevSandbox(ctx, namespace, repo, sandbox); err != nil {
				log.Printf("Failed to cache DevSandbox %s: %v", item.GetName(), err)
			}
		}
	}
}

func (s *Server) deleteDevSandbox(c *gin.Context) {
	namespace := c.MustGet(auth.UserKey).(string)
	repo := c.Param("repo")
	name := c.Param("name") // This is the sandbox name
	ctx := c.Request.Context()

	if err := s.K8sManager.ScaledownDevSandboxHelper(ctx, namespace, name); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to delete dev sandbox", "details": err.Error()})
		return
	}

	if err := s.Store.DeleteDevSandbox(c.Request.Context(), namespace, repo, name); err != nil {
		log.Printf("Failed to DEL DevSandbox data from Redis: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to DEL DevSandbox data from Redis"})
		return
	}

	c.Status(http.StatusOK)
}

func (s *Server) scaleUpDevSandbox(c *gin.Context) {
	namespace := c.MustGet(auth.UserKey).(string)
	name := c.Param("name")

	if err := s.K8sManager.ScaleupDevSandboxHelper(c.Request.Context(), namespace, name); err != nil {
		log.Printf("Failed to scale up dev sandbox %s: %v", name, err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to scale up dev sandbox"})
		return
	}

	c.Status(http.StatusOK)
}

func (s *Server) scaleDownDevSandbox(c *gin.Context) {
	namespace := c.MustGet(auth.UserKey).(string)
	name := c.Param("name")
	if err := s.K8sManager.ScaledownDevSandboxHelper(c.Request.Context(), namespace, name); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to scale down dev sandbox", "details": err.Error()})
		return
	}

	if err := s.K8sManager.UpdateDevSandboxAnnotation(c.Request.Context(), namespace, name, "agentState", "sandbox paused"); err != nil {
		log.Printf("Failed to update dev sandbox annotation: %v", err)
	}

	c.Status(http.StatusOK)
}
