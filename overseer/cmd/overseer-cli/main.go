package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/klog/v2"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client/config"
	"sigs.k8s.io/yaml"

	overseerv1alpha1 "github.com/gke-labs/gemini-for-kubernetes-development/overseer/pkg/api/v1alpha1"
	sandboxtaskv1alpha1 "github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/api/sandboxtask/v1alpha1"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/clients"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/github"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/k8s"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/models"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/sandbox"
	githubv39 "github.com/google/go-github/v39/github"
)

var (
	overseerName     string
	namespace        string
	IssueModelsOrder = []string{
		"gemini-3-flash-preview",
		"gemini-3.1-pro-preview",
		"gemini-2.5-pro",
		"gemini-2.5-flash",
	}
)

func main() {
	klog.InitFlags(nil)

	overseerName = os.Getenv("OVERSEER_NAME")
	namespace = os.Getenv("NAMESPACE")

	rootCmd := &cobra.Command{
		Use:   "overseer-cli",
		Short: "CLI for Overseer to manage sandboxes and tasks",
	}

	rootCmd.AddCommand(buildIssueCommand())
	rootCmd.AddCommand(buildPRCommand())
	rootCmd.AddCommand(buildChoreCommand())
	rootCmd.AddCommand(buildReconcileCommand())
	rootCmd.AddCommand(buildDeleteCommand())

	if err := rootCmd.Execute(); err != nil {
		if isRetryable(err) {
			klog.Infof("Retryable error: %v", err)
			os.Exit(2)
		}
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

func buildIssueCommand() *cobra.Command {
	var number int
	var prNumber int
	var taskType string
	var prompt string

	cmd := &cobra.Command{
		Use:   "issue",
		Short: "Create/ensure sandbox and task for an issue",
		RunE: func(_ *cobra.Command, _ []string) error {
			return runIssue(context.Background(), number, prNumber, taskType, prompt)
		},
	}

	cmd.Flags().IntVar(&number, "number", 0, "Issue number")
	cmd.Flags().IntVar(&prNumber, "pr", 0, "PR number to extract issue from")
	cmd.Flags().StringVar(&taskType, "task", "fix-issue", "Task type (e.g., fix-issue, triage-issue)")
	cmd.Flags().StringVar(&prompt, "prompt", "", "Custom prompt for the task")

	return cmd
}

func buildPRCommand() *cobra.Command {
	var number int
	var taskType string
	var submit bool
	var prompt string

	cmd := &cobra.Command{
		Use:   "pr",
		Short: "Create/ensure sandbox and task for a PR",
		RunE: func(_ *cobra.Command, _ []string) error {
			return runPR(context.Background(), number, taskType, submit, prompt)
		},
	}

	cmd.Flags().IntVar(&number, "number", 0, "PR number")
	cmd.Flags().StringVar(&taskType, "task", "review", "Task type (e.g., review, address-feedback, investigate-failures)")
	cmd.Flags().BoolVar(&submit, "submit", false, "Submit agent draft from task as review")
	cmd.Flags().StringVar(&prompt, "prompt", "", "Custom prompt for the task")
	_ = cmd.MarkFlagRequired("number")

	return cmd
}

func buildChoreCommand() *cobra.Command {
	var name string
	var file string

	cmd := &cobra.Command{
		Use:   "chore",
		Short: "Create/ensure sandbox and task for a chore",
		RunE: func(_ *cobra.Command, _ []string) error {
			return runChore(context.Background(), name, file)
		},
	}

	cmd.Flags().StringVar(&name, "name", "", "Chore name")
	cmd.Flags().StringVar(&file, "file", "", "Chore definition file path")
	_ = cmd.MarkFlagRequired("file")

	return cmd
}

func buildReconcileCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "reconcile",
		Short: "Reconcile chores: delete sandboxes for chores that are excluded or no longer present",
		RunE: func(_ *cobra.Command, _ []string) error {
			return runReconcile(context.Background())
		},
	}
	return cmd
}

type ChoreDefinition struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Schedule    string `json:"schedule"`
	SkipPR      bool   `json:"skipPR,omitempty"`
	Prompt      string `json:"-"`
}

type RetryableError struct {
	Message string
}

func (e *RetryableError) Error() string {
	return e.Message
}

func isRetryable(err error) bool {
	if err == nil {
		return false
	}
	_, ok := err.(*RetryableError)
	return ok
}

func isGitHubTransient(err error) bool {
	if err == nil {
		return false
	}
	// Check for rate limit or server errors
	if githubErr, ok := err.(*githubv39.ErrorResponse); ok {
		if githubErr.Response != nil {
			status := githubErr.Response.StatusCode
			return status == 403 || status == 429 || (status >= 500 && status <= 599)
		}
	}
	// Also check for network timeouts/connection issues
	errStr := err.Error()
	return strings.Contains(errStr, "timeout") || strings.Contains(errStr, "connection refused") || strings.Contains(errStr, "EOF")
}

func patchOwnerReferences(existing []metav1.OwnerReference, newRef metav1.OwnerReference) []metav1.OwnerReference {
	found := false
	for i, ref := range existing {
		if ref.APIVersion == newRef.APIVersion && ref.Kind == newRef.Kind && ref.Name == newRef.Name {
			existing[i] = newRef
			found = true
			break
		}
	}
	if !found {
		existing = append(existing, newRef)
	}
	return existing
}

type CLIConfig struct {
	BotLogin  string
	BotName   string
	BotEmail  string
	UserLogin string
	UserName  string
	UserEmail string
}

func loadConfig() CLIConfig {
	userLogin := os.Getenv("GITHUB_USER_ID")
	userName := os.Getenv("GITHUB_USER_NAME")
	if userName == "" {
		userName = userLogin
	}
	botLogin := os.Getenv("GITHUB_BOT_LOGIN")
	botName := os.Getenv("GITHUB_BOT_NAME")
	if botName == "" {
		botName = botLogin
	}
	return CLIConfig{
		BotLogin:  botLogin,
		BotName:   botName,
		BotEmail:  os.Getenv("GITHUB_BOT_EMAIL"),
		UserLogin: userLogin,
		UserName:  userName,
		UserEmail: os.Getenv("GITHUB_USER_EMAIL"),
	}
}

