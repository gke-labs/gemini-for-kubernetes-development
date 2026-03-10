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
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/klog/v2"
	"sigs.k8s.io/controller-runtime/pkg/client/config"
	"sigs.k8s.io/yaml"

	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/clients"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/github"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/k8s"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/models"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/sandbox"
	githubv39 "github.com/google/go-github/v39/github"
)

var (
	repoWatchName string
	overseerName  string
	namespace     string
)

func main() {
	klog.InitFlags(nil)

	repoWatchName = os.Getenv("REPOWATCH_NAME")
	overseerName = os.Getenv("OVERSEER_NAME")
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
	cmd.Flags().StringVar(&taskType, "task", "fix-issue", "Task type (e.g., fix-issue, triage-issue)")

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
	cmd.Flags().StringVar(&taskType, "task", "review", "Task type (e.g., review, address-feedback, investigate-failures)")
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

func getManager() (*k8s.Manager, *clients.KubernetesClient, error) {
	cfg, err := config.GetConfig()
	if err != nil {
		return nil, nil, fmt.Errorf("unable to get kubeconfig: %w", err)
	}

	dynClient, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return nil, nil, fmt.Errorf("unable to create dynamic client: %w", err)
	}

	clientset, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, nil, fmt.Errorf("unable to create clientset: %w", err)
	}

	kubeClient := &clients.KubernetesClient{
		DynamicClient: dynClient,
		Clientset:     clientset,
	}
	return k8s.NewManager(kubeClient), kubeClient, nil
}

type RepoConfig struct {
	Name             string
	Namespace        string
	RepoURL          string
	GithubSecretName string
	RobotAccount     string
	MaxActiveIssues  *int32
	MaxActiveReviews *int32
}

func getRepoConfig(ctx context.Context, manager *k8s.Manager) (*RepoConfig, error) {
	if overseerName != "" {
		u, err := manager.GetOverseer(ctx, overseerName)
		if err != nil {
			return nil, fmt.Errorf("failed to get Overseer %s: %w", overseerName, err)
		}

		repoURL, _, _ := unstructured.NestedString(u.Object, "spec", "repoURL")
		githubSecretName, _, _ := unstructured.NestedString(u.Object, "spec", "githubSecretName")
		robotAccount, _, _ := unstructured.NestedString(u.Object, "spec", "robotAccount")

		var maxActiveIssues *int32
		if val, found, _ := unstructured.NestedInt64(u.Object, "spec", "maxActiveIssues"); found {
			v := int32(val)
			maxActiveIssues = &v
		}

		var maxActiveReviews *int32
		if val, found, _ := unstructured.NestedInt64(u.Object, "spec", "maxActiveReviews"); found {
			v := int32(val)
			maxActiveReviews = &v
		}

		return &RepoConfig{
			Name:             overseerName,
			Namespace:        namespace,
			RepoURL:          repoURL,
			GithubSecretName: githubSecretName,
			RobotAccount:     robotAccount,
			MaxActiveIssues:  maxActiveIssues,
			MaxActiveReviews: maxActiveReviews,
		}, nil
	}

	if repoWatchName != "" && namespace != "" {
		u, err := manager.GetRepoWatch(ctx, namespace, repoWatchName)
		if err != nil {
			return nil, fmt.Errorf("failed to get RepoWatch %s: %w", repoWatchName, err)
		}

		repoURL, _, _ := unstructured.NestedString(u.Object, "spec", "repoURL")
		githubSecretName, _, _ := unstructured.NestedString(u.Object, "spec", "githubSecretName")
		robotAccount, _, _ := unstructured.NestedString(u.Object, "spec", "overseer", "robotAccount")

		var maxActiveIssues *int32
		if val, found, _ := unstructured.NestedInt64(u.Object, "spec", "overseer", "maxActiveIssues"); found {
			v := int32(val)
			maxActiveIssues = &v
		}

		var maxActiveReviews *int32
		if val, found, _ := unstructured.NestedInt64(u.Object, "spec", "overseer", "maxActiveReviews"); found {
			v := int32(val)
			maxActiveReviews = &v
		}

		return &RepoConfig{
			Name:             repoWatchName,
			Namespace:        namespace,
			RepoURL:          repoURL,
			GithubSecretName: githubSecretName,
			RobotAccount:     robotAccount,
			MaxActiveIssues:  maxActiveIssues,
			MaxActiveReviews: maxActiveReviews,
		}, nil
	}

	return nil, fmt.Errorf("either OVERSEER_NAME or (REPOWATCH_NAME and NAMESPACE) environment variables must be set")
}

