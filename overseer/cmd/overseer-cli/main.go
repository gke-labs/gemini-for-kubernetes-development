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
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/klog/v2"
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

func runChore(ctx context.Context, name string, file string) error {
	if overseerName == "" || namespace == "" {
		return fmt.Errorf("OVERSEER_NAME and NAMESPACE environment variables must be set")
	}

	choresMode := os.Getenv("CHORES_MODE")
	if choresMode == "disabled" {
		fmt.Printf("Chore handling is disabled (CHORES_MODE=disabled). Skipping.\n")
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
			fmt.Printf("[dryrun] Ensuring sandbox %s is deleted for chore %s (%s)\n", sandboxName, chore.Name, reason)
			_ = deleteSandbox(ctx, kubeClient, namespace, sandboxName)
			return nil
		}
		fmt.Printf("Chore %s %s. Ensuring sandbox is deleted.\n", chore.Name, reason)
		return deleteSandbox(ctx, kubeClient, namespace, sandboxName)
	}

	if choresMode == "dryrun" {
		fmt.Printf("[dryrun] Would create sandbox and task chore for chore %s in Overseer %s\n", chore.Name, overseerName)
		return nil
	}

	owner, repo, err := parseRepoURL(overseer.Spec.RepoURL)
	if err != nil {
		return fmt.Errorf("failed to parse RepoURL: %w", err)
	}

	// Check if sandbox exists
	_, err = kubeClient.DynamicClient.Resource(k8s.SandboxGVR).Namespace(namespace).Get(ctx, sandboxName, metav1.GetOptions{})
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			// Create Sandbox
			fmt.Printf("Creating sandbox %s...\n", sandboxName)
			if err := createChoreSandbox(ctx, kubeClient, &overseer, chore, sandboxName); err != nil {
				return fmt.Errorf("failed to create chore sandbox: %w", err)
			}
		} else {
			return fmt.Errorf("failed to check if sandbox exists: %w", err)
		}
	}

	// Create Task
	taskType := "chore"
	fmt.Printf("Creating task %s for sandbox %s...\n", taskType, sandboxName)
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

	fmt.Println("Done.")
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

