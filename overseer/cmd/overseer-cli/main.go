package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
	corev1 "k8s.io/api/core/v1"
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
	"github.com/gke-labs/gemini-for-kubernetes-development/overseer/pkg/cli/installer"
	"github.com/gke-labs/gemini-for-kubernetes-development/overseer/pkg/cli/version"
	sandboxtaskv1alpha1 "github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/api/sandboxtask/v1alpha1"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/clients"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/commands"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/github"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/k8s"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/models"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/sandbox"
	githubv39 "github.com/google/go-github/v39/github"
)

var (
	overseerName      string
	namespace         string
	repoURL           string
	image             string
	workspaceDiskSize string
	iamEmail          string
	IssueModelsOrder  = []string{
		"gemini-3.5-flash",
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

	rootCmd.PersistentFlags().StringVar(&namespace, "namespace", namespace, "Kubernetes namespace (defaults to $NAMESPACE env var, or deduced from git origin remote)")
	rootCmd.PersistentFlags().StringVar(&repoURL, "repo", os.Getenv("REPO"), "Repository URL (defaults to $REPO env var or deduced from git upstream remote)")

	rootCmd.PersistentPreRunE = func(cmd *cobra.Command, args []string) error {
		if namespace == "" {
			namespace = deduceNamespaceFromGit()
		}
		if repoURL == "" {
			repoURL = deduceRepoURLFromGit()
		}
		return nil
	}

	rootCmd.AddCommand(buildIssueCommand())
	rootCmd.AddCommand(buildPRCommand())
	rootCmd.AddCommand(buildTaskCommand())

	adminCmd := &cobra.Command{
		Use:   "admin",
		Short: "Admin commands",
	}

	choreCmd := &cobra.Command{
		Use:   "chore",
		Short: "Manage chores",
	}
	choreCmd.AddCommand(buildChoreEnsureCommand())
	choreCmd.AddCommand(buildReconcileCommand())

	adminCmd.AddCommand(choreCmd)

	onboardCmd := &cobra.Command{
		Use:   "onboard [github-id]",
		Short: "Onboard a new user",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cmd.SilenceUsage = true
			return runAdminOnboard(context.Background(), args[0], iamEmail)
		},
	}
	onboardCmd.Flags().StringVar(&iamEmail, "email", "", "GCP IAM identity (user email) to bind to the namespace")
	adminCmd.AddCommand(onboardCmd)
	adminCmd.AddCommand(installer.BuildInstallerCmd())
	adminCmd.AddCommand(version.BuildVersionCmd())
	rootCmd.AddCommand(adminCmd)

	sandboxCmd := &cobra.Command{
		Use:   "sandbox",
		Short: "Manage sandboxes",
	}
	sandboxCmd.AddCommand(buildListCommand())
	sandboxCmd.AddCommand(buildChatCommand())
	sandboxCmd.AddCommand(buildConnectCommand())
	sandboxCmd.AddCommand(buildDeleteCommand())
	sandboxCmd.AddCommand(buildSuspendCommand())
	sandboxCmd.AddCommand(buildResumeCommand())

	rootCmd.AddCommand(sandboxCmd)
	rootCmd.AddCommand(buildRepoCommand())
	rootCmd.AddCommand(buildSecretCommand())

	if err := rootCmd.Execute(); err != nil {
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
		RunE: func(cmd *cobra.Command, _ []string) error {
			cmd.SilenceUsage = true
			return runIssue(context.Background(), number, prNumber, taskType, prompt)
		},
	}

	cmd.Flags().IntVar(&number, "number", 0, "Issue number")
	cmd.Flags().IntVar(&prNumber, "pr", 0, "PR number to extract issue from")
	cmd.Flags().StringVar(&taskType, "task", "fix-issue", "Task type (e.g., fix-issue, triage-issue)")
	cmd.Flags().StringVar(&prompt, "prompt", "", "Custom prompt for the task")
	cmd.Flags().StringVar(&image, "image", "golang:1.21", "Sandbox image")
	cmd.Flags().StringVar(&workspaceDiskSize, "workspace-disk-size", "6G", "Workspace disk size")

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
		RunE: func(cmd *cobra.Command, _ []string) error {
			cmd.SilenceUsage = true
			return runPR(context.Background(), number, taskType, submit, prompt)
		},
	}

	cmd.Flags().IntVar(&number, "number", 0, "PR number")
	cmd.Flags().StringVar(&taskType, "task", "review", "Task type (e.g., review, address-feedback, investigate-failures)")
	cmd.Flags().BoolVar(&submit, "submit", false, "Submit agent draft from task as review")
	cmd.Flags().StringVar(&prompt, "prompt", "", "Custom prompt for the task")
	cmd.Flags().StringVar(&image, "image", "golang:1.21", "Sandbox image")
	cmd.Flags().StringVar(&workspaceDiskSize, "workspace-disk-size", "6G", "Workspace disk size")

	_ = cmd.MarkFlagRequired("number")

	return cmd
}

