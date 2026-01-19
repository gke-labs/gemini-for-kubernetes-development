package api

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/auth"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/clients"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/models"
	"github.com/google/go-github/v39/github"
	yaml "go.yaml.in/yaml/v3"
	"k8s.io/apimachinery/pkg/api/errors"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/klog/v2"
)

func (s *Server) createRepoWatch(c *gin.Context) {
	log := klog.FromContext(c.Request.Context())
	namespace := c.MustGet(auth.UserKey).(string)

	if !s.ensureGeminiKeySet(c, namespace) {
		return
	}

	var payload struct {
		YAML string `json:"yaml"`
	}
	if err := c.ShouldBindJSON(&payload); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if payload.YAML == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "YAML content is required"})
		return
	}

	decoder := yaml.NewDecoder(strings.NewReader(payload.YAML))
	for {
		var unstructuredObj map[string]interface{}
		if err := decoder.Decode(&unstructuredObj); err != nil {
			if err == io.EOF {
				break
			}
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid YAML content"})
			return
		}

		unstructuredObj = fixYAMLIntegers(unstructuredObj).(map[string]interface{})
		obj := &unstructured.Unstructured{Object: unstructuredObj}

		// Enforce namespace
		obj.SetNamespace(namespace)

		gvk := obj.GroupVersionKind()

		// Handle RepoWatch specific logic
		if gvk.Group == "review.gemini.google.com" && gvk.Kind == "RepoWatch" {
			// Auto-populate labels if missing
			labels, found, _ := unstructured.NestedStringSlice(obj.Object, "spec", "review", "labels")
			if !found || len(labels) == 0 {
				// Ensure githubSecretName is set for getGitHubToken to work
				_, found, _ = unstructured.NestedString(obj.Object, "spec", "githubSecretName")
				if !found {
					_ = unstructured.SetNestedField(obj.Object, "github-pat", "spec", "githubSecretName")
				}

				token, tokenErr := s.K8sManager.GetGitHubToken(c.Request.Context(), obj)
				if tokenErr == nil {
					client := clients.NewGitHubClient(c.Request.Context(), token)

					repoURL, _, _ := unstructured.NestedString(obj.Object, "spec", "repoURL")
					if owner, repoName, urlErr := parseRepoURL(repoURL); urlErr == nil {
						suggested, suggestErr := getSuggestedLabels(c.Request.Context(), client, owner, repoName)
						if suggestErr == nil && len(suggested) > 0 {
							var suggestedInterface []interface{}
							for _, s := range suggested {
								var inner []interface{}
								for _, l := range s {
									inner = append(inner, l)
								}
								suggestedInterface = append(suggestedInterface, inner)
							}
							if setErr := unstructured.SetNestedSlice(obj.Object, suggestedInterface, "spec", "review", "labels"); setErr != nil {
								log.Info("Failed to set suggested labels", "err", setErr)
							}
						} else if suggestErr != nil {
							log.Info("Failed to get suggested labels", "err", suggestErr)
						}
					}
				} else {
					log.Info("Debug: Could not get token for label suggestion", "err", tokenErr)
				}
			}
		}

		// Determine GVR
		var gvr schema.GroupVersionResource
		if gvk.Group == "review.gemini.google.com" && gvk.Kind == "RepoWatch" {
			gvr = schema.GroupVersionResource{
				Group:    "review.gemini.google.com",
				Version:  "v1alpha1",
				Resource: "repowatches",
			}
		} else if gvk.Group == "configdir.gke.io" && gvk.Kind == "ConfigDir" {
			gvr = schema.GroupVersionResource{
				Group:    "configdir.gke.io",
				Version:  "v1alpha1",
				Resource: "configdirs",
			}
		} else if gvk.Group == "" && gvk.Kind == "ConfigMap" {
			gvr = schema.GroupVersionResource{
				Group:    "",
				Version:  "v1",
				Resource: "configmaps",
			}
		} else {
			log.Info("Skipping disallowed resource type", "group", gvk.Group, "kind", gvk.Kind)
			continue
		}

		_, err := s.K8sManager.Client.Resource(gvr).Namespace(namespace).Create(c.Request.Context(), obj, v1.CreateOptions{})
		if err != nil {
			if errors.IsAlreadyExists(err) {
				log.Info("Resource already exists, attempting update...", "kind", gvk.Kind, "name", obj.GetName())
				// Get existing resourceVersion
				existing, getErr := s.K8sManager.Client.Resource(gvr).Namespace(namespace).Get(c.Request.Context(), obj.GetName(), v1.GetOptions{})
				if getErr == nil {
					obj.SetResourceVersion(existing.GetResourceVersion())
					_, updateErr := s.K8sManager.Client.Resource(gvr).Namespace(namespace).Update(c.Request.Context(), obj, v1.UpdateOptions{})
					if updateErr != nil {
						log.Info("Failed to update existing resource", "kind", gvk.Kind, "err", updateErr)
					}
				} else {
					log.Info("Failed to get existing resource for update", "kind", gvk.Kind, "err", getErr)
				}
				continue
			}
			log.Info("Failed to create resource from YAML", "kind", gvk.Kind, "err", err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("Failed to create %s: %v", gvk.Kind, err)})
			return
		}
	}

	c.Status(http.StatusOK)
}