func runChore(ctx context.Context, name string, file string) error {
	if overseerName == "" || namespace == "" {
		return fmt.Errorf("OVERSEER_NAME and NAMESPACE environment variables must be set")
	}

	conf := loadConfig()
	choresMode := os.Getenv("CHORES_MODE")
	if choresMode == "disabled" {
		klog.Infof("Chore handling is disabled (CHORES_MODE=disabled). Skipping.")
		return nil
	}

	chore, err := parseChore(file)
	if err != nil {
		return err
	}
	if name != "" {
		chore.Name = name
	}
	if chore.Name == "" {
		return fmt.Errorf("chore name is required (either in frontmatter or via --name)")
	}

	cfg, err := config.GetConfig()
	if err != nil {
		return fmt.Errorf("unable to get kubeconfig: %w", err)
	}

	dynClient, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return fmt.Errorf("unable to create dynamic client: %w", err)
	}

	clientset, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return fmt.Errorf("unable to create clientset: %w", err)
	}

	kubeClient := &clients.KubernetesClient{
		DynamicClient: dynClient,
		Clientset:     clientset,
	}
	manager := k8s.NewManager(kubeClient)

	rwUnstructured, err := getOverseer(ctx, kubeClient.DynamicClient, overseerName)
	if err != nil {
		return fmt.Errorf("failed to get Overseer %s: %w", overseerName, err)
	}

	var overseer overseerv1alpha1.Overseer
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(rwUnstructured.Object, &overseer); err != nil {
		return fmt.Errorf("failed to convert Overseer: %w", err)
	}

	sandboxName := fmt.Sprintf("chore-%s-%s", overseer.Name, slugify(chore.Name))

	isAllowed := isChoreAllowed(overseer.Spec.Chores, chore.Name)
	isPaused := strings.EqualFold(chore.Schedule, "never")

	if !isAllowed || isPaused {
		var reasons []string
		if !isAllowed {
			reasons = append(reasons, "excluded by configuration")
		}
		if isPaused {
			reasons = append(reasons, "paused (schedule: never)")
		}
		reason := strings.Join(reasons, " and ")

		if choresMode == "dryrun" {
			klog.Infof("[dryrun] Ensuring sandbox %s is deleted for chore %s (%s)", sandboxName, chore.Name, reason)
			_ = deleteSandbox(ctx, kubeClient, namespace, sandboxName)
			return nil
		}
		klog.Infof("Chore %s %s. Ensuring sandbox is deleted.", chore.Name, reason)
		return deleteSandbox(ctx, kubeClient, namespace, sandboxName)
	}

	if choresMode == "dryrun" {
		klog.Infof("[dryrun] Would create sandbox and task chore for chore %s in Overseer %s", chore.Name, overseerName)
		return nil
	}

	owner, repo, err := parseRepoURL(overseer.Spec.RepoURL)
	if err != nil {
		return fmt.Errorf("failed to parse RepoURL: %w", err)
	}

	// 1. Check if a task for this chore already exists BEFORE creating sandbox
	taskList, err := manager.ListSandboxTasks(ctx, namespace, sandboxName)
	if err == nil {
		for i := range taskList.Items {
			task := &taskList.Items[i]
			if task.Spec.Type == "chore" {
				state := task.Status.TaskState
				if state == "" {
					state = "Pending"
				}
				if state == "Completed" {
					if time.Since(task.CreationTimestamp.Time) < 1*time.Hour {
						klog.V(2).Infof("Chore %s: Task already exists in state %s (created %v ago). Skipping.", chore.Name, state, time.Since(task.CreationTimestamp.Time))
						return nil
					}
					klog.Infof("Chore %s: Found old Completed task (%v ago). Deleting to allow periodic run.", chore.Name, time.Since(task.CreationTimestamp.Time))
					_ = manager.DeleteSandboxTask(ctx, namespace, task.Name)
					// Fall through to create new task
				}
				if state == "Running" || state == "Pending" {
					if time.Since(task.CreationTimestamp.Time) < 2*time.Hour {
						klog.V(2).Infof("Chore %s: Task already exists in state %s (created %v ago). Skipping.", chore.Name, state, time.Since(task.CreationTimestamp.Time))
						return nil
					}
					klog.Warningf("Chore %s: Found STALE task in state %s (created %v ago). Deleting stale task and allowing new one.", chore.Name, state, time.Since(task.CreationTimestamp.Time))
					_ = manager.DeleteSandboxTask(ctx, namespace, task.Name)
					return &RetryableError{Message: fmt.Sprintf("deleted stale task for chore %s, waiting for pod termination", chore.Name)}
				}
				if state == "Failed" {
					if time.Since(task.CreationTimestamp.Time) < 1*time.Hour {
						klog.V(2).Infof("Chore %s: Task failed recently (%v ago). Skipping for backoff.", chore.Name, time.Since(task.CreationTimestamp.Time))
						return nil
					}
					klog.Warningf("Chore %s: Found old Failed task (%v ago). Deleting to allow retry.", chore.Name, time.Since(task.CreationTimestamp.Time))
					_ = manager.DeleteSandboxTask(ctx, namespace, task.Name)
					return &RetryableError{Message: fmt.Sprintf("deleted failed task for chore %s, waiting for backoff/pod termination", chore.Name)}
				}
			}
		}
	}

	// 2. Ensure Sandbox exists and is configured correctly
	klog.Infof("Ensuring sandbox %s exists...", sandboxName)
	if err := createChoreSandbox(ctx, kubeClient, &overseer, chore, sandboxName, conf); err != nil {
		return fmt.Errorf("failed to ensure chore sandbox: %w", err)
	}

	// 3. Create Task
	taskType := "chore"
	klog.Infof("Creating task %s for sandbox %s...", taskType, sandboxName)
	params := map[string]string{
		"AGENT_PROMPT": chore.Prompt,
		"CHORE_NAME":   chore.Name,
		"CHORE_FILE":   file,
		"REPO_OWNER":   owner,
		"REPO_NAME":    repo,
		"CLONE_URL":    overseer.Spec.RepoURL,
	}
	if chore.SkipPR {
		params["SKIP_PR"] = "true"
	}

	err = manager.CreateSandboxTask(ctx, namespace, sandboxName, "Sandbox", taskType, params)
	if err != nil {
		return fmt.Errorf("failed to create sandbox task: %w", err)
	}

	klog.Info("Done.")
	return nil
}

func parseChore(path string) (*ChoreDefinition, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	parts := strings.SplitN(string(data), "---", 3)
	if len(parts) < 3 {
		return nil, fmt.Errorf("invalid chore format in %s: missing frontmatter", path)
	}

	var chore ChoreDefinition
	if err := yaml.Unmarshal([]byte(parts[1]), &chore); err != nil {
		return nil, fmt.Errorf("failed to unmarshal frontmatter in %s: %w", path, err)
	}

	chore.Prompt = strings.TrimSpace(parts[2])
	return &chore, nil
}

func slugify(s string) string {
	s = strings.ToLower(s)
	s = strings.ReplaceAll(s, " ", "-")
	var res strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			res.WriteRune(r)
		}
	}
	return res.String()
}

func createChoreSandbox(ctx context.Context, kubeClient *clients.KubernetesClient, overseer *overseerv1alpha1.Overseer, chore *ChoreDefinition, sandboxName string, conf CLIConfig) error {
	cloneURL := overseer.Spec.RepoURL
	if !strings.HasSuffix(cloneURL, ".git") {
		cloneURL += ".git"
	}

	_, repo, err := parseRepoURL(overseer.Spec.RepoURL)
	if err != nil {
		return fmt.Errorf("failed to parse RepoURL: %w", err)
	}

	userLogin := conf.UserLogin
	userName := conf.UserName
	userEmail := conf.UserEmail

	botLogin := conf.BotLogin
	botName := conf.BotName
	botEmail := conf.BotEmail

	apiKeySecretName := overseer.Spec.GeminiAPIKeySecretName
	if apiKeySecretName == "" {
		apiKeySecretName = "gemini-api-key"
	}

	githubSecretName := overseer.Spec.RobotAccount

	scriptToken, err := getTokenFromScript()
	if err != nil {
		klog.Warningf("failed to get token from script: %v", err)
	}

	opt := sandbox.AgentSandboxOptions{
		DevSandboxOptions: sandbox.DevSandboxOptions{
			Name:      sandboxName,
			Namespace: namespace,
			Labels: map[string]string{
				"review.gemini.google.com/overseer": overseer.Name,
				"sandbox.gemini.google.com/type":    "chore",
				"chore.gemini.google.com/name":      slugify(chore.Name),
			},
			CloneURL:            cloneURL,
			HTMLURL:             strings.TrimSuffix(overseer.Spec.RepoURL, ".git"),
			Branch:              "main", // Default branch for chores
			Origin:              fmt.Sprintf("github.com/%s/%s", userLogin, repo),
			PushEnabled:         true,
			UserLogin:           userLogin,
			UserName:            userName,
			UserEmail:           userEmail,
			BotLogin:            botLogin,
			BotName:             botName,
			BotEmail:            botEmail,
			LLMAPIKeySecretName: apiKeySecretName,
			GithubSecretName:    githubSecretName,
			LLMAPIKey:           scriptToken,
			OverseerName:        overseerName,
			RepoSandboxImage:    os.Getenv("REPO_SANDBOX_IMAGE"),
			ConfigDirImage:      os.Getenv("CONFIG_DIR_IMAGE"),
			HTTPEnabled:         true,
			Replicas:            1,
			WorkspaceDiskSize:   overseer.Spec.WorkspaceDiskSize,
			ServiceAccountName:  "overseer-sandbox",
		},
		IssueRepo: repo,
	}

	opt.LLMProvider = "gemini-cli"
	opt.LLMConfigdirRef = overseer.Spec.ConfigdirRef
	opt.Image = overseer.Spec.Image

	sb, svc := sandbox.NewAgentSandbox(opt)
	sb.SetName(sandboxName)

	sb.SetOwnerReferences([]metav1.OwnerReference{
		{
			APIVersion:         "overseer.gemini.google.com/v1alpha1",
			Kind:               "Overseer",
			Name:               overseer.Name,
			UID:                overseer.UID,
			Controller:         ptr.To(true),
			BlockOwnerDeletion: ptr.To(true),
		},
	})

	createdSb, err := ensureSandbox(ctx, kubeClient.DynamicClient, namespace, sb)
	if err != nil {
		return fmt.Errorf("failed to ensure chore sandbox: %w", err)
	}

	return ensureService(ctx, kubeClient.Clientset, namespace, svc, createdSb)
}

func ensureSandbox(ctx context.Context, dynClient dynamic.Interface, namespace string, sb *unstructured.Unstructured) (*unstructured.Unstructured, error) {
	// 1. Fetch existing sandbox to merge OwnerReferences without stripping other controllers
	existing, err := dynClient.Resource(k8s.SandboxGVR).Namespace(namespace).Get(ctx, sb.GetName(), metav1.GetOptions{})
	if err == nil {
		newRefs := sb.GetOwnerReferences()
		if len(newRefs) > 0 {
			merged := existing.GetOwnerReferences()
			for _, nr := range newRefs {
				merged = patchOwnerReferences(merged, nr)
			}
			sb.SetOwnerReferences(merged)
		}
	} else if !errors.IsNotFound(err) {
		return nil, fmt.Errorf("failed to fetch existing sandbox %s: %w", sb.GetName(), err)
	}

	// 2. Use Server-Side Apply to ensure the Sandbox and its merged OwnerReferences.
	// This avoids redundant Update calls and robustly handles adoption.
	sb.SetManagedFields(nil)
	data, err := json.Marshal(sb)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal sandbox %s: %w", sb.GetName(), err)
	}

	force := true
	updated, err := dynClient.Resource(k8s.SandboxGVR).Namespace(namespace).Patch(ctx, sb.GetName(), types.ApplyPatchType, data, metav1.PatchOptions{
		FieldManager: "overseer",
		Force:        &force,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to apply sandbox %s: %w", sb.GetName(), err)
	}
	return updated, nil
}