func buildChoreEnsureCommand() *cobra.Command {
	var name string
	var file string

	cmd := &cobra.Command{
		Use:   "ensure",
		Short: "Create/ensure sandbox and task for a chore",
		RunE: func(cmd *cobra.Command, _ []string) error {
			cmd.SilenceUsage = true
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
		RunE: func(cmd *cobra.Command, _ []string) error {
			cmd.SilenceUsage = true
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
		return fmt.Errorf("OVERSEER_NAME environment variable and namespace must be set")
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

	userLogin, userName, userEmail := resolveGithubUserFromSecret(ctx, kubeClient.Clientset, namespace)

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

	githubAPIURL := os.Getenv("GITHUB_API_URL")
	var ghHost string
	if githubAPIURL != "" {
		u, err := url.Parse(githubAPIURL)
		if err == nil && u.Host != "" {
			ghHost = u.Host
		}
	}

	repoSandboxImage, configDirImage := resolveDefaultImages(ctx, kubeClient.Clientset)

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
			RepoSandboxImage:    repoSandboxImage,
			ConfigDirImage:      configDirImage,
			HTTPEnabled:         true,
			Replicas:            1,
			WorkspaceDiskSize:   overseer.Spec.WorkspaceDiskSize,
			ServiceAccountName: func() string {
				if os.Getenv("OVERSEER") != "" || os.Getenv("OVERSEER_NAME") != "" {
					return "overseer-sandbox"
				}
				return "issue-sandbox"
			}(),
			GHHost: ghHost,
			Secrets: func() []sandbox.SecretMount {
				var mounts []sandbox.SecretMount
				for _, s := range overseer.Spec.Secrets {
					mounts = append(mounts, sandbox.SecretMount{
						Name:      s.Name,
						MountPath: s.MountPath,
					})
				}
				return mounts
			}(),
			Env: func() []sandbox.EnvVar {
				var envs []sandbox.EnvVar
				for _, e := range overseer.Spec.Env {
					envs = append(envs, sandbox.EnvVar{
						Name:  e.Name,
						Value: e.Value,
					})
				}
				return envs
			}(),
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
	if namespace == "" {
		return fmt.Errorf("namespace must be set")
	}
	if overseerName == "" && repoURL == "" {
		return fmt.Errorf("repository URL must be set (via --repo flag, REPO env var, or git remote upstream)")
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

	var overseer overseerv1alpha1.Overseer
	if overseerName != "" {
		rwUnstructured, err := getOverseer(ctx, kubeClient.DynamicClient, overseerName)
		if err != nil {
			return fmt.Errorf("failed to get Overseer %s: %w", overseerName, err)
		}
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(rwUnstructured.Object, &overseer); err != nil {
			return fmt.Errorf("failed to convert Overseer: %w", err)
		}
	} else {
		gvr := schema.GroupVersionResource{
			Group:    "review.gemini.google.com",
			Version:  "v1alpha1",
			Resource: "repowatches",
		}

		name := ""
		// If repoURL contains '/', it is definitely a URL, so skip direct Get
		if !strings.Contains(repoURL, "/") {
			// Try to check if repoURL is actually the name of a RepoWatch resource
			_, err := kubeClient.DynamicClient.Resource(gvr).Namespace(namespace).Get(ctx, repoURL, metav1.GetOptions{})
			if err == nil {
				name = repoURL
			} else if !errors.IsNotFound(err) {
				return fmt.Errorf("failed to check if RepoWatch %s exists: %w", repoURL, err)
			}
		}

		if name == "" {
			// Fallback to finding by URL
			var err2 error
			name, err2 = findRepoWatchNameByURL(ctx, repoURL)
			if err2 != nil {
				return err2
			}
		}

		if name == "" {
			return fmt.Errorf("no matching RepoWatch found for URL/Name %s; run 'overseer-cli repo init' first", repoURL)
		}

		// Fetch RepoWatch
		rwUnstructured, err := kubeClient.DynamicClient.Resource(gvr).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return fmt.Errorf("failed to get RepoWatch %s: %w", name, err)
		}

		// Populate dummy Overseer from RepoWatch
		overseer.Name = name
		actualRepoURL, _, _ := unstructured.NestedString(rwUnstructured.Object, "spec", "repoURL")
		overseer.Spec.RepoURL = actualRepoURL

		img, _, _ := unstructured.NestedString(rwUnstructured.Object, "spec", "issue", "image")
		overseer.Spec.Image = img

		secret, _, _ := unstructured.NestedString(rwUnstructured.Object, "spec", "issue", "llm", "apiKeySecretRef")
		if secret == "" {
			secret = "gemini-api-key"
		}
		overseer.Spec.GeminiAPIKeySecretName = secret

		configdir, _, _ := unstructured.NestedString(rwUnstructured.Object, "spec", "issue", "llm", "configdirRef")
		overseer.Spec.ConfigdirRef = configdir

		prompt, _, _ := unstructured.NestedString(rwUnstructured.Object, "spec", "issue", "llm", "prompt")
		overseer.Spec.IssuePrompt = prompt

		robot, _, _ := unstructured.NestedString(rwUnstructured.Object, "spec", "issue", "robotAccount")
		overseer.Spec.RobotAccount = robot

		maxActive, _, _ := unstructured.NestedInt64(rwUnstructured.Object, "spec", "issue", "maxActiveSandboxes")
		maxActiveInt32 := int32(maxActive)
		overseer.Spec.MaxActiveIssues = &maxActiveInt32

		diskSize, _, _ := unstructured.NestedString(rwUnstructured.Object, "spec", "issue", "workspaceDiskSize")
		if diskSize == "" {
			diskSize = "10Gi"
		}
		overseer.Spec.WorkspaceDiskSize = diskSize
	}

	ghClient, err := github.NewClient(ctx)
	if err != nil {
		return fmt.Errorf("failed to create github client: %w", err)
	}
	if apiURL := os.Getenv("GITHUB_API_URL"); apiURL != "" {
		u, err := url.Parse(apiURL)
		if err != nil {
			return fmt.Errorf("invalid GITHUB_API_URL: %w", err)
		}
		ghClient.BaseURL = u
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
	if namespace == "" {
		return fmt.Errorf("namespace must be set")
	}
	if overseerName == "" && repoURL == "" {
		return fmt.Errorf("repository URL must be set (via --repo flag, REPO env var, or git remote upstream)")
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

	var overseer overseerv1alpha1.Overseer
	if overseerName != "" {
		rwUnstructured, err := getOverseer(ctx, kubeClient.DynamicClient, overseerName)
		if err != nil {
			return fmt.Errorf("failed to get Overseer %s: %w", overseerName, err)
		}
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(rwUnstructured.Object, &overseer); err != nil {
			return fmt.Errorf("failed to convert Overseer: %w", err)
		}
	} else {
		gvr := schema.GroupVersionResource{
			Group:    "review.gemini.google.com",
			Version:  "v1alpha1",
			Resource: "repowatches",
		}

		name := ""
		// If repoURL contains '/', it is definitely a URL, so skip direct Get
		if !strings.Contains(repoURL, "/") {
			// Try to check if repoURL is actually the name of a RepoWatch resource
			_, err := kubeClient.DynamicClient.Resource(gvr).Namespace(namespace).Get(ctx, repoURL, metav1.GetOptions{})
			if err == nil {
				name = repoURL
			} else if !errors.IsNotFound(err) {
				return fmt.Errorf("failed to check if RepoWatch %s exists: %w", repoURL, err)
			}
		}

		if name == "" {
			// Fallback to finding by URL
			var err2 error
			name, err2 = findRepoWatchNameByURL(ctx, repoURL)
			if err2 != nil {
				return err2
			}
		}

		if name == "" {
			return fmt.Errorf("no matching RepoWatch found for URL/Name %s; run 'overseer-cli repo init' first", repoURL)
		}

		// Fetch RepoWatch
		rwUnstructured, err := kubeClient.DynamicClient.Resource(gvr).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return fmt.Errorf("failed to get RepoWatch %s: %w", name, err)
		}

		// Populate dummy Overseer from RepoWatch
		overseer.Name = name
		actualRepoURL, _, _ := unstructured.NestedString(rwUnstructured.Object, "spec", "repoURL")
		overseer.Spec.RepoURL = actualRepoURL

		img, _, _ := unstructured.NestedString(rwUnstructured.Object, "spec", "review", "image")
		overseer.Spec.Image = img

		secret, _, _ := unstructured.NestedString(rwUnstructured.Object, "spec", "review", "llm", "apiKeySecretRef")
		if secret == "" {
			secret = "gemini-api-key"
		}
		overseer.Spec.GeminiAPIKeySecretName = secret

		configdir, _, _ := unstructured.NestedString(rwUnstructured.Object, "spec", "review", "llm", "configdirRef")
		overseer.Spec.ConfigdirRef = configdir

		prompt, _, _ := unstructured.NestedString(rwUnstructured.Object, "spec", "review", "llm", "prompt")
		overseer.Spec.Review.Prompt = prompt

		robot, _, _ := unstructured.NestedString(rwUnstructured.Object, "spec", "review", "robotAccount")
		overseer.Spec.RobotAccount = robot

		maxFiles, _, _ := unstructured.NestedInt64(rwUnstructured.Object, "spec", "review", "maxReviewFiles")
		overseer.Spec.Review.MaxReviewFiles = int(maxFiles)

		ignoreFiles, _, _ := unstructured.NestedSlice(rwUnstructured.Object, "spec", "review", "ignoreFiles")
		if len(ignoreFiles) > 0 {
			var ignores []string
			for _, ig := range ignoreFiles {
				ignores = append(ignores, fmt.Sprintf("%v", ig))
			}
			overseer.Spec.Review.IgnoreFiles = ignores
		}

		severity, _, _ := unstructured.NestedString(rwUnstructured.Object, "spec", "review", "severityThreshold")
		overseer.Spec.Review.SeverityThreshold = severity

		maxActive, _, _ := unstructured.NestedInt64(rwUnstructured.Object, "spec", "review", "maxActiveSandboxes")
		maxActiveInt32 := int32(maxActive)
		overseer.Spec.MaxActiveReviews = &maxActiveInt32

		diskSize, _, _ := unstructured.NestedString(rwUnstructured.Object, "spec", "review", "workspaceDiskSize")
		if diskSize == "" {
			diskSize = "10Gi"
		}
		overseer.Spec.WorkspaceDiskSize = diskSize
	}

	ghClient, err := github.NewClient(ctx)
	if err != nil {
		return fmt.Errorf("failed to create github client: %w", err)
	}
	if apiURL := os.Getenv("GITHUB_API_URL"); apiURL != "" {
		u, err := url.Parse(apiURL)
		if err != nil {
			return fmt.Errorf("invalid GITHUB_API_URL: %w", err)
		}
		ghClient.BaseURL = u
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
	if apiURL := os.Getenv("GITHUB_API_URL"); apiURL != "" {
		u, err := url.Parse(apiURL)
		if err != nil {
			return fmt.Errorf("invalid GITHUB_API_URL: %w", err)
		}
		client.BaseURL = u
	}

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
	userLogin, userName, userEmail := resolveGithubUserFromSecret(ctx, kubeClient.Clientset, namespace)

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

	githubAPIURL := os.Getenv("GITHUB_API_URL")
	var ghHost string
	if githubAPIURL != "" {
		u, err := url.Parse(githubAPIURL)
		if err == nil && u.Host != "" {
			ghHost = u.Host
		}
	}

	repoSandboxImage, configDirImage := resolveDefaultImages(ctx, kubeClient.Clientset)

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
			RepoSandboxImage:    repoSandboxImage,
			ConfigDirImage:      configDirImage,
			HTTPEnabled:         true,
			Replicas:            1,
			ServiceAccountName: func() string {
				if os.Getenv("OVERSEER") != "" || os.Getenv("OVERSEER_NAME") != "" {
					return "overseer-sandbox"
				}
				return "issue-sandbox"
			}(),
			WorkspaceDiskSize: overseer.Spec.WorkspaceDiskSize,
			GHHost:            ghHost,
			Secrets: func() []sandbox.SecretMount {
				var mounts []sandbox.SecretMount
				for _, s := range overseer.Spec.Secrets {
					mounts = append(mounts, sandbox.SecretMount{
						Name:      s.Name,
						MountPath: s.MountPath,
					})
				}
				return mounts
			}(),
			Env: func() []sandbox.EnvVar {
				var envs []sandbox.EnvVar
				for _, e := range overseer.Spec.Env {
					envs = append(envs, sandbox.EnvVar{
						Name:  e.Name,
						Value: e.Value,
					})
				}
				return envs
			}(),
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

	userLogin, userName, userEmail := resolveGithubUserFromSecret(ctx, kubeClient.Clientset, namespace)

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

	githubAPIURL := os.Getenv("GITHUB_API_URL")
	var ghHost string
	if githubAPIURL != "" {
		u, err := url.Parse(githubAPIURL)
		if err == nil && u.Host != "" {
			ghHost = u.Host
		}
	}

	repoSandboxImage, configDirImage := resolveDefaultImages(ctx, kubeClient.Clientset)

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
			RepoSandboxImage:      repoSandboxImage,
			ConfigDirImage:        configDirImage,
			HTTPEnabled:           true,
			Replicas:              1,
			ServiceAccountName: func() string {
				if os.Getenv("OVERSEER") != "" || os.Getenv("OVERSEER_NAME") != "" {
					return "overseer-sandbox"
				}
				return "review-sandbox"
			}(),
			GHHost: ghHost,
			Secrets: func() []sandbox.SecretMount {
				var mounts []sandbox.SecretMount
				for _, s := range overseer.Spec.Secrets {
					mounts = append(mounts, sandbox.SecretMount{
						Name:      s.Name,
						MountPath: s.MountPath,
					})
				}
				return mounts
			}(),
			Env: func() []sandbox.EnvVar {
				var envs []sandbox.EnvVar
				for _, e := range overseer.Spec.Env {
					envs = append(envs, sandbox.EnvVar{
						Name:  e.Name,
						Value: e.Value,
					})
				}
				return envs
			}(),
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
		return fmt.Errorf("OVERSEER_NAME environment variable and namespace must be set")
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
		Use:   "delete <target>",
		Short: "Delete a sandbox and its associated resources (like the -lb service)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cmd.SilenceUsage = true
			return runDeleteSandbox(context.Background(), args[0])
		},
	}

	return cmd
}

func buildSuspendCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "suspend <target>",
		Short: "Suspend a sandbox (scale it down to 0 replicas)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cmd.SilenceUsage = true
			return runSuspendSandbox(context.Background(), args[0])
		},
	}
	return cmd
}

func buildResumeCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "resume <target>",
		Short: "Resume a suspended sandbox (scale it up to 1 replica)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cmd.SilenceUsage = true
			return runResumeSandbox(context.Background(), args[0])
		},
	}
	return cmd
}

func buildListCommand() *cobra.Command {
	var filterType string
	var listPRs bool

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List sandboxes or handled pull requests",
		RunE: func(cmd *cobra.Command, _ []string) error {
			cmd.SilenceUsage = true
			if listPRs {
				return runListPRs(context.Background())
			}
			return runListSandboxes(context.Background(), filterType)
		},
	}

	cmd.Flags().StringVar(&filterType, "type", "", "Filter by sandbox type (review, issue, chore)")
	cmd.Flags().BoolVar(&listPRs, "prs", false, "List PRs handled by the system")

	return cmd
}

func runListSandboxes(ctx context.Context, sandboxType string) error {
	if namespace == "" {
		return fmt.Errorf("namespace must be set")
	}

	cfg, err := config.GetConfig()
	if err != nil {
		return fmt.Errorf("unable to get kubeconfig: %w", err)
	}

	dynClient, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return fmt.Errorf("unable to create dynamic client: %w", err)
	}

	listOptions := metav1.ListOptions{}
	var selectors []string
	if overseerName != "" {
		selectors = append(selectors, fmt.Sprintf("review.gemini.google.com/overseer=%s", overseerName))
	}
	if sandboxType != "" {
		selectors = append(selectors, fmt.Sprintf("sandbox.gemini.google.com/type=%s", sandboxType))
	}
	if len(selectors) > 0 {
		listOptions.LabelSelector = strings.Join(selectors, ",")
	}

	sandboxList, err := dynClient.Resource(k8s.SandboxGVR).Namespace(namespace).List(ctx, listOptions)
	if err != nil {
		return fmt.Errorf("failed to list sandboxes: %w", err)
	}

	fmt.Printf("%-40s %-15s %-20s\n", "NAME", "TYPE", "CREATED")
	for _, item := range sandboxList.Items {
		name := item.GetName()
		labels := item.GetLabels()
		sType := labels["sandbox.gemini.google.com/type"]
		created := item.GetCreationTimestamp().Format(time.RFC3339)
		fmt.Printf("%-40s %-15s %-20s\n", name, sType, created)
	}

	return nil
}

func runListPRs(ctx context.Context) error {
	if overseerName == "" {
		return fmt.Errorf("OVERSEER_NAME environment variable must be set")
	}

	cfg, err := config.GetConfig()
	if err != nil {
		return fmt.Errorf("unable to get kubeconfig: %w", err)
	}

	dynClient, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return fmt.Errorf("unable to create dynamic client: %w", err)
	}

	rwUnstructured, err := getOverseer(ctx, dynClient, overseerName)
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

	opts := &githubv39.PullRequestListOptions{
		State:       "open",
		ListOptions: githubv39.ListOptions{PerPage: 100},
	}

	prs, _, err := ghClient.PullRequests.List(ctx, owner, repo, opts)
	if err != nil {
		return fmt.Errorf("failed to list pull requests: %w", err)
	}

	fmt.Printf("%-10s %-50s %-20s\n", "NUMBER", "TITLE", "URL")
	for _, pr := range prs {
		fmt.Printf("%-10d %-50s %-20s\n", pr.GetNumber(), truncateString(pr.GetTitle(), 50), pr.GetHTMLURL())
	}

	return nil
}