func (s *Server) getDefaultRepoWatch(c *gin.Context) {
	defaultRepoWatch := `
apiVersion: review.gemini.google.com/v1alpha1
kind: RepoWatch
metadata:
  name: change-name
spec:
  repoURL: https://github.com/gke-labs/gemini-for-kubernetes-development
  pollIntervalSeconds: 300
  githubSecretName: github-pat
  dev:
    maxActiveSandboxes: 1
    maxSandboxes: 3
    devcontainerConfigRef: devcontainer-json
    llm:
      apiKeySecretRef: gemini-vscode-tokens
  review:
    reviewShutdownAfterMinutes: 30
    devcontainerConfigRef: devcontainer-json
    llm:
      apiKeySecretRef: gemini-vscode-tokens
      prompt: >-
        You are an expert kubernetes developer who is helping with code reviews.
        Please look at the most recent commit and provide a review feedback.
        Would you approve it ?
        Please pay attention to the following:
        1. Does the fix resolve the original problem.
        2. Look for linked issues to understand the original problem.
        3. Are there tests to check the fix.
    maxActiveSandboxes: 3
    maxSandboxes: 5
  issueHandlers:
    - llm:
        apiKeySecretRef: gemini-vscode-tokens
        prompt: >-
          You are a helpful assistant that triages GitHub issues for a
          Kubernetes-related open source project.
          Your task is to categorize incoming issues based on their content and
          assign appropriate labels.
          Analyze the issue title and body to determine the most relevant
          category from the following options:
          1. bug: Issues that describe unexpected behavior, errors, or
          malfunctions in the software.
          2. feature: Suggestions for new features, enhancements, or
          improvements to existing functionality.
          2. cleanup: Suggestions for cleaning up code, removing deprecated
          features, or improving code quality.
          3. document: Issues related to documentation errors, omissions, or
          requests for clarification.
          4. support: Questions or requests for help regarding the use of the
          software.
          5. other: Any issue that does not fit into the above categories.

          Start the response with "/kind <Category>" where <Category> is one of
          bug , feature , document, support, cleanup or other
          In the next line, provide a concise explanation of your reasoning for
          the assigned category.

          Issue Title: "{{.Title}}"
          Issue Body: "{{.Body}}"
          HTML URL: "{{.HTMLURL}}"
      issueShutdownAfterMinutes: 30
      maxActiveSandboxes: 1
      maxSandboxes: 3
      name: triage
`
	c.JSON(http.StatusOK, gin.H{"yaml": defaultRepoWatch})
}

