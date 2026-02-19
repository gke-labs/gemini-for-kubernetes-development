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
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/klog/v2"
	"sigs.k8s.io/controller-runtime/pkg/client/config"

	reviewv1alpha1 "github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/api/repowatch/v1alpha1"
	sandboxtaskv1alpha1 "github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/api/sandboxtask/v1alpha1"
	pkgapi "github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/api"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/clients"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/github"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/k8s"
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
			if submit {
				return runSubmitReview(context.Background(), number)
			}
			return runPR(context.Background(), number, taskType)
		},
	}

	cmd.Flags().IntVar(&number, "number", 0, "PR number")
	cmd.Flags().StringVar(&taskType, "task", "review", "Task type (e.g., review, address-feedback)")
	cmd.Flags().BoolVar(&submit, "submit", false, "Submit the agent draft as review")
	_ = cmd.MarkFlagRequired("number")

	return cmd
}

func runSubmitReview(ctx context.Context, number int) error {
	if repoWatchName == "" || namespace == "" {
		return fmt.Errorf("REPOWATCH_NAME and NAMESPACE environment variables must be set")
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

	sandboxName := fmt.Sprintf("%s-pr-%d", repoWatchName, number)

	// List Sandbox Tasks to find the latest review task
	tasks, err := manager.ListSandboxTasks(ctx, namespace, sandboxName)
	if err != nil {
		return fmt.Errorf("failed to list tasks for sandbox %s: %w", sandboxName, err)
	}

	var latestReviewTask *sandboxtaskv1alpha1.SandboxTask
	for i := range tasks.Items {
		task := &tasks.Items[i]
		if task.Spec.Type == "review" {
			if latestReviewTask == nil || task.CreationTimestamp.After(latestReviewTask.CreationTimestamp.Time) {
				latestReviewTask = task
			}
		}
	}

	if latestReviewTask == nil {
		return fmt.Errorf("no review task found for sandbox %s", sandboxName)
	}

	draft := latestReviewTask.GetAnnotations()["agentDraft"]
	if draft == "" {
		return fmt.Errorf("no agentDraft found in task %s", latestReviewTask.Name)
	}

	fmt.Printf("Submitting agent draft from task %s as review for PR %d...\n", latestReviewTask.Name, number)
	err = pkgapi.SubmitReviewDraft(ctx, manager, namespace, repoWatchName, fmt.Sprintf("%d", number), draft)
	if err != nil {
		return fmt.Errorf("failed to submit review: %w", err)
	}

	fmt.Println("Done.")
	return nil
}

func runIssue(ctx context.Context, number int, taskType string) error {
	if repoWatchName == "" || namespace == "" {
		return fmt.Errorf("REPOWATCH_NAME and NAMESPACE environment variables must be set")
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

func runPR(ctx context.Context, number int, taskType string) error {
	// Similar to runIssue but for PRs
	if repoWatchName == "" || namespace == "" {
		return fmt.Errorf("REPOWATCH_NAME and NAMESPACE environment variables must be set")
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
			HTTPEnabled:           true,
			Replicas:              1,
			ServiceAccountName:    "review-sandbox",
		},
		IssueID:    fmt.Sprintf("%d", pr.GetNumber()),
		IssueTitle: pr.GetTitle(),
		IssueRepo:  repoWatch.Name,
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
