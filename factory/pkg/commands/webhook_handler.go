package commands

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"k8s.io/klog/v2"
)

type WebhookHandlerFlags struct {
	Port         string
	QueueDir     string
	TriggerLabel string
}

func NewWebhookHandlerCommand(ctx context.Context) *cobra.Command {
	var flags WebhookHandlerFlags

	cmd := &cobra.Command{
		Use:   "gh-webhook-handler",
		Short: "Start a local webhook listener and token broker server inside the Overseer pod",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if flags.QueueDir == "" {
				flags.QueueDir = "/workspaces/overseer/queues"
			}
			return runWebhookHandler(ctx, flags.Port, flags.QueueDir, flags.TriggerLabel)
		},
	}

	cmd.Flags().StringVar(&flags.Port, "port", "8080", "Port to listen on")
	cmd.Flags().StringVar(&flags.QueueDir, "queue-dir", "/workspaces/overseer/queues", "Directory path for the task queues")
	cmd.Flags().StringVar(&flags.TriggerLabel, "trigger-label", "factory", "Label name that triggers issue fixes")

	return cmd
}

func runWebhookHandler(ctx context.Context, port, queueDir, triggerLabel string) error {
	incomingDir := filepath.Join(queueDir, "incoming")
	if err := os.MkdirAll(incomingDir, 0755); err != nil {
		return fmt.Errorf("failed to create incoming queue dir: %w", err)
	}

	http.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("OK"))
	})

	// Endpoint 1: Local Event Receiver
	http.HandleFunc("/events", func(w http.ResponseWriter, r *http.Request) {
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

		eventType := r.Header.Get("X-GitHub-Event")
		klog.Infof("Received local event: %s", eventType)

		switch eventType {
		case "issues":
			handleIssuesEvent(bodyBytes, incomingDir, triggerLabel)
		case "pull_request":
			handlePullRequestEvent(bodyBytes, incomingDir)
		case "pull_request_review":
			handlePRReviewEvent(bodyBytes, incomingDir)
		case "check_run":
			handleCheckRunEvent(bodyBytes, incomingDir)
		default:
			klog.Infof("Event type %s ignored", eventType)
		}

		w.WriteHeader(http.StatusOK)
		w.Write([]byte("Event received"))
	})

	// Endpoint 2: Token Broker for Sandboxes
	http.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		role := r.URL.Query().Get("role")
		owner := r.URL.Query().Get("owner")
		repo := r.URL.Query().Get("repo")

		if owner == "" || repo == "" {
			http.Error(w, "owner and repo query parameters are required", http.StatusBadRequest)
			return
		}

		appID, keyPEM, err := loadAppCredentials(role)
		if err != nil {
			klog.Errorf("Failed to load credentials for role %s: %v", role, err)
			http.Error(w, fmt.Sprintf("Failed to load credentials: %v", err), http.StatusInternalServerError)
			return
		}

		token, err := fetchInstallationToken(r.Context(), appID, keyPEM, owner, repo)
		if err != nil {
			klog.Errorf("Failed to fetch token for %s/%s with role %s: %v", owner, repo, role, err)
			http.Error(w, fmt.Sprintf("Failed to fetch installation token: %v", err), http.StatusBadGateway)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{
			"token": token,
		})
	})

	klog.Infof("Starting Local Webhook Listener / Token Broker on port %s...", port)
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

func loadAppCredentials(role string) (string, []byte, error) {
	appIDKey := "PRIMARY_APP_ID"
	pemKey := "PRIMARY_PRIVATE_KEY"
	pemPathKey := "PRIMARY_PRIVATE_KEY_PATH"

	if strings.EqualFold(role, "reviewer") {
		appIDKey = "REVIEWER_APP_ID"
		pemKey = "REVIEWER_PRIVATE_KEY"
		pemPathKey = "REVIEWER_PRIVATE_KEY_PATH"
	}

	appID := os.Getenv(appIDKey)
	if appID == "" {
		appID = os.Getenv("PRIMARY_APP_ID")
		pemKey = "PRIMARY_PRIVATE_KEY"
		pemPathKey = "PRIMARY_PRIVATE_KEY_PATH"
	}

	if appID == "" {
		return "", nil, fmt.Errorf("app ID not configured (checked %s)", appIDKey)
	}

	var privateKeyPEM []byte
	if pemPath := os.Getenv(pemPathKey); pemPath != "" {
		data, err := os.ReadFile(pemPath)
		if err != nil {
			return "", nil, fmt.Errorf("reading private key file %s: %w", pemPath, err)
		}
		privateKeyPEM = data
	} else if pemStr := os.Getenv(pemKey); pemStr != "" {
		privateKeyPEM = []byte(pemStr)
	}

	if len(privateKeyPEM) == 0 {
		return "", nil, fmt.Errorf("private key not configured (checked %s and %s)", pemKey, pemPathKey)
	}

	return appID, privateKeyPEM, nil
}