func runChore(ctx context.Context, name string, file string) error {
	manager, kubeClient, err := getManager()
	if err != nil {
		return err
	}

	repoConfig, err := getRepoConfig(ctx, manager)
	if err != nil {
		return err
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
		fmt.Printf("[dryrun] Would create sandbox and task chore for chore %s in %s\n", chore.Name, repoConfig.Name)
		return nil
	}

	sandboxName := fmt.Sprintf("devc-%s-chore-%s", repoConfig.Name, slugify(chore.Name))
	var sandboxExists bool
	_, err = kubeClient.DynamicClient.Resource(k8s.SandboxGVR).Namespace(namespace).Get(ctx, sandboxName, metav1.GetOptions{})
	if err == nil {
		sandboxExists = true
	} else if !strings.Contains(err.Error(), "not found") {
		return fmt.Errorf("failed to check if sandbox exists: %w", err)
	}

	if !sandboxExists {
		fmt.Printf("Creating sandbox %s...\n", sandboxName)
		if err := createChoreSandbox(ctx, kubeClient, repoConfig, chore, sandboxName); err != nil {
			return fmt.Errorf("failed to create sandbox: %w", err)
		}
	}

	taskName := fmt.Sprintf("%s-chore", sandboxName)
	fmt.Printf("Ensuring task %s...\n", taskName)
	params := map[string]string{
		"AGENT_PROMPT": chore.Prompt,
	}

	err = manager.CreateSandboxTask(ctx, namespace, sandboxName, "Sandbox", "chore", params)
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

func createChoreSandbox(ctx context.Context, kubeClient *clients.KubernetesClient, repoConfig *RepoConfig, chore *ChoreDefinition, sandboxName string) error {
	cloneURL := repoConfig.RepoURL
	if !strings.HasSuffix(cloneURL, ".git") {
		cloneURL += ".git"
	}

	_, repo, err := parseRepoURL(repoConfig.RepoURL)
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

	apiKeySecretName := "gemini-api-key"
	githubSecretName := repoConfig.GithubSecretName

	opt := sandbox.AgentSandboxOptions{
		DevSandboxOptions: sandbox.DevSandboxOptions{
			Name:      sandboxName,
			Namespace: namespace,
			Labels: map[string]string{
				"overseer.gemini.google.com/overseer": repoConfig.Name,
				"sandbox.gemini.google.com/type":      "chore",
				"chore.gemini.google.com/name":        slugify(chore.Name),
			},
			CloneURL:            cloneURL,
			HTMLURL:             strings.TrimSuffix(repoConfig.RepoURL, ".git"),
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
			RepoSandboxImage:    os.Getenv("REPO_SANDBOX_IMAGE"),
			ConfigDirImage:      os.Getenv("CONFIG_DIR_IMAGE"),
			HTTPEnabled:         true,
			Replicas:            1,
			ServiceAccountName:  "issue-sandbox",
		},
		IssueRepo:      repo,
		SkipDevcPrefix: true,
	}

	sb, svc := sandbox.NewAgentSandbox(opt)
	sb.SetName(sandboxName)

	_, err = kubeClient.DynamicClient.Resource(k8s.SandboxGVR).Namespace(namespace).Create(ctx, sb, metav1.CreateOptions{})
	if err != nil {
		return err
	}

	_, err = kubeClient.Clientset.CoreV1().Services(namespace).Create(ctx, svc, metav1.CreateOptions{})
	return err
}

func runIssue(ctx context.Context, number int, prNumber int, taskType string) error {
	manager, kubeClient, err := getManager()
	if err != nil {
		return err
	}

	repoConfig, err := getRepoConfig(ctx, manager)
	if err != nil {
		return err
	}

	repoMode := os.Getenv("REPO_MODE")
	if repoMode == "dryrun" {
		if number != 0 {
			fmt.Printf("[dryrun] Would create/ensure sandbox and task %s for issue %d in %s\n", taskType, number, repoConfig.Name)
		} else if prNumber != 0 {
			fmt.Printf("[dryrun] Would create/ensure sandbox and task %s for issue from PR %d in %s\n", taskType, prNumber, repoConfig.Name)
		}
		return nil
	}

	ghClient, err := github.NewClient(ctx)
	if err != nil {
		return fmt.Errorf("failed to create github client: %w", err)
	}

	owner, repo, err := parseRepoURL(repoConfig.RepoURL)
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

	sandboxName := fmt.Sprintf("devc-%s-issue-%d", repoConfig.Name, number)

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
	if !sandboxIsActive && repoConfig.MaxActiveIssues != nil {
		maxIssues := *repoConfig.MaxActiveIssues
		activeCount, err := countActiveSandboxes(ctx, kubeClient.DynamicClient, namespace, repoConfig.Name, "issue")
		if err != nil {
			return fmt.Errorf("failed to count active issue sandboxes: %w", err)
		}
		if int32(activeCount) >= maxIssues {
			return fmt.Errorf("limit_reached: max active issues limit (%d) reached (currently %d active)", maxIssues, activeCount)
		}
	}

	if !sandboxExists {
		fmt.Printf("Creating sandbox %s...\n", sandboxName)
		if err := createIssueSandbox(ctx, kubeClient, repoConfig, issue, sandboxName); err != nil {
			return fmt.Errorf("failed to create sandbox: %w", err)
		}
	} else if !sandboxIsActive {
		fmt.Printf("Activating sandbox %s...\n", sandboxName)
		// Scale up
		sandboxUpdate := &unstructured.Unstructured{
			Object: map[string]interface{}{
				"apiVersion": "agents.x-k8s.io/v1alpha1",
				"kind":       "Sandbox",
				"metadata": map[string]interface{}{
					"name":      sandboxName,
					"namespace": namespace,
				},
				"spec": map[string]interface{}{
					"replicas": int64(1),
				},
			},
		}
		_, err = kubeClient.DynamicClient.Resource(k8s.SandboxGVR).Namespace(namespace).Apply(ctx, sandboxName, sandboxUpdate, metav1.ApplyOptions{FieldManager: "overseer-cli", Force: true})
		if err != nil {
			return fmt.Errorf("failed to scale up sandbox: %w", err)
		}
	}

	taskName := fmt.Sprintf("%s-%s", sandboxName, taskType)
	fmt.Printf("Ensuring task %s...\n", taskName)
	return ensureTask(ctx, kubeClient, repoConfig, sandboxName, taskName, taskType)
}

func runPR(ctx context.Context, number int, taskType string, submit bool) error {
	manager, kubeClient, err := getManager()
	if err != nil {
		return err
	}

	repoConfig, err := getRepoConfig(ctx, manager)
	if err != nil {
		return err
	}

	repoMode := os.Getenv("REPO_MODE")
	if repoMode == "dryrun" {
		fmt.Printf("[dryrun] Would create/ensure sandbox and task %s for PR %d in %s\n", taskType, number, repoConfig.Name)
		return nil
	}

	if submit {
		return submitAgentDraft(ctx, manager, kubeClient, repoConfig, number)
	}

	ghClient, err := github.NewClient(ctx)
	if err != nil {
		return fmt.Errorf("failed to create github client: %w", err)
	}

	owner, repo, err := parseRepoURL(repoConfig.RepoURL)
	if err != nil {
		return fmt.Errorf("failed to parse RepoURL: %w", err)
	}

	pr, _, err := ghClient.PullRequests.Get(ctx, owner, repo, number)
	if err != nil {
		return fmt.Errorf("failed to get PR %d: %w", number, err)
	}

	sandboxName := fmt.Sprintf("devc-%s-pr-%d", repoConfig.Name, number)

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
	if !sandboxIsActive && repoConfig.MaxActiveReviews != nil {
		maxReviews := *repoConfig.MaxActiveReviews
		activeCount, err := countActiveSandboxes(ctx, kubeClient.DynamicClient, namespace, repoConfig.Name, "review")
		if err != nil {
			return fmt.Errorf("failed to count active review sandboxes: %w", err)
		}
		if int32(activeCount) >= maxReviews {
			return fmt.Errorf("limit_reached: max active reviews limit (%d) reached (currently %d active)", maxReviews, activeCount)
		}
	}

	if !sandboxExists {
		fmt.Printf("Creating sandbox %s...\n", sandboxName)
		if err := createPRSandbox(ctx, kubeClient, repoConfig, pr, sandboxName); err != nil {
			return fmt.Errorf("failed to create sandbox: %w", err)
		}
	} else if !sandboxIsActive {
		fmt.Printf("Activating sandbox %s...\n", sandboxName)
		// Scale up
		sandboxUpdate := &unstructured.Unstructured{
			Object: map[string]interface{}{
				"apiVersion": "agents.x-k8s.io/v1alpha1",
				"kind":       "Sandbox",
				"metadata": map[string]interface{}{
					"name":      sandboxName,
					"namespace": namespace,
				},
				"spec": map[string]interface{}{
					"replicas": int64(1),
				},
			},
		}
		_, err = kubeClient.DynamicClient.Resource(k8s.SandboxGVR).Namespace(namespace).Apply(ctx, sandboxName, sandboxUpdate, metav1.ApplyOptions{FieldManager: "overseer-cli", Force: true})
		if err != nil {
			return fmt.Errorf("failed to scale up sandbox: %w", err)
		}
	}

	taskName := fmt.Sprintf("%s-%s", sandboxName, taskType)
	fmt.Printf("Ensuring task %s...\n", taskName)
	return ensureTask(ctx, kubeClient, repoConfig, sandboxName, taskName, taskType)
}

func submitAgentDraft(ctx context.Context, manager *k8s.Manager, kubeClient *clients.KubernetesClient, repoConfig *RepoConfig, prNumber int) error {
	sandboxName := fmt.Sprintf("devc-%s-pr-%d", repoConfig.Name, prNumber)
	draft, err := manager.GetSandboxAnnotation(ctx, namespace, sandboxName, "sandbox.gemini.google.com/agent-draft")
	if err != nil {
		return fmt.Errorf("failed to get agent draft from sandbox %s: %w", sandboxName, err)
	}
	if draft == "" {
		return fmt.Errorf("no agent draft found in sandbox %s", sandboxName)
	}

	owner, repo, err := parseRepoURL(repoConfig.RepoURL)
	if err != nil {
		return fmt.Errorf("failed to parse RepoURL: %w", err)
	}

	ghClient, err := github.NewClient(ctx)
	if err != nil {
		return fmt.Errorf("failed to create github client: %w", err)
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

	fmt.Printf("Creating review on GitHub for %s/%s PR %d...\n", owner, repo, prNumber)
	review, _, err := ghClient.PullRequests.CreateReview(ctx, owner, repo, prNumber, reviewRequest)
	if err != nil {
		return fmt.Errorf("failed to create review on GitHub: %w", err)
	}
	fmt.Printf("Successfully created review: %s\n", review.GetHTMLURL())

	// Update sandbox reviewState
	if err := manager.UpdateSandboxAnnotation(ctx, namespace, sandboxName, "reviewState", "submitted"); err != nil {
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

func createIssueSandbox(ctx context.Context, kubeClient *clients.KubernetesClient, repoConfig *RepoConfig, issue *githubv39.Issue, sandboxName string) error {
	cloneURL := strings.Replace(issue.GetRepositoryURL(), "api.github.com/repos", "github.com", 1) + ".git"

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

	apiKeySecretName := "gemini-api-key"
	githubSecretName := repoConfig.GithubSecretName

	opt := sandbox.AgentSandboxOptions{
		DevSandboxOptions: sandbox.DevSandboxOptions{
			Name:      sandboxName,
			Namespace: namespace,
			Labels: map[string]string{
				"overseer.gemini.google.com/overseer": repoConfig.Name,
				"sandbox.gemini.google.com/type":      "issue",
			},
			CloneURL:            cloneURL,
			HTMLURL:             issue.GetHTMLURL(),
			Branch:              branchName,
			Origin:              fmt.Sprintf("github.com/%s/%s", userLogin, repoConfig.Name),
			PushEnabled:         false,
			UserLogin:           userLogin,
			UserName:            userName,
			UserEmail:           userEmail,
			BotLogin:            botLogin,
			BotName:             botName,
			BotEmail:            botEmail,
			LLMAPIKeySecretName: apiKeySecretName,
			GithubSecretName:    githubSecretName,
			RepoSandboxImage:    os.Getenv("REPO_SANDBOX_IMAGE"),
			ConfigDirImage:      os.Getenv("CONFIG_DIR_IMAGE"),
			HTTPEnabled:         true,
			Replicas:            1,
			ServiceAccountName:  "issue-sandbox",
		},
		IssueID:    fmt.Sprintf("%d", issue.GetNumber()),
		IssueTitle: issue.GetTitle(),
		IssueRepo:  repoConfig.Name,
	}

	sb, svc := sandbox.NewAgentSandbox(opt)

	_, err := kubeClient.DynamicClient.Resource(k8s.SandboxGVR).Namespace(namespace).Create(ctx, sb, metav1.CreateOptions{})
	if err != nil {
		return err
	}

	_, err = kubeClient.Clientset.CoreV1().Services(namespace).Create(ctx, svc, metav1.CreateOptions{})
	return err
}

func createPRSandbox(ctx context.Context, kubeClient *clients.KubernetesClient, repoConfig *RepoConfig, pr *githubv39.PullRequest, sandboxName string) error {
	userLogin := os.Getenv("GITHUB_USER_ID")
	userName := os.Getenv("GITHUB_USER_NAME")
	if userName == "" {
		userName = userLogin
	}
	userEmail := os.Getenv("GITHUB_USER_EMAIL")

	botLogin := os.Getenv("GITHUB_BOT_LOGIN")
	botName := os.Getenv("GITHUB_BOT_NAME")
	botEmail := os.Getenv("GITHUB_BOT_EMAIL")

	apiKeySecretName := "gemini-api-key"
	githubSecretName := repoConfig.GithubSecretName

	opt := sandbox.ReviewSandboxOptions{
		DevSandboxOptions: sandbox.DevSandboxOptions{
			Name:      sandboxName,
			Namespace: namespace,
			Labels: map[string]string{
				"overseer.gemini.google.com/overseer": repoConfig.Name,
				"sandbox.gemini.google.com/type":      "review",
			},
			UserLogin:           userLogin,
			UserName:            userName,
			UserEmail:           userEmail,
			BotLogin:            botLogin,
			BotName:             botName,
			BotEmail:            botEmail,
			LLMAPIKeySecretName: apiKeySecretName,
			GithubSecretName:    githubSecretName,
			RepoSandboxImage:    os.Getenv("REPO_SANDBOX_IMAGE"),
			ConfigDirImage:      os.Getenv("CONFIG_DIR_IMAGE"),
			HTTPEnabled:         true,
			Replicas:            1,
			ServiceAccountName:  "review-sandbox",
		},
		PRNumber:       pr.GetNumber(),
		PRTitle:        pr.GetTitle(),
		PRHTMLURL:      pr.GetHTMLURL(),
		PRDiffURL:      pr.GetDiffURL(),
		PRCloneURL:     fmt.Sprintf("%s#refs/heads/%s", pr.GetHead().GetRepo().GetCloneURL(), pr.GetHead().GetRef()),
		RepoName:       repoConfig.Name,
		SkipDevcPrefix: true,
	}

	sb, svc := sandbox.NewReviewSandbox(opt)

	_, err := kubeClient.DynamicClient.Resource(k8s.SandboxGVR).Namespace(namespace).Create(ctx, sb, metav1.CreateOptions{})
	if err != nil {
		return err
	}

	_, err = kubeClient.Clientset.CoreV1().Services(namespace).Create(ctx, svc, metav1.CreateOptions{})
	return err
}

func ensureTask(ctx context.Context, kubeClient *clients.KubernetesClient, repoConfig *RepoConfig, sandboxName, taskName, taskType string) error {
	manager := k8s.NewManager(kubeClient)

	// Check if task exists
	gvr := schema.GroupVersionResource{
		Group:    "custom.agents.x-k8s.io",
		Version:  "v1alpha1",
		Resource: "sandboxtasks",
	}
	_, err := kubeClient.DynamicClient.Resource(gvr).Namespace(namespace).Get(ctx, taskName, metav1.GetOptions{})
	if err == nil {
		fmt.Printf("Task %s already exists.\n", taskName)
		return nil
	} else if !strings.Contains(err.Error(), "not found") {
		return fmt.Errorf("failed to check if task exists: %w", err)
	}

	params := map[string]string{}
	if strings.Contains(taskName, "pr") {
		prNumber := strings.Split(strings.Split(taskName, "-pr-")[1], "-")[0]
		params["PULL_REQUEST_ID"] = prNumber
	} else if strings.Contains(taskName, "issue") {
		issueNumber := strings.Split(strings.Split(taskName, "-issue-")[1], "-")[0]
		params["ISSUE_ID"] = issueNumber
	}

	err = manager.CreateSandboxTask(ctx, namespace, sandboxName, "Sandbox", taskType, params)
	if err != nil {
		return fmt.Errorf("failed to create sandbox task: %w", err)
	}

	fmt.Printf("Successfully created task %s\n", taskName)
	return nil
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

	return prNumber, nil
}

func countActiveSandboxes(ctx context.Context, dynClient dynamic.Interface, namespace, resourceName, sandboxType string) (int, error) {
	labelSelector := ""
	if overseerName != "" {
		labelSelector = fmt.Sprintf("overseer.gemini.google.com/overseer=%s,sandbox.gemini.google.com/type=%s", resourceName, sandboxType)
	} else {
		labelSelector = fmt.Sprintf("review.gemini.google.com/repowatch=%s,sandbox.gemini.google.com/type=%s", resourceName, sandboxType)
	}

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