func ensureService(ctx context.Context, clientset kubernetes.Interface, namespace string, svc *corev1.Service, owner *unstructured.Unstructured) error {
	// 1. Manage OwnerReferences via strategic Update or initial configuration
	existing, err := clientset.CoreV1().Services(namespace).Get(ctx, svc.Name, metav1.GetOptions{})
	if err == nil {
		if owner != nil {
			newRef := metav1.OwnerReference{
				APIVersion:         owner.GetAPIVersion(),
				Kind:               owner.GetKind(),
				Name:               owner.GetName(),
				UID:                owner.GetUID(),
				Controller:         ptr.To(true),
				BlockOwnerDeletion: ptr.To(true),
			}
			existing.OwnerReferences = patchOwnerReferences(existing.OwnerReferences, newRef)
			// Apply merged references back to our template for SSA
			svc.OwnerReferences = existing.OwnerReferences

			// Proactively update to handle adoption and avoid GC race conditions
			_, err = clientset.CoreV1().Services(namespace).Update(ctx, existing, metav1.UpdateOptions{})
			if err != nil && !errors.IsConflict(err) {
				return fmt.Errorf("failed to update service owner references: %w", err)
			}
		}
	} else if errors.IsNotFound(err) {
		if owner != nil {
			svc.OwnerReferences = []metav1.OwnerReference{
				{
					APIVersion:         owner.GetAPIVersion(),
					Kind:               owner.GetKind(),
					Name:               owner.GetName(),
					UID:                owner.GetUID(),
					Controller:         ptr.To(true),
					BlockOwnerDeletion: ptr.To(true),
				},
			}
		}
	} else {
		return fmt.Errorf("failed to get service %s: %w", svc.Name, err)
	}

	// 2. Use Server-Side Apply to enforce the desired state (Spec and OwnerReferences).
	svc.APIVersion = "v1"
	svc.Kind = "Service"
	svc.ManagedFields = nil // Let Kubernetes manage this

	data, err := json.Marshal(svc)
	if err != nil {
		return fmt.Errorf("failed to marshal service %s: %w", svc.Name, err)
	}

	force := true
	_, err = clientset.CoreV1().Services(namespace).Patch(ctx, svc.Name, types.ApplyPatchType, data, metav1.PatchOptions{
		FieldManager: "overseer",
		Force:        &force,
	})
	if err != nil {
		return fmt.Errorf("failed to apply service %s: %w", svc.Name, err)
	}
	return nil
}

func runIssue(ctx context.Context, number int, prNumber int, taskType string, customPrompt string) error {
	if overseerName == "" || namespace == "" {
		return fmt.Errorf("OVERSEER_NAME and NAMESPACE environment variables must be set")
	}

	conf := loadConfig()
	issueMode := os.Getenv("ISSUE_MODE")
	if issueMode == "disabled" {
		klog.Infof("Issue handling is disabled (ISSUE_MODE=disabled). Skipping.")
		return nil
	}
	if issueMode == "dryrun" {
		if number != 0 {
			klog.Infof("[dryrun] Would create/ensure sandbox and task %s for issue %d in Overseer %s", taskType, number, overseerName)
		} else if prNumber != 0 {
			klog.Infof("[dryrun] Would create/ensure sandbox and task %s for issue from PR %d in Overseer %s", taskType, prNumber, overseerName)
		}
		return nil
	}

	cfg, err := config.GetConfig()
	if err != nil {
		return fmt.Errorf("unable to get kubeconfig: %w", err)
	}

	dynClient, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return fmt.Errorf("unable to create dynamic client: %w", err)
	}

	clientset, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return fmt.Errorf("unable to create clientset: %w", err)
	}

	kubeClient := &clients.KubernetesClient{
		DynamicClient: dynClient,
		Clientset:     clientset,
	}
	manager := k8s.NewManager(kubeClient)

	rwUnstructured, err := getOverseer(ctx, kubeClient.DynamicClient, overseerName)
	if err != nil {
		return fmt.Errorf("failed to get Overseer %s: %w", overseerName, err)
	}

	var overseer overseerv1alpha1.Overseer
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(rwUnstructured.Object, &overseer); err != nil {
		return fmt.Errorf("failed to convert Overseer: %w", err)
	}

	ghClient, err := github.NewClient(ctx)
	if err != nil {
		return fmt.Errorf("failed to create github client: %w", err)
	}

	owner, repo, err := parseRepoURL(overseer.Spec.RepoURL)
	if err != nil {
		return fmt.Errorf("failed to parse RepoURL: %w", err)
	}

	if number == 0 && prNumber != 0 {
		klog.Infof("Resolving issue from PR %d...", prNumber)
		number, err = resolveIssueFromPR(ctx, owner, repo, prNumber)
		if err != nil {
			return fmt.Errorf("failed to resolve issue from PR: %w", err)
		}
		klog.Infof("Resolved to issue %d", number)
	}

	if number == 0 {
		return fmt.Errorf("either --number or --pr must be provided")
	}

	issue, _, err := ghClient.Issues.Get(ctx, owner, repo, number)
	if err != nil {
		if isGitHubTransient(err) {
			return &RetryableError{Message: fmt.Sprintf("transient error getting issue %d: %v", number, err)}
		}
		return fmt.Errorf("failed to get issue %d: %w", number, err)
	}

	sandboxName := fmt.Sprintf("%s-issue-%d", overseer.Name, number)

	var lastReopenedAt *time.Time
	var eventsLastCheckedAt *time.Time
	var sandboxUnstructured *unstructured.Unstructured
	var eventsFetchSuccess bool

	sUnstructured, err := kubeClient.DynamicClient.Resource(k8s.SandboxGVR).Namespace(namespace).Get(ctx, sandboxName, metav1.GetOptions{})
	if err == nil {
		sandboxUnstructured = sUnstructured
		annotations := sandboxUnstructured.GetAnnotations()
		if annotations != nil {
			if annotations["lastReopenedAt"] != "" {
				t, err := time.Parse(time.RFC3339, annotations["lastReopenedAt"])
				if err != nil {
					klog.Warningf("Issue #%d: Failed to parse lastReopenedAt annotation %q: %v", number, annotations["lastReopenedAt"], err)
				} else {
					lastReopenedAt = &t
				}
			}
			if annotations["eventsLastCheckedAt"] != "" {
				t, err := time.Parse(time.RFC3339, annotations["eventsLastCheckedAt"])
				if err != nil {
					klog.Warningf("Issue #%d: Failed to parse eventsLastCheckedAt annotation %q: %v", number, annotations["eventsLastCheckedAt"], err)
				} else {
					eventsLastCheckedAt = &t
				}
			}
		}
	}

	// Fetch events to check for reopened status
	if eventsLastCheckedAt != nil && !issue.GetUpdatedAt().After(*eventsLastCheckedAt) {
		klog.V(4).Infof("Issue #%d: No new events since last check at %v. Skipping event pagination.", number, eventsLastCheckedAt)
		eventsFetchSuccess = true
	} else {
		listEventsOpt := &githubv39.ListOptions{PerPage: 100}
		eventsFetchSuccess = true
		page1Events, resp, err := ghClient.Issues.ListIssueEvents(ctx, owner, repo, number, listEventsOpt)
		if err != nil {
			if isGitHubTransient(err) {
				return &RetryableError{Message: fmt.Sprintf("transient error listing events for issue %d: %v", number, err)}
			}
			klog.Warningf("Issue #%d: Failed to list events: %v", number, err)
			eventsFetchSuccess = false
		} else {
			lastPage := resp.LastPage
			if lastPage == 0 {
				lastPage = 1
			}
		StopEvents:
			for p := lastPage; p >= 1; p-- {
				var events []*githubv39.IssueEvent
				if p == 1 {
					events = page1Events
				} else {
					listEventsOpt.Page = p
					var err error
					events, _, err = ghClient.Issues.ListIssueEvents(ctx, owner, repo, number, listEventsOpt)
					if err != nil {
						klog.Warningf("Issue #%d: Failed to list events on page %d: %v", number, p, err)
						eventsFetchSuccess = false
						break StopEvents
					}
				}
				for i := len(events) - 1; i >= 0; i-- {
					e := events[i]
					createdAt := e.GetCreatedAt()
					if eventsLastCheckedAt != nil && createdAt.Before(*eventsLastCheckedAt) {
						break StopEvents
					}
					if e.GetEvent() == "reopened" {
						t := e.GetCreatedAt()
						if lastReopenedAt == nil || t.After(*lastReopenedAt) {
							lastReopenedAt = &t
						}
					}
				}
			}
		}
	}

	// 1. Check if a task of the same type already exists for this issue BEFORE creating sandbox
	taskList, err := manager.ListSandboxTasks(ctx, namespace, sandboxName)
	if err == nil {
		for i := range taskList.Items {
			task := &taskList.Items[i]
			if task.Spec.Type == taskType {
				state := task.Status.TaskState
				if state == "" {
					state = "Pending"
				}
				if state == "Completed" {
					if lastReopenedAt != nil && task.CreationTimestamp.Before(&metav1.Time{Time: *lastReopenedAt}) {
						klog.Infof("Issue #%d: Found historical task %s, but issue was reopened since then. Proceeding with new task.", number, taskType)
						continue
					}
					klog.V(2).Infof("Issue #%d: Task %s already exists in state %s. Skipping.", number, taskType, state)
					return nil
				}
				if state == "Running" || state == "Pending" {
					if time.Since(task.CreationTimestamp.Time) < 2*time.Hour {
						klog.V(2).Infof("Issue #%d: Task %s already exists in state %s (created %v ago). Skipping.", number, taskType, state, time.Since(task.CreationTimestamp.Time))
						return nil
					}
					klog.Warningf("Issue #%d: Found STALE task %s in state %s (created %v ago). Deleting stale task and allowing new one.", number, taskType, state, time.Since(task.CreationTimestamp.Time))
					_ = manager.DeleteSandboxTask(ctx, namespace, task.Name)
					return &RetryableError{Message: fmt.Sprintf("deleted stale task for issue %d, waiting for pod termination", number)}
				}
				if state == "Failed" {
					if time.Since(task.CreationTimestamp.Time) < 1*time.Hour {
						klog.V(2).Infof("Issue #%d: Task %s failed recently (%v ago). Skipping for backoff.", number, taskType, time.Since(task.CreationTimestamp.Time))
						return nil
					}
					klog.Warningf("Issue #%d: Found old Failed task %s (%v ago). Deleting to allow retry.", number, taskType, time.Since(task.CreationTimestamp.Time))
					_ = manager.DeleteSandboxTask(ctx, namespace, task.Name)
					return &RetryableError{Message: fmt.Sprintf("deleted failed task for issue %d, waiting for backoff/pod termination", number)}
				}
			}
		}
	}

	var sandboxIsActive bool
	if sandboxUnstructured != nil {
		replicas, found, err := unstructured.NestedInt64(sandboxUnstructured.Object, "spec", "replicas")
		if err == nil && (!found || replicas > 0) {
			sandboxIsActive = true
		}
	}

	// 2. Check limit only if we need to create or activate a sandbox
	if !sandboxIsActive && overseer.Spec.MaxActiveIssues != nil {
		maxIssues := *overseer.Spec.MaxActiveIssues
		activeCount, err := countActiveSandboxes(ctx, kubeClient.DynamicClient, namespace, overseerName, "issue")
		if err != nil {
			return fmt.Errorf("failed to count active issue sandboxes: %w", err)
		}
		if int32(activeCount) >= maxIssues {
			// Instead of returning nil, return an error so overseer logs it and skips
			return fmt.Errorf("limit_reached: max active issues limit (%d) reached (currently %d active)", maxIssues, activeCount)
		}
	}

	// 3. Ensure Sandbox exists and is configured correctly
	klog.Infof("Ensuring sandbox %s exists...", sandboxName)
	if err := createIssueSandbox(ctx, kubeClient, &overseer, issue, conf); err != nil {
		return fmt.Errorf("failed to ensure issue sandbox: %w", err)
	}

	// Cache timestamps in sandbox annotations now that sandbox exists
	if eventsFetchSuccess {
		if lastReopenedAt != nil {
			_ = manager.UpdateSandboxAnnotation(ctx, namespace, sandboxName, "lastReopenedAt", lastReopenedAt.Format(time.RFC3339))
		}
		_ = manager.UpdateSandboxAnnotation(ctx, namespace, sandboxName, "eventsLastCheckedAt", issue.GetUpdatedAt().Format(time.RFC3339))
	}

	// 4. Create Task
	klog.Infof("Creating task %s for sandbox %s...", taskType, sandboxName)
	agentPrompt := customPrompt
	if agentPrompt == "" {
		agentPrompt = overseer.Spec.IssuePrompt
	}

	params := map[string]string{
		"ISSUE_URL":       issue.GetHTMLURL(),
		"PULL_REQUEST_ID": fmt.Sprintf("%d", prNumber),
		"AGENT_PROMPT":    agentPrompt,
		"PR_LABEL":        "overseer",
	}
	params["model"] = strings.Join(IssueModelsOrder, ",")

	err = manager.CreateSandboxTask(ctx, namespace, sandboxName, "Sandbox", taskType, params)
	if err != nil {
		return fmt.Errorf("failed to create sandbox task: %w", err)
	}

	klog.Info("Done.")
	return nil
}

