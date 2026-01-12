package main

import (
	"context"
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
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/store"

	"k8s.io/klog/v2"
)

const (
	sessionName = "repo-agent-session"
)

func main() {
	// Redis client
	redisAddr := os.Getenv("REDIS_ADDR")
	if redisAddr == "" {
		redisAddr = "localhost:6379"
	}
	rdb := store.NewClient(redisAddr)

	// Ping redis to ensure connection
	_, err := rdb.Ping(context.Background()).Result()
	if err != nil {
		klog.Fatalf("Failed to connect to Redis: %v", err)
	}

	// Pre-populate mock data in Redis
	store.PopulateMockData(context.Background(), rdb)

	// Kubernetes client
	kube, err := clients.NewKubernetesClient()
	if err != nil {
		klog.Fatalf("Failed to create kubernetes client: %v", err)
	}

	// K8s Manager
	k8sManager := k8s.NewManager(kube, rdb)

	// Allowed Users
	var allowedUsers []string
	if allowedUsersStr := os.Getenv("GITHUB_ALLOWED_USERS"); allowedUsersStr != "" {
		allowedUsers = strings.Split(allowedUsersStr, ",")
		klog.Infof("GitHub authentication restricted to users: %v", allowedUsers)
	} else {
		klog.Info("No GITHUB_ALLOWED_USERS environment variable set. All GitHub users allowed to authenticate.")
	}

	// Authenticator
	authenticator := auth.NewAuthenticator(k8sManager, allowedUsers)

	// Store
	redisStore := store.NewRedisStore(rdb)

	// API Server
	server := api.NewServer(k8sManager, authenticator, redisStore)

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
