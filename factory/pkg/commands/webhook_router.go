package commands

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/clients"
	"github.com/spf13/cobra"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/klog/v2"
)

type WebhookRouterFlags struct {
	Port string
}

func NewWebhookRouterCommand(ctx context.Context) *cobra.Command {
	var flags WebhookRouterFlags

	cmd := &cobra.Command{
		Use:   "gh-webhook-router",
		Short: "Start the global GitHub App webhook router server",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runWebhookRouter(ctx, flags.Port)
		},
	}

	cmd.Flags().StringVar(&flags.Port, "port", "8080", "Port to listen on")

	return cmd
}

type webhookRouterPayload struct {
	Repository struct {
		FullName string `json:"full_name"`
	} `json:"repository"`
}

type installationEvent struct {
	Action       string `json:"action"`
	Repositories []struct {
		FullName string `json:"full_name"`
	} `json:"repositories"`
}

type installationReposEvent struct {
	Action            string `json:"action"`
	RepositoriesAdded []struct {
		FullName string `json:"full_name"`
	} `json:"repositories_added"`
}

func verifyRouterSignature(body []byte, signature string, secret []byte) bool {
	if signature == "" {
		return false
	}
	if !strings.HasPrefix(signature, "sha256=") {
		return false
	}
	hexSig := signature[7:]
	expectedSig := hmac.New(sha256.New, secret)
	expectedSig.Write(body)
	expectedHex := hex.EncodeToString(expectedSig.Sum(nil))
	return hmac.Equal([]byte(hexSig), []byte(expectedHex))
}

func runWebhookRouter(ctx context.Context, port string) error {
	webhookSecretStr := os.Getenv("GITHUB_WEBHOOK_SECRET")
	var webhookSecret []byte
	if webhookSecretStr != "" {
		webhookSecret = []byte(webhookSecretStr)
		klog.Info("GitHub Webhook Secret configured. Signature verification enabled.")
	} else {
		klog.Warning("GITHUB_WEBHOOK_SECRET is not set. Signature verification is disabled.")
	}

	var kubeClient *clients.KubernetesClient
	var err error
	if os.Getenv("DISABLE_K8S") != "true" {
		kubeClient, err = clients.NewKubernetesClient()
		if err != nil {
			klog.Warningf("Kubernetes client initialization failed (auto-onboarding will be disabled): %v", err)
		} else {
			klog.Info("Kubernetes client initialized. Auto-onboarding enabled.")
		}
	}

	http.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("OK"))
	})

	http.HandleFunc("/webhook", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}

		bodyBytes, err := io.ReadAll(r.Body)
		if err != nil {
			klog.Errorf("Failed to read body: %v", err)
			http.Error(w, "Failed to read request body", http.StatusInternalServerError)
			return
		}

		// Verify signature if configured
		if len(webhookSecret) > 0 {
			sig := r.Header.Get("X-Hub-Signature-256")
			if !verifyRouterSignature(bodyBytes, sig, webhookSecret) {
				klog.Warning("Invalid signature validation failed")
				http.Error(w, "Invalid signature", http.StatusForbidden)
				return
			}
		}

		eventType := r.Header.Get("X-GitHub-Event")
		klog.Infof("Received GitHub Event: %s", eventType)

		// Check for installation/onboarding events
		var dynClient dynamic.Interface
		if kubeClient != nil {
			dynClient = kubeClient.DynamicClient
		}

		if eventType == "installation" {
			handleInstallationEvent(ctx, dynClient, bodyBytes)
			w.WriteHeader(http.StatusOK)
			w.Write([]byte("Installation event processed"))
			return
		}

		if eventType == "installation_repositories" {
			handleInstallationReposEvent(ctx, dynClient, bodyBytes)
			w.WriteHeader(http.StatusOK)
			w.Write([]byte("Installation repos event processed"))
			return
		}

		// Forward repository events to namespace local service
		var payload webhookRouterPayload
		if err := json.Unmarshal(bodyBytes, &payload); err != nil {
			klog.Errorf("Failed to parse JSON body: %v", err)
			http.Error(w, "Invalid JSON payload", http.StatusBadRequest)
			return
		}

		repoName := payload.Repository.FullName
		if repoName == "" {
			klog.Warning("No repository name found in payload")
			http.Error(w, "No repository full_name found in payload", http.StatusBadRequest)
			return
		}

		targetNamespace := SlugifyNamespace(repoName)
		targetURL := fmt.Sprintf("http://overseer-local-listener.%s.svc.cluster.local:8080/events", targetNamespace)

		klog.Infof("Routing event for repo %s to namespace %s (URL: %s)", repoName, targetNamespace, targetURL)

		forwardCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
		defer cancel()

		req, err := http.NewRequestWithContext(forwardCtx, "POST", targetURL, bytes.NewReader(bodyBytes))
		if err != nil {
			klog.Errorf("Failed to create forward request: %v", err)
			http.Error(w, "Failed to create forward request", http.StatusInternalServerError)
			return
		}

		// Forward GitHub custom headers
		for k, vv := range r.Header {
			if strings.HasPrefix(strings.ToLower(k), "x-github-") || strings.HasPrefix(strings.ToLower(k), "x-hub-") {
				for _, v := range vv {
					req.Header.Add(k, v)
				}
			}
		}
		req.Header.Set("Content-Type", "application/json")

		client := &http.Client{}
		resp, err := client.Do(req)
		if err != nil {
			klog.Errorf("Failed to forward event to target URL %s: %v", targetURL, err)
			http.Error(w, fmt.Sprintf("Failed to route to target namespace: %v", err), http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()

		respBytes, _ := io.ReadAll(resp.Body)
		if resp.StatusCode >= 400 {
			klog.Errorf("Target returned status %d: %s", resp.StatusCode, string(respBytes))
			w.WriteHeader(resp.StatusCode)
			w.Write(respBytes)
			return
		}

		w.WriteHeader(http.StatusOK)
		w.Write([]byte("Event forwarded successfully"))
	})

	klog.Infof("Starting Global Webhook Router on port %s...", port)
	server := &http.Server{Addr: ":" + port}

	go func() {
		<-ctx.Done()
		klog.Info("Shutting down server gracefully...")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		server.Shutdown(shutdownCtx)
	}()

	return server.ListenAndServe()
}