func runPR(ctx context.Context, number int, taskType string, submit bool, customPrompt string) error {
	// Similar to runIssue but for PRs
	if overseerName == "" || namespace == "" {
		return fmt.Errorf("OVERSEER_NAME and NAMESPACE environment variables must be set")
	}

	conf := loadConfig()
	mode := ""
	modeName := ""
	if submit || taskType == "review" {
		modeName = "REVIEW_MODE"
		mode = os.Getenv(modeName)
	} else {
		modeName = "PR_MODE"
		mode = os.Getenv(modeName)
	}

	if mode == "disabled" {
		klog.Infof("PR/Review handling is disabled (%s=disabled). Skipping.", modeName)
		return nil
	}

	if mode == "dryrun" {
		klog.Infof("[dryrun] Would create/ensure sandbox and task %s for PR %d in Overseer %s", taskType, number, overseerName)
		return nil
	}

	cfg, err := config.GetConfig()
	if err != nil {
		return fmt.Errorf("unable to get kubeconfig: %w", err)
	}

	dynClient, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return fmt.Errorf("unable to create dynamic client: %w", err)
	}

	clientset, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return fmt.Errorf("unable to create clientset: %w", err)
	}

	kubeClient := &clients.KubernetesClient{
		DynamicClient: dynClient,
		Clientset:     clientset,
	}
	manager := k8s.NewManager(kubeClient)

	botLogin := conf.BotLogin
	userLogin := conf.UserLogin

	if submit {
		klog.Infof("Submitting agent draft for PR %d...", number)
		return submitAgentDraft(ctx, manager, kubeClient, namespace, overseerName, number)
	}

	rwUnstructured, err := getOverseer(ctx, kubeClient.DynamicClient, overseerName)
	if err != nil {
		return fmt.Errorf("failed to get Overseer %s: %w", overseerName, err)
	}

	var overseer overseerv1alpha1.Overseer
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(rwUnstructured.Object, &overseer); err != nil {
		return fmt.Errorf("failed to convert Overseer: %w", err)
	}

	ghClient, err := github.NewClient(ctx)
	if err != nil {
		return fmt.Errorf("failed to create github client: %w", err)
	}

	owner, repo, err := parseRepoURL(overseer.Spec.RepoURL)
	if err != nil {
		return fmt.Errorf("failed to parse RepoURL: %w", err)
	}

	pr, _, err := ghClient.PullRequests.Get(ctx, owner, repo, number)
	if err != nil {
		if isGitHubTransient(err) {
			return &RetryableError{Message: fmt.Sprintf("transient error getting PR %d: %v", number, err)}
		}
		return fmt.Errorf("failed to get PR %d: %w", number, err)
	}

	sandboxName := fmt.Sprintf("%s-pr-%d", overseer.Name, number)

	head := pr.GetHead()
	if head == nil || head.GetSHA() == "" {
		return fmt.Errorf("PR #%d has no head SHA", number)
	}
	headSHA := head.GetSHA()

	// Check if a task for this SHA already exists (only for review tasks)
	// We do this BEFORE the limit check so that already-handled PRs don't consume the "active" quota
	// if they happen to have lingering sandboxes.
	if taskType == "review" {
		// 1. Check local Kubernetes first for an actively running or completed task for this SHA
		// to save GitHub API quota and prevent concurrency conflicts.
		taskList, err := manager.ListSandboxTasks(ctx, namespace, sandboxName)
		if err == nil {
			for i := range taskList.Items {
				task := &taskList.Items[i]
				if task.Spec.Type == "review" {
					state := task.Status.TaskState
					if state == "" {
						state = "Pending"
					}

					if strings.EqualFold(task.Spec.Params["HEAD_SHA"], headSHA) {
						if state == "Completed" {
							klog.V(2).Infof("PR #%d: Review task for SHA %s already exists in state %s. Skipping.", number, headSHA, state)
							return nil
						}
						if state == "Running" || state == "Pending" {
							if time.Since(task.CreationTimestamp.Time) < 2*time.Hour {
								klog.V(2).Infof("PR #%d: Review task for SHA %s already exists in state %s (created %v ago). Skipping.", number, headSHA, state, time.Since(task.CreationTimestamp.Time))
								return nil
							}
							klog.Warningf("PR #%d: Found STALE review task for SHA %s in state %s (created %v ago). Deleting stale task and allowing new one.", number, headSHA, state, time.Since(task.CreationTimestamp.Time))
							_ = manager.DeleteSandboxTask(ctx, namespace, task.Name)
							return &RetryableError{Message: fmt.Sprintf("deleted stale review task for PR %d, waiting for pod termination", number)}
						}
						if state == "Failed" {
							if time.Since(task.CreationTimestamp.Time) < 1*time.Hour {
								klog.V(2).Infof("PR #%d: Review task for SHA %s failed recently (%v ago). Skipping for backoff.", number, headSHA, time.Since(task.CreationTimestamp.Time))
								return nil
							}
							klog.Warningf("PR #%d: Found old Failed review task for SHA %s (%v ago). Deleting to allow retry.", number, headSHA, time.Since(task.CreationTimestamp.Time))
							_ = manager.DeleteSandboxTask(ctx, namespace, task.Name)
							return &RetryableError{Message: fmt.Sprintf("deleted failed review task for PR %d, waiting for backoff/pod termination", number)}
						}
					} else {
						// Another SHA is being reviewed
						if state == "Running" || state == "Pending" {
							if time.Since(task.CreationTimestamp.Time) < 2*time.Hour {
								klog.V(2).Infof("PR #%d: Review task for DIFFERENT SHA %s is currently %s (created %v ago). Skipping to avoid concurrency conflict.", number, task.Spec.Params["HEAD_SHA"], state, time.Since(task.CreationTimestamp.Time))
								return &RetryableError{Message: fmt.Sprintf("another SHA is being reviewed for PR %d, waiting for it to finish", number)}
							}
							klog.Warningf("PR #%d: Found STALE review task for DIFFERENT SHA %s in state %s (created %v ago). Deleting stale task and allowing new one.", number, task.Spec.Params["HEAD_SHA"], state, time.Since(task.CreationTimestamp.Time))
							_ = manager.DeleteSandboxTask(ctx, namespace, task.Name)
							return &RetryableError{Message: fmt.Sprintf("deleted stale review task for PR %d (different SHA), waiting for pod termination", number)}
						}
						if state == "Failed" {
							klog.Warningf("PR #%d: Found old Failed review task for DIFFERENT SHA %s. Deleting to avoid clutter.", number, task.Spec.Params["HEAD_SHA"])
							_ = manager.DeleteSandboxTask(ctx, namespace, task.Name)
						}
						if state == "Completed" {
							klog.Warningf("PR #%d: Found old Completed review task for DIFFERENT SHA %s. Deleting to avoid clutter.", number, task.Spec.Params["HEAD_SHA"])
							_ = manager.DeleteSandboxTask(ctx, namespace, task.Name)
						}
					}
				}
			}
		}
	}

	var sandboxIsActive bool
	var sandboxUnstructured *unstructured.Unstructured
	sUnstructured, err := kubeClient.DynamicClient.Resource(k8s.SandboxGVR).Namespace(namespace).Get(ctx, sandboxName, metav1.GetOptions{})
	if err == nil {
		sandboxUnstructured = sUnstructured
		replicas, found, err := unstructured.NestedInt64(sandboxUnstructured.Object, "spec", "replicas")
		if err == nil && (!found || replicas > 0) {
			sandboxIsActive = true
		}
	} else if !errors.IsNotFound(err) {
		return fmt.Errorf("failed to check if sandbox exists: %w", err)
	}

	// Check if a task for this SHA already exists (only for review tasks)
	var lastReopenedAt *time.Time
	var eventsFetchSuccess bool

	if taskType == "review" {
		// 2. Then check GitHub for already submitted reviews for this SHA
		if botLogin == "" && userLogin == "" {
			klog.Warningf("PR #%d: Neither GITHUB_BOT_LOGIN nor GITHUB_USER_ID is set. Skipping GitHub review deduplication.", number)
		} else {
			// Check for reopened events to allow fresh reviews on resurrected PRs
			var eventsLastCheckedAt *time.Time

			// Try to get cached timestamps from sandbox annotations
			if sandboxUnstructured != nil {
				annotations := sandboxUnstructured.GetAnnotations()
				if annotations != nil {
					if annotations["lastReopenedAt"] != "" {
						t, err := time.Parse(time.RFC3339, annotations["lastReopenedAt"])
						if err != nil {
							klog.Warningf("PR #%d: Failed to parse lastReopenedAt annotation %q: %v", number, annotations["lastReopenedAt"], err)
						} else {
							lastReopenedAt = &t
						}
					}
					if annotations["eventsLastCheckedAt"] != "" {
						t, err := time.Parse(time.RFC3339, annotations["eventsLastCheckedAt"])
						if err != nil {
							klog.Warningf("PR #%d: Failed to parse eventsLastCheckedAt annotation %q: %v", number, annotations["eventsLastCheckedAt"], err)
						} else {
							eventsLastCheckedAt = &t
						}
					}
				}
			}

			// Skip event pagination if PR hasn't been updated since we last checked
			if eventsLastCheckedAt != nil && !pr.GetUpdatedAt().After(*eventsLastCheckedAt) {
				klog.V(4).Infof("PR #%d: No new events since last check at %v. Skipping event pagination.", number, eventsLastCheckedAt)
				eventsFetchSuccess = true
			} else {
				listEventsOpt := &githubv39.ListOptions{PerPage: 100}
				eventsFetchSuccess = true

				// Get the first page to find out the last page number (events are chronological)
				page1Events, resp, err := ghClient.Issues.ListIssueEvents(ctx, owner, repo, number, listEventsOpt)
				if err != nil {
					if isGitHubTransient(err) {
						return &RetryableError{Message: fmt.Sprintf("transient error listing events for PR %d: %v", number, err)}
					}
					klog.Warningf("PR #%d: Failed to list events: %v", number, err)
					eventsFetchSuccess = false
				} else {
					lastPage := resp.LastPage
					if lastPage == 0 {
						lastPage = 1
					}

				StopEvents:
					for p := lastPage; p >= 1; p-- {
						var events []*githubv39.IssueEvent
						if p == 1 {
							events = page1Events
						} else {
							listEventsOpt.Page = p
							var err error
							events, _, err = ghClient.Issues.ListIssueEvents(ctx, owner, repo, number, listEventsOpt)
							if err != nil {
								klog.Warningf("PR #%d: Failed to list events on page %d: %v", number, p, err)
								eventsFetchSuccess = false
								break StopEvents
							}
						}

						// Scan events on this page backwards (newest to oldest)
						for i := len(events) - 1; i >= 0; i-- {
							e := events[i]
							createdAt := e.GetCreatedAt()

							// If we reached events older than our last checkpoint, we can stop
							if eventsLastCheckedAt != nil && createdAt.Before(*eventsLastCheckedAt) {
								break StopEvents
							}

							if e.GetEvent() == "reopened" {
								t := e.GetCreatedAt()
								if lastReopenedAt == nil || t.After(*lastReopenedAt) {
									lastReopenedAt = &t
								}
							}
						}
					}
				}
			}

			listOpt := &githubv39.ListOptions{PerPage: 100}
		ReviewLoop:
			for {
				reviews, resp, err := ghClient.PullRequests.ListReviews(ctx, owner, repo, number, listOpt)
				if err != nil {
					if isGitHubTransient(err) {
						return &RetryableError{Message: fmt.Sprintf("transient error listing reviews for PR %d: %v", number, err)}
					}
					return fmt.Errorf("failed to list reviews for PR %d: %w", number, err)
				}

				for _, r := range reviews {
					if r.GetUser() == nil {
						continue
					}
					login := r.GetUser().GetLogin()
					// Handle [bot] suffix for GitHub Apps
					loginNoBot := strings.TrimSuffix(login, "[bot]")
					botLoginNoBot := strings.TrimSuffix(botLogin, "[bot]")
					userLoginNoBot := strings.TrimSuffix(userLogin, "[bot]")

					isBot := (botLogin != "" && (strings.EqualFold(login, botLogin) || strings.EqualFold(loginNoBot, botLoginNoBot))) ||
						(userLogin != "" && (strings.EqualFold(login, userLogin) || strings.EqualFold(loginNoBot, userLoginNoBot)))

					// If shared accounts are used, also verify the hidden signature in the body
					if isBot && strings.EqualFold(r.GetCommitID(), headSHA) {
						hasSignature := strings.Contains(r.GetBody(), "<!-- overseer-review -->")
						// Backward compatibility: if it's from the bot and SHA matches, assume it's automated if it has the legacy signature or empty body
						isLegacyBot := strings.Contains(r.GetBody(), "generated by Overseer") || r.GetBody() == ""
						if hasSignature || isLegacyBot {
							state := r.GetState()
							submittedAt := r.GetSubmittedAt()

							if !submittedAt.IsZero() && lastReopenedAt != nil && submittedAt.Before(*lastReopenedAt) {
								klog.Infof("PR #%d: Found historical automated review for SHA %s, but PR was reopened since then. Proceeding with new review.", number, headSHA)
								continue
							}

							if state == "DISMISSED" {
								klog.Infof("PR #%d: Automated review for SHA %s was DISMISSED. Allowing re-review.", number, headSHA)
								continue
							}

							klog.V(2).Infof("PR #%d: Automated review for SHA %s already exists on GitHub by %s (state: %s). Skipping.", number, headSHA, login, state)
							return nil
						}
						klog.V(2).Infof("PR #%d: Found review by %s for SHA %s, but it lacks the automated signature. Assuming it is a manual review.", number, login, headSHA)
					}
				}
				if resp.NextPage == 0 {
					break ReviewLoop
				}
				listOpt.Page = resp.NextPage
			}
		}
	}

	// Check limit only if we need to create or activate a sandbox
	if !sandboxIsActive && overseer.Spec.MaxActiveReviews != nil {
		maxReviews := *overseer.Spec.MaxActiveReviews
		activeCount, err := countActiveSandboxes(ctx, kubeClient.DynamicClient, namespace, overseerName, "review")
		if err != nil {
			return fmt.Errorf("failed to count active review sandboxes: %w", err)
		}
		if int32(activeCount) >= maxReviews {
			return fmt.Errorf("limit_reached: max active reviews limit (%d) reached (currently %d active)", maxReviews, activeCount)
		}
	}

	// Ensure Sandbox exists and is configured correctly
	klog.Infof("Ensuring sandbox %s exists...", sandboxName)
	if err := createPRSandbox(ctx, kubeClient, &overseer, pr, conf); err != nil {
		return fmt.Errorf("failed to ensure PR sandbox: %w", err)
	}

	// Cache timestamps in sandbox annotations now that sandbox exists
	if eventsFetchSuccess {
		if lastReopenedAt != nil {
			_ = manager.UpdateSandboxAnnotation(ctx, namespace, sandboxName, "lastReopenedAt", lastReopenedAt.Format(time.RFC3339))
		}
		_ = manager.UpdateSandboxAnnotation(ctx, namespace, sandboxName, "eventsLastCheckedAt", pr.GetUpdatedAt().Format(time.RFC3339))
	}

	// Create Task
	klog.Infof("Creating task %s for sandbox %s...", taskType, sandboxName)
	agentPrompt := customPrompt
	if agentPrompt == "" {
		agentPrompt = overseer.Spec.Review.Prompt
	}

	params := map[string]string{
		"PULL_REQUEST_ID": fmt.Sprintf("%d", number),
		"ISSUE_URL":       pr.GetHTMLURL(),
		"AGENT_PROMPT":    agentPrompt,
		"HEAD_SHA":        headSHA,
	}

	err = manager.CreateSandboxTask(ctx, namespace, sandboxName, "Sandbox", taskType, params)
	if err != nil {
		return fmt.Errorf("failed to create sandbox task: %w", err)
	}

	klog.Info("Done.")
	return nil
}

