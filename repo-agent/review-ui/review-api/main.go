package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"log"
	"os"
	"strings"

	"github.com/gin-contrib/sessions"
	"github.com/gin-contrib/sessions/cookie"
	"github.com/gin-gonic/gin"

	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/review-ui/review-api/pkg/api"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/review-ui/review-api/pkg/auth"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/review-ui/review-api/pkg/k8s"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/review-ui/review-api/pkg/store"

	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
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
		log.Fatalf("Failed to connect to Redis: %v", err)
	}

	// Pre-populate mock data in Redis
	store.PopulateMockData(context.Background(), rdb)

	// Kubernetes client
	config, err := rest.InClusterConfig()
	if err != nil {
		log.Printf("Failed to get in-cluster config, trying local config: %v", err)
		kubeconfig := os.Getenv("KUBECONFIG")
		if kubeconfig == "" {
			kubeconfig = os.Getenv("HOME") + "/.kube/config"
		}
		config, err = clientcmd.BuildConfigFromFlags("", kubeconfig)
		if err != nil {
			log.Fatalf("Failed to get local kubeconfig: %v", err)
		}
	}
	k8sClient, err := dynamic.NewForConfig(config)
	if err != nil {
		log.Fatalf("Failed to create kubernetes client: %v", err)
	}
	k8sClientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		log.Fatalf("Failed to create clientset: %v", err)
	}

	// K8s Manager
	k8sManager := k8s.NewManager(k8sClient, k8sClientset, rdb)

	// Allowed Users
	var allowedUsers []string
	if allowedUsersStr := os.Getenv("GITHUB_ALLOWED_USERS"); allowedUsersStr != "" {
		allowedUsers = strings.Split(allowedUsersStr, ",")
		log.Printf("GitHub authentication restricted to users: %v", allowedUsers)
	} else {
		log.Println("No GITHUB_ALLOWED_USERS environment variable set. All GitHub users allowed to authenticate.")
	}

	// Authenticator
	authenticator := auth.NewAuthenticator(k8sManager, allowedUsers)

	// API Server
	server := api.NewServer(k8sManager, authenticator)

	// Gin router
	router := gin.Default()
	sessionSecret := os.Getenv("SESSION_SECRET")
	if sessionSecret == "" {
		// Generate a random secret if not provided
		b := make([]byte, 32)
		if _, err := rand.Read(b); err != nil {
			log.Fatalf("Failed to generate random session secret: %v", err)
		}
		sessionSecret = base64.StdEncoding.EncodeToString(b)
	}
	store := cookie.NewStore([]byte(sessionSecret))
	router.Use(sessions.Sessions(sessionName, store))

	// Register Routes
	server.RegisterRoutes(router)

	err = router.Run(":8080")
	if err != nil {
		log.Fatalf("Failed to start router: %v", err)
	}
}
