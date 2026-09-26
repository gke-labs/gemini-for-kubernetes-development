package api

import (
	"bytes"
	"io"

	"github.com/gin-gonic/gin"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/auth"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/k8s"
	"k8s.io/klog/v2"
)

type Server struct {
	K8sManager *k8s.Manager
	Auth       *auth.Authenticator
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
		api.POST("/board/:board/issues/:id/fix", s.kickoffFix)
		api.POST("/board/:board/issues/:id/triage", s.kickoffTriage)
		api.POST("/board/:board/issues/:id/publish-triage", s.publishBoardTriage)
		api.POST("/board/:board/issues/:id/triage-reject", s.rejectBoardTriage)
		api.PUT("/board/:board/issues/:id/plan-draft", s.putBoardPlanDraft)
		api.PUT("/board/:board/issues/:id/draft", s.putBoardTriageDraft)
		api.POST("/board/:board/issues/:id/plan", s.kickoffPlan)
		api.POST("/board/:board/issues/:id/plan-feedback", s.planBoardFeedback)
		api.POST("/board/:board/issues/:id/plan-refresh", s.planBoardRefresh)
		api.POST("/board/:board/prs/:id/iterate", s.iterateBoardPR)
		api.POST("/board/:board/prs/:id/address-comments", s.addressBoardPR)
		api.POST("/board/:board/prs/:id/investigate", s.investigateBoardPR)
		api.POST("/board/:board/prs/:id/auto-iterate", s.autoIterateBoardPR)
		api.POST("/board/:board/research", s.startResearchSession)
		// Addressed by session id alone, with no board in the path: the
		// board is only the mailbox that got the sandbox made, and
		// nothing after that click goes through it.
		api.GET("/research", s.getResearchSessions)
		api.GET("/research/:session", s.getResearchSession)
		api.DELETE("/research/:session", s.deleteResearchSession)
		api.POST("/research/:session/prompt", s.promptResearchSession)
		api.POST("/research/:session/permission", s.resolveResearchPermission)
		api.POST("/research/:session/cancel", s.cancelResearchSession)
		api.POST("/board/:board/explore", s.kickoffExplore)
		api.GET("/board/:board/exploration", s.getBoardExploration)
		api.GET("/board/:board/exploration/doc", s.getBoardExplorationDoc)

		api.POST("/board/:board/runbook", s.kickoffRunbook)
		api.GET("/board/:board/runbook", s.getBoardRunbooks)
		api.DELETE("/board/:board/runbook/instance/:instance", s.removeRunbookInstance)
		api.POST("/board/:board/issues/:id/plan-approve", s.planBoardApprove)
		api.POST("/board/:board/issues/:id/plan-reject", s.planBoardReject)
		api.POST("/board/:board/prs/:id/review", s.kickoffReview)
		api.POST("/board/:board/issues/:id/rerun", s.rerunBoardIssue)
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

	// The research event stream, attached directly to the router for the
	// same reason the sandbox proxy is: the logging middleware buffers
	// the response, which would hold every event until the conversation
	// ended.
	research := router.Group("/api/research-events")
	research.Use(s.Auth.Middleware())
	{
		research.GET("/:session", s.streamResearchEvents)
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