func handleInstallationEvent(ctx context.Context, dynClient dynamic.Interface, body []byte) {
	if dynClient == nil {
		klog.Warning("Kubernetes client not available. Skipping auto-onboarding.")
		return
	}

	var payload installationEvent
	if err := json.Unmarshal(body, &payload); err != nil {
		klog.Errorf("Failed to parse installation webhook payload: %v", err)
		return
	}

	if payload.Action != "created" && payload.Action != "unsuspended" {
		return
	}

	for _, repo := range payload.Repositories {
		onboardRepo(ctx, dynClient, repo.FullName)
	}
}

func handleInstallationReposEvent(ctx context.Context, dynClient dynamic.Interface, body []byte) {
	if dynClient == nil {
		klog.Warning("Kubernetes client not available. Skipping auto-onboarding.")
		return
	}

	var payload installationReposEvent
	if err := json.Unmarshal(body, &payload); err != nil {
		klog.Errorf("Failed to parse installation_repositories webhook payload: %v", err)
		return
	}

	if payload.Action != "added" {
		return
	}

	for _, repo := range payload.RepositoriesAdded {
		onboardRepo(ctx, dynClient, repo.FullName)
	}
}

func onboardRepo(ctx context.Context, dynClient dynamic.Interface, repoFullName string) {
	klog.Infof("Auto-onboarding repository: %s", repoFullName)

	nsName := SlugifyNamespace(repoFullName)
	crName := strings.TrimPrefix(nsName, "f-")
	if len(crName) > 63 {
		crName = crName[:63]
	}

	repoURL := fmt.Sprintf("https://github.com/%s", repoFullName)

	overseerGVR := schema.GroupVersionResource{
		Group:    "overseer.gemini.google.com",
		Version:  "v1alpha1",
		Resource: "overseers",
	}

	obj := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "overseer.gemini.google.com/v1alpha1",
			"kind":       "Overseer",
			"metadata": map[string]interface{}{
				"name": crName,
			},
			"spec": map[string]interface{}{
				"repoURL":      repoURL,
				"pollInterval": "30m",
				"repo": map[string]interface{}{
					"issueMode":  "enabled",
					"prMode":     "enabled",
					"reviewMode": "enabled",
				},
				"chores": map[string]interface{}{
					"mode": "enabled",
				},
			},
		},
	}

	_, err := dynClient.Resource(overseerGVR).Create(ctx, obj, metav1.CreateOptions{})
	if err != nil {
		if apierrors.IsAlreadyExists(err) {
			klog.Infof("Overseer CR %s already exists. Skipping creation.", crName)
			return
		}
		klog.Errorf("Failed to create Overseer CR for repository %s: %v", repoFullName, err)
		return
	}

	klog.Infof("Successfully created Overseer CR %s for repository %s", crName, repoFullName)
}
