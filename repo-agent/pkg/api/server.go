package api

import (
	"bytes"
	"io"

	"github.com/gin-gonic/gin"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/auth"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/k8s"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/podacpd"
	"k8s.io/klog/v2"
)

type Server struct {
	K8sManager *k8s.Manager
	Auth       *auth.Authenticator
	// ACPD reaches sandboxes' agent sessions, over a port-forward to the
	// daemon's /v1/sessions. Nil reaches none.
	ACPD *podacpd.Dialer
}

func NewServer(manager *k8s.Manager, authenticator *auth.Authenticator) *Server {
	return &Server{
		K8sManager: manager,
		Auth:       authenticator,
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
		api.GET("/settings", s.getSettings)
		api.POST("/settings", s.updateSettings)

		api.GET("/boards", s.getBoards)
		api.GET("/repo-suggestions", s.getRepoSuggestions)
		api.GET("/sandbox-card/:name", s.getSandboxCard)
		api.GET("/sandbox-card/:name/log", s.getSandboxTaskLog)
		api.POST("/sandbox-card/:name/lifecycle", s.sandboxLifecycle)
		api.GET("/sandbox-card/:name/terminal", s.sandboxTerminal)
		api.POST("/boards", s.createBoard)
		api.DELETE("/board/:board", s.deleteBoard)
		api.GET("/board/:board/spec", s.getBoardSpec)
		api.PUT("/board/:board/spec", s.putBoardSpec)
		api.GET("/board/:board/work", s.getBoardWork)
		// The recipes the rows offer, and any one's launch on a row.
		api.GET("/board/:board/recipes", s.getBoardRecipes)
		api.POST("/board/:board/issues/:id/recipes/:recipe", s.launchRecipe("issue"))
		api.POST("/board/:board/prs/:id/recipes/:recipe", s.launchRecipe("pr"))
		api.POST("/board/:board/prs/:id/auto-iterate", s.autoIterateBoardPR)
		api.POST("/board/:board/research", s.startResearchSession)
		// The canned openings as text, for a pane that puts one in the
		// member's box to edit rather than running it behind a button.
		api.GET("/board/:board/research/prompts", s.getResearchPrompts)
		// Addressed by session id alone, with no board in the path: the
		// board is only where the Request that made the sandbox was
		// filed, and nothing after that click goes through it.
		api.GET("/research", s.getResearchSessions)
		api.PATCH("/research/:session", s.renameResearchSession)
		api.DELETE("/research/:session", s.deleteResearchSession)
		// A factory task's agent session (plan, triage, research): watched
		// while the task runs, continued once it has ended.
		api.GET("/task-sessions/:sandbox/:task", s.getTaskSession)
		api.POST("/task-sessions/:sandbox/:task/prompt", s.promptTaskSession)
		api.POST("/task-sessions/:sandbox/:task/permission", s.resolveTaskSessionPermission)
		api.POST("/task-sessions/:sandbox/:task/cancel", s.cancelTaskSession)
		api.POST("/task-sessions/:sandbox/:task/mode", s.setTaskSessionMode)
		// The session's revises and its draft's actions. The server files
		// the Request on the issue's row, or on the sandbox when it has no
		// issue.
		api.POST("/task-sessions/:sandbox/:task/revise", s.reviseTaskSession)
		api.POST("/task-sessions/:sandbox/:task/draft/:verb", s.taskSessionDraftAction)

		api.POST("/board/:board/runbook", s.kickoffRunbook)
		api.GET("/board/:board/runbook", s.getBoardRunbooks)
		api.DELETE("/board/:board/runbook/instance/:instance", s.removeRunbookInstance)
		// A local-only run's files, served from its sandbox.
		api.GET("/board/:board/runbook/instance/:instance/file/:file", s.getLocalRunFile)
		api.POST("/board/:board/prs/:id/promote", s.promoteBoardPR)
		api.POST("/board/:board/prs/:id/abandon", s.abandonBoardReview)

		api.POST("/feedback", s.submitFeedback)
		api.GET("/proxy", s.proxy)
		api.GET("/usage/*path", s.proxyUsage)

		// Overseer routes (admin only)
		overseer := api.Group("/overseers")
		overseer.Use(s.Auth.AdminMiddleware())
		{
			overseer.GET("", s.getOverseers)
			overseer.GET("/:name", s.getOverseer)
			overseer.GET("/:name/chores", s.getOverseerChores)
			overseer.GET("/:name/sandboxes", s.getOverseerSandboxes)
			overseer.GET("/:name/sandboxes/:sandboxName/tasks", s.getOverseerSandboxTasks)
			overseer.GET("/:name/sandboxes/:sandboxName/tasks/:taskID/logs", s.getOverseerSandboxTaskLogs)
			overseer.GET("/:name/sandboxes/:sandboxName/tasks/:taskID/telemetry", s.getOverseerSandboxTaskTelemetry)
			overseer.GET("/:name/sandboxes/:sandboxName/logs", s.getOverseerSandboxLogs)
			overseer.DELETE("/:name/sandboxes/:sandboxName", s.deleteOverseerSandbox)
			overseer.POST("/:name/sandboxes/:sandboxName/scaleup", s.scaleUpOverseerSandbox)
			overseer.POST("/:name/sandboxes/:sandboxName/scaledown", s.scaleDownOverseerSandbox)
			overseer.POST("/:name/sandboxes/:sandboxName/unpause", s.scaleUpOverseerSandbox)
			overseer.POST("/:name/sandboxes/:sandboxName/pause", s.scaleDownOverseerSandbox)
			overseer.GET("/:name/logs", s.getOverseerLogs)
			overseer.GET("/:name/chores/:choreName/logs", s.getChoreLogs)
			overseer.POST("/:name/chores/:choreName/pause", s.pauseChore)
			overseer.POST("/:name/chores/:choreName/resume", s.resumeChore)
			overseer.GET("/:name/queue", s.getOverseerQueue)
			overseer.GET("/:name/status", s.getOverseerStatus)
			overseer.POST("/:name/queue/:filename/priority", s.updateOverseerQueueTaskPriority)
		}
	}

	// Protected terminal routes (WebSocket)
	terminal := router.Group("/api/terminal")
	terminal.Use(s.Auth.Middleware())
	{
		terminal.GET("/:namespace/:name", s.overseerTerminal)
	}

	// The task session event stream, attached directly to the router for the
	// same reason the sandbox proxy is: the logging middleware buffers
	// the response, which would hold every event until the conversation
	// ended.
	taskSessions := router.Group("/api/task-session-events")
	taskSessions.Use(s.Auth.Middleware())
	{
		taskSessions.GET("/:sandbox/:task", s.streamTaskSessionEvents)
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