func truncateString(s string, l int) string {
	if len(s) > l {
		return s[:l-3] + "..."
	}
	return s
}

func buildConnectCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "connect <target>",
		Short: "Connect to a sandbox via SSH + tmux",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cmd.SilenceUsage = true
			return runConnect(cmd.Context(), args[0])
		},
	}
	return cmd
}

func runConnect(ctx context.Context, target string) error {
	if namespace == "" {
		return fmt.Errorf("namespace must be set")
	}

	cfg, err := config.GetConfig()
	if err != nil {
		return fmt.Errorf("unable to get kubeconfig: %w", err)
	}

	dynClient, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return fmt.Errorf("unable to create dynamic client: %w", err)
	}

	sandboxName, err := resolveSandboxName(ctx, dynClient, target)
	if err != nil {
		return err
	}

	// Find the pod for the sandbox using the helper from repo-agent/pkg/sandbox
	podID, err := sandbox.FindSandboxPodInNamespace(ctx, sandboxName, namespace)
	if err != nil {
		return fmt.Errorf("failed to find pod for sandbox %q: %w", sandboxName, err)
	}
	if podID == nil {
		return fmt.Errorf("no pod found for sandbox %q in namespace %q", sandboxName, namespace)
	}

	// Update ~/.ssh/config
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("failed to get user home dir: %w", err)
	}

	sshConfigPath := filepath.Join(homeDir, ".ssh", "config")

	// Call exported helper from repo-agent/pkg/commands
	if err := commands.UpdateSSHConfig(ctx, sshConfigPath, sandboxName, *podID); err != nil {
		return fmt.Errorf("failed to update ssh config: %w", err)
	}

	// Launch tmux via exported helper
	if err := commands.LaunchTmux(ctx, sandboxName); err != nil {
		return fmt.Errorf("running tmux: %w", err)
	}

	return nil
}

func buildChatCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "chat <target>",
		Short: "Continue Gemini session inside a sandbox",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cmd.SilenceUsage = true
			return runChat(cmd.Context(), args[0])
		},
	}
	return cmd
}

func runChat(ctx context.Context, target string) error {
	if namespace == "" {
		return fmt.Errorf("namespace must be set")
	}

	cfg, err := config.GetConfig()
	if err != nil {
		return fmt.Errorf("unable to get kubeconfig: %w", err)
	}

	dynClient, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return fmt.Errorf("unable to create dynamic client: %w", err)
	}

	sandboxName, err := resolveSandboxName(ctx, dynClient, target)
	if err != nil {
		return err
	}

	// Find the pod for the sandbox
	podID, err := sandbox.FindSandboxPodInNamespace(ctx, sandboxName, namespace)
	if err != nil {
		return fmt.Errorf("failed to find pod for sandbox %q: %w", sandboxName, err)
	}
	if podID == nil {
		return fmt.Errorf("no pod found for sandbox %q in namespace %q", sandboxName, namespace)
	}

	// Update ~/.ssh/config
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("failed to get user home dir: %w", err)
	}

	sshConfigPath := filepath.Join(homeDir, ".ssh", "config")

	if err := commands.UpdateSSHConfig(ctx, sshConfigPath, sandboxName, *podID); err != nil {
		return fmt.Errorf("failed to update ssh config: %w", err)
	}

	// Launch interactive command via SSH
	args := []string{
		"ssh", "-t", sandboxName, "gemini",
	}

	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("starting ssh chat: %w", err)
	}

	if err := cmd.Wait(); err != nil {
		return fmt.Errorf("waiting for ssh chat: %w", err)
	}

	return nil
}

func buildTaskCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "task",
		Short: "Manage tasks in sandboxes",
	}

	createCmd := &cobra.Command{
		Use:   "create <target> <command>",
		Short: "Create a script task in a sandbox",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			cmd.SilenceUsage = true
			return runCreateTask(cmd.Context(), args[0], args[1])
		},
	}
	cmd.AddCommand(createCmd)
	cmd.AddCommand(buildTaskListCommand())
	cmd.AddCommand(buildTaskLogsCommand())

	return cmd
}

func buildTaskListCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "list <target>",
		Short: "List all tasks in a sandbox",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cmd.SilenceUsage = true
			return runListTasks(context.Background(), args[0])
		},
	}
	return cmd
}

func buildTaskLogsCommand() *cobra.Command {
	var follow bool

	cmd := &cobra.Command{
		Use:   "logs <task-name>",
		Short: "Get or follow logs for a task",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cmd.SilenceUsage = true
			return runTaskLogs(context.Background(), args[0], follow)
		},
	}

	cmd.Flags().BoolVarP(&follow, "follow", "f", false, "Specify if the logs should be streamed")
	return cmd
}

func runCreateTask(ctx context.Context, target string, command string) error {
	if namespace == "" {
		return fmt.Errorf("namespace must be set")
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

	manager := k8s.NewManager(&clients.KubernetesClient{DynamicClient: dynClient, Clientset: clientset})

	sandboxName, err := resolveSandboxName(ctx, dynClient, target)
	if err != nil {
		return err
	}

	// Create Task
	taskType := "script"
	fmt.Printf("Creating task %s for sandbox %s...\n", taskType, sandboxName)
	params := map[string]string{
		"command": command,
	}

	err = manager.CreateSandboxTask(ctx, namespace, sandboxName, "Sandbox", taskType, params)
	if err != nil {
		return fmt.Errorf("failed to create sandbox task: %w", err)
	}

	fmt.Println("Task created successfully.")

	// Wait for the task to be created and listed
	time.Sleep(1 * time.Second)

	// Find the task name
	taskList, err := manager.ListSandboxTasks(ctx, namespace, sandboxName)
	if err != nil {
		return fmt.Errorf("failed to list tasks: %w", err)
	}

	var latestTask *sandboxtaskv1alpha1.SandboxTask
	for i := range taskList.Items {
		task := &taskList.Items[i]
		if task.Spec.Type == "script" {
			if latestTask == nil || task.CreationTimestamp.After(latestTask.CreationTimestamp.Time) {
				latestTask = task
			}
		}
	}

	if latestTask == nil {
		return fmt.Errorf("failed to find the created task")
	}

	taskName := latestTask.GetName()
	fmt.Printf("Streaming logs for task %s...\n", taskName)

	// Find the pod for the sandbox
	podID, err := sandbox.FindSandboxPodInNamespace(ctx, sandboxName, namespace)
	if err != nil {
		return fmt.Errorf("failed to find pod for sandbox %q: %w", sandboxName, err)
	}
	if podID == nil {
		return fmt.Errorf("no pod found for sandbox %q in namespace %q", sandboxName, namespace)
	}

	// Stream logs using kubectl exec
	logPath := fmt.Sprintf("/workspaces/.agent/logs/%s.log", taskName)

	// Run kubectl exec -i <pod> -n <namespace> -- tail -f <logPath>
	args := []string{
		"kubectl", "exec", "-i", podID.Name, "-n", podID.Namespace, "--", "tail", "-f", logPath,
	}

	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("starting log streaming: %w", err)
	}

	if err := cmd.Wait(); err != nil {
		// Ignore error if context was canceled or process killed by user
		if ctx.Err() == nil {
			return fmt.Errorf("waiting for log streaming: %w", err)
		}
	}

	return nil
}

