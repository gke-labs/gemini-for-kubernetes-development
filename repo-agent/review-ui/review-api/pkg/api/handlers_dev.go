package api

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
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

	sandboxes := []models.DevSandbox{}
	// prefix: dev:ns:NAMESPACE:repo:REPO:dev:NAME
	prefix := fmt.Sprintf("dev:ns:%s:repo:%s:dev:*", namespace, repo)
	iter := s.Redis.Scan(c.Request.Context(), 0, prefix, 0).Iterator()
	for iter.Next(c.Request.Context()) {
		key := iter.Val()
		parts := strings.Split(key, ":")
		// dev:ns:NAMESPACE:repo:REPO:dev:NAME
		// 0   1  2         3    4    5   6
		if len(parts) != 7 {
			continue
		}
		name := parts[6]

		data, err := s.Redis.HGetAll(c.Request.Context(), key).Result()
		if err != nil {
			log.Printf("Failed to get DevSandbox %s from Redis for repo %s: %v", name, repo, err)
			continue
		}

		sandbox := models.DevSandbox{
			Name: name,
		}
		if val, ok := data["sandbox"]; ok {
			sandbox.Sandbox = val
		}
		if val, ok := data["sandboxReplica"]; ok {
			sandbox.SandboxReplica = val
		}
		if val, ok := data["branchURL"]; ok {
			sandbox.BranchURL = val
		}
		if val, ok := data["branch"]; ok {
			sandbox.Branch = val
		}

		sandboxes = append(sandboxes, sandbox)
	}
	if err := iter.Err(); err != nil {
		log.Printf("Error during Redis SCAN for dev sandboxes: %v", err)
	}

	c.JSON(http.StatusOK, sandboxes)
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

			key := fmt.Sprintf("dev:ns:%s:repo:%s:dev:%s", namespace, repo, item.GetName())
			if err := s.Redis.HSet(ctx, key,
				"sandbox", item.GetName(),
				"branch", branch,
				"branchURL", branchURL,
				"sandboxReplica", fmt.Sprintf("%d", replicas),
			).Err(); err != nil {
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

	key := fmt.Sprintf("dev:ns:%s:repo:%s:dev:%s", namespace, repo, name)
	if err := s.Redis.Del(c.Request.Context(), key).Err(); err != nil {
		log.Printf("Failed to DEL DevSandbox data from Redis: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to DEL DevSandbox data from Redis"})
		return
	}

	c.Status(http.StatusOK)
}

func (s *Server) scaleUpDevSandbox(c *gin.Context) {
	namespace := c.MustGet(auth.UserKey).(string)
	name := c.Param("name")

	gvr := schema.GroupVersionResource{
		Group:    "custom.agents.x-k8s.io",
		Version:  "v1alpha1",
		Resource: "devsandboxes",
	}

	// Get the existing resource to find max replicas? Or just set to 1.
	// Usually 1.

	sandbox := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "custom.agents.x-k8s.io/v1alpha1",
			"kind":       "DevSandbox",
			"metadata": map[string]interface{}{
				"name":      name,
				"namespace": namespace,
			},
			"spec": map[string]interface{}{
				"replicas": int64(1),
			},
		},
	}

	_, err := s.K8sManager.Client.Resource(gvr).Namespace(namespace).Apply(c.Request.Context(), name,
		sandbox, v1.ApplyOptions{FieldManager: "review-ui", Force: true})
	if err != nil {
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
	c.Status(http.StatusOK)
}