func generateJWT(appID string, privateKeyPEM []byte) (string, error) {
	block, _ := pem.Decode(privateKeyPEM)
	if block == nil {
		return "", fmt.Errorf("failed to parse PEM block containing private key")
	}

	privKey, err := x509.ParsePKCS1PrivateKey(block.Bytes)
	if err != nil {
		key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
		if err != nil {
			return "", fmt.Errorf("parsing private key: %w", err)
		}
		var ok bool
		privKey, ok = key.(*rsa.PrivateKey)
		if !ok {
			return "", fmt.Errorf("not an RSA private key")
		}
	}

	header := `{"alg":"RS256","typ":"JWT"}`
	now := time.Now().Unix()
	claims := fmt.Sprintf(`{"iss":"%s","iat":%d,"exp":%d}`, appID, now-60, now+600)

	b64Header := base64.RawURLEncoding.EncodeToString([]byte(header))
	b64Claims := base64.RawURLEncoding.EncodeToString([]byte(claims))
	payload := b64Header + "." + b64Claims

	h := sha256.New()
	h.Write([]byte(payload))
	hashed := h.Sum(nil)

	signature, err := rsa.SignPKCS1v15(rand.Reader, privKey, crypto.SHA256, hashed)
	if err != nil {
		return "", fmt.Errorf("signing payload: %w", err)
	}

	b64Signature := base64.RawURLEncoding.EncodeToString(signature)
	return payload + "." + b64Signature, nil
}

