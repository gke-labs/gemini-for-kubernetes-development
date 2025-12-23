package api

import (
	"bytes"
	"io"
	"log"

	"github.com/gin-gonic/gin"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/review-ui/review-api/pkg/auth"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/review-ui/review-api/pkg/k8s"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/review-ui/review-api/pkg/store"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/review-ui/review-api/pkg/templates"
	"github.com/go-redis/redis/v8"
)

type Server struct {
	K8sManager *k8s.Manager
	Auth       *auth.Authenticator
	Redis      *redis.Client
	Store      store.Store
	Templates  *templates.Manager
}

func NewServer(manager *k8s.Manager, authenticator *auth.Authenticator, store store.Store) *Server {
	return &Server{
		K8sManager: manager,
		Auth:       authenticator,
		Redis:      manager.Redis,
		Store:      store,
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
	standard.GET("/api/auth/login", s.Auth.Login)
	standard.GET("/api/auth/callback", s.Auth.Callback)
	standard.GET("/api/auth/status", s.Auth.Status)
	standard.POST("/api/auth/logout", s.Auth.Logout)
	standard.GET("/api/auth/providers", s.Auth.GetProviders)
	standard.POST("/api/auth/github-config", s.Auth.UpdateGithubConfig)

	// Protected routes
	api := standard.Group("/api")
	api.Use(s.Auth.Middleware())
	{
		api.GET("/repos", s.getRepos)
		api.POST("/repos", s.createRepoWatch)
		api.GET("/getRepoWatch", s.getDefaultRepoWatch)
		api.GET("/templates", s.getTemplates)
		api.GET("/repos/:repo/yaml", s.getRepoWatchYAML)
		api.PUT("/repos/:repo", s.updateRepoWatch)
		api.DELETE("/repos/:repo", s.deleteRepoWatch)

		api.GET("/settings", s.getSettings)
		api.POST("/settings", s.updateSettings)

		api.GET("/repo/:repo/prs", s.getPRs)
		api.POST("/repo/:repo/prs/:id/draft", s.saveDraft)
		api.POST("/repo/:repo/prs/:id/submitreview", s.submitReview)
		api.DELETE("/repo/:repo/prs/:id", s.deletePR)
		api.GET("/repo/:repo/issues/:handler", s.getIssues)
		api.POST("/repo/:repo/issues/:issue_id/handler/:handler/draft", s.saveIssueDraft)
		api.POST("/repo/:repo/issues/:issue_id/handler/:handler/submitcomment", s.submitIssueComment)
		api.DELETE("/repo/:repo/issues/:issue_id/handler/:handler", s.deleteIssue)
		api.POST("/repo/:repo/prs/:id/scaleup", s.scaleUpPR)
		api.POST("/repo/:repo/prs/:id/scaledown", s.scaleDownPR)
		api.POST("/repo/:repo/issues/:issue_id/handler/:handler/scaleup", s.scaleUpIssue)
		api.POST("/repo/:repo/issues/:issue_id/handler/:handler/scaledown", s.scaleDownIssue)
		api.GET("/repo/:repo/dev", s.getDevSandboxes)
		api.DELETE("/repo/:repo/dev/:name", s.deleteDevSandbox)
		api.POST("/repo/:repo/dev/:name/scaleup", s.scaleUpDevSandbox)
		api.POST("/repo/:repo/dev/:name/scaledown", s.scaleDownDevSandbox)
		api.GET("/proxy", s.proxy)
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
