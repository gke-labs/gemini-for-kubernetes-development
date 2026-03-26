package main

import (
	"crypto/rand"
	"encoding/base64"
	"os"
	"strings"

	"github.com/gin-contrib/sessions"
	"github.com/gin-contrib/sessions/cookie"
	"github.com/gin-gonic/gin"

	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/api"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/auth"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/clients"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/k8s"

	"k8s.io/klog/v2"
)

const (
	sessionName = "repo-agent-session"
)

func main() {
	// Kubernetes client
	kube, err := clients.NewKubernetesClient()
	if err != nil {
		klog.Fatalf("Failed to create kubernetes client: %v", err)
	}

	// K8s Manager
	k8sManager := k8s.NewManager(kube)

	// Allowed Users
	var allowedUsers []string
	if allowedUsersStr := os.Getenv("GITHUB_ALLOWED_USERS"); allowedUsersStr != "" {
		allowedUsers = strings.Split(allowedUsersStr, ",")
		klog.Infof("GitHub authentication restricted to users: %v", allowedUsers)
	} else {
		klog.Info("No GITHUB_ALLOWED_USERS environment variable set. All GitHub users allowed to authenticate.")
	}

	// Admin Users
	var adminUsers []string
	if adminUsersStr := os.Getenv("GITHUB_ADMIN_USERS"); adminUsersStr != "" {
		adminUsers = strings.Split(adminUsersStr, ",")
		klog.Infof("GitHub admin users: %v", adminUsers)
	}

	// Authenticator
	authenticator := auth.NewAuthenticator(k8sManager, allowedUsers, adminUsers)

	// API Server
	server := api.NewServer(k8sManager, authenticator)
	if os.Getenv("METADATA_TRACEABILITY_ENABLED") == "true" {
		server.TraceabilityMetadataEnabled = true
		klog.Info("Metadata traceability enabled for GitHub issues, PRs, and comments.")
	}

	// Gin router
	router := gin.Default()
	sessionSecret := os.Getenv("SESSION_SECRET")
	if sessionSecret == "" {
		// Generate a random secret if not provided
		b := make([]byte, 32)
		if _, err := rand.Read(b); err != nil {
			klog.Fatalf("Failed to generate random session secret: %v", err)
		}
		sessionSecret = base64.StdEncoding.EncodeToString(b)
	}
	store := cookie.NewStore([]byte(sessionSecret))
	router.Use(sessions.Sessions(sessionName, store))

	// Register Routes
	server.RegisterRoutes(router)

	err = router.Run(":8080")
	if err != nil {
		klog.Fatalf("Failed to start router: %v", err)
	}
}