func fetchInstallationToken(ctx context.Context, appID string, privateKeyPEM []byte, owner string, repo string) (string, error) {
	jwtToken, err := generateJWT(appID, privateKeyPEM)
	if err != nil {
		return "", err
	}

	reqURL := fmt.Sprintf("https://api.github.com/repos/%s/%s/installation", owner, repo)
	req, err := http.NewRequestWithContext(ctx, "GET", reqURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+jwtToken)
	req.Header.Set("Accept", "application/vnd.github.v3+json")
	req.Header.Set("User-Agent", "overseer-factory-app")

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("failed to get installation ID: status %d, body %s", resp.StatusCode, string(body))
	}

	var inst struct {
		ID int64 `json:"id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&inst); err != nil {
		return "", err
	}

	tokenURL := fmt.Sprintf("https://api.github.com/app/installations/%d/access_tokens", inst.ID)
	tokenReq, err := http.NewRequestWithContext(ctx, "POST", tokenURL, nil)
	if err != nil {
		return "", err
	}
	tokenReq.Header.Set("Authorization", "Bearer "+jwtToken)
	tokenReq.Header.Set("Accept", "application/vnd.github.v3+json")
	tokenReq.Header.Set("User-Agent", "overseer-factory-app")

	tokenResp, err := client.Do(tokenReq)
	if err != nil {
		return "", err
	}
	defer tokenResp.Body.Close()

	if tokenResp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(tokenResp.Body)
		return "", fmt.Errorf("failed to fetch access token: status %d, body %s", tokenResp.StatusCode, string(body))
	}

	var tok struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(tokenResp.Body).Decode(&tok); err != nil {
		return "", err
	}

	return tok.Token, nil
}

func handleIssuesEvent(body []byte, incomingDir, triggerLabel string) {
	var payload struct {
		Action string `json:"action"`
		Issue  struct {
			Number  int    `json:"number"`
			HTMLURL string `json:"html_url"`
			Title   string `json:"title"`
			Body    string `json:"body"`
			Labels  []struct {
				Name string `json:"name"`
			} `json:"labels"`
			State string `json:"state"`
		} `json:"issue"`
	}

	if err := json.Unmarshal(body, &payload); err != nil {
		klog.Errorf("Failed to parse issues webhook: %v", err)
		return
	}

	if payload.Action != "opened" && payload.Action != "labeled" {
		return
	}

	hasTriggerLabel := false
	for _, l := range payload.Issue.Labels {
		if strings.EqualFold(l.Name, triggerLabel) {
			hasTriggerLabel = true
			break
		}
	}

	if !hasTriggerLabel {
		return
	}

	workflowPath := findWorkflowPath(payload.Issue.Body)
	isChore := workflowPath != ""

	taskType := "issue-fix"
	if isChore {
		taskType = "agent-chore"
	}

	filename := fmt.Sprintf("task-issue-%d.yaml", payload.Issue.Number)
	task := &QueueTask{
		Type:      taskType,
		URL:       payload.Issue.HTMLURL,
		Number:    payload.Issue.Number,
		Priority:  "medium",
		Phase:     3,
		CreatedAt: time.Now(),
		Status:    "Pending",
	}
	if isChore {
		task.Phase = 4
		task.AgentFile = workflowPath
		task.SessionID = fmt.Sprintf("issue-%d", payload.Issue.Number)
	}

	if err := writeTaskAtomically(incomingDir, filename, task); err != nil {
		klog.Errorf("Failed to queue issue fix task: %v", err)
	} else {
		klog.Infof("Successfully queued issue fix task #%d", payload.Issue.Number)
	}
}

func handlePullRequestEvent(body []byte, incomingDir string) {
	var payload struct {
		Action      string `json:"action"`
		PullRequest struct {
			Number  int    `json:"number"`
			HTMLURL string `json:"html_url"`
			Head    struct {
				Ref string `json:"ref"`
				SHA string `json:"sha"`
			} `json:"head"`
			User struct {
				Login string `json:"login"`
			} `json:"user"`
			State string `json:"state"`
		} `json:"pull_request"`
	}

	if err := json.Unmarshal(body, &payload); err != nil {
		klog.Errorf("Failed to parse PR webhook: %v", err)
		return
	}

	if payload.Action != "opened" && payload.Action != "synchronize" {
		return
	}

	botName := os.Getenv("GITHUB_LOGIN")
	if strings.EqualFold(payload.PullRequest.User.Login, botName) {
		return
	}

	filename := fmt.Sprintf("task-pr-%d-review.yaml", payload.PullRequest.Number)
	task := &QueueTask{
		Type:      "pr-review",
		URL:       payload.PullRequest.HTMLURL,
		Number:    payload.PullRequest.Number,
		Priority:  "medium",
		Phase:     2,
		CreatedAt: time.Now(),
		Status:    "Pending",
		CommitSHA: payload.PullRequest.Head.SHA,
	}

	if err := writeTaskAtomically(incomingDir, filename, task); err != nil {
		klog.Errorf("Failed to queue review task for PR #%d: %v", payload.PullRequest.Number, err)
	} else {
		klog.Infof("Successfully queued review task for PR #%d", payload.PullRequest.Number)
	}
}

func handlePRReviewEvent(body []byte, incomingDir string) {
	var payload struct {
		Action      string `json:"action"`
		PullRequest struct {
			Number  int    `json:"number"`
			HTMLURL string `json:"html_url"`
			Head    struct {
				SHA string `json:"sha"`
			} `json:"head"`
		} `json:"pull_request"`
		Review struct {
			State string `json:"state"`
			User  struct {
				Login string `json:"login"`
			} `json:"user"`
		} `json:"review"`
	}

	if err := json.Unmarshal(body, &payload); err != nil {
		klog.Errorf("Failed to parse PR review webhook: %v", err)
		return
	}

	if payload.Action != "submitted" {
		return
	}

	if payload.Review.State != "CHANGES_REQUESTED" {
		return
	}

	filename := fmt.Sprintf("task-pr-%d-comments.yaml", payload.PullRequest.Number)
	task := &QueueTask{
		Type:      "pr-comments",
		URL:       payload.PullRequest.HTMLURL,
		Number:    payload.PullRequest.Number,
		Priority:  "medium",
		Phase:     2,
		CreatedAt: time.Now(),
		Status:    "Pending",
		CommitSHA: payload.PullRequest.Head.SHA,
	}

	if err := writeTaskAtomically(incomingDir, filename, task); err != nil {
		klog.Errorf("Failed to queue comments task for PR #%d: %v", payload.PullRequest.Number, err)
	} else {
		klog.Infof("Successfully queued comments task for PR #%d", payload.PullRequest.Number)
	}
}

func handleCheckRunEvent(body []byte, incomingDir string) {
	var payload struct {
		Action   string `json:"action"`
		CheckRun struct {
			Status     string `json:"status"`
			Conclusion string `json:"conclusion"`
			HeadSHA    string `json:"head_sha"`
			PullRequests []struct {
				Number  int    `json:"number"`
				HTMLURL string `json:"html_url"`
			} `json:"pull_requests"`
		} `json:"check_run"`
	}

	if err := json.Unmarshal(body, &payload); err != nil {
		klog.Errorf("Failed to parse check_run webhook: %v", err)
		return
	}

	if payload.Action != "completed" {
		return
	}

	conclusion := payload.CheckRun.Conclusion
	if conclusion != "failure" && conclusion != "timed_out" && conclusion != "cancelled" {
		return
	}

	for _, pr := range payload.CheckRun.PullRequests {
		filename := fmt.Sprintf("task-pr-%d-investigate.yaml", pr.Number)
		task := &QueueTask{
			Type:      "pr-investigate",
			URL:       pr.HTMLURL,
			Number:    pr.Number,
			Priority:  "medium",
			Phase:     3,
			CreatedAt: time.Now(),
			Status:    "Pending",
			CommitSHA: payload.CheckRun.HeadSHA,
		}

		if err := writeTaskAtomically(incomingDir, filename, task); err != nil {
			klog.Errorf("Failed to queue CI failure task for PR #%d: %v", pr.Number, err)
		} else {
			klog.Infof("Successfully queued CI failure task for PR #%d", pr.Number)
		}
	}
}
