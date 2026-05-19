package commands

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/clients"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/envd"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/github"
	factorysandbox "github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/sandbox"
	"github.com/spf13/cobra"
)

type ReviewFlags struct {
	PRURL  string
	Prompt string
}

func NewReviewCommand(ctx context.Context) *cobra.Command {
	var flags ReviewFlags

	cmd := &cobra.Command{
		Use:   "review",
		Short: "Review a GitHub pull request in a sandbox",
		Example: `  # Review a PR with custom guidelines
  factory pr review --pr-url https://github.com/owner/repo/pull/1 --prompt "Focus on performance and security vulnerabilities"

  # Review using a specific credential secret
  factory pr review --pr-url https://github.com/owner/repo/pull/1 --secret-name my-bot-secret`,
		RunE: func(_ *cobra.Command, _ []string) error {
			if flags.PRURL == "" {
				return fmt.Errorf("--pr-url is required")
			}
			ctx, cancel := context.WithTimeout(ctx, rootFlags.Timeout)
			defer cancel()
			return runReview(ctx, flags.PRURL, flags.Prompt)
		},
	}

	cmd.Flags().StringVar(&flags.PRURL, "pr-url", "", "GitHub PR URL (e.g. https://github.com/owner/repo/pull/123)")
	cmd.Flags().StringVar(&flags.Prompt, "prompt", "Review this PR and provide helpful feedback", "Custom prompt for the review task")

	return cmd
}

func runReview(ctx context.Context, prURL, prompt string) error {
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

	fmt.Printf("Connecting to sandbox %s via envd...\n", sandboxName)
	client, err := envd.Connect(ctx, rootFlags.Namespace, sandboxName)
	if err != nil {
		return fmt.Errorf("connecting to sandbox: %w", err)
	}
	defer client.Close()

	fmt.Println("Running hello world task via envd...")
	if err := client.RunTask(ctx, "echo 'hello world'", nil); err != nil {
		return fmt.Errorf("running task: %w", err)
	}

	fmt.Println("\nReview execution completed.")
	return nil
}
