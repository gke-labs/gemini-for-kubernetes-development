package api

import (
	"context"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"net/http"
	"net/http/httputil"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	pkgk8s "github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/k8s"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/models"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/sandbox"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/klog/v2"
)

func (s *Server) getDevSandboxes(c *gin.Context) {
	log := klog.FromContext(c.Request.Context())
	namespace := s.Auth.GetNamespaceFromContext(c)
	repo := c.Param("repo")

	sandboxes, err := s.listDevSandboxesFromK8s(c.Request.Context(), namespace, repo)
	if err != nil {
		log.Info("Error listing dev sandboxes", "err", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to list dev sandboxes"})
		return
	}

	c.JSON(http.StatusOK, sandboxes)
}

func (s *Server) listDevSandboxesFromK8s(ctx context.Context, namespace, repo string) ([]models.DevSandbox, error) {
	log := klog.FromContext(ctx)
	var sandboxes []models.DevSandbox

	// 1. Fetch RepoWatch to get Ideas
	if rw, err := s.K8sManager.GetRepoWatch(ctx, namespace, repo); err == nil {
		ideas, found, err := unstructured.NestedSlice(rw.Object, "spec", "dev", "ideas")
		if found && err == nil {
			for _, idea := range ideas {
				ideaMap, ok := idea.(map[string]interface{})
				if !ok {
					continue
				}

				id, _, _ := unstructured.NestedString(ideaMap, "id")
				name, _, _ := unstructured.NestedString(ideaMap, "name")
				desc, _, _ := unstructured.NestedString(ideaMap, "description")

				// We use the name field for IdeaID if Name is not suitable, but model has separate fields.
				// In the UI, IdeaID is used as the key.
				sandboxes = append(sandboxes, models.DevSandbox{
					Name:        name, // Display name
					IdeaID:      id,
					Description: desc,
					// Approach and Branch are empty to signify this is the Idea metadata
				})
			}
		}
	} else {
		log.Info("Failed to get RepoWatch for ideas", "err", err)
	}

	// 2. Fetch active Dev Sandboxes
	gvr := schema.GroupVersionResource{
		Group:    "agents.x-k8s.io",
		Version:  "v1alpha1",
		Resource: "sandboxes",
	}
	list, err := s.K8sManager.Client.Resource(gvr).Namespace(namespace).List(context.Background(),
		v1.ListOptions{
			LabelSelector: fmt.Sprintf("review.gemini.google.com/repowatch=%s,sandbox.gemini.google.com/type=dev", repo),
		})
	if err != nil {
		return nil, fmt.Errorf("failed to list DevSandbox CRs: %w", err)
	}

	for _, item := range list.Items {
		replicas, found, err := unstructured.NestedInt64(item.Object, "spec", "replicas")
		if err != nil || !found {
			log.Info("Replicas (.spec.replicas) not found in DevSandbox", "name", item.GetName())
			continue
		}

		annotations := item.GetAnnotations()
		if annotations == nil {
			annotations = make(map[string]string)
		}

		branch := annotations["sandbox.gemini.google.com/branch"]
		if branch == "" {
			// Try to read from env or fallback
			branch = "nobranch"
		}

		cloneURL := annotations["sandbox.gemini.google.com/clone-url"]
		if cloneURL == "" {
			cloneURL = "https://github.com/noorg/norepo.git"
		}

		repoParts := strings.Split(strings.TrimSuffix(cloneURL, ".git"), "/")
		if len(repoParts) >= 2 {
			repoName := repoParts[len(repoParts)-1]
			owner := repoParts[len(repoParts)-2]
			// Construct branch URL: https://github.com/OWNER/REPO/tree/BRANCH
			branchURL := fmt.Sprintf("https://github.com/%s/%s/tree/%s", owner, repoName, branch)

			agentState := ""
			agentStateMessage := ""
			sandboxStatus := ""
			var labels []string

			if val, ok := annotations["agentState"]; ok {
				agentState = val
			}
			if val, ok := annotations["agentStateMessage"]; ok {
				agentStateMessage = val
			}
			if val, ok := annotations["sandbox.gemini.google.com/pod-status"]; ok {
				sandboxStatus = val
			}
			if val, ok := annotations["agentLabels"]; ok {
				_ = json.Unmarshal([]byte(val), &labels)
			}

			// Read Idea Labels
			itemLabels := item.GetLabels()
			ideaID := ""
			approach := ""
			parentApproach := ""
			if itemLabels != nil {
				if val, ok := itemLabels["repo-agent.gemini.google.com/idea-id"]; ok {
					ideaID = val
				}
				if val, ok := itemLabels["repo-agent.gemini.google.com/approach"]; ok {
					approach = val
				}
				if val, ok := itemLabels["repo-agent.gemini.google.com/parent-approach"]; ok {
					parentApproach = val
				}
			}

			name := item.GetName()

			sandbox := models.DevSandbox{
				Name:              name,
				Sandbox:           name, // Should be name without prefix for API consistency? Or full name? UI expects name returned by create.
				Branch:            branch,
				BranchURL:         branchURL,
				SandboxReplica:    fmt.Sprintf("%d", replicas),
				AgentState:        agentState,
				AgentStateMessage: agentStateMessage,
				SandboxStatus:     sandboxStatus,
				Labels:            labels,
				IdeaID:            ideaID,
				Approach:          approach,
				ParentApproach:    parentApproach,
			}
			sandboxes = append(sandboxes, sandbox)
		}
	}
	return sandboxes, nil
}

func (s *Server) createDevSandbox(c *gin.Context) {
	log := klog.FromContext(c.Request.Context())
	namespace := s.Auth.GetNamespaceFromContext(c)
	repo := c.Param("repo")

	var req struct {
		Branch         string `json:"branch"`
		Prompt         string `json:"prompt"`
		IdeaID         string `json:"ideaID"`
		Approach       string `json:"approach"`
		BaseBranch     string `json:"baseBranch"`
		ParentApproach string `json:"parentApproach"`
		Description    string `json:"description"`
	}
	if err := c.BindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request body"})
		return
	}

	// Case 1: Creating a new Idea (Exploration) - Just metadata, no sandbox
	// This part handles the creation of "Idea" metadata within the RepoWatch CRD.
	// It does NOT provision a Pod or Sandbox CR yet; it serves as a placeholder for future work.
	if req.IdeaID != "" && req.Approach == "" { // Fetch RepoWatch
		rw, err := s.K8sManager.GetRepoWatch(c.Request.Context(), namespace, repo)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to get RepoWatch", "details": err.Error()})
			return
		}

		ideas, found, err := unstructured.NestedSlice(rw.Object, "spec", "dev", "ideas")
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to read ideas", "details": err.Error()})
			return
		}
		if !found {
			ideas = []interface{}{}
		}

		// Check if ID already exists
		for _, idea := range ideas {
			ideaMap, ok := idea.(map[string]interface{})
			if ok {
				id, _, _ := unstructured.NestedString(ideaMap, "id")
				if id == req.IdeaID {
					c.JSON(http.StatusConflict, gin.H{"error": "Idea ID already exists"})
					return
				}
			}
		}

		// Add new idea
		newIdea := map[string]interface{}{
			"id":          req.IdeaID,
			"name":        req.IdeaID, // Use ID as name for now, or we could accept a separate name
			"description": req.Description,
			"createdAt":   time.Now().Format(time.RFC3339),
			"author":      namespace,
		}
		ideas = append(ideas, newIdea)

		if err := unstructured.SetNestedSlice(rw.Object, ideas, "spec", "dev", "ideas"); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to set ideas", "details": err.Error()})
			return
		}

		// Update RepoWatch
		_, updateErr := s.K8sManager.Client.Resource(schema.GroupVersionResource{
			Group:    "review.gemini.google.com",
			Version:  "v1alpha1",
			Resource: "repowatches",
		}).Namespace(namespace).Update(c.Request.Context(), rw, v1.UpdateOptions{})

		if updateErr != nil {
			log.Info("Failed to update RepoWatch with new idea", "err", updateErr)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update RepoWatch", "details": updateErr.Error()})
			return
		}

		c.JSON(http.StatusCreated, gin.H{"status": "created", "ideaID": req.IdeaID})
		return
	}

	// Case 2: Creating a Sandbox (Standalone or Approach)

	branchName := req.Branch
	if req.IdeaID != "" {
		// If creating an approach
		if req.Approach == "" {
			// Should have been caught by Case 1, but if for some reason we end up here with Branch set?
			// But createDevSandbox logic for IdeaID usually derives branch name.
			// If IdeaID is set, Approach SHOULD be set if we are creating a sandbox.
			// Unless the logic below defaults it.
			req.Approach = "initial" // Fallback if someone calls this endpoint incorrectly for an approach
		}
		if branchName == "" {
			branchName = fmt.Sprintf("ideas/%s/%s", req.IdeaID, req.Approach)
		}
	}

	if branchName == "" {
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
	repoURL = strings.TrimSuffix(repoURL, ".git") + ".git"

	repoParts := strings.Split(strings.TrimSuffix(repoURL, ".git"), "/")
	repoName := repoParts[len(repoParts)-1]

	cloneURL := repoURL
	if req.BaseBranch != "" {
		cloneURL = fmt.Sprintf("%s#refs/heads/%s", repoURL, req.BaseBranch)
	}

	htmlURL := strings.TrimSuffix(repoURL, ".git")
	originURL := fmt.Sprintf("github.com/%s/%s.git", namespace, repoName)

	githubSecretName, _, _ := unstructured.NestedString(rw.Object, "spec", "githubSecretName")
	apiKeySecretRef, _, _ := unstructured.NestedString(rw.Object, "spec", "dev", "llm", "apiKeySecretRef")
	configdirRef, _, _ := unstructured.NestedString(rw.Object, "spec", "dev", "llm", "configdirRef")
	llmProvider, _, _ := unstructured.NestedString(rw.Object, "spec", "dev", "llm", "provider")
	image, _, _ := unstructured.NestedString(rw.Object, "spec", "dev", "image")
	devContainerConfigRef, _, _ := unstructured.NestedString(rw.Object, "spec", "dev", "devcontainerConfigRef")
	dindSupport, _, _ := unstructured.NestedString(rw.Object, "spec", "dev", "dindSupport")
	workspaceDiskSize, _, _ := unstructured.NestedString(rw.Object, "spec", "dev", "workspaceDiskSize")

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
		log.Info("Failed to get github secret for user", "user", namespace, "err", err)
	}
	if userName == "" {
		userName = namespace
	}

	// Sanitize branch name for K8s resource name to match controller logic
	// We replace special characters and hash the result to ensure the K8s resource name
	// is valid (RFC 1123) and unique, avoiding length limits.
	safeBranch := strings.ReplaceAll(branchName, "/", "-")
	safeBranch = strings.ReplaceAll(safeBranch, "_", "-")
	safeBranch = strings.ReplaceAll(safeBranch, ".", "-")
	safeBranch = strings.ToLower(safeBranch)

	fullSuffix := fmt.Sprintf("dev-%s-%s", repoName, safeBranch)
	h := fnv.New32a()
	h.Write([]byte(fullSuffix))
	hashedSuffix := fmt.Sprintf("%08x", h.Sum32())
	sandboxName := fmt.Sprintf("dev-%s", hashedSuffix)

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
				log.Info("Failed to update excludeBranches in local object", "err", err)
			} else {
				// Update RepoWatch in K8s
				_, updateErr := s.K8sManager.Client.Resource(schema.GroupVersionResource{
					Group:    "review.gemini.google.com",
					Version:  "v1alpha1",
					Resource: "repowatches",
				}).Namespace(namespace).Update(c.Request.Context(), rw, v1.UpdateOptions{})
				if updateErr != nil {
					log.Info("Failed to update RepoWatch to remove excluded branch", "err", updateErr)
				}
			}
		}
	}

	annotations := map[string]string{}
	// Description is now stored in RepoWatch for the Idea, but we can still add it here if provided
	if req.Description != "" {
		annotations["repo-agent.gemini.google.com/idea-description"] = req.Description
	}

	opts := sandbox.DevSandboxOptions{
		Name:      sandboxName,
		Namespace: namespace,
		Labels: map[string]string{
			"review.gemini.google.com/repowatch": repo,
			"sandbox.gemini.google.com/type":     "dev",
			"sandbox-type":                       "dev",
		},
		Annotations: annotations,
		CloneURL:    cloneURL,
		HTMLURL:     htmlURL,

		Branch:      branchName,
		Origin:      originURL,
		PushEnabled: true,
		UserLogin:   namespace,
		UserName:    userName,
		UserEmail:   userEmail,

		LLMProvider:         llmProvider,
		LLMConfigdirRef:     configdirRef,
		LLMAPIKeySecretName: apiKeySecretRef,
		Prompt:              req.Prompt,

		GithubSecretName:      githubSecretName,
		DevcontainerConfigRef: devContainerConfigRef,
		Image:                 image,

		HTTPEnabled: true,
		Replicas:    1,

		ServiceAccountName: "issue-sandbox",

		DindSupport:       dindSupport,
		WorkspaceDiskSize: workspaceDiskSize,
		IdeaID:            req.IdeaID,
		Approach:          req.Approach,
		ParentApproach:    req.ParentApproach,
	}

	sb, svc := sandbox.NewDevSandbox(opts)

	// Create Service
	// We use Clientset for Service creation as it is core resource
	_, err = s.K8sManager.Clientset.CoreV1().Services(namespace).Create(c.Request.Context(), svc, v1.CreateOptions{})
	if err != nil && !apierrors.IsAlreadyExists(err) { // Ignore if exists
		log.Info("Failed to create Service for DevSandbox", "err", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create Service", "details": err.Error()})
		return
	}

	// Create Sandbox (using dynamic client)
	sandboxGVR := schema.GroupVersionResource{
		Group:    "agents.x-k8s.io",
		Version:  "v1alpha1",
		Resource: "sandboxes",
	}

	_, err = s.K8sManager.Client.Resource(sandboxGVR).Namespace(namespace).Create(c.Request.Context(), sb, v1.CreateOptions{})
	if err != nil {
		log.Info("Failed to create DevSandbox", "err", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create DevSandbox", "details": err.Error()})
		return
	}

	// Create initial dev-setup task
	taskParams := map[string]string{
		"REPO_URL":          repoURL,
		"BRANCH_NAME":       branchName,
		"GITHUB_USER_LOGIN": namespace,
		"GITHUB_USER_EMAIL": userEmail,
		"GITHUB_USER_NAME":  userName,
	}
	if req.BaseBranch != "" {
		taskParams["SOURCE_BRANCH"] = req.BaseBranch
	}
	if req.Prompt != "" {
		taskParams["AGENT_PROMPT"] = req.Prompt
	}

	if err := s.K8sManager.CreateSandboxTask(c.Request.Context(), namespace, sb.GetName(), "Sandbox", "dev-setup", taskParams); err != nil {
		log.Info("Failed to create initial dev-setup task", "err", err)
		// We don't fail the request, just log it. The user can retry or creating task manually.
	}

	c.JSON(http.StatusCreated, gin.H{"status": "created", "name": sandboxName})
}

func (s *Server) deleteDevSandbox(c *gin.Context) {
	namespace := s.Auth.GetNamespaceFromContext(c)
	name := c.Param("name") // This is the sandbox name
	ctx := c.Request.Context()

	resourceName := name
	if err := s.K8sManager.ScaledownDevSandboxHelper(ctx, namespace, resourceName); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to delete dev sandbox", "details": err.Error()})
		return
	}

	c.Status(http.StatusOK)
}