func runListTasks(ctx context.Context, target string) error {
	if namespace == "" {
		return fmt.Errorf("namespace must be set")
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

	manager := k8s.NewManager(&clients.KubernetesClient{DynamicClient: dynClient, Clientset: clientset})

	sandboxName, err := resolveSandboxName(ctx, dynClient, target)
	if err != nil {
		return err
	}

	taskList, err := manager.ListSandboxTasks(ctx, namespace, sandboxName)
	if err != nil {
		return fmt.Errorf("failed to list tasks for sandbox %s: %w", sandboxName, err)
	}

	fmt.Printf("%-50s %-15s %-12s %-20s\n", "NAME", "TYPE", "STATE", "CREATED")
	for i := range taskList.Items {
		task := &taskList.Items[i]
		name := task.GetName()
		tType := task.Spec.Type
		state := task.Status.TaskState
		if state == "" {
			state = "Pending"
		}
		created := task.CreationTimestamp.Format(time.RFC3339)
		fmt.Printf("%-50s %-15s %-12s %-20s\n", name, tType, state, created)
	}

	return nil
}

func runTaskLogs(ctx context.Context, taskName string, follow bool) error {
	if namespace == "" {
		return fmt.Errorf("namespace must be set")
	}

	cfg, err := config.GetConfig()
	if err != nil {
		return fmt.Errorf("unable to get kubeconfig: %w", err)
	}

	dynClient, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return fmt.Errorf("unable to create dynamic client: %w", err)
	}

	// Get SandboxTask to find sandboxName
	gvr := schema.GroupVersionResource{
		Group:    "custom.agents.x-k8s.io",
		Version:  "v1alpha1",
		Resource: "sandboxtasks",
	}
	taskUnstructured, err := dynClient.Resource(gvr).Namespace(namespace).Get(ctx, taskName, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("failed to get task %s: %w", taskName, err)
	}

	sandboxName, found, err := unstructured.NestedString(taskUnstructured.Object, "spec", "sandboxName")
	if err != nil || !found || sandboxName == "" {
		return fmt.Errorf("sandboxName not found in task spec for %s: %w", taskName, err)
	}

	// Find pod
	podID, err := sandbox.FindSandboxPodInNamespace(ctx, sandboxName, namespace)
	if err != nil {
		return fmt.Errorf("failed to find pod for sandbox %s: %w", sandboxName, err)
	}
	if podID == nil {
		return fmt.Errorf("no active pod found for sandbox %s in namespace %s", sandboxName, namespace)
	}

	logPath := fmt.Sprintf("/workspaces/.agent/logs/%s.log", taskName)

	var args []string
	if follow {
		fmt.Printf("Streaming logs for task %s (Ctrl+C to stop)...\n", taskName)
		args = []string{"kubectl", "exec", "-i", podID.Name, "-n", podID.Namespace, "--", "tail", "-f", logPath}
	} else {
		args = []string{"kubectl", "exec", "-i", podID.Name, "-n", podID.Namespace, "--", "cat", logPath}
	}

	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("failed to start command: %w", err)
	}

	if err := cmd.Wait(); err != nil {
		if ctx.Err() == nil {
			return fmt.Errorf("log execution failed: %w", err)
		}
	}

	return nil
}

func runDeleteSandbox(ctx context.Context, target string) error {
	if namespace == "" {
		return fmt.Errorf("namespace must be set")
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

	sandboxName, err := resolveSandboxName(ctx, dynClient, target)
	if err != nil {
		return err
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

func runSuspendSandbox(ctx context.Context, target string) error {
	if namespace == "" {
		return fmt.Errorf("namespace must be set")
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

	manager := k8s.NewManager(&clients.KubernetesClient{DynamicClient: dynClient, Clientset: clientset})

	sandboxName, err := resolveSandboxName(ctx, dynClient, target)
	if err != nil {
		return err
	}

	fmt.Printf("Suspending sandbox %s...\n", sandboxName)
	err = manager.ScaledownDevSandboxHelper(ctx, namespace, sandboxName)
	if err != nil {
		return err
	}

	fmt.Println("Sandbox suspended successfully.")
	return nil
}

func runResumeSandbox(ctx context.Context, target string) error {
	if namespace == "" {
		return fmt.Errorf("namespace must be set")
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

	manager := k8s.NewManager(&clients.KubernetesClient{DynamicClient: dynClient, Clientset: clientset})

	sandboxName, err := resolveSandboxName(ctx, dynClient, target)
	if err != nil {
		return err
	}

	fmt.Printf("Resuming sandbox %s...\n", sandboxName)
	err = manager.ScaleupDevSandboxHelper(ctx, namespace, sandboxName)
	if err != nil {
		return err
	}

	fmt.Println("Sandbox resumed successfully.")
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

func deduceNamespaceFromGit() string {
	cmd := exec.Command("git", "remote", "get-url", "origin")
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		return ""
	}
	urlStr := strings.TrimSpace(out.String())
	if urlStr == "" {
		return ""
	}

	// Handle HTTPS/HTTP
	if strings.HasPrefix(urlStr, "http://") || strings.HasPrefix(urlStr, "https://") {
		u, err := url.Parse(urlStr)
		if err != nil {
			return ""
		}
		parts := strings.Split(strings.TrimPrefix(u.Path, "/"), "/")
		if len(parts) > 0 {
			return parts[0]
		}
	}

	// Handle SSH (git@github.com:user/repo.git)
	if strings.Contains(urlStr, "@") && strings.Contains(urlStr, ":") {
		parts := strings.Split(urlStr, ":")
		if len(parts) > 1 {
			subParts := strings.Split(parts[1], "/")
			if len(subParts) > 0 {
				return subParts[0]
			}
		}
	}

	return ""
}

func deduceRepoURLFromGit() string {
	cmd := exec.Command("git", "remote", "get-url", "upstream")
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		return ""
	}
	urlStr := strings.TrimSpace(out.String())
	if urlStr == "" {
		return ""
	}
	return strings.TrimSuffix(urlStr, ".git")
}

func buildRepoCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "repo",
		Short: "Manage RepoWatch resources",
	}

	var nameFlag string
	cmd.PersistentFlags().StringVar(&nameFlag, "name", "", "Name for the RepoWatch resource")

	var githubSecret string
	initCmd := &cobra.Command{
		Use:   "init",
		Short: "Create a RepoWatch resource for the current repository",
		RunE: func(cmd *cobra.Command, _ []string) error {
			cmd.SilenceUsage = true
			return runRepoInit(context.Background(), nameFlag, githubSecret)
		},
	}
	initCmd.Flags().StringVar(&githubSecret, "github-secret", "github-pat", "Name of the GitHub secret")
	cmd.AddCommand(initCmd)

	cmd.AddCommand(&cobra.Command{
		Use:   "list",
		Short: "List all RepoWatch resources",
		RunE: func(cmd *cobra.Command, _ []string) error {
			cmd.SilenceUsage = true
			return runRepoList(context.Background())
		},
	})

	cmd.AddCommand(&cobra.Command{
		Use:   "get [name]",
		Short: "Get a RepoWatch resource as YAML",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cmd.SilenceUsage = true
			name := nameFlag
			if name == "" && len(args) > 0 {
				name = args[0]
			}
			if name == "" {
				var err error
				name, err = findRepoWatchNameByURL(context.Background(), repoURL)
				if err != nil {
					return err
				}
			}
			if name == "" {
				name = deduceDefaultName(repoURL)
			}
			return runRepoGet(context.Background(), name)
		},
	})

	deleteCmd := &cobra.Command{
		Use:   "delete [name]",
		Short: "Delete a RepoWatch resource",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cmd.SilenceUsage = true
			name := nameFlag
			if name == "" && len(args) > 0 {
				name = args[0]
			}
			if name == "" {
				var err error
				name, err = findRepoWatchNameByURL(context.Background(), repoURL)
				if err != nil {
					return err
				}
			}
			if name == "" {
				name = deduceDefaultName(repoURL)
			}
			return runRepoDelete(context.Background(), name)
		},
	}
	cmd.AddCommand(deleteCmd)

	editCmd := &cobra.Command{
		Use:   "edit [name]",
		Short: "Edit a RepoWatch resource",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cmd.SilenceUsage = true
			name := nameFlag
			if name == "" && len(args) > 0 {
				name = args[0]
			}
			if name == "" {
				var err error
				name, err = findRepoWatchNameByURL(context.Background(), repoURL)
				if err != nil {
					return err
				}
			}
			if name == "" {
				name = deduceDefaultName(repoURL)
			}
			return runRepoEdit(context.Background(), name)
		},
	}
	cmd.AddCommand(editCmd)

	return cmd
}

