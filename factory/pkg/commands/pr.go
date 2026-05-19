package commands

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/clients"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/envd"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/github"
	factorysandbox "github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/sandbox"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/tasks"
	"github.com/spf13/cobra"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func NewPRCommand(ctx context.Context) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "pr",
		Short: "Manage GitHub pull request workflows",
	}
	cmd.AddCommand(NewReviewCommand(ctx))
	cmd.AddCommand(NewInvestigateCommand(ctx))
	return cmd
}

type InvestigateFlags struct {
	PRURL  string
	Prompt string
}

func NewInvestigateCommand(ctx context.Context) *cobra.Command {
	var flags InvestigateFlags

	cmd := &cobra.Command{
		Use:   "investigate",
		Short: "Investigate CI check failures for a GitHub pull request in a sandbox",
		Example: `  # Investigate PR check failures
  factory pr investigate --pr-url https://github.com/owner/repo/pull/1`,
		RunE: func(_ *cobra.Command, _ []string) error {
			if flags.PRURL == "" {
				return fmt.Errorf("--pr-url is required")
			}
			ctx, cancel := context.WithTimeout(ctx, rootFlags.Timeout)
			defer cancel()
			return runInvestigate(ctx, flags.PRURL, flags.Prompt)
		},
	}

	cmd.Flags().StringVar(&flags.PRURL, "pr-url", "", "GitHub PR URL (e.g. https://github.com/owner/repo/pull/123)")
	cmd.Flags().StringVar(&flags.Prompt, "prompt", "Investigate check failures for this PR", "Custom prompt for the investigate task")

	return cmd
}

