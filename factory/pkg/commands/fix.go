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
	factorysandbox "github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/sandbox"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/tasks"
	"github.com/spf13/cobra"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type FixFlags struct {
	IssueURL string
	Prompt   string
}

func NewFixCommand(ctx context.Context) *cobra.Command {
	var flags FixFlags

	cmd := &cobra.Command{
		Use:   "fix",
		Short: "Fix a bug for a given GitHub issue URL in a sandbox",
		Example: `  # Fix an issue with a custom prompt
  factory fix --issue-url https://github.com/owner/repo/issues/1 --prompt "Use Go 1.26 and add unit tests"

  # Override workspace disk size and base image
  factory fix --issue-url https://github.com/owner/repo/issues/1 --workspace-disk-size 20Gi --image kind.local/my-golang:latest`,
		RunE: func(_ *cobra.Command, _ []string) error {
			if flags.IssueURL == "" {
				return fmt.Errorf("--issue-url is required")
			}
			ctx, cancel := context.WithTimeout(ctx, rootFlags.Timeout)
			defer cancel()
			return runFix(ctx, flags.IssueURL, flags.Prompt)
		},
	}

	cmd.Flags().StringVar(&flags.IssueURL, "issue-url", "", "GitHub issue URL (e.g. https://github.com/owner/repo/issues/123)")
	cmd.Flags().StringVar(&flags.Prompt, "prompt", "Fix this issue in the repository and push a PR", "Custom prompt for the fix task")

	return cmd
}

func runFix(ctx context.Context, issueURL, prompt string) error {
	fmt.Printf("Resolving issue URL: %s...\n", issueURL)

	u, err := url.Parse(issueURL)
	if err != nil {
		return fmt.Errorf("invalid issue URL: %w", err)
	}
	parts := strings.Split(strings.TrimPrefix(u.Path, "/"), "/")
	if len(parts) < 4 || parts[2] != "issues" {
		return fmt.Errorf("expected URL format https://github.com/owner/repo/issues/123, got %s", issueURL)
	}
	owner, repo := parts[0], parts[1]
	issueNum, err := strconv.Atoi(parts[3])
	if err != nil {
		return fmt.Errorf("invalid issue number in URL: %s", parts[3])
	}

	cloneURL := fmt.Sprintf("https://github.com/%s/%s.git", owner, repo)
	issueTitle := fmt.Sprintf("Issue #%d", issueNum)

	kubeClient, err := clients.NewKubernetesClient()
	if err != nil {
		return fmt.Errorf("creating k8s client: %w", err)
	}

	fmt.Printf("Ensuring sandbox for issue #%d...\n", issueNum)
	sandboxName, err := factorysandbox.EnsureIssueSandbox(ctx, kubeClient, rootFlags.Namespace, issueNum, issueURL, cloneURL, issueTitle, rootFlags.Image, rootFlags.DiskSize)
	if err != nil {
		return fmt.Errorf("ensuring issue sandbox: %w", err)
	}

	secret, err := kubeClient.Clientset.CoreV1().Secrets(rootFlags.Namespace).Get(ctx, rootFlags.SecretName, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("fetching %s secret in namespace %s: %w (make sure to run 'factory user onboard' first)", rootFlags.SecretName, rootFlags.Namespace, err)
	}
	githubLogin := string(secret.Data[KeyGithubLogin])
	githubEmail := string(secret.Data[KeyGithubEmail])

	branchName := fmt.Sprintf("issue-%d-%d", issueNum, time.Now().Unix())

	params := tasks.FixIssueParams{
		Repo: tasks.Repo{
			CloneURL: cloneURL,
		},
		Issue: tasks.Issue{
			Number:  issueNum,
			HTMLURL: issueURL,
			Title:   issueTitle,
			Body:    prompt,
		},
		Branch:  branchName,
		Models:  []string{"gemini-3-flash-preview", "gemini-3.1-pro-preview", "gemini-2.5-pro"},
		DraftPR: false,
		PRLabel: "factory",
	}

	scriptBytes, err := tasks.GetFixIssueScript()
	if err != nil {
		return fmt.Errorf("getting fix-issue script: %w", err)
	}

	promptBytes, err := tasks.RenderFixIssuePrompt(params)
	if err != nil {
		return fmt.Errorf("rendering fix-issue prompt: %w", err)
	}

	fmt.Printf("Connecting to sandbox %s via envd...\n", sandboxName)
	client, err := envd.Connect(ctx, rootFlags.Namespace, sandboxName)
	if err != nil {
		return fmt.Errorf("connecting to sandbox: %w", err)
	}
	defer client.Close()

	taskDir := fmt.Sprintf("/workspaces/tasks/fix-%s", time.Now().Format("20060102-150405"))
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
		"GEMINI_API_KEY":             string(secret.Data[KeyGeminiAPIKey]),
		"GEMINI_CLI_TRUST_WORKSPACE": "true",
		"REPO_OWNER":                 owner,
		"REPO_NAME":                  repo,
		"CLONE_URL":                  cloneURL,
		"ISSUE_NUMBER":               strconv.Itoa(issueNum),
		"PROMPT_FILE":                promptPath,
		"GITHUB_USER_ID":             githubLogin,
		"GITHUB_USER_EMAIL":          githubEmail,
		"GITHUB_USER_NAME":           githubLogin,
		"BRANCH_NAME":                branchName,
		"MODELS":                     "gemini-3-flash-preview gemini-3.1-pro-preview gemini-2.5-pro",
	}

	fmt.Println("Running fix-issue task via envd...")
	cmdStr := fmt.Sprintf("bash -c 'set -o pipefail; bash %s 2>&1 | tee %s/execution.log'", scriptPath, taskDir)
	if err := client.RunTask(ctx, cmdStr, envMap); err != nil {
		return fmt.Errorf("running task: %w", err)
	}

	fmt.Println("\nTask execution completed.")
	return nil
}