func (s *Server) getRepoWatchYAML(c *gin.Context) {
	log := klog.FromContext(c.Request.Context())
	namespace := c.MustGet(auth.UserKey).(string)
	name := c.Param("repo")

	repoWatch, err := s.K8sManager.GetRepoWatch(c.Request.Context(), namespace, name)
	if err != nil {
		log.Info("Failed to get RepoWatch", "namespace", namespace, "name", name, "err", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to get RepoWatch"})
		return
	}

	spec, found, err := unstructured.NestedMap(repoWatch.Object, "spec")
	if err != nil || !found {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to get spec from RepoWatch"})
		return
	}

	yamlBytes, err := yaml.Marshal(spec)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to marshal spec to YAML"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"yaml": string(yamlBytes)})
}

func (s *Server) updateRepoWatch(c *gin.Context) {
	log := klog.FromContext(c.Request.Context())
	namespace := c.MustGet(auth.UserKey).(string)
	name := c.Param("repo")

	var payload struct {
		RepoURL       string `json:"repoURL"`
		AddPR         int    `json:"addPR"`
		ExcludePR     int    `json:"excludePR"`
		ExcludeIssue  int    `json:"excludeIssue"`
		HandlerName   string `json:"handlerName"`
		ExcludeBranch string `json:"excludeBranch"`
		YAML          string `json:"yaml"`
	}
	if err := c.ShouldBindJSON(&payload); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if payload.AddPR == 0 && payload.ExcludePR == 0 && payload.ExcludeIssue == 0 && payload.YAML == "" && payload.ExcludeBranch == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "addPR, excludePR, excludeIssue, excludeBranch or yaml is required"})
		return
	}

	gvr := schema.GroupVersionResource{
		Group:    "review.gemini.google.com",
		Version:  "v1alpha1",
		Resource: "repowatches",
	}

	// Get existing resource
	existing, err := s.K8sManager.Client.Resource(gvr).Namespace(namespace).Get(c.Request.Context(), name, v1.GetOptions{})
	if err != nil {
		log.Info("Failed to get RepoWatch for update", "err", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("Failed to get RepoWatch: %v", err)})
		return
	}

	if payload.YAML != "" {
		var spec map[string]interface{}
		if err := yaml.Unmarshal([]byte(payload.YAML), &spec); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid YAML content"})
			return
		}

		spec = fixYAMLIntegers(spec).(map[string]interface{})

		if err := unstructured.SetNestedField(existing.Object, spec, "spec"); err != nil {
			log.Info("Failed to set spec", "err", err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update object structure"})
			return
		}

		// If repoURL changed in YAML, we should update Store too, but strictly speaking
		// we should extract it from the new spec.
		if newURL, found, _ := unstructured.NestedString(existing.Object, "spec", "repoURL"); found {
			if err := s.Store.SaveRepo(c.Request.Context(), namespace, name, newURL); err != nil {
				log.Info("Failed to update repo URL in Store", "name", name, "err", err)
			}
		}
	}

	// Add PR if provided
	if payload.AddPR != 0 {
		pullRequestsSlice, found, err := unstructured.NestedSlice(existing.Object, "spec", "review", "pullRequests")
		if err != nil {
			log.Info("Failed to get pullRequests", "err", err)
		}

		var pullRequests []int64
		if found {
			for _, v := range pullRequestsSlice {
				if i, ok := v.(int64); ok {
					pullRequests = append(pullRequests, i)
				} else if i, ok := v.(int); ok {
					pullRequests = append(pullRequests, int64(i))
				}
			}
		}

		// Check for duplicates
		exists := false
		for _, pr := range pullRequests {
			if pr == int64(payload.AddPR) {
				exists = true
				break
			}
		}

		if !exists {
			pullRequests = append(pullRequests, int64(payload.AddPR))
			if err := unstructured.SetNestedSlice(existing.Object, convInt64SliceToInterfaceSlice(pullRequests), "spec", "review", "pullRequests"); err != nil {
				log.Info("Failed to set pullRequests", "err", err)
				c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update object structure for pullRequests"})
				return
			}
		}

		// Remove from ExcludePullRequests if present
		excludeSlice, found, _ := unstructured.NestedSlice(existing.Object, "spec", "review", "excludePullRequests")
		if found {
			var newExclude []int64
			changed := false
			for _, v := range excludeSlice {
				val := int64(0)
				if i, ok := v.(int64); ok {
					val = i
				} else if i, ok := v.(int); ok {
					val = int64(i)
				}

				if val != int64(payload.AddPR) {
					newExclude = append(newExclude, val)
				} else {
					changed = true
				}
			}
			if changed {
				if err := unstructured.SetNestedSlice(existing.Object, convInt64SliceToInterfaceSlice(newExclude), "spec", "review", "excludePullRequests"); err != nil {
					log.Info("Failed to update excludePullRequests", "err", err)
				}
			}
		}
	}

	// Exclude Issue if provided
	if payload.ExcludeIssue != 0 && payload.HandlerName != "" {
		handlersSlice, found, err := unstructured.NestedSlice(existing.Object, "spec", "issueHandlers")
		if err != nil {
			log.Info("Failed to get issueHandlers", "err", err)
		}

		if found {
			var newHandlers []interface{}
			updated := false
			for _, h := range handlersSlice {
				handlerMap, ok := h.(map[string]interface{})
				if !ok {
					newHandlers = append(newHandlers, h)
					continue
				}

				name, _ := handlerMap["name"].(string)
				if name == payload.HandlerName {
					// Found the handler, update excludeIssues
					excludeSlice, _, _ := unstructured.NestedSlice(handlerMap, "excludeIssues")

					var excludeIssues []int64
					for _, v := range excludeSlice {
						if i, ok := v.(int64); ok {
							excludeIssues = append(excludeIssues, i)
						} else if i, ok := v.(int); ok {
							excludeIssues = append(excludeIssues, int64(i))
						}
					}

					exists := false
					for _, issue := range excludeIssues {
						if issue == int64(payload.ExcludeIssue) {
							exists = true
							break
						}
					}

					if !exists {
						excludeIssues = append(excludeIssues, int64(payload.ExcludeIssue))
						// Update the handler map
						if err := unstructured.SetNestedSlice(handlerMap, convInt64SliceToInterfaceSlice(excludeIssues), "excludeIssues"); err != nil {
							log.Info("Failed to set excludeIssues", "err", err)
						} else {
							updated = true
						}
					}
				}
				newHandlers = append(newHandlers, handlerMap)
			}

			if updated {
				if err := unstructured.SetNestedSlice(existing.Object, newHandlers, "spec", "issueHandlers"); err != nil {
					log.Info("Failed to update issueHandlers", "err", err)
					c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update object structure for issueHandlers"})
					return
				}
			}
		}
	}

	// Exclude PR if provided
	if payload.ExcludePR != 0 {
		// Add to excludePullRequests
		excludeSlice, found, err := unstructured.NestedSlice(existing.Object, "spec", "review", "excludePullRequests")
		if err != nil {
			log.Info("Failed to get excludePullRequests", "err", err)
		}

		var excludeRequests []int64
		if found {
			for _, v := range excludeSlice {
				if i, ok := v.(int64); ok {
					excludeRequests = append(excludeRequests, i)
				} else if i, ok := v.(int); ok {
					excludeRequests = append(excludeRequests, int64(i))
				}
			}
		}

		exists := false
		for _, pr := range excludeRequests {
			if pr == int64(payload.ExcludePR) {
				exists = true
				break
			}
		}

		if !exists {
			excludeRequests = append(excludeRequests, int64(payload.ExcludePR))
			if err := unstructured.SetNestedSlice(existing.Object, convInt64SliceToInterfaceSlice(excludeRequests), "spec", "review", "excludePullRequests"); err != nil {
				log.Info("Failed to set excludePullRequests", "err", err)
				c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update object structure for excludePullRequests"})
				return
			}
		}

		// Remove from PullRequests if present
		pullSlice, found, _ := unstructured.NestedSlice(existing.Object, "spec", "review", "pullRequests")
		if found {
			var newPull []int64
			changed := false
			for _, v := range pullSlice {
				val := int64(0)
				if i, ok := v.(int64); ok {
					val = i
				} else if i, ok := v.(int); ok {
					val = int64(i)
				}

				if val != int64(payload.ExcludePR) {
					newPull = append(newPull, val)
				} else {
					changed = true
				}
			}
			if changed {
				if err := unstructured.SetNestedSlice(existing.Object, convInt64SliceToInterfaceSlice(newPull), "spec", "review", "pullRequests"); err != nil {
					log.Info("Failed to update pullRequests", "err", err)
				}
			}
		}
	}

	// Exclude Branch if provided
	if payload.ExcludeBranch != "" {
		excludeSlice, found, err := unstructured.NestedStringSlice(existing.Object, "spec", "dev", "excludeBranches")
		if err != nil {
			log.Info("Failed to get excludeBranches", "err", err)
		}

		var excludeBranches []string
		if found {
			excludeBranches = excludeSlice
		}

		exists := false
		for _, b := range excludeBranches {
			if b == payload.ExcludeBranch {
				exists = true
				break
			}
		}

		if !exists {
			excludeBranches = append(excludeBranches, payload.ExcludeBranch)
			if err := unstructured.SetNestedStringSlice(existing.Object, excludeBranches, "spec", "dev", "excludeBranches"); err != nil {
				log.Info("Failed to set excludeBranches", "err", err)
				c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update object structure for excludeBranches"})
				return
			}
		}
	}

	// Apply update
	_, err = s.K8sManager.Client.Resource(gvr).Namespace(namespace).Update(c.Request.Context(), existing, v1.UpdateOptions{})
	if err != nil {
		log.Info("Failed to update RepoWatch", "err", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("Failed to update RepoWatch: %v", err)})
		return
	}

	c.Status(http.StatusOK)
}