func runInvestigate(ctx context.Context, prURL, prompt string) error {
	fmt.Printf("Resolving PR URL: %s...\n", prURL)

	u, err := url.Parse(prURL)
	if err != nil {
		return fmt.Errorf("invalid PR URL: %w", err)
	}
	parts := strings.Split(strings.TrimPrefix(u.Path, "/"), "/")
	if len(parts) < 4 || parts[2] != "pull" {
		return fmt.Errorf("expected URL format https://github.com/owner/repo/pull/123, got %s", prURL)
	}
	owner, repo := parts[0], parts[1]
	prNum, err := strconv.Atoi(parts[3])
	if err != nil {
		return fmt.Errorf("invalid PR number in URL: %s", parts[3])
	}

	ghClient, err := github.NewClient(ctx)
	if err != nil {
		return fmt.Errorf("creating github client: %w", err)
	}
	pr, _, err := ghClient.PullRequests.Get(ctx, owner, repo, prNum)
	if err != nil {
		return fmt.Errorf("fetching github PR #%d: %w", prNum, err)
	}

	// Fetch failed check runs to populate FailedRuns
	headSHA := pr.GetHead().GetSHA()
	checks, _, err := ghClient.Checks.ListCheckRunsForRef(ctx, owner, repo, headSHA, nil)
	if err != nil {
		return fmt.Errorf("listing check runs: %w", err)
	}

	var failedRuns []tasks.FailedRun
	var failedRunIDs []string
	for _, run := range checks.CheckRuns {
		if run.GetConclusion() == "failure" {
			failedRuns = append(failedRuns, tasks.FailedRun{
				ID:   run.GetID(),
				Name: run.GetName(),
				URL:  run.GetHTMLURL(),
			})
			failedRunIDs = append(failedRunIDs, fmt.Sprintf("%d", run.GetID()))
		}
	}

	if len(failedRuns) == 0 {
		fmt.Printf("No failing checks found for PR #%d.\n", prNum)
		return nil
	}

	// Fetch PR comments
	comments, _, err := ghClient.Issues.ListComments(ctx, owner, repo, prNum, nil)
	if err != nil {
		return fmt.Errorf("listing PR comments: %w", err)
	}
	var prComments []tasks.PRComment
	for _, c := range comments {
		prComments = append(prComments, tasks.PRComment{
			UserLogin: c.GetUser().GetLogin(),
			CreatedAt: c.GetCreatedAt().Format(time.RFC3339),
			Body:      c.GetBody(),
		})
	}

	kubeClient, err := clients.NewKubernetesClient()
	if err != nil {
		return fmt.Errorf("creating k8s client: %w", err)
	}

	cloneURL := fmt.Sprintf("%s#refs/heads/%s", pr.GetHead().GetRepo().GetCloneURL(), pr.GetHead().GetRef())
	fmt.Printf("Ensuring review sandbox for PR #%d...\n", prNum)
	sandboxName, err := factorysandbox.EnsureReviewSandbox(ctx, kubeClient, rootFlags.Namespace, prNum, pr.GetTitle(), pr.GetHTMLURL(), pr.GetDiffURL(), cloneURL, rootFlags.Image, rootFlags.DiskSize)
	if err != nil {
		return fmt.Errorf("ensuring review sandbox: %w", err)
	}

	secret, err := kubeClient.Clientset.CoreV1().Secrets(rootFlags.Namespace).Get(ctx, rootFlags.SecretName, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("fetching %s secret in namespace %s: %w (make sure to run 'factory user onboard' first)", rootFlags.SecretName, rootFlags.Namespace, err)
	}
	githubLogin := string(secret.Data[KeyGithubLogin])
	githubEmail := string(secret.Data[KeyGithubEmail])

	params := tasks.InvestigateParams{
		PullRequest: tasks.PullRequest{
			Number: prNum,
			URL:    prURL,
			Title:  pr.GetTitle(),
			Body:   pr.GetBody(),
		},
		FailedRuns:    failedRuns,
		IssueComments: prComments,
		Models:        []string{"gemini-3-flash-preview", "gemini-3.1-pro-preview", "gemini-2.5-pro"},
	}

	scriptBytes, err := tasks.GetInvestigateScript()
	if err != nil {
		return fmt.Errorf("getting investigate script: %w", err)
	}

	promptBytes, err := tasks.RenderInvestigatePrompt(params)
	if err != nil {
		return fmt.Errorf("rendering investigate prompt: %w", err)
	}

	fmt.Printf("Connecting to sandbox %s via envd...\n", sandboxName)
	client, err := envd.Connect(ctx, rootFlags.Namespace, sandboxName)
	if err != nil {
		return fmt.Errorf("connecting to sandbox: %w", err)
	}
	defer client.Close()

	taskDir := fmt.Sprintf("/workspaces/tasks/investigate-%s", time.Now().Format("20060102-150405"))
	promptPath := fmt.Sprintf("%s/agent-prompt.txt", taskDir)
	scriptPath := fmt.Sprintf("%s/pre-script.sh", taskDir)

	fmt.Println("Writing prompt and script into sandbox...")
	if err := client.WriteFile(ctx, promptPath, promptBytes); err != nil {
		return fmt.Errorf("writing prompt: %w", err)
	}
	if err := client.WriteFile(ctx, scriptPath, scriptBytes); err != nil {
		return fmt.Errorf("writing script: %w", err)
	}

	envMap := map[string]string{
		"GITHUB_TOKEN":               string(secret.Data[KeyGithubToken]),
		"GEMINI_API_KEY":             string(secret.Data[KeyGeminiApiKey]),
		"GEMINI_CLI_TRUST_WORKSPACE": "true",
		"REPO_NAME":                  repo,
		"CLONE_URL":                  cloneURL,
		"PROMPT_FILE":                promptPath,
		"GITHUB_USER_ID":             githubLogin,
		"GITHUB_USER_EMAIL":          githubEmail,
		"GITHUB_USER_NAME":           githubLogin,
		"PR_NUMBER":                  strconv.Itoa(prNum),
		"FAILED_RUNS":                strings.Join(failedRunIDs, " "),
		"MODELS":                     "gemini-3-flash-preview gemini-3.1-pro-preview gemini-2.5-pro",
	}

	fmt.Println("Running investigate task via envd...")
	cmdStr := fmt.Sprintf("bash -c 'set -o pipefail; bash %s 2>&1 | tee %s/execution.log'", scriptPath, taskDir)
	if err := client.RunTask(ctx, cmdStr, envMap); err != nil {
		return fmt.Errorf("running task: %w", err)
	}

	fmt.Println("\nInvestigate execution completed.")
	return nil
}
