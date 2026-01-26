package commands

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/clients"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/github"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/prompts"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/sandbox"
	"github.com/spf13/cobra"
	"k8s.io/klog/v2"
)

// GithubFixIssueOptions holds options for the RunCode function.
type GithubFixIssueOptions struct {
	URL string
}

// BuildGithubFixIssueCommand creates a new cobra command for using a dev sandbox to solve a github issue
func BuildGithubFixIssueCommand() *cobra.Command {
	var opt GithubFixIssueOptions

	cmd := &cobra.Command{
		Use:   "github-fix-issue",
		Short: "Fix a github issue using an LLM in a dev sandbox",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) != 0 {
				return fmt.Errorf("command does not take positional arguments")
			}

			return RunGithubFixIssue(cmd.Context(), opt)
		},
	}

	cmd.Flags().StringVar(&opt.URL, "url", opt.URL, "GitHub issue URL")
	return cmd
}

// RunGithubFixIssue launches VS Code connected to the specified dev sandbox.
func RunGithubFixIssue(ctx context.Context, opt GithubFixIssueOptions) error {
	log := klog.FromContext(ctx)

	codebotRobotToken := os.Getenv("CODEBOT_ROBOT_GITHUB_TOKEN")
	if codebotRobotToken == "" {
		return fmt.Errorf("CODEBOT_ROBOT_GITHUB_TOKEN environment variable is not set")
	}

	kube, err := clients.NewKubernetesClient()
	if err != nil {
		return err
	}

	githubAPI, err := github.NewClient(ctx)
	if err != nil {
		return fmt.Errorf("failed to create github client: %w", err)
	}

	if opt.URL == "" {
		return fmt.Errorf("--url is required")
	}

	issue, err := github.ParseIssueURL(opt.URL)
	if err != nil {
		return err
	}
	repo := issue.Repo

	prompt, err := prompts.FixIssuePrompt(ctx, githubAPI, issue)
	if err != nil {
		return fmt.Errorf("failed to generate prompt for issue: %w", err)
	}

	sb, found, err := sandbox.FindSandboxForIssue(ctx, kube, repo, issue)
	if err != nil {
		return err
	}

	if !found {
		sb, err = sandbox.LaunchSandboxForIssue(ctx, kube, repo, issue)
		if err != nil {
			return fmt.Errorf("launching sandbox for issue: %w", err)
		}
	}

	podID := sb.GetPodID()

	geminiAPIKey, err := GetGeminiAPIKey(podID.Namespace + "/" + podID.Name)
	if err != nil {
		return err
	}

	if err := sb.SetupGit(ctx); err != nil {
		return fmt.Errorf("setting up git in sandbox: %w", err)
	}

	if err := sb.SetupGitRepos(ctx); err != nil {
		return fmt.Errorf("setting up git branches in sandbox: %w", err)
	}

	// Copy the prompt into the pod (for now)
	if len(prompt) > 0 {
		log.Info("copying prompt into sandbox pod", "pod", podID)

		path := "/workspaces/prompt.txt"
		if err := sb.WriteFile(ctx, path, prompt); err != nil {
			return fmt.Errorf("copying prompt into sandbox pod: %w", err)
		}

		log.Info("Copied prompt into sandbox pod", "pod", podID, "path", path)
	}

	// HACK: Avoid git lock issues
	time.Sleep(5 * time.Second)

	if err := sb.CheckoutNewBranch(ctx); err != nil {
		return fmt.Errorf("checking out branch: %w", err)
	}

	if err := sandbox.ConfigureGemini(ctx, sb); err != nil {
		return fmt.Errorf("configuring gemini in sandbox: %w", err)
	}

	// Run gemini with API key and prompt
	{
		log.Info("Running gemini in pod", "pod", podID)

		workdir := fmt.Sprintf("/workspaces/%s", sb.GetRepo().FilesystemName())

		// TODO:
		// export GEMINI_TELEMETRY_ENABLED=true
		// export GEMINI_TELEMETRY_OTLP_ENDPOINT=http://otel-portal.otel-system:4317

		opts := sandbox.ExecOptions{
			Command: []string{"sh", "-c", fmt.Sprintf("cd %s && export GEMINI_API_KEY=%s && gemini --yolo --model gemini-3-pro-preview < /workspaces/prompt.txt", workdir, geminiAPIKey)},
			Stdout:  os.Stdout,
			Stderr:  os.Stderr,
		}
		opts.Secrets = []string{geminiAPIKey}

		if err := sb.Exec(ctx, opts); err != nil {
			return fmt.Errorf("running gemini: %w", err)
		}
	}

	return nil
}