func runRepoInit(ctx context.Context, nameFlag string, githubSecret string) error {
	if namespace == "" {
		return fmt.Errorf("namespace must be set")
	}
	if repoURL == "" {
		return fmt.Errorf("repository URL must be set (deduced from git or set via --repo)")
	}

	// Check if RepoWatch already exists for this URL
	existingName, err := findRepoWatchNameByURL(ctx, repoURL)
	if err != nil {
		return err
	}
	if existingName != "" {
		fmt.Printf("RepoWatch already exists for URL %s with name %s. Skipping creation.\n", repoURL, existingName)
		return nil
	}

	repoName := nameFlag
	if repoName == "" {
		repoName = deduceDefaultName(repoURL)
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

	_, err = clientset.CoreV1().Secrets(namespace).Get(ctx, githubSecret, metav1.GetOptions{})
	if err != nil {
		if errors.IsNotFound(err) {
			return fmt.Errorf("github secret %s not found in namespace %s.\nPlease set it first using:\n  overseer-cli secret set github-pat <token> --namespace %s", githubSecret, namespace, namespace)
		}
		return fmt.Errorf("failed to check github secret: %w", err)
	}

	geminiSecret := "gemini-vscode-tokens"
	_, err = clientset.CoreV1().Secrets(namespace).Get(ctx, geminiSecret, metav1.GetOptions{})
	if err != nil {
		if errors.IsNotFound(err) {
			return fmt.Errorf("gemini secret %s not found in namespace %s.\nPlease set it first using:\n  overseer-cli secret set gemini <token> --namespace %s", geminiSecret, namespace, namespace)
		}
		return fmt.Errorf("failed to check gemini secret: %w", err)
	}

	gvr := schema.GroupVersionResource{
		Group:    "review.gemini.google.com",
		Version:  "v1alpha1",
		Resource: "repowatches",
	}

	repoWatch := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "review.gemini.google.com/v1alpha1",
			"kind":       "RepoWatch",
			"metadata": map[string]interface{}{
				"name":      repoName,
				"namespace": namespace,
			},
			"spec": map[string]interface{}{
				"repoURL":             repoURL,
				"pollIntervalSeconds": int64(300),
				"githubSecretName":    githubSecret,
				"dev": map[string]interface{}{
					"maxActiveSandboxes": int64(0),
					"maxSandboxes":       int64(0),
					"image":              "ghcr.io/gke-labs/gemini-for-kubernetes-development/generic-golang:latest",
					"llm": map[string]interface{}{
						"apiKeySecretRef": "gemini-vscode-tokens",
						"provider":        "gemini-cli",
					},
				},
				"review": map[string]interface{}{
					"reviewShutdownAfterMinutes": int64(30),
					"image":                      "ghcr.io/gke-labs/gemini-for-kubernetes-development/generic-golang:latest",
					"maxActiveSandboxes":         int64(0),
					"maxSandboxes":               int64(0),
					"llm": map[string]interface{}{
						"apiKeySecretRef": "gemini-vscode-tokens",
						"provider":        "gemini-cli",
						"prompt":          "You are an expert kubernetes developer who is helping with code reviews.\nPlease look at the most recent commit and provide a review feedback.\nWould you approve it ?\nPlease pay attention to the following:\n1. Does the fix resolve the original problem.\n2. Look for linked issues to understand the original problem.\n3. Are there tests to check the fix.",
					},
				},
				"issue": map[string]interface{}{
					"maxActiveSandboxes":        int64(0),
					"maxSandboxes":              int64(0),
					"issueShutdownAfterMinutes": int64(0),
					"image":                     "ghcr.io/gke-labs/gemini-for-kubernetes-development/generic-golang:latest",
					"llm": map[string]interface{}{
						"apiKeySecretRef": "gemini-vscode-tokens",
						"provider":        "gemini-cli",
					},
					"models": []interface{}{
						"gemini-3.5-flash",
						"gemini-3-flash-preview",
						"gemini-3.1-pro-preview",
						"gemini-2.5-pro",
						"gemini-2.5-flash",
					},
					"handlers": []interface{}{
						map[string]interface{}{
							"name":     "fix",
							"taskType": "fix-issue",
							"labels": []interface{}{
								"repo-agent",
							},
							"prompt": "Fix this issue\n",
						},
					},
				},
			},
		},
	}

	fmt.Printf("Creating RepoWatch %s in namespace %s...\n", repoName, namespace)
	_, err = dynClient.Resource(gvr).Namespace(namespace).Create(ctx, repoWatch, metav1.CreateOptions{})
	if err != nil {
		return fmt.Errorf("failed to create RepoWatch: %w", err)
	}

	fmt.Println("Done.")
	return nil
}

func runRepoList(ctx context.Context) error {
	if namespace == "" {
		return fmt.Errorf("namespace must be set")
	}

	cfg, err := config.GetConfig()
	if err != nil {
		return fmt.Errorf("unable to get kubeconfig: %w", err)
	}

	dynClient, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return fmt.Errorf("unable to create dynamic client: %w", err)
	}

	gvr := schema.GroupVersionResource{
		Group:    "review.gemini.google.com",
		Version:  "v1alpha1",
		Resource: "repowatches",
	}

	list, err := dynClient.Resource(gvr).Namespace(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("failed to list RepoWatches: %w", err)
	}

	fmt.Printf("%-30s %-50s\n", "NAME", "REPO URL")
	for _, item := range list.Items {
		name := item.GetName()
		repoURL, _, _ := unstructured.NestedString(item.Object, "spec", "repoURL")
		fmt.Printf("%-30s %-50s\n", name, repoURL)
	}

	return nil
}

func runRepoDelete(ctx context.Context, name string) error {
	if namespace == "" {
		return fmt.Errorf("namespace must be set")
	}

	cfg, err := config.GetConfig()
	if err != nil {
		return fmt.Errorf("unable to get kubeconfig: %w", err)
	}

	dynClient, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return fmt.Errorf("unable to create dynamic client: %w", err)
	}

	gvr := schema.GroupVersionResource{
		Group:    "review.gemini.google.com",
		Version:  "v1alpha1",
		Resource: "repowatches",
	}

	fmt.Printf("Deleting RepoWatch %s in namespace %s...\n", name, namespace)
	err = dynClient.Resource(gvr).Namespace(namespace).Delete(ctx, name, metav1.DeleteOptions{})
	if err != nil {
		return fmt.Errorf("failed to delete RepoWatch: %w", err)
	}

	fmt.Println("Done.")
	return nil
}