func submitAgentDraft(ctx context.Context, manager *k8s.Manager, kubeClient *clients.KubernetesClient, namespace, overseerName string, prNumber int) error {
	rwUnstructured, err := getOverseer(ctx, kubeClient.DynamicClient, overseerName)
	if err != nil {
		return fmt.Errorf("failed to get Overseer %s: %w", overseerName, err)
	}

	sandboxName := fmt.Sprintf("%s-pr-%d", overseerName, prNumber)

	taskList, err := manager.ListSandboxTasks(ctx, namespace, sandboxName)
	if err != nil {
		return fmt.Errorf("failed to list tasks for sandbox %s: %w", sandboxName, err)
	}

	var latestReviewTask *sandboxtaskv1alpha1.SandboxTask
	for i := range taskList.Items {
		task := &taskList.Items[i]
		if task.Spec.Type == "review" && task.Status.TaskState == "Completed" {
			if latestReviewTask == nil || task.CreationTimestamp.After(latestReviewTask.CreationTimestamp.Time) {
				latestReviewTask = task
			}
		}
	}

	if latestReviewTask == nil {
		return fmt.Errorf("no completed review task found for sandbox %s", sandboxName)
	}

	currentSHA := latestReviewTask.Spec.Params["HEAD_SHA"]

	sandboxUnstructured, err := kubeClient.DynamicClient.Resource(k8s.SandboxGVR).Namespace(namespace).Get(ctx, sandboxName, metav1.GetOptions{})
	if err == nil {
		annotations := sandboxUnstructured.GetAnnotations()
		if annotations != nil {
			if state, ok := annotations["reviewState"]; ok {
				if state == "submitted" {
					klog.Infof("Review for PR %d already submitted (legacy).", prNumber)
					return nil
				}
				if currentSHA != "" && state == "submitted:"+currentSHA {
					klog.Infof("Review for PR %d and SHA %s already submitted.", prNumber, currentSHA)
					return nil
				}
			}
		}
	}

	// Get the task again as Unstructured to read annotations
	gvr := schema.GroupVersionResource{
		Group:    "custom.agents.x-k8s.io",
		Version:  "v1alpha1",
		Resource: "sandboxtasks",
	}
	taskUnstructured, err := kubeClient.DynamicClient.Resource(gvr).Namespace(namespace).Get(ctx, latestReviewTask.Name, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("failed to get task %s: %w", latestReviewTask.Name, err)
	}

	annotations := taskUnstructured.GetAnnotations()
	draft, ok := annotations["agentDraft"]
	if !ok {
		return fmt.Errorf("no agentDraft annotation found on task %s", latestReviewTask.Name)
	}

	var overseer overseerv1alpha1.Overseer
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(rwUnstructured.Object, &overseer); err != nil {
		return fmt.Errorf("failed to convert Overseer: %w", err)
	}

	token := os.Getenv("GITHUB_TOKEN")
	if token == "" {
		githubSecretName := overseer.Spec.RobotAccount

		rwUnstructuredCopy := rwUnstructured.DeepCopy()
		_ = unstructured.SetNestedField(rwUnstructuredCopy.Object, githubSecretName, "spec", "githubSecretName")
		// workaround since GetGithubToken expects the secret name to be in the spec, but our unstructured doesn't have it set there
		// all requires namespace
		_ = unstructured.SetNestedField(rwUnstructuredCopy.Object, namespace, "metadata", "namespace")

		// Get GitHub token from secret
		token, err = manager.GetGitHubToken(ctx, rwUnstructuredCopy)
		if err != nil {
			return fmt.Errorf("failed to get github token: %w", err)
		}
	}

	// Create GitHub client
	client := clients.NewGitHubClient(ctx, token)

	// Parse repo URL
	repoURL, found, err := unstructured.NestedString(rwUnstructured.Object, "spec", "repoURL")
	if err != nil || !found {
		return fmt.Errorf("repoURL not found in Overseer %s", overseerName)
	}
	owner, repoName, err := parseRepoURL(repoURL)
	if err != nil {
		return fmt.Errorf("failed to parse repo URL %s: %w", repoURL, err)
	}

	// Try Unmarshalling the yaml review payload into PullRequestReviewRequest
	agentOutput := &models.ReviewAgentOutput{}
	reviewRequest := &githubv39.PullRequestReviewRequest{}
	err = yaml.Unmarshal([]byte(draft), &agentOutput)
	if err != nil || agentOutput.Review == nil {
		if err != nil {
			klog.Warningf("Failed to unmarshal review payload as YAML, using as plain body: %v", err)
		} else {
			klog.Warning("Review field missing in YAML, using draft as plain body")
		}

		reviewRequest.Body = githubv39.String(draft)
	} else {
		reviewRequest = agentOutput.Review.ToGitHubReviewRequest()
	}

	// Set event to COMMENT to submit directly instead of creating a draft
	reviewRequest.Event = githubv39.String("COMMENT")
	if currentSHA != "" {
		reviewRequest.CommitID = githubv39.String(currentSHA)
	}

	// Add a hidden signature to definitively identify automated reviews
	signature := "<!-- overseer-review -->"
	body := ""
	if reviewRequest.Body != nil {
		body = *reviewRequest.Body
	}
	if !strings.Contains(body, signature) {
		reviewRequest.Body = githubv39.String(body + "\n\n" + signature)
	}

	klog.Infof("Creating review on GitHub for %s/%s PR %d (SHA %s)...", owner, repoName, prNumber, currentSHA)
	review, _, err := client.PullRequests.CreateReview(ctx, owner, repoName, prNumber, reviewRequest)
	if err != nil {
		if isGitHubTransient(err) {
			return &RetryableError{Message: fmt.Sprintf("transient error creating review on GitHub: %v", err)}
		}
		return fmt.Errorf("failed to create review on GitHub: %w", err)
	}
	klog.Infof("Successfully created review: %s", review.GetHTMLURL())

	// Update sandbox reviewState
	reviewState := "submitted"
	if currentSHA != "" {
		reviewState = "submitted:" + currentSHA
	}
	if err := manager.UpdateSandboxAnnotation(ctx, namespace, sandboxName, "reviewState", reviewState); err != nil {
		klog.Warningf("Failed to update reviewState annotation: %v", err)
	}

	klog.Info("Done.")
	return nil
}