func (s *Server) scaleUpDevSandbox(c *gin.Context) {
	log := klog.FromContext(c.Request.Context())
	namespace := s.Auth.GetNamespaceFromContext(c)
	name := c.Param("name")

	resourceName := name
	if err := s.K8sManager.ScaleupDevSandboxHelper(c.Request.Context(), namespace, resourceName); err != nil {
		log.Info("Failed to scale up dev sandbox", "name", name, "err", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to scale up dev sandbox"})
		return
	}

	c.Status(http.StatusOK)
}

func (s *Server) scaleDownDevSandbox(c *gin.Context) {
	log := klog.FromContext(c.Request.Context())
	namespace := s.Auth.GetNamespaceFromContext(c)
	name := c.Param("name")

	resourceName := name
	if err := s.K8sManager.ScaledownDevSandboxHelper(c.Request.Context(), namespace, resourceName); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to scale down dev sandbox", "details": err.Error()})
		return
	}

	if err := s.K8sManager.UpdateSandboxAnnotation(c.Request.Context(), namespace, resourceName, "agentState", "sandbox paused"); err != nil {
		log.Info("Failed to update dev sandbox annotation", "err", err)
	}

	c.Status(http.StatusOK)
}
func (s *Server) getDevTasks(c *gin.Context) {
	namespace := s.Auth.GetNamespaceFromContext(c)
	sandboxName := c.Param("name")

	resourceName := sandboxName
	taskList, err := s.K8sManager.ListSandboxTasks(c.Request.Context(), namespace, resourceName)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to list tasks", "details": err.Error()})
		return
	}

	var tasks []models.Task
	for _, taskItem := range taskList.Items {
		taskType := taskItem.Spec.Type
		taskState := taskItem.Status.TaskState
		result := taskItem.Status.Result

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
			Stats:             convertStats(taskItem.Status.Stats),
		})
	}
	// Sort tasks by creation timestamp (newest first)
	sort.Slice(tasks, func(i, j int) bool {
		return tasks[i].CreationTimestamp > tasks[j].CreationTimestamp
	})

	c.JSON(http.StatusOK, tasks)
}