func createChoreSandbox(ctx context.Context, kubeClient *clients.KubernetesClient, overseer *overseerv1alpha1.Overseer, chore *ChoreDefinition, sandboxName string) error {
	cloneURL := overseer.Spec.RepoURL
	if !strings.HasSuffix(cloneURL, ".git") {
		cloneURL += ".git"
	}

	_, repo, err := parseRepoURL(overseer.Spec.RepoURL)
	if err != nil {
		return fmt.Errorf("failed to parse RepoURL: %w", err)
	}

	userLogin := os.Getenv("GITHUB_USER_ID")
	userName := os.Getenv("GITHUB_USER_NAME")
	if userName == "" {
		userName = userLogin
	}
	userEmail := os.Getenv("GITHUB_USER_EMAIL")

	botLogin := os.Getenv("GITHUB_BOT_LOGIN")
	botName := os.Getenv("GITHUB_BOT_NAME")
	botEmail := os.Getenv("GITHUB_BOT_EMAIL")

	apiKeySecretName := overseer.Spec.GeminiAPIKeySecretName
	if apiKeySecretName == "" {
		apiKeySecretName = "gemini-api-key"
	}

	githubSecretName := overseer.Spec.RobotAccount

	scriptToken, err := getTokenFromScript()
	if err != nil {
		fmt.Printf("Warning: failed to get token from script: %v\n", err)
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

	_, err = kubeClient.DynamicClient.Resource(k8s.SandboxGVR).Namespace(namespace).Create(ctx, sb, metav1.CreateOptions{})
	if err != nil {
		return err
	}

	_, err = kubeClient.Clientset.CoreV1().Services(namespace).Create(ctx, svc, metav1.CreateOptions{})
	return err
}

func runIssue(ctx context.Context, number int, prNumber int, taskType string, customPrompt string) error {
	if overseerName == "" || namespace == "" {
		return fmt.Errorf("OVERSEER_NAME and NAMESPACE environment variables must be set")
	}

	issueMode := os.Getenv("ISSUE_MODE")
	if issueMode == "disabled" {
		fmt.Printf("Issue handling is disabled (ISSUE_MODE=disabled). Skipping.\n")
		return nil
	}
	if issueMode == "dryrun" {
		if number != 0 {
			fmt.Printf("[dryrun] Would create/ensure sandbox and task %s for issue %d in Overseer %s\n", taskType, number, overseerName)
		} else if prNumber != 0 {
			fmt.Printf("[dryrun] Would create/ensure sandbox and task %s for issue from PR %d in Overseer %s\n", taskType, prNumber, overseerName)
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
		fmt.Printf("Resolving issue from PR %d...\n", prNumber)
		number, err = resolveIssueFromPR(ctx, owner, repo, prNumber)
		if err != nil {
			return fmt.Errorf("failed to resolve issue from PR: %w", err)
		}
		fmt.Printf("Resolved to issue %d\n", number)
	}

	if number == 0 {
		return fmt.Errorf("either --number or --pr must be provided")
	}

	issue, _, err := ghClient.Issues.Get(ctx, owner, repo, number)
	if err != nil {
		return fmt.Errorf("failed to get issue %d: %w", number, err)
	}

	sandboxName := fmt.Sprintf("%s-issue-%d", overseer.Name, number)

	var sandboxExists bool
	var sandboxIsActive bool
	sandboxUnstructured, err := kubeClient.DynamicClient.Resource(k8s.SandboxGVR).Namespace(namespace).Get(ctx, sandboxName, metav1.GetOptions{})
	if err == nil {
		sandboxExists = true
		replicas, found, err := unstructured.NestedInt64(sandboxUnstructured.Object, "spec", "replicas")
		if err == nil && (!found || replicas > 0) {
			sandboxIsActive = true
		}
	} else if !strings.Contains(err.Error(), "not found") {
		return fmt.Errorf("failed to check if sandbox exists: %w", err)
	}

	// Check limit only if we need to create or activate a sandbox
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

	// Create Sandbox if it doesn't exist
	if !sandboxExists {
		fmt.Printf("Creating sandbox %s...\n", sandboxName)
		if err := createIssueSandbox(ctx, kubeClient, &overseer, issue); err != nil {
			return fmt.Errorf("failed to create issue sandbox: %w", err)
		}
	}

	// Create Task
	fmt.Printf("Creating task %s for sandbox %s...\n", taskType, sandboxName)
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

	fmt.Println("Done.")
	return nil
}

func runPR(ctx context.Context, number int, taskType string, submit bool, customPrompt string) error {
	// Similar to runIssue but for PRs
	if overseerName == "" || namespace == "" {
		return fmt.Errorf("OVERSEER_NAME and NAMESPACE environment variables must be set")
	}

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
		fmt.Printf("PR/Review handling is disabled (%s=disabled). Skipping.\n", modeName)
		return nil
	}

	if mode == "dryrun" {
		fmt.Printf("[dryrun] Would create/ensure sandbox and task %s for PR %d in Overseer %s\n", taskType, number, overseerName)
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

	if submit {
		fmt.Printf("Submitting agent draft for PR %d...\n", number)
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
		return fmt.Errorf("failed to get PR %d: %w", number, err)
	}

	sandboxName := fmt.Sprintf("%s-pr-%d", overseer.Name, number)
	headSHA := pr.GetHead().GetSHA()

	var sandboxExists bool
	var sandboxIsActive bool
	sandboxUnstructured, err := kubeClient.DynamicClient.Resource(k8s.SandboxGVR).Namespace(namespace).Get(ctx, sandboxName, metav1.GetOptions{})
	if err == nil {
		sandboxExists = true
		replicas, found, err := unstructured.NestedInt64(sandboxUnstructured.Object, "spec", "replicas")
		if err == nil && (!found || replicas > 0) {
			sandboxIsActive = true
		}
	} else if !strings.Contains(err.Error(), "not found") {
		return fmt.Errorf("failed to check if sandbox exists: %w", err)
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

	// Check if a task for this SHA already exists (only for review tasks)
	if taskType == "review" {
		taskList, err := manager.ListSandboxTasks(ctx, namespace, sandboxName)
		if err == nil {
			for i := range taskList.Items {
				task := &taskList.Items[i]
				if task.Spec.Type == "review" && task.Spec.Params["HEAD_SHA"] == headSHA {
					if task.Status.TaskState == "Completed" || task.Status.TaskState == "Running" || task.Status.TaskState == "Pending" {
						fmt.Printf("Review task for SHA %s already exists in state %s. Skipping.\n", headSHA, task.Status.TaskState)
						return nil
					}
				}
			}
		}
	}

	// Create Sandbox if it doesn't exist
	if !sandboxExists {
		fmt.Printf("Creating sandbox %s...\n", sandboxName)
		if err := createPRSandbox(ctx, kubeClient, &overseer, pr); err != nil {
			return fmt.Errorf("failed to create PR sandbox: %w", err)
		}
	}

	// Create Task
	fmt.Printf("Creating task %s for sandbox %s...\n", taskType, sandboxName)
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

	fmt.Println("Done.")
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
					fmt.Printf("Review for PR %d already submitted (legacy).\n", prNumber)
					return nil
				}
				if currentSHA != "" && state == "submitted:"+currentSHA {
					fmt.Printf("Review for PR %d and SHA %s already submitted.\n", prNumber, currentSHA)
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
			fmt.Printf("Warning: failed to unmarshal review payload as YAML, using as plain body: %v\n", err)
		} else {
			fmt.Printf("Warning: review field missing in YAML, using draft as plain body\n")
		}

		reviewRequest.Body = githubv39.String(draft)
	} else {
		reviewRequest = agentOutput.Review.ToGitHubReviewRequest()
	}

	// Set event to COMMENT to submit directly instead of creating a draft
	reviewRequest.Event = githubv39.String("COMMENT")

	fmt.Printf("Creating review on GitHub for %s/%s PR %d...\n", owner, repoName, prNumber)
	review, _, err := client.PullRequests.CreateReview(ctx, owner, repoName, prNumber, reviewRequest)
	if err != nil {
		return fmt.Errorf("failed to create review on GitHub: %w", err)
	}
	fmt.Printf("Successfully created review: %s\n", review.GetHTMLURL())

	// Update sandbox reviewState
	reviewState := "submitted"
	if currentSHA != "" {
		reviewState = "submitted:" + currentSHA
	}
	if err := manager.UpdateSandboxAnnotation(ctx, namespace, sandboxName, "reviewState", reviewState); err != nil {
		fmt.Printf("Warning: failed to update reviewState annotation: %v\n", err)
	}

	fmt.Println("Done.")
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

func createIssueSandbox(ctx context.Context, kubeClient *clients.KubernetesClient, overseer *overseerv1alpha1.Overseer, issue *githubv39.Issue) error {
	// Replicate logic from repowatch_controller.go:createIssueSandbox
	name := fmt.Sprintf("%s-issue-%d", overseer.Name, issue.GetNumber())
	cloneURL := strings.Replace(issue.GetRepositoryURL(), "api.github.com/repos", "github.com", 1) + ".git"

	// We need to fetch user info. In Overseer, we might just use env vars.
	userLogin := os.Getenv("GITHUB_USER_ID")
	userName := os.Getenv("GITHUB_USER_NAME")
	if userName == "" {
		userName = userLogin
	}
	userEmail := os.Getenv("GITHUB_USER_EMAIL")

	botLogin := os.Getenv("GITHUB_BOT_LOGIN")
	botName := os.Getenv("GITHUB_BOT_NAME")
	botEmail := os.Getenv("GITHUB_BOT_EMAIL")

	branchName := fmt.Sprintf("issue-%d-%s", issue.GetNumber(), randString(4))

	apiKeySecretName := overseer.Spec.GeminiAPIKeySecretName
	if apiKeySecretName == "" {
		apiKeySecretName = "gemini-api-key"
	}

	githubSecretName := overseer.Spec.RobotAccount

	scriptToken, err := getTokenFromScript()
	if err != nil {
		fmt.Printf("Warning: failed to get token from script: %v\n", err)
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

	_, err = kubeClient.DynamicClient.Resource(k8s.SandboxGVR).Namespace(namespace).Create(ctx, sb, metav1.CreateOptions{})
	if err != nil {
		return err
	}

	_, err = kubeClient.Clientset.CoreV1().Services(namespace).Create(ctx, svc, metav1.CreateOptions{})
	return err
}

func createPRSandbox(ctx context.Context, kubeClient *clients.KubernetesClient, overseer *overseerv1alpha1.Overseer, pr *githubv39.PullRequest) error {
	name := fmt.Sprintf("%s-pr-%d", overseer.Name, pr.GetNumber())

	userLogin := os.Getenv("GITHUB_USER_ID")
	userName := os.Getenv("GITHUB_USER_NAME")
	if userName == "" {
		userName = userLogin
	}
	userEmail := os.Getenv("GITHUB_USER_EMAIL")

	botLogin := os.Getenv("GITHUB_BOT_LOGIN")
	botName := os.Getenv("GITHUB_BOT_NAME")
	botEmail := os.Getenv("GITHUB_BOT_EMAIL")

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
		fmt.Printf("Warning: failed to get token from script: %v\n", err)
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
		PRCloneURL:        fmt.Sprintf("%s#refs/heads/%s", pr.GetHead().GetRepo().GetCloneURL(), pr.GetHead().GetRef()),
		RepoName:          overseer.Name,
		MaxReviewFiles:    maxReviewFiles,
		IgnoreFiles:       overseer.Spec.Review.IgnoreFiles,
		SeverityThreshold: overseer.Spec.Review.SeverityThreshold,
		LLMExtensions:     overseer.Spec.Extensions,
		WorkspaceDiskSize: overseer.Spec.WorkspaceDiskSize,
	}

	sb, svc := sandbox.NewReviewSandbox(opt)

	_, err = kubeClient.DynamicClient.Resource(k8s.SandboxGVR).Namespace(namespace).Create(ctx, sb, metav1.CreateOptions{})
	if err != nil {
		return err
	}

	_, err = kubeClient.Clientset.CoreV1().Services(namespace).Create(ctx, svc, metav1.CreateOptions{})
	return err
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
				fmt.Printf("Chore %s %s. Deleting sandbox %s.\n", choreSlug, reason, item.GetName())
				if err := deleteSandbox(ctx, kubeClient, namespace, item.GetName()); err != nil {
					fmt.Printf("Warning: failed to delete sandbox %s: %v\n", item.GetName(), err)
				}
			}
		}
	}

	fmt.Println("Reconciliation complete.")
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
	fmt.Printf("Deleting sandbox %s...\n", sandboxName)
	err := kubeClient.DynamicClient.Resource(k8s.SandboxGVR).Namespace(namespace).Delete(ctx, sandboxName, metav1.DeleteOptions{})
	if err != nil && !errors.IsNotFound(err) {
		// handle string check if errors package is not behaving as expected with dynamic client
		if !strings.Contains(err.Error(), "not found") {
			return err
		}
	}
	// Also delete service
	serviceName := sandboxName + "-lb"
	fmt.Printf("Deleting service %s...\n", serviceName)
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