func runRepoEdit(ctx context.Context, name string) error {
	if namespace == "" {
		return fmt.Errorf("namespace must be set")
	}

	cmd := exec.Command("kubectl", "edit", "repowatch", name, "-n", namespace)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	return cmd.Run()
}

func runRepoGet(ctx context.Context, name string) error {
	if namespace == "" {
		return fmt.Errorf("namespace must be set")
	}

	cfg, err := config.GetConfig()
	if err != nil {
		return fmt.Errorf("unable to get kubeconfig: %w", err)
	}

	dynClient, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return fmt.Errorf("unable to create dynamic client: %w", err)
	}

	gvr := schema.GroupVersionResource{
		Group:    "review.gemini.google.com",
		Version:  "v1alpha1",
		Resource: "repowatches",
	}

	item, err := dynClient.Resource(gvr).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("failed to get RepoWatch: %w", err)
	}

	fmt.Printf("RepoWatch: %s\n", item.GetName())

	repoURL, _, _ := unstructured.NestedString(item.Object, "spec", "repoURL")
	fmt.Printf("URL:       %s\n", repoURL)
	fmt.Printf("Namespace: %s\n\n", item.GetNamespace())

	fmt.Println("Review Config:")
	maxActive, _, _ := unstructured.NestedInt64(item.Object, "spec", "review", "maxActiveSandboxes")
	maxTotal, _, _ := unstructured.NestedInt64(item.Object, "spec", "review", "maxSandboxes")
	fmt.Printf("  Max Active Sandboxes: %d\n", maxActive)
	fmt.Printf("  Max Sandboxes:        %d\n\n", maxTotal)

	fmt.Println("Issue Config:")
	maxActiveIssue, _, _ := unstructured.NestedInt64(item.Object, "spec", "issue", "maxActiveSandboxes")
	maxTotalIssue, _, _ := unstructured.NestedInt64(item.Object, "spec", "issue", "maxSandboxes")
	fmt.Printf("  Max Active Sandboxes: %d\n", maxActiveIssue)
	fmt.Printf("  Max Sandboxes:        %d\n\n", maxTotalIssue)

	fmt.Println("Status:")
	pendingPRs, _, _ := unstructured.NestedSlice(item.Object, "status", "pendingPRs")
	if len(pendingPRs) > 0 {
		fmt.Printf("  Pending PRs: ")
		var prs []string
		for _, pr := range pendingPRs {
			prs = append(prs, fmt.Sprintf("%v", pr))
		}
		fmt.Println(strings.Join(prs, ", "))
	} else {
		fmt.Println("  Pending PRs: None")
	}

	issueSandboxes, _, _ := unstructured.NestedMap(item.Object, "status", "issueSandboxes")
	if len(issueSandboxes) > 0 {
		fmt.Println("  Issue Sandboxes:")
		for handler, list := range issueSandboxes {
			fmt.Printf("    Handler: %s\n", handler)
			listSlice, ok := list.([]interface{})
			if !ok {
				continue
			}
			for _, s := range listSlice {
				sMap, ok := s.(map[string]interface{})
				if !ok {
					continue
				}
				num, _, _ := unstructured.NestedInt64(sMap, "number")
				sName, _, _ := unstructured.NestedString(sMap, "sandboxName")
				status, _, _ := unstructured.NestedString(sMap, "status")
				fmt.Printf("      - #%d (%s) [%s]\n", num, sName, status)
			}
		}
	} else {
		fmt.Println("  Issue Sandboxes: None")
	}

	return nil
}

func findRepoWatchNameByURL(ctx context.Context, repoURL string) (string, error) {
	if namespace == "" {
		return "", fmt.Errorf("namespace must be set")
	}

	cfg, err := config.GetConfig()
	if err != nil {
		return "", fmt.Errorf("unable to get kubeconfig: %w", err)
	}

	dynClient, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return "", fmt.Errorf("unable to create dynamic client: %w", err)
	}

	gvr := schema.GroupVersionResource{
		Group:    "review.gemini.google.com",
		Version:  "v1alpha1",
		Resource: "repowatches",
	}

	list, err := dynClient.Resource(gvr).Namespace(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return "", fmt.Errorf("failed to list RepoWatches: %w", err)
	}

	for _, item := range list.Items {
		url, _, _ := unstructured.NestedString(item.Object, "spec", "repoURL")
		if url == repoURL {
			return item.GetName(), nil
		}
	}
	return "", nil
}

func deduceDefaultName(repoURL string) string {
	parts := strings.Split(strings.TrimSuffix(repoURL, "/"), "/")
	repoName := ""
	if len(parts) > 0 {
		repoName = parts[len(parts)-1]
	}
	if len(repoName) > 30 {
		repoName = repoName[:30]
	}
	return repoName
}

func buildSecretCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "secret",
		Short: "Manage secrets in the namespace",
	}

	var githubEmail string
	var githubName string

	setCmd := &cobra.Command{
		Use:   "set [github-pat|gemini] [token]",
		Short: "Set a secret value",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			cmd.SilenceUsage = true
			return runSecretSet(context.Background(), args[0], args[1], githubEmail, githubName)
		},
	}
	setCmd.Flags().StringVar(&githubEmail, "github-email", "", "GitHub email (defaults to git config user.email) - only for github-pat")
	setCmd.Flags().StringVar(&githubName, "github-name", "", "GitHub name (defaults to git config user.name) - only for github-pat")
	cmd.AddCommand(setCmd)

	clearCmd := &cobra.Command{
		Use:   "clear [github-pat|gemini|all]",
		Short: "Clear/delete a secret",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cmd.SilenceUsage = true
			return runSecretClear(context.Background(), args[0])
		},
	}
	cmd.AddCommand(clearCmd)

	return cmd
}

func runSecretSet(ctx context.Context, secretType string, token string, email string, name string) error {
	if namespace == "" {
		return fmt.Errorf("namespace must be set")
	}

	cfg, err := config.GetConfig()
	if err != nil {
		return fmt.Errorf("unable to get kubeconfig: %w", err)
	}

	clientset, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return fmt.Errorf("unable to create clientset: %w", err)
	}

	var secretName string
	var data map[string][]byte

	switch secretType {
	case "github-pat":
		secretName = "github-pat"
		// Deduce email and name if not provided
		if email == "" {
			email = getGitConfig("user.email")
		}
		if name == "" {
			name = getGitConfig("user.name")
		}
		data = map[string][]byte{
			"manual_pat": []byte(token),
			"pat":        []byte(token),
		}
		if name != "" {
			data["name"] = []byte(name)
		}
		if email != "" {
			data["email"] = []byte(email)
		}
	case "gemini":
		secretName = "gemini-vscode-tokens"
		data = map[string][]byte{
			"gemini": []byte(token),
		}
	default:
		return fmt.Errorf("unknown secret type: %s. Supported types: github-pat, gemini", secretType)
	}

	// Check if exists
	_, err = clientset.CoreV1().Secrets(namespace).Get(ctx, secretName, metav1.GetOptions{})
	if err == nil {
		return fmt.Errorf("secret %s already exists in namespace %s. Use clear command first if you want to recreate it", secretName, namespace)
	}
	if !errors.IsNotFound(err) {
		return fmt.Errorf("failed to check secret existence: %w", err)
	}

	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      secretName,
			Namespace: namespace,
		},
		Data: data,
	}

	fmt.Printf("Creating secret %s in namespace %s...\n", secretName, namespace)
	_, err = clientset.CoreV1().Secrets(namespace).Create(ctx, secret, metav1.CreateOptions{})
	if err != nil {
		return fmt.Errorf("failed to create secret: %w", err)
	}

	fmt.Println("Done.")
	return nil
}

