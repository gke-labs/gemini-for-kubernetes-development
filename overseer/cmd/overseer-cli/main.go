package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/spf13/cobra"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/klog/v2"
	"sigs.k8s.io/controller-runtime/pkg/client/config"
	"sigs.k8s.io/yaml"

	reviewv1alpha1 "github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/api/repowatch/v1alpha1"
	sandboxtaskv1alpha1 "github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/api/sandboxtask/v1alpha1"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/clients"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/github"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/k8s"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/models"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/sandbox"
	githubv39 "github.com/google/go-github/v39/github"
)

var (
	repoWatchName string
	namespace     string
)

func main() {
	klog.InitFlags(nil)

	repoWatchName = os.Getenv("REPOWATCH_NAME")
	namespace = os.Getenv("NAMESPACE")

	rootCmd := &cobra.Command{
		Use:   "overseer-cli",
		Short: "CLI for Overseer to manage sandboxes and tasks",
	}

	rootCmd.AddCommand(buildIssueCommand())
	rootCmd.AddCommand(buildPRCommand())
	rootCmd.AddCommand(buildChoreCommand())

	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

func buildIssueCommand() *cobra.Command {
	var number int
	var prNumber int
	var taskType string

	cmd := &cobra.Command{
		Use:   "issue",
		Short: "Create/ensure sandbox and task for an issue",
		RunE: func(_ *cobra.Command, _ []string) error {
			return runIssue(context.Background(), number, prNumber, taskType)
		},
	}

	cmd.Flags().IntVar(&number, "number", 0, "Issue number")
	cmd.Flags().IntVar(&prNumber, "pr", 0, "PR number to extract issue from")
	cmd.Flags().StringVar(&taskType, "task", "fix-issue", "Task type (e.g., fix-issue, triage-issue, investigate-failures)")

	return cmd
}

func buildPRCommand() *cobra.Command {
	var number int
	var taskType string
	var submit bool

	cmd := &cobra.Command{
		Use:   "pr",
		Short: "Create/ensure sandbox and task for a PR",
		RunE: func(_ *cobra.Command, _ []string) error {
			return runPR(context.Background(), number, taskType, submit)
		},
	}

	cmd.Flags().IntVar(&number, "number", 0, "PR number")
	cmd.Flags().StringVar(&taskType, "task", "review", "Task type (e.g., review, address-feedback)")
	cmd.Flags().BoolVar(&submit, "submit", false, "Submit agent draft from task as review")
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

type ChoreDefinition struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Schedule    string `json:"schedule"`
	Prompt      string `json:"-"`
}

func runChore(ctx context.Context, name string, file string) error {
	if repoWatchName == "" || namespace == "" {
		return fmt.Errorf("REPOWATCH_NAME and NAMESPACE environment variables must be set")
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

	choresMode := os.Getenv("CHORES_MODE")
	if choresMode == "dryrun" {
		fmt.Printf("[dryrun] Would create sandbox and task chore for chore %s in RepoWatch %s\n", chore.Name, repoWatchName)
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

	rwUnstructured, err := manager.GetRepoWatch(ctx, namespace, repoWatchName)
	if err != nil {
		return fmt.Errorf("failed to get RepoWatch %s: %w", repoWatchName, err)
	}

	var repoWatch reviewv1alpha1.RepoWatch
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(rwUnstructured.Object, &repoWatch); err != nil {
		return fmt.Errorf("failed to convert RepoWatch: %w", err)
	}

	sandboxName := fmt.Sprintf("chore-%s-%s", repoWatch.Name, slugify(chore.Name))

	owner, repo, err := parseRepoURL(repoWatch.Spec.RepoURL)
	if err != nil {
		return fmt.Errorf("failed to parse RepoURL: %w", err)
	}

	// Check if sandbox exists
	_, err = kubeClient.DynamicClient.Resource(k8s.SandboxGVR).Namespace(namespace).Get(ctx, sandboxName, metav1.GetOptions{})
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			// Create Sandbox
			fmt.Printf("Creating sandbox %s...\n", sandboxName)
			if err := createChoreSandbox(ctx, kubeClient, &repoWatch, chore, sandboxName); err != nil {
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
		"CLONE_URL":    repoWatch.Spec.RepoURL,
	}

	// Add other params from repoWatch if applicable
	if repoWatch.Spec.Issue != nil {
		if repoWatch.Spec.Issue.LLM.Provider != "" {
			params["AGENT_LLM_PROVIDER"] = repoWatch.Spec.Issue.LLM.Provider
		}
		if repoWatch.Spec.Issue.LLM.APIKeySecretRef != "" {
			params["AGENT_LLM_API_KEY_SECRET"] = repoWatch.Spec.Issue.LLM.APIKeySecretRef
		}
		if repoWatch.Spec.Issue.LLM.ConfigdirRef != "" {
			params["AGENT_LLM_CONFIGDIR"] = repoWatch.Spec.Issue.LLM.ConfigdirRef
		}
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

func getInstallationName(repoWatch *reviewv1alpha1.RepoWatch) string {
	if repoWatch != nil && repoWatch.Spec.InstallationName != "" {
		return repoWatch.Spec.InstallationName
	}
	return os.Getenv("INSTALLATION_NAME")
}

func createChoreSandbox(ctx context.Context, kubeClient *clients.KubernetesClient, repoWatch *reviewv1alpha1.RepoWatch, chore *ChoreDefinition, sandboxName string) error {
	cloneURL := repoWatch.Spec.RepoURL
	if !strings.HasSuffix(cloneURL, ".git") {
		cloneURL += ".git"
	}

	_, repo, err := parseRepoURL(repoWatch.Spec.RepoURL)
	if err != nil {
		return fmt.Errorf("failed to parse RepoURL: %w", err)
	}

	userLogin := os.Getenv("GITHUB_USER_ID")
	userName := os.Getenv("GITHUB_USER_NAME")
	userEmail := os.Getenv("GITHUB_USER_EMAIL")

	apiKeySecretName := ""
	if repoWatch.Spec.Issue != nil {
		apiKeySecretName = repoWatch.Spec.Issue.LLM.APIKeySecretRef
	}
	if apiKeySecretName == "" {
		apiKeySecretName = "gemini-api-key"
	}

	opt := sandbox.AgentSandboxOptions{
		DevSandboxOptions: sandbox.DevSandboxOptions{
			Name:      sandboxName,
			Namespace: repoWatch.Namespace,
			Labels: map[string]string{
				"review.gemini.google.com/repowatch": repoWatch.Name,
				"sandbox.gemini.google.com/type":     "chore",
				"chore.gemini.google.com/name":       slugify(chore.Name),
			},
			CloneURL:            cloneURL,
			HTMLURL:             strings.TrimSuffix(repoWatch.Spec.RepoURL, ".git"),
			Branch:              "main", // Default branch for chores
			Origin:              fmt.Sprintf("github.com/%s/%s", userLogin, repo),
			PushEnabled:         true,
			UserLogin:           userLogin,
			UserName:            userName,
			UserEmail:           userEmail,
			LLMAPIKeySecretName: apiKeySecretName,
			GithubSecretName:    repoWatch.Spec.GithubSecretName,
			RepoSandboxImage:    os.Getenv("REPO_SANDBOX_IMAGE"),
			ConfigDirImage:      os.Getenv("CONFIG_DIR_IMAGE"),
			InstallationName:    getInstallationName(repoWatch),
			HTTPEnabled:         true,
			Replicas:            1,
			ServiceAccountName:  "issue-sandbox",
		},
		IssueRepo:      repo,
		SkipDevcPrefix: true,
	}

	if repoWatch.Spec.Issue != nil {
		opt.LLMProvider = repoWatch.Spec.Issue.LLM.Provider
		opt.LLMConfigdirRef = repoWatch.Spec.Issue.LLM.ConfigdirRef
		opt.Image = repoWatch.Spec.Issue.Image
		opt.DevcontainerConfigRef = repoWatch.Spec.Issue.DevcontainerConfigRef
	}

	sb, svc := sandbox.NewAgentSandbox(opt)
	sb.SetName(sandboxName)

	_, err = kubeClient.DynamicClient.Resource(k8s.SandboxGVR).Namespace(repoWatch.Namespace).Create(ctx, sb, metav1.CreateOptions{})
	if err != nil {
		return err
	}

	_, err = kubeClient.Clientset.CoreV1().Services(repoWatch.Namespace).Create(ctx, svc, metav1.CreateOptions{})
	return err
}

func runIssue(ctx context.Context, number int, prNumber int, taskType string) error {
	if repoWatchName == "" || namespace == "" {
		return fmt.Errorf("REPOWATCH_NAME and NAMESPACE environment variables must be set")
	}

	repoMode := os.Getenv("REPO_MODE")
	if repoMode == "dryrun" {
		if number != 0 {
			fmt.Printf("[dryrun] Would create/ensure sandbox and task %s for issue %d in RepoWatch %s\n", taskType, number, repoWatchName)
		} else if prNumber != 0 {
			fmt.Printf("[dryrun] Would create/ensure sandbox and task %s for issue from PR %d in RepoWatch %s\n", taskType, prNumber, repoWatchName)
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

	rwUnstructured, err := manager.GetRepoWatch(ctx, namespace, repoWatchName)
	if err != nil {
		return fmt.Errorf("failed to get RepoWatch %s: %w", repoWatchName, err)
	}

	var repoWatch reviewv1alpha1.RepoWatch
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(rwUnstructured.Object, &repoWatch); err != nil {
		return fmt.Errorf("failed to convert RepoWatch: %w", err)
	}

	ghClient, err := github.NewClient(ctx)
	if err != nil {
		return fmt.Errorf("failed to create github client: %w", err)
	}

	owner, repo, err := parseRepoURL(repoWatch.Spec.RepoURL)
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

	sandboxName := fmt.Sprintf("devc-%s-issue-%d", repoWatch.Name, number)

	// Check if sandbox exists
	_, err = kubeClient.DynamicClient.Resource(k8s.SandboxGVR).Namespace(namespace).Get(ctx, sandboxName, metav1.GetOptions{})
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			// Create Sandbox
			fmt.Printf("Creating sandbox %s...\n", sandboxName)
			if err := createIssueSandbox(ctx, kubeClient, &repoWatch, issue); err != nil {
				return fmt.Errorf("failed to create issue sandbox: %w", err)
			}
		} else {
			return fmt.Errorf("failed to check if sandbox exists: %w", err)
		}
	}

	// Create Task
	fmt.Printf("Creating task %s for sandbox %s...\n", taskType, sandboxName)
	params := map[string]string{
		"ISSUE_URL":    issue.GetHTMLURL(),
		"AGENT_PROMPT": repoWatch.Spec.Issue.LLM.Prompt,
	}
	// Add other params if needed, similar to repowatch_controller.go
	if repoWatch.Spec.Issue.LLM.Provider != "" {
		params["AGENT_LLM_PROVIDER"] = repoWatch.Spec.Issue.LLM.Provider
	}
	if repoWatch.Spec.Issue.LLM.APIKeySecretRef != "" {
		params["AGENT_LLM_API_KEY_SECRET"] = repoWatch.Spec.Issue.LLM.APIKeySecretRef
	}
	if repoWatch.Spec.Issue.LLM.ConfigdirRef != "" {
		params["AGENT_LLM_CONFIGDIR"] = repoWatch.Spec.Issue.LLM.ConfigdirRef
	}
	if len(repoWatch.Spec.Issue.Models) > 0 {
		params["model"] = strings.Join(repoWatch.Spec.Issue.Models, ",")
	}

	err = manager.CreateSandboxTask(ctx, namespace, sandboxName, "Sandbox", taskType, params)
	if err != nil {
		return fmt.Errorf("failed to create sandbox task: %w", err)
	}

	fmt.Println("Done.")
	return nil
}

func runPR(ctx context.Context, number int, taskType string, submit bool) error {
	// Similar to runIssue but for PRs
	if repoWatchName == "" || namespace == "" {
		return fmt.Errorf("REPOWATCH_NAME and NAMESPACE environment variables must be set")
	}

	repoMode := os.Getenv("REPO_MODE")
	if repoMode == "dryrun" {
		fmt.Printf("[dryrun] Would create/ensure sandbox and task %s for PR %d in RepoWatch %s\n", taskType, number, repoWatchName)
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
		return submitAgentDraft(ctx, manager, kubeClient, namespace, repoWatchName, number)
	}

	rwUnstructured, err := manager.GetRepoWatch(ctx, namespace, repoWatchName)
	if err != nil {
		return fmt.Errorf("failed to get RepoWatch %s: %w", repoWatchName, err)
	}

	var repoWatch reviewv1alpha1.RepoWatch
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(rwUnstructured.Object, &repoWatch); err != nil {
		return fmt.Errorf("failed to convert RepoWatch: %w", err)
	}

	ghClient, err := github.NewClient(ctx)
	if err != nil {
		return fmt.Errorf("failed to create github client: %w", err)
	}

	owner, repo, err := parseRepoURL(repoWatch.Spec.RepoURL)
	if err != nil {
		return fmt.Errorf("failed to parse RepoURL: %w", err)
	}

	pr, _, err := ghClient.PullRequests.Get(ctx, owner, repo, number)
	if err != nil {
		return fmt.Errorf("failed to get PR %d: %w", number, err)
	}

	sandboxName := fmt.Sprintf("%s-pr-%d", repoWatch.Name, number)

	// Check if sandbox exists
	_, err = kubeClient.DynamicClient.Resource(k8s.SandboxGVR).Namespace(namespace).Get(ctx, sandboxName, metav1.GetOptions{})
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			// Create Sandbox
			fmt.Printf("Creating sandbox %s...\n", sandboxName)
			if err := createPRSandbox(ctx, kubeClient, &repoWatch, pr); err != nil {
				return fmt.Errorf("failed to create PR sandbox: %w", err)
			}
		} else {
			return fmt.Errorf("failed to check if sandbox exists: %w", err)
		}
	}

	// Create Task
	fmt.Printf("Creating task %s for sandbox %s...\n", taskType, sandboxName)
	params := map[string]string{
		"PULL_REQUEST_ID": fmt.Sprintf("%d", number),
		"AGENT_PROMPT":    repoWatch.Spec.Review.LLM.Prompt,
	}
	if repoWatch.Spec.Review.LLM.Provider != "" {
		params["AGENT_LLM_PROVIDER"] = repoWatch.Spec.Review.LLM.Provider
	}
	if repoWatch.Spec.Review.LLM.APIKeySecretRef != "" {
		params["AGENT_LLM_API_KEY_SECRET"] = repoWatch.Spec.Review.LLM.APIKeySecretRef
	}
	if repoWatch.Spec.Review.LLM.ConfigdirRef != "" {
		params["AGENT_LLM_CONFIGDIR"] = repoWatch.Spec.Review.LLM.ConfigdirRef
	}

	err = manager.CreateSandboxTask(ctx, namespace, sandboxName, "Sandbox", taskType, params)
	if err != nil {
		return fmt.Errorf("failed to create sandbox task: %w", err)
	}

	fmt.Println("Done.")
	return nil
}

func submitAgentDraft(ctx context.Context, manager *k8s.Manager, kubeClient *clients.KubernetesClient, namespace, repoWatchName string, prNumber int) error {
	rwUnstructured, err := manager.GetRepoWatch(ctx, namespace, repoWatchName)
	if err != nil {
		return fmt.Errorf("failed to get RepoWatch %s: %w", repoWatchName, err)
	}

	sandboxName := fmt.Sprintf("%s-pr-%d", repoWatchName, prNumber)

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

	// Get GitHub token from secret
	token, err := manager.GetGitHubToken(ctx, rwUnstructured)
	if err != nil {
		return fmt.Errorf("failed to get github token: %w", err)
	}

	// Create GitHub client
	client := clients.NewGitHubClient(ctx, token)

	// Parse repo URL
	repoURL, found, err := unstructured.NestedString(rwUnstructured.Object, "spec", "repoURL")
	if err != nil || !found {
		return fmt.Errorf("repoURL not found in RepoWatch %s", repoWatchName)
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

	// Not setting event sets it as a draft
	reviewRequest.Event = nil

	if os.Getenv("ENABLE_TRACEABILITY_METADATA") == "true" {
		installationName, _, _ := unstructured.NestedString(rwUnstructured.Object, "spec", "installationName")
		if installationName == "" {
			installationName = os.Getenv("INSTALLATION_NAME")
		}

		metadata := github.TraceabilityMetadata{
			Enabled:          true,
			Sandbox:          sandboxName,
			RepoWatch:        repoWatchName,
			TaskType:         "pr-review",
			InstallationName: installationName,
		}
		if reviewRequest.Body != nil {
			body := *reviewRequest.Body + metadata.FormatHTMLComment()
			reviewRequest.Body = &body
		}
	}

	fmt.Printf("Creating review on GitHub for %s/%s PR %d...\n", owner, repoName, prNumber)
	review, _, err := client.PullRequests.CreateReview(ctx, owner, repoName, prNumber, reviewRequest)
	if err != nil {
		return fmt.Errorf("failed to create review on GitHub: %w", err)
	}
	fmt.Printf("Successfully created review: %s\n", review.GetHTMLURL())

	// Update sandbox reviewState
	if err := manager.UpdateSandboxAnnotation(ctx, namespace, sandboxName, "reviewState", "submitted"); err != nil {
		fmt.Printf("Warning: failed to update reviewState annotation: %v\n", err)
	}

	// scale down sandbox
	fmt.Printf("Scaling down sandbox %s...\n", sandboxName)
	err = manager.ScaledownSandbox(ctx, namespace, repoWatchName, fmt.Sprintf("%d", prNumber))
	if err != nil {
		fmt.Printf("Warning: failed to scaledown sandbox: %v\n", err)
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

func createIssueSandbox(ctx context.Context, kubeClient *clients.KubernetesClient, repoWatch *reviewv1alpha1.RepoWatch, issue *githubv39.Issue) error {
	// Replicate logic from repowatch_controller.go:createIssueSandbox
	name := fmt.Sprintf("%s-issue-%d", repoWatch.Name, issue.GetNumber())
	cloneURL := strings.Replace(issue.GetRepositoryURL(), "api.github.com/repos", "github.com", 1) + ".git"

	// We need to fetch user info. In Overseer, we might just use env vars.
	userLogin := os.Getenv("GITHUB_USER_ID")
	userName := os.Getenv("GITHUB_USER_NAME")
	userEmail := os.Getenv("GITHUB_USER_EMAIL")

	branchName := fmt.Sprintf("issue-%d-%s", issue.GetNumber(), randString(4))

	apiKeySecretName := repoWatch.Spec.Issue.LLM.APIKeySecretRef
	if apiKeySecretName == "" {
		apiKeySecretName = "gemini-api-key"
	}

	opt := sandbox.AgentSandboxOptions{
		DevSandboxOptions: sandbox.DevSandboxOptions{
			Name:      name,
			Namespace: repoWatch.Namespace,
			Labels: map[string]string{
				"review.gemini.google.com/repowatch": repoWatch.Name,
				"sandbox.gemini.google.com/type":     "issue",
			},
			CloneURL:              cloneURL,
			HTMLURL:               issue.GetHTMLURL(),
			Branch:                branchName,
			Origin:                fmt.Sprintf("github.com/%s/%s", userLogin, repoWatch.Name), // simplified
			PushEnabled:           false,
			UserLogin:             userLogin,
			UserName:              userName,
			UserEmail:             userEmail,
			LLMProvider:           repoWatch.Spec.Issue.LLM.Provider,
			LLMConfigdirRef:       repoWatch.Spec.Issue.LLM.ConfigdirRef,
			LLMAPIKeySecretName:   apiKeySecretName,
			Prompt:                repoWatch.Spec.Issue.LLM.Prompt,
			GithubSecretName:      repoWatch.Spec.GithubSecretName,
			DevcontainerConfigRef: repoWatch.Spec.Issue.DevcontainerConfigRef,
			Image:                 repoWatch.Spec.Issue.Image,
			RepoSandboxImage:      os.Getenv("REPO_SANDBOX_IMAGE"),
			ConfigDirImage:        os.Getenv("CONFIG_DIR_IMAGE"),
			InstallationName:      getInstallationName(repoWatch),
			HTTPEnabled:           true,
			Replicas:              1,
			ServiceAccountName:    "issue-sandbox",
		},
		IssueID:    fmt.Sprintf("%d", issue.GetNumber()),
		IssueTitle: issue.GetTitle(),
		IssueRepo:  repoWatch.Name,
	}

	sb, svc := sandbox.NewAgentSandbox(opt)

	_, err := kubeClient.DynamicClient.Resource(k8s.SandboxGVR).Namespace(repoWatch.Namespace).Create(ctx, sb, metav1.CreateOptions{})
	if err != nil {
		return err
	}

	_, err = kubeClient.Clientset.CoreV1().Services(repoWatch.Namespace).Create(ctx, svc, metav1.CreateOptions{})
	return err
}

func createPRSandbox(ctx context.Context, kubeClient *clients.KubernetesClient, repoWatch *reviewv1alpha1.RepoWatch, pr *githubv39.PullRequest) error {
	// Replicate logic from repowatch_controller.go:createPRSandbox
	name := fmt.Sprintf("%s-pr-%d", repoWatch.Name, pr.GetNumber())
	cloneURL := pr.GetBase().GetRepo().GetCloneURL()

	userLogin := os.Getenv("GITHUB_USER_ID")
	userName := os.Getenv("GITHUB_USER_NAME")
	userEmail := os.Getenv("GITHUB_USER_EMAIL")

	apiKeySecretName := repoWatch.Spec.Review.LLM.APIKeySecretRef
	if apiKeySecretName == "" {
		apiKeySecretName = "gemini-api-key"
	}

	opt := sandbox.AgentSandboxOptions{
		DevSandboxOptions: sandbox.DevSandboxOptions{
			Name:      name,
			Namespace: repoWatch.Namespace,
			Labels: map[string]string{
				"review.gemini.google.com/repowatch": repoWatch.Name,
				"sandbox.gemini.google.com/type":     "review",
			},
			CloneURL:              cloneURL,
			HTMLURL:               pr.GetHTMLURL(),
			Branch:                pr.GetHead().GetRef(),
			Origin:                pr.GetHead().GetRepo().GetCloneURL(),
			PushEnabled:           false,
			UserLogin:             userLogin,
			UserName:              userName,
			UserEmail:             userEmail,
			LLMProvider:           repoWatch.Spec.Review.LLM.Provider,
			LLMConfigdirRef:       repoWatch.Spec.Review.LLM.ConfigdirRef,
			LLMAPIKeySecretName:   apiKeySecretName,
			Prompt:                repoWatch.Spec.Review.LLM.Prompt,
			GithubSecretName:      repoWatch.Spec.GithubSecretName,
			DevcontainerConfigRef: repoWatch.Spec.Review.DevcontainerConfigRef,
			Image:                 repoWatch.Spec.Review.Image,
			RepoSandboxImage:      os.Getenv("REPO_SANDBOX_IMAGE"),
			ConfigDirImage:        os.Getenv("CONFIG_DIR_IMAGE"),
			InstallationName:      getInstallationName(repoWatch),
			HTTPEnabled:           true,
			Replicas:              1,
			ServiceAccountName:    "review-sandbox",
		},
		IssueID:        fmt.Sprintf("%d", pr.GetNumber()),
		IssueTitle:     pr.GetTitle(),
		IssueRepo:      repoWatch.Name,
		SkipDevcPrefix: true,
	}

	sb, svc := sandbox.NewAgentSandbox(opt)
	sb.SetName(name) // Resource name should not have devc- prefix for PRs to match controller

	_, err := kubeClient.DynamicClient.Resource(k8s.SandboxGVR).Namespace(repoWatch.Namespace).Create(ctx, sb, metav1.CreateOptions{})
	if err != nil {
		return err
	}

	_, err = kubeClient.Clientset.CoreV1().Services(repoWatch.Namespace).Create(ctx, svc, metav1.CreateOptions{})
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
