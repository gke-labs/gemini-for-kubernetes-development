package main

import (
	"context"
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
	if user, ns, ok := auth.DevAuthActive(); ok {
		klog.Warningf("DEV AUTH ACTIVE — every request is attributed to %q in namespace %q. "+
			"This exists for running the API on a laptop against a live cluster; "+
			"REPO_AGENT_DEV_USER must never be set in a deployed environment.", user, ns)
	}

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

	// Gin router
	router := gin.Default()
	sessionSecret := os.Getenv("SESSION_SECRET")
	if sessionSecret == "" {
		// Persisted in the system namespace so restarts keep the same
		// cookie-encryption key and users stay logged in.
		sessionSecret, err = k8s.EnsureSessionSecret(context.Background(), kube.Clientset)
		if err != nil {
			klog.Fatalf("Failed to ensure session secret: %v", err)
		}
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