func runSecretClear(ctx context.Context, secretType string) error {
	if namespace == "" {
		return fmt.Errorf("namespace must be set")
	}

	cfg, err := config.GetConfig()
	if err != nil {
		return fmt.Errorf("unable to get kubeconfig: %w", err)
	}

	clientset, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return fmt.Errorf("unable to create clientset: %w", err)
	}

	deleteSecret := func(name string) error {
		fmt.Printf("Deleting secret %s in namespace %s...\n", name, namespace)
		err := clientset.CoreV1().Secrets(namespace).Delete(ctx, name, metav1.DeleteOptions{})
		if err != nil {
			if errors.IsNotFound(err) {
				fmt.Printf("Secret %s not found in namespace %s. Skipping.\n", name, namespace)
				return nil
			}
			return fmt.Errorf("failed to delete secret %s: %w", name, err)
		}
		fmt.Println("Deleted.")
		return nil
	}

	switch secretType {
	case "github-pat":
		return deleteSecret("github-pat")
	case "gemini":
		return deleteSecret("gemini-vscode-tokens")
	case "all":
		err1 := deleteSecret("github-pat")
		err2 := deleteSecret("gemini-vscode-tokens")
		if err1 != nil || err2 != nil {
			return fmt.Errorf("failed to clear all secrets: errors: %v, %v", err1, err2)
		}
		return nil
	default:
		return fmt.Errorf("unknown secret type: %s. Supported types: github-pat, gemini, all", secretType)
	}
}

func runAdminOnboard(ctx context.Context, githubID string, email string) error {
	if githubID == "" {
		return fmt.Errorf("github-id is required")
	}

	cfg, err := config.GetConfig()
	if err != nil {
		return fmt.Errorf("unable to get kubeconfig: %w", err)
	}

	clientset, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return fmt.Errorf("unable to create clientset: %w", err)
	}

	namespace := githubID

	fmt.Printf("Bootstrapping namespace %s...\n", namespace)
	err = k8s.BootstrapNamespaceSimple(ctx, clientset, namespace)
	if err != nil {
		return fmt.Errorf("failed to bootstrap namespace %s: %w", namespace, err)
	}

	if email != "" {
		fmt.Printf("Binding GCP IAM identity %s to namespace %s...\n", email, namespace)
		err = k8s.BindUserIAMToNamespace(ctx, clientset, namespace, email)
		if err != nil {
			return fmt.Errorf("failed to bind IAM identity: %w", err)
		}
	}

	fmt.Println("Onboarding completed successfully.")
	return nil
}

func getGitConfig(key string) string {
	cmd := exec.Command("git", "config", key)
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		return ""
	}
	return strings.TrimSpace(out.String())
}

func resolveDefaultImages(ctx context.Context, clientset kubernetes.Interface) (repoSandboxImage string, configDirImage string) {
	repoSandboxImage = os.Getenv("REPO_SANDBOX_IMAGE")
	configDirImage = os.Getenv("CONFIGDIR_CLI_IMAGE")
	if configDirImage == "" {
		configDirImage = os.Getenv("CONFIG_DIR_IMAGE") // check legacy env too
	}

	if repoSandboxImage != "" && configDirImage != "" {
		return repoSandboxImage, configDirImage
	}

	// Try to resolve from running repowatch-controller in repo-agent-system
	ss, err := clientset.AppsV1().StatefulSets("repo-agent-system").Get(ctx, "repowatch-controller", metav1.GetOptions{})
	if err == nil && len(ss.Spec.Template.Spec.Containers) > 0 {
		container := ss.Spec.Template.Spec.Containers[0]

		// Extract from env
		for _, envVar := range container.Env {
			if envVar.Name == "REPO_SANDBOX_IMAGE" && repoSandboxImage == "" {
				repoSandboxImage = envVar.Value
			}
			if envVar.Name == "CONFIGDIR_CLI_IMAGE" && configDirImage == "" {
				configDirImage = envVar.Value
			}
		}

		// If still not found, try to deduce from controller image tag
		if repoSandboxImage == "" || configDirImage == "" {
			controllerImage := container.Image
			// e.g. ghcr.io/gke-labs/gemini-for-kubernetes-development/repowatch-controller:16bd72d608c922c132257fb5023bf2d6b940ad64
			parts := strings.Split(controllerImage, ":")
			if len(parts) == 2 {
				tag := parts[1]
				// Try to split by '/' to get registry
				imageParts := strings.Split(parts[0], "/")
				if len(imageParts) > 1 {
					registry := strings.Join(imageParts[:len(imageParts)-1], "/")
					if repoSandboxImage == "" {
						repoSandboxImage = fmt.Sprintf("%s/repo-sandbox:%s", registry, tag)
					}
					if configDirImage == "" {
						configDirImage = fmt.Sprintf("%s/configdir-cli:%s", registry, tag)
					}
				}
			}
		}
	}

	// Final fallbacks if everything fails
	if repoSandboxImage == "" {
		repoSandboxImage = "ghcr.io/gke-labs/gemini-for-kubernetes-development/repo-sandbox:latest"
	}
	if configDirImage == "" {
		configDirImage = "ghcr.io/gke-labs/gemini-for-kubernetes-development/configdir-cli:latest"
	}

	return repoSandboxImage, configDirImage
}

func resolveGithubUserFromSecret(ctx context.Context, clientset kubernetes.Interface, namespace string) (userLogin, userName, userEmail string) {
	userLogin = os.Getenv("GITHUB_USER_ID")
	userName = os.Getenv("GITHUB_USER_NAME")
	userEmail = os.Getenv("GITHUB_USER_EMAIL")

	if userLogin != "" {
		if userName == "" {
			userName = userLogin
		}
		return userLogin, userName, userEmail
	}

	// Fallback to secret-based resolution
	userLogin = namespace // namespace IS the github ID
	userName = userLogin
	userEmail = ""

	secretName := "github-pat"
	secret, err := clientset.CoreV1().Secrets(namespace).Get(ctx, secretName, metav1.GetOptions{})
	if err == nil && secret.Data != nil {
		if nameBytes, ok := secret.Data["name"]; ok && len(nameBytes) > 0 {
			userName = string(nameBytes)
		}
		if emailBytes, ok := secret.Data["email"]; ok && len(emailBytes) > 0 {
			userEmail = string(emailBytes)
		}
	}

	return userLogin, userName, userEmail
}

func resolveSandboxName(ctx context.Context, dynClient dynamic.Interface, target string) (string, error) {
	if _, err := strconv.Atoi(target); err != nil {
		return target, nil
	}

	listOptions := metav1.ListOptions{}
	sandboxList, err := dynClient.Resource(k8s.SandboxGVR).Namespace(namespace).List(ctx, listOptions)
	if err != nil {
		return "", fmt.Errorf("failed to list sandboxes: %w", err)
	}

	for _, item := range sandboxList.Items {
		name := item.GetName()
		if strings.Contains(name, "-pr-"+target) || strings.Contains(name, "-issue-"+target) {
			return name, nil
		}
	}
	return "", fmt.Errorf("could not resolve target %q to a sandbox", target)
}