func (s *Server) createDevTask(c *gin.Context) {
	namespace := s.Auth.GetNamespaceFromContext(c)
	sandboxName := c.Param("name")

	var payload struct {
		Prompt   string            `json:"prompt"`
		TaskType string            `json:"taskType"`
		Params   map[string]string `json:"params"`
	}
	if err := c.ShouldBindJSON(&payload); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	taskType := payload.TaskType
	if taskType == "" {
		taskType = "generic-task"
	}

	params := map[string]string{}
	if payload.Prompt != "" {
		params["AGENT_PROMPT"] = payload.Prompt
	}
	for k, v := range payload.Params {
		params[k] = v
	}

	resourceName := sandboxName
	err := s.K8sManager.CreateSandboxTask(c.Request.Context(), namespace, resourceName, "Sandbox", taskType, params)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create task", "details": err.Error()})
		return
	}

	// Scale up the sandbox so it can process the task
	if err := s.K8sManager.ScaleupDevSandboxHelper(c.Request.Context(), namespace, resourceName); err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Failed to scale up sandbox after task creation", "details": err.Error()})
		return
	}

	c.Status(http.StatusOK)
}

func (s *Server) getDevTaskLogs(c *gin.Context) {
	log := klog.FromContext(c.Request.Context())
	namespace := s.Auth.GetNamespaceFromContext(c)
	sandboxName := c.Param("name")
	taskID := c.Param("taskID")

	serviceName := fmt.Sprintf("%s-lb", sandboxName)

	targetURL := fmt.Sprintf("http://%s.%s.svc.cluster.local:13339", serviceName, namespace)

	proxyURL, err := url.Parse(targetURL)
	if err != nil {
		log.Error(err, "Failed to parse target URL", "url", targetURL)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Invalid target URL"})
		return
	}

	proxy := httputil.NewSingleHostReverseProxy(proxyURL)
	originalDirector := proxy.Director
	proxy.Director = func(req *http.Request) {
		originalDirector(req)
		req.URL.Path = fmt.Sprintf("/logs/%s", taskID)
	}

	// Custom error handler for proxy
	proxy.ErrorHandler = func(w http.ResponseWriter, _ *http.Request, err error) {
		log.Error(err, "Proxy error", "target", targetURL)
		// If connection refused, it might mean the pod is not ready or port not exposed yet
		http.Error(w, "Failed to connect to agent server logs (pod might be starting or scaled down)", http.StatusBadGateway)
	}

	proxy.ServeHTTP(c.Writer, c.Request)
}