func parseRepoURL(url string) (string, string, error) {
	u := strings.TrimPrefix(url, "https://")
	u = strings.TrimSuffix(u, ".git")
	parts := strings.Split(u, "/")
	if len(parts) < 3 {
		return "", "", fmt.Errorf("invalid repo URL: %s", url)
	}
	return parts[len(parts)-2], parts[len(parts)-1], nil
}

func createIssueSandbox(ctx context.Context, kubeClient *clients.KubernetesClient, overseer *overseerv1alpha1.Overseer, issue *githubv39.Issue, conf CLIConfig) error {
	// Replicate logic from repowatch_controller.go:createIssueSandbox
	name := fmt.Sprintf("%s-issue-%d", overseer.Name, issue.GetNumber())
	cloneURL := strings.Replace(issue.GetRepositoryURL(), "api.github.com/repos", "github.com", 1) + ".git"

	userLogin := conf.UserLogin
	userName := conf.UserName
	userEmail := conf.UserEmail

	botLogin := conf.BotLogin
	botName := conf.BotName
	botEmail := conf.BotEmail

	branchName := fmt.Sprintf("issue-%d-%s", issue.GetNumber(), randString(4))

	apiKeySecretName := overseer.Spec.GeminiAPIKeySecretName
	if apiKeySecretName == "" {
		apiKeySecretName = "gemini-api-key"
	}

	githubSecretName := overseer.Spec.RobotAccount

	scriptToken, err := getTokenFromScript()
	if err != nil {
		klog.Warningf("failed to get token from script: %v", err)
	}

	opt := sandbox.AgentSandboxOptions{
		DevSandboxOptions: sandbox.DevSandboxOptions{
			Name:      name,
			Namespace: namespace,
			Labels: map[string]string{
				"review.gemini.google.com/overseer": overseer.Name,
				"sandbox.gemini.google.com/type":    "issue",
			},
			CloneURL:            cloneURL,
			HTMLURL:             issue.GetHTMLURL(),
			Branch:              branchName,
			Origin:              fmt.Sprintf("github.com/%s/%s", userLogin, overseer.Name), // simplified
			PushEnabled:         false,
			UserLogin:           userLogin,
			UserName:            userName,
			UserEmail:           userEmail,
			BotLogin:            botLogin,
			BotName:             botName,
			BotEmail:            botEmail,
			LLMProvider:         "gemini-cli",
			LLMConfigdirRef:     overseer.Spec.ConfigdirRef,
			LLMAPIKeySecretName: apiKeySecretName,
			Prompt:              overseer.Spec.IssuePrompt,
			GithubSecretName:    githubSecretName,
			LLMAPIKey:           scriptToken,
			Image:               overseer.Spec.Image,
			RepoSandboxImage:    os.Getenv("REPO_SANDBOX_IMAGE"),
			ConfigDirImage:      os.Getenv("CONFIG_DIR_IMAGE"),
			HTTPEnabled:         true,
			Replicas:            1,
			ServiceAccountName:  "overseer-sandbox",
			WorkspaceDiskSize:   overseer.Spec.WorkspaceDiskSize,
		},
		IssueID:    fmt.Sprintf("%d", issue.GetNumber()),
		IssueTitle: issue.GetTitle(),
		IssueRepo:  overseer.Name,
	}

	sb, svc := sandbox.NewAgentSandbox(opt)

	sb.SetOwnerReferences([]metav1.OwnerReference{
		{
			APIVersion:         "overseer.gemini.google.com/v1alpha1",
			Kind:               "Overseer",
			Name:               overseer.Name,
			UID:                overseer.UID,
			Controller:         ptr.To(true),
			BlockOwnerDeletion: ptr.To(true),
		},
	})

	createdSb, err := ensureSandbox(ctx, kubeClient.DynamicClient, namespace, sb)
	if err != nil {
		return fmt.Errorf("failed to ensure issue sandbox: %w", err)
	}

	return ensureService(ctx, kubeClient.Clientset, namespace, svc, createdSb)
}

