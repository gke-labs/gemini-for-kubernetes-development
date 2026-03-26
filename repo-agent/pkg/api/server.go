package api

import (
	"bytes"
	"io"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/auth"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/k8s"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/templates"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/klog/v2"
)

type Server struct {
	K8sManager                  *k8s.Manager
	Auth                        *auth.Authenticator
	Templates                   *templates.Manager
	TraceabilityMetadataEnabled bool
}

func NewServer(manager *k8s.Manager, authenticator *auth.Authenticator) *Server {
	return &Server{
		K8sManager: manager,
		Auth:       authenticator,
		Templates:  templates.NewManager(manager.Clientset),
	}
}

func (s *Server) RegisterRoutes(router *gin.Engine) {
	// Standard group for routes that require logging (non-streaming/non-websocket)
	standard := router.Group("/")
	// Add middleware to log requests and responses
	standard.Use(RequestLoggerMiddleware())
	standard.Use(ResponseLoggerMiddleware())

	// Public routes
	standard.GET("/", s.healthCheckOk)
	standard.GET("/api/", s.healthCheckOk)
	standard.GET("/api/version", s.getVersion)
	standard.GET("/api/auth/login", s.Auth.Login)
	standard.GET("/api/auth/callback", s.Auth.Callback)
	standard.GET("/api/auth/status", s.Auth.Status)
	standard.POST("/api/auth/logout", s.Auth.Logout)
	standard.GET("/api/auth/providers", s.Auth.GetProviders)
	standard.POST("/api/auth/github-config", s.Auth.UpdateGithubConfig)
	standard.POST("/api/auth/switch-namespace", s.Auth.SwitchNamespace)

	// Protected routes
	api := standard.Group("/api")
	api.Use(s.Auth.Middleware())
	{
		api.GET("/repos", s.getRepos)
		api.POST("/repos", s.createRepoWatch)
		api.GET("/repos/:repo", s.getRepo)
		api.GET("/getRepoWatch", s.getDefaultRepoWatch)
		api.GET("/templates", s.getTemplates)
		api.GET("/repos/:repo/yaml", s.getRepoWatchYAML)
		api.PUT("/repos/:repo", s.updateRepoWatch)
		api.DELETE("/repos/:repo", s.deleteRepoWatch)
		api.GET("/repos/:repo/instructions", s.getInstructions)
		api.POST("/repos/:repo/instructions", s.updateInstructions)

		api.GET("/settings", s.getSettings)
		api.POST("/settings", s.updateSettings)

		api.GET("/repo/:repo/prs", s.getPRs)
		api.GET("/repo/:repo/prs/:id/tasks", s.getPRTasks)
		api.GET("/repo/:repo/prs/:id/tasks/:taskID/logs", s.getTaskLogs)
		api.GET("/repo/:repo/prs/:id/details", s.getPRDetails)
		api.GET("/repo/:repo/prs/:id/commits", s.getPRCommits)
		api.POST("/repo/:repo/prs/:id/tasks", s.createPRTask)
		api.POST("/repo/:repo/tasks/:taskID/draft", s.saveTaskDraft)
		api.POST("/repo/:repo/prs/:id/draft", s.saveDraft)
		api.POST("/repo/:repo/prs/:id/submitreview", s.submitReview)
		api.DELETE("/repo/:repo/prs/:id", s.deletePR)
		api.GET("/repo/:repo/issues", s.getIssues)
		api.GET("/repo/:repo/issues/:issue_id/tasks", s.getIssueTasks)
		api.GET("/repo/:repo/issues/:issue_id/tasks/:taskID/logs", s.getIssueTaskLogs)
		api.GET("/repo/:repo/issues/:issue_id/details", s.getIssueDetails)
		api.GET("/repo/:repo/issues/:issue_id/commits", s.getIssueCommits)
		api.POST("/repo/:repo/issues/:issue_id/rollback", s.rollbackIssue)
		api.POST("/repo/:repo/issues/:issue_id/tasks", s.createIssueTask)
		api.POST("/repo/:repo/issues/:issue_id/draft", s.saveIssueDraft)
		api.POST("/repo/:repo/issues/:issue_id/submitcomment", s.submitIssueComment)
		api.DELETE("/repo/:repo/issues/:issue_id", s.deleteIssue)
		api.POST("/repo/:repo/prs/:id/scaleup", s.scaleUpPR)
		api.POST("/repo/:repo/prs/:id/scaledown", s.scaleDownPR)
		api.POST("/repo/:repo/issues/:issue_id/scaleup", s.scaleUpIssue)
		api.POST("/repo/:repo/issues/:issue_id/scaledown", s.scaleDownIssue)
		api.GET("/repo/:repo/dev", s.getDevSandboxes)
		api.POST("/repo/:repo/dev", s.createDevSandbox)
		api.DELETE("/repo/:repo/dev/:name", s.deleteDevSandbox)
		api.POST("/repo/:repo/dev/:name/scaleup", s.scaleUpDevSandbox)
		api.POST("/repo/:repo/dev/:name/scaledown", s.scaleDownDevSandbox)
		api.GET("/repo/:repo/dev/:name/tasks", s.getDevTasks)
		api.POST("/repo/:repo/dev/:name/tasks", s.createDevTask)
		api.GET("/repo/:repo/dev/:name/tasks/:taskID/logs", s.getDevTaskLogs)
		api.GET("/repo/:repo/chores/:name/tasks/:taskID/logs", s.getChoreTaskLogs)
		api.POST("/feedback", s.submitFeedback)
		api.GET("/proxy", s.proxy)

		// Overseer routes (admin only)
		overseer := api.Group("/overseers")
		overseer.Use(s.Auth.AdminMiddleware())
		{
			overseer.GET("", s.getOverseers)
			overseer.GET("/:name", s.getOverseer)
			overseer.GET("/:name/chores", s.getOverseerChores)
			overseer.GET("/:name/sandboxes", s.getOverseerSandboxes)
			overseer.GET("/:name/logs", s.getOverseerLogs)
			overseer.GET("/:name/chores/:choreName/logs", s.getChoreLogs)
			overseer.GET("/:name/chores/:choreName/tasks", s.getChoreTasks)
			overseer.POST("/:name/chores/:choreName/pause", s.pauseChore)
			overseer.POST("/:name/chores/:choreName/resume", s.resumeChore)
		}
	}

	// Protected terminal routes (WebSocket)
	terminal := router.Group("/api/terminal")
	terminal.Use(s.Auth.Middleware())
	{
		terminal.GET("/:namespace/:name", s.terminal)
	}

	// Protected sandbox proxy routes
	// These are attached directly to router to bypass the logging middleware which buffers responses
	// and breaks WebSockets/Streaming.
	sandbox := router.Group("/sandbox")
	sandbox.Use(s.Auth.Middleware())
	{
		sandbox.Any("/:namespace/:name/*path", s.proxySandbox)
	}
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

		klog.Infof("Request Method: %s\n", c.Request.Method)
		klog.Infof("Request URL: %s\n", c.Request.URL.String())
		//log.Printf("Request Headers: %v\n", c.Request.Header)
		klog.Infof("Request Body: %s\n", string(bodyBytes))

		c.Next() // Process the request further
	}
}

func ResponseLoggerMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		blw := &bodyLogWriter{body: bytes.NewBufferString(""), ResponseWriter: c.Writer}
		c.Writer = blw

		c.Next() // Process the request and generate the response

		klog.Infof("Response Status: %d\n", c.Writer.Status())
		klog.Infof("Response Headers: %v\n", c.Writer.Header())
		klog.Infof("Response Body: %s\n", blw.body.String())
	}
}

func (s *Server) getInstructions(c *gin.Context) {
	repoName := c.Param("repo")
	namespace := s.Auth.GetNamespaceFromContext(c)
	if namespace == "" {
		namespace = "default"
	}

	rw, err := s.K8sManager.GetRepoWatch(c.Request.Context(), namespace, repoName)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to get RepoWatch", "details": err.Error()})
		return
	}

	configDirRef, found, err := unstructured.NestedString(rw.Object, "spec", "review", "llm", "configdirRef")
	if err != nil || !found {
		// Try root level or other locations if structure varies?
		// Assuming standard structure for now.
		c.JSON(http.StatusNotFound, gin.H{"error": "ConfigDirRef not found in RepoWatch spec"})
		return
	}

	cd, err := s.K8sManager.GetConfigDir(c.Request.Context(), namespace, configDirRef)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to get ConfigDir", "details": err.Error()})
		return
	}

	files, found, err := unstructured.NestedSlice(cd.Object, "spec", "files")
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to read files from ConfigDir", "details": err.Error()})
		return
	}

	var current, draft string
	if found {
		for _, f := range files {
			fileMap, ok := f.(map[string]interface{})
			if !ok {
				continue
			}
			path, _, _ := unstructured.NestedString(fileMap, "path")
			content, _, _ := unstructured.NestedString(fileMap, "source", "inline")

			if path == ".gemini/user-instructions.json" {
				current = content
			} else if path == ".gemini/user-instructions.draft.json" {
				draft = content
			}
		}
	}

	c.JSON(http.StatusOK, gin.H{
		"current": current,
		"draft":   draft,
	})
}

func (s *Server) updateInstructions(c *gin.Context) {
	repoName := c.Param("repo")
	namespace := s.Auth.GetNamespaceFromContext(c)
	if namespace == "" {
		namespace = "default"
	}

	var req struct {
		Current string `json:"current"`
		Draft   string `json:"draft"`
		Action  string `json:"action"` // "save_draft", "publish", "discard_draft"
	}

	if err := c.BindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request body"})
		return
	}

	rw, err := s.K8sManager.GetRepoWatch(c.Request.Context(), namespace, repoName)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to get RepoWatch", "details": err.Error()})
		return
	}

	configDirRef, found, err := unstructured.NestedString(rw.Object, "spec", "review", "llm", "configdirRef")
	if err != nil || !found {
		c.JSON(http.StatusNotFound, gin.H{"error": "ConfigDirRef not found in RepoWatch spec"})
		return
	}

	switch req.Action {
	case "save_draft":
		if err := s.K8sManager.UpdateConfigDirFile(c.Request.Context(), namespace, configDirRef, ".gemini/user-instructions.draft.json", req.Draft); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to save draft", "details": err.Error()})
			return
		}
	case "discard_draft":
		if err := s.K8sManager.UpdateConfigDirFile(c.Request.Context(), namespace, configDirRef, ".gemini/user-instructions.draft.json", ""); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to discard draft", "details": err.Error()})
			return
		}
	case "publish":
		// Update current
		if err := s.K8sManager.UpdateConfigDirFile(c.Request.Context(), namespace, configDirRef, ".gemini/user-instructions.json", req.Current); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update current instructions", "details": err.Error()})
			return
		}
		// Remove draft
		if err := s.K8sManager.UpdateConfigDirFile(c.Request.Context(), namespace, configDirRef, ".gemini/user-instructions.draft.json", ""); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to remove draft", "details": err.Error()})
			return
		}
	default:
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid action"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"status": "success"})
}