func (s *Server) deleteRepoWatch(c *gin.Context) {
	log := klog.FromContext(c.Request.Context())
	namespace := c.MustGet(auth.UserKey).(string)
	name := c.Param("repo")

	gvr := schema.GroupVersionResource{
		Group:    "review.gemini.google.com",
		Version:  "v1alpha1",
		Resource: "repowatches",
	}

	err := s.K8sManager.Client.Resource(gvr).Namespace(namespace).Delete(c.Request.Context(), name, v1.DeleteOptions{})
	if err != nil {
		log.Info("Failed to delete RepoWatch", "err", err)
		if !errors.IsNotFound(err) {
			c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("Failed to delete RepoWatch: %v", err)})
			return
		}
	}

	// Also delete from Store
	if err := s.Store.DeleteRepo(c.Request.Context(), namespace, name); err != nil {
		log.Info("Failed to delete repo from Store", "name", name, "err", err)
		// Don't fail the request if Store fails, as K8s deletion is the source of truth
	}

	c.Status(http.StatusOK)
}

func (s *Server) getRepos(c *gin.Context) {
	log := klog.FromContext(c.Request.Context())
	namespace := c.MustGet(auth.UserKey).(string)
	s.fetchAndPopulateRepos(c.Request.Context(), namespace)

	repos := []models.Repo{}
	repoNames, err := s.Store.ListRepos(c.Request.Context(), namespace)
	if err != nil {
		log.Info("Error during Store ListRepos", "err", err)
	}

	for _, repoName := range repoNames {
		repoWatch, err := s.K8sManager.GetRepoWatch(c.Request.Context(), namespace, repoName)
		if err != nil {
			log.Info("Failed to get RepoWatch", "namespace", namespace, "name", repoName, "err", err)
			continue
		}

		repoURL, found, _ := unstructured.NestedString(repoWatch.Object, "spec", "repoURL")
		if !found {
			log.Info("repoURL not found in RepoWatch CR", "name", repoWatch.GetName())
			continue
		}

		repo := models.Repo{
			Name:      repoName,
			Namespace: namespace,
			URL:       repoURL,
		}

		// Extract review config
		if maxActiveSandboxes, found, err := unstructured.NestedInt64(repoWatch.Object, "spec", "review", "maxActiveSandboxes"); err == nil && found && maxActiveSandboxes > 0 {
			repo.Review = &models.ReviewConfig{MaxActiveSandboxes: maxActiveSandboxes}
			if assignees, found, err := unstructured.NestedStringSlice(repoWatch.Object, "spec", "review", "assignees"); err == nil && found {
				repo.Review.Assignees = assignees
			}
		}

		// Extract PendingPRs
		if pendingPRsSlice, found, err := unstructured.NestedSlice(repoWatch.Object, "status", "pendingPRs"); err == nil && found {
			var pendingPRs []models.PendingPR
			var prNumbers []int64
			for _, v := range pendingPRsSlice {
				if i, ok := v.(int64); ok {
					prNumbers = append(prNumbers, i)
				} else if i, ok := v.(int); ok {
					prNumbers = append(prNumbers, int64(i))
				}
			}

			if len(prNumbers) > 0 {
				// Sort and limit to top 10
				sort.Slice(prNumbers, func(i, j int) bool {
					return prNumbers[i] > prNumbers[j]
				})
				if len(prNumbers) > 10 {
					prNumbers = prNumbers[:10]
				}

				// Try to get GitHub client to fetch titles
				token, tokenErr := s.K8sManager.GetGitHubToken(c.Request.Context(), repoWatch)
				var client *github.Client
				if tokenErr == nil {
					client = clients.NewGitHubClient(c.Request.Context(), token)
				}

				owner, repoName, urlErr := parseRepoURL(repoURL)

				for _, prNum := range prNumbers {
					pendingPR := models.PendingPR{Number: prNum}
					// Only fetch title if we have a client and valid repo info
					if client != nil && urlErr == nil {
						pr, _, err := client.PullRequests.Get(c.Request.Context(), owner, repoName, int(prNum))
						if err == nil {
							pendingPR.Title = pr.GetTitle()
							pendingPR.HTMLURL = pr.GetHTMLURL()
						} else {
							log.Info("Failed to get PR title", "prNumber", prNum, "err", err)
						}
					}
					pendingPRs = append(pendingPRs, pendingPR)
				}
			}
			repo.PendingPRs = pendingPRs
		}

		// Extract ExcludePullRequests
		if excludePRsSlice, found, err := unstructured.NestedSlice(repoWatch.Object, "spec", "review", "excludePullRequests"); err == nil && found {
			var excludePRs []int64
			for _, v := range excludePRsSlice {
				if i, ok := v.(int64); ok {
					excludePRs = append(excludePRs, i)
				} else if i, ok := v.(int); ok {
					excludePRs = append(excludePRs, int64(i))
				}
			}
			repo.ExcludePullRequests = excludePRs
		}

		// Extract dev config
		if maxActiveSandboxes, found, err := unstructured.NestedInt64(repoWatch.Object, "spec", "dev", "maxActiveSandboxes"); err == nil && found && maxActiveSandboxes > 0 {
			repo.Dev = &models.DevConfig{MaxActiveSandboxes: maxActiveSandboxes}
		}

		// Extract PendingDevBranches
		if pendingBranchesSlice, found, err := unstructured.NestedStringSlice(repoWatch.Object, "status", "pendingDevBranches"); err == nil && found {
			repo.PendingDevBranches = pendingBranchesSlice
		}

		// Extract ExcludeBranches
		if excludeBranchesSlice, found, err := unstructured.NestedStringSlice(repoWatch.Object, "spec", "dev", "excludeBranches"); err == nil && found {
			repo.ExcludeBranches = excludeBranchesSlice
		}

		// Extract issue handlers
		if handlers, found, err := unstructured.NestedSlice(repoWatch.Object, "spec", "issueHandlers"); err == nil && found {
			var issueHandlers []models.IssueHandler
			for _, h := range handlers {
				handlerMap, ok := h.(map[string]interface{})
				if !ok {
					continue
				}
				name, _ := handlerMap["name"].(string)
				maxActiveSandboxes, _ := handlerMap["maxActiveSandboxes"].(int64)
				pushBranch, _ := handlerMap["pushBranch"].(bool)

				if maxActiveSandboxes > 0 {
					issueHandlers = append(issueHandlers, models.IssueHandler{
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

	c.JSON(http.StatusOK, repos)
}

func (s *Server) fetchAndPopulateRepos(ctx context.Context, namespace string) {
	if !s.Store.RequiresPopulate() {
		return
	}
	log := klog.FromContext(ctx)
	gvr := schema.GroupVersionResource{
		Group:    "review.gemini.google.com",
		Version:  "v1alpha1",
		Resource: "repowatches",
	}
	list, err := s.K8sManager.Client.Resource(gvr).Namespace(namespace).List(context.Background(), v1.ListOptions{})
	if err != nil {
		log.Info("Failed to list RepoWatch CRs. Serving mock data.", "err", err)
		return
	}

	for _, item := range list.Items {
		repoURL, found, err := unstructured.NestedString(item.Object, "spec", "repoURL")
		if err != nil || !found {
			log.Info("repoURL not found in RepoWatch CR", "name", item.GetName())
			continue
		}
		// Ensure the URL is in Store
		if err := s.Store.SaveRepo(ctx, namespace, item.GetName(), repoURL); err != nil {
			log.Info("Failed to cache repo URL", "name", item.GetName(), "err", err)
		}
	}
}

func (s *Server) getTemplates(c *gin.Context) {
	log := klog.FromContext(c.Request.Context())
	namespace := c.MustGet(auth.UserKey).(string)
	templates, err := s.Templates.List(c.Request.Context(), namespace)
	if err != nil {
		log.Info("Failed to list templates", "err", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to list templates"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"templates": templates})
}