func createPRSandbox(ctx context.Context, kubeClient *clients.KubernetesClient, overseer *overseerv1alpha1.Overseer, pr *githubv39.PullRequest, conf CLIConfig) error {
	name := fmt.Sprintf("%s-pr-%d", overseer.Name, pr.GetNumber())

	userLogin := conf.UserLogin
	userName := conf.UserName
	userEmail := conf.UserEmail

	botLogin := conf.BotLogin
	botName := conf.BotName
	botEmail := conf.BotEmail

	apiKeySecretName := overseer.Spec.GeminiAPIKeySecretName
	if apiKeySecretName == "" {
		apiKeySecretName = "gemini-api-key"
	}

	maxReviewFiles := overseer.Spec.Review.MaxReviewFiles
	if maxReviewFiles == 0 {
		maxReviewFiles = 150
	}
	githubSecretName := overseer.Spec.RobotAccount

	scriptToken, err := getTokenFromScript()
	if err != nil {
		klog.Warningf("failed to get token from script: %v", err)
	}

	head := pr.GetHead()
	if head == nil || head.GetRepo() == nil {
		return fmt.Errorf("PR #%d head or repo is nil", pr.GetNumber())
	}
	opt := sandbox.ReviewSandboxOptions{
		DevSandboxOptions: sandbox.DevSandboxOptions{
			Name:      name,
			Namespace: namespace,
			Labels: map[string]string{
				"review.gemini.google.com/overseer": overseer.Name,
				"sandbox.gemini.google.com/type":    "review",
			},
			UserLogin:             userLogin,
			UserName:              userName,
			UserEmail:             userEmail,
			BotLogin:              botLogin,
			BotName:               botName,
			BotEmail:              botEmail,
			LLMProvider:           "gemini-cli",
			LLMConfigdirRef:       overseer.Spec.ConfigdirRef,
			LLMAPIKeySecretName:   apiKeySecretName,
			Prompt:                overseer.Spec.Review.Prompt,
			GithubSecretName:      githubSecretName,
			LLMAPIKey:             scriptToken,
			DevcontainerConfigRef: "",
			Image:                 overseer.Spec.Image,
			RepoSandboxImage:      os.Getenv("REPO_SANDBOX_IMAGE"),
			ConfigDirImage:        os.Getenv("CONFIG_DIR_IMAGE"),
			HTTPEnabled:           true,
			Replicas:              1,
			ServiceAccountName:    "overseer-sandbox",
		},
		PRNumber:          pr.GetNumber(),
		PRTitle:           pr.GetTitle(),
		PRHTMLURL:         pr.GetHTMLURL(),
		PRDiffURL:         pr.GetDiffURL(),
		PRCloneURL:        fmt.Sprintf("%s#refs/heads/%s", head.GetRepo().GetCloneURL(), head.GetRef()),
		RepoName:          overseer.Name,
		MaxReviewFiles:    maxReviewFiles,
		IgnoreFiles:       overseer.Spec.Review.IgnoreFiles,
		SeverityThreshold: overseer.Spec.Review.SeverityThreshold,
		LLMExtensions:     overseer.Spec.Extensions,
		WorkspaceDiskSize: overseer.Spec.WorkspaceDiskSize,
	}

	sb, svc := sandbox.NewReviewSandbox(opt)

	sb.SetOwnerReferences([]metav1.OwnerReference{
		{
			APIVersion:         "overseer.gemini.google.com/v1alpha1",
			Kind:               "Overseer",
			Name:               overseer.Name,
			UID:                overseer.UID,
			Controller:         ptr.To(true),
			BlockOwnerDeletion: ptr.To(true),
		},
	})

	createdSb, err := ensureSandbox(ctx, kubeClient.DynamicClient, namespace, sb)
	if err != nil {
		return fmt.Errorf("failed to ensure PR sandbox: %w", err)
	}

	return ensureService(ctx, kubeClient.Clientset, namespace, svc, createdSb)
}

var letterBytes = "abcdefghijklmnopqrstuvwxyz0123456789"
var seededRand = rand.New(rand.NewSource(time.Now().UnixNano()))

func randString(n int) string {
	b := make([]byte, n)
	for i := range b {
		b[i] = letterBytes[seededRand.Intn(len(letterBytes))]
	}
	return string(b)
}

func resolveIssueFromPR(ctx context.Context, owner, repo string, prNumber int) (int, error) {
	// Try using gh CLI to get closing issues
	cmd := exec.CommandContext(ctx, "gh", "pr", "view", fmt.Sprintf("%d", prNumber), "--repo", fmt.Sprintf("%s/%s", owner, repo), "--json", "closingIssuesReferences")
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	if err := cmd.Run(); err == nil {
		var result struct {
			ClosingIssuesReferences []struct {
				Number int `json:"number"`
			} `json:"closingIssuesReferences"`
		}
		if err := json.Unmarshal(stdout.Bytes(), &result); err == nil && len(result.ClosingIssuesReferences) > 0 {
			return result.ClosingIssuesReferences[0].Number, nil
		}
	}

	// Fallback: just return the PR number as its own issue number
	return prNumber, nil
}

func countActiveSandboxes(ctx context.Context, dynClient dynamic.Interface, namespace, overseerName, sandboxType string) (int, error) {
	labelSelector := fmt.Sprintf("review.gemini.google.com/overseer=%s,sandbox.gemini.google.com/type=%s", overseerName, sandboxType)
	listOptions := metav1.ListOptions{
		LabelSelector: labelSelector,
	}
	sandboxList, err := dynClient.Resource(k8s.SandboxGVR).Namespace(namespace).List(ctx, listOptions)
	if err != nil {
		return 0, err
	}
	activeCount := 0
	for _, item := range sandboxList.Items {
		replicas, found, err := unstructured.NestedInt64(item.Object, "spec", "replicas")
		if err == nil && found && replicas == 0 {
			continue
		}
		activeCount++
	}
	return activeCount, nil
}

func getOverseer(ctx context.Context, dynClient dynamic.Interface, name string) (*unstructured.Unstructured, error) {
	gvr := schema.GroupVersionResource{
		Group:    "overseer.gemini.google.com",
		Version:  "v1alpha1",
		Resource: "overseers",
	}
	return dynClient.Resource(gvr).Get(ctx, name, metav1.GetOptions{})
}

func runReconcile(ctx context.Context) error {
	if overseerName == "" || namespace == "" {
		return fmt.Errorf("OVERSEER_NAME and NAMESPACE environment variables must be set")
	}

	cfg, err := config.GetConfig()
	if err != nil {
		return fmt.Errorf("unable to get kubeconfig: %w", err)
	}

	dynClient, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return fmt.Errorf("unable to create dynamic client: %w", err)
	}

	clientset, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return fmt.Errorf("unable to create clientset: %w", err)
	}

	kubeClient := &clients.KubernetesClient{
		DynamicClient: dynClient,
		Clientset:     clientset,
	}

	rwUnstructured, err := getOverseer(ctx, kubeClient.DynamicClient, overseerName)
	if err != nil {
		return fmt.Errorf("failed to get Overseer %s: %w", overseerName, err)
	}

	var overseer overseerv1alpha1.Overseer
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(rwUnstructured.Object, &overseer); err != nil {
		return fmt.Errorf("failed to convert Overseer: %w", err)
	}

	// 1. Get current chores in .agents/
	currentChores := make(map[string]bool)
	pausedChores := make(map[string]bool)
	choresMode := os.Getenv("CHORES_MODE")
	if choresMode != "disabled" && choresMode != "dryrun" {
		files, err := os.ReadDir(".agents")
		if err == nil {
			for _, f := range files {
				if f.IsDir() {
					continue
				}
				if strings.HasSuffix(f.Name(), ".yaml") || strings.HasSuffix(f.Name(), ".yml") || strings.HasSuffix(f.Name(), ".md") {
					chore, err := parseChore(".agents/" + f.Name())
					if err == nil && chore.Name != "" {
						isAllowed := isChoreAllowed(overseer.Spec.Chores, chore.Name)
						isPaused := strings.EqualFold(chore.Schedule, "never")
						if isAllowed && !isPaused {
							currentChores[slugify(chore.Name)] = true
						} else if isAllowed && isPaused {
							pausedChores[slugify(chore.Name)] = true
						}
					}
				}
			}
		}
	}

	// 2. List all chore sandboxes
	labelSelector := fmt.Sprintf("review.gemini.google.com/overseer=%s,sandbox.gemini.google.com/type=chore", overseer.Name)
	listOptions := metav1.ListOptions{
		LabelSelector: labelSelector,
	}
	sandboxList, err := kubeClient.DynamicClient.Resource(k8s.SandboxGVR).Namespace(namespace).List(ctx, listOptions)
	if err != nil {
		return fmt.Errorf("failed to list chore sandboxes: %w", err)
	}

	// 3. Delete sandboxes for chores that are no longer present or are excluded (or if all chores are effectively disallowed due to mode)
	for _, item := range sandboxList.Items {
		choreSlug, found, _ := unstructured.NestedString(item.Object, "metadata", "labels", "chore.gemini.google.com/name")
		if found {
			if !currentChores[choreSlug] {
				reason := "no longer present or is excluded"
				if pausedChores[choreSlug] {
					reason = "paused (schedule: never)"
				}
				if choresMode == "disabled" || choresMode == "dryrun" {
					reason = fmt.Sprintf("chores are %s", choresMode)
				}
				klog.Infof("Chore %s %s. Deleting sandbox %s.", choreSlug, reason, item.GetName())
				if err := deleteSandbox(ctx, kubeClient, namespace, item.GetName()); err != nil {
					klog.Warningf("Failed to delete sandbox %s: %v", item.GetName(), err)
				}
			}
		}
	}

	klog.Info("Reconciliation complete.")
	return nil
}

func isChoreAllowed(spec *overseerv1alpha1.ChoresSpec, name string) bool {
	if spec == nil {
		return true
	}
	if len(spec.Exclude) > 0 {
		for _, e := range spec.Exclude {
			if e == name {
				return false
			}
		}
	}
	if len(spec.Include) > 0 {
		for _, i := range spec.Include {
			if i == name {
				return true
			}
		}
		return false
	}
	return true
}

func buildDeleteCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "delete",
		Short: "Delete resources",
	}

	sandboxCmd := &cobra.Command{
		Use:   "sandbox [name]",
		Short: "Delete a sandbox and its associated resources (like the -lb service)",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			return runDeleteSandbox(context.Background(), args[0])
		},
	}
	cmd.AddCommand(sandboxCmd)

	return cmd
}

func runDeleteSandbox(ctx context.Context, sandboxName string) error {
	if namespace == "" {
		return fmt.Errorf("NAMESPACE environment variable must be set")
	}

	cfg, err := config.GetConfig()
	if err != nil {
		return fmt.Errorf("unable to get kubeconfig: %w", err)
	}

	dynClient, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return fmt.Errorf("unable to create dynamic client: %w", err)
	}

	clientset, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return fmt.Errorf("unable to create clientset: %w", err)
	}

	kubeClient := &clients.KubernetesClient{
		DynamicClient: dynClient,
		Clientset:     clientset,
	}

	return deleteSandbox(ctx, kubeClient, namespace, sandboxName)
}

func deleteSandbox(ctx context.Context, kubeClient *clients.KubernetesClient, namespace, sandboxName string) error {
	klog.Infof("Deleting sandbox %s...", sandboxName)
	err := kubeClient.DynamicClient.Resource(k8s.SandboxGVR).Namespace(namespace).Delete(ctx, sandboxName, metav1.DeleteOptions{})
	if err != nil && !errors.IsNotFound(err) {
		// handle string check if errors package is not behaving as expected with dynamic client
		if !strings.Contains(err.Error(), "not found") {
			return err
		}
	}
	// Also delete service
	serviceName := sandboxName + "-lb"
	klog.Infof("Deleting service %s...", serviceName)
	err = kubeClient.Clientset.CoreV1().Services(namespace).Delete(ctx, serviceName, metav1.DeleteOptions{})
	if err != nil && !errors.IsNotFound(err) {
		if !strings.Contains(err.Error(), "not found") {
			return err
		}
	}
	return nil
}

func getTokenFromScript() (string, error) {
	dir := os.Getenv("TOKENSCRIPT_DIR")
	if dir == "" {
		return "", nil
	}

	files, err := os.ReadDir(dir)
	if err != nil {
		return "", fmt.Errorf("failed to read tokenscript dir: %w", err)
	}

	for _, f := range files {
		if f.IsDir() || strings.HasPrefix(f.Name(), "..") {
			continue
		}

		path := filepath.Join(dir, f.Name())
		cmd := exec.Command(path)
		var out bytes.Buffer
		cmd.Stdout = &out
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			return "", fmt.Errorf("failed to run tokenscript %s: %w", path, err)
		}

		return strings.TrimSpace(out.String()), nil
	}

	return "", nil
}
