package commands

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/clients"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/github"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/prompts"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/sandbox"
	githubapi "github.com/google/go-github/v39/github"
	"github.com/spf13/cobra"
	"k8s.io/klog/v2"
)

// GithubFeedbackOptions holds options for the RunCode function.
type GithubFeedbackOptions struct {
	PullRequest string
	Issue       string

	Model string
}

func (o *GithubFeedbackOptions) InitDefaults() {
	o.Model = "gemini-3-pro-preview"
}

// BuildGithubFeedbackCommand creates a new cobra command for using a dev sandbox to address github feedback
func BuildGithubFeedbackCommand() *cobra.Command {
	var opt GithubFeedbackOptions

	opt.InitDefaults()

	cmd := &cobra.Command{
		Use:   "github-feedback",
		Short: "Address github pull request feedback using an LLM in a dev sandbox",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) != 0 {
				return fmt.Errorf("command does not take positional arguments")
			}

			return RunGithubFeedback(cmd.Context(), opt)
		},
	}

	cmd.Flags().StringVar(&opt.Issue, "issue", opt.Issue, "GitHub issue URL")
	cmd.Flags().StringVar(&opt.PullRequest, "pull-request", opt.PullRequest, "GitHub pull request URL")
	cmd.Flags().StringVar(&opt.Model, "model", opt.Model, "LLM model to use")

	return cmd
}

// RunGithubFeedback launches gemini-cli to respond to the specified GitHub pull request feedback.
func RunGithubFeedback(ctx context.Context, opt GithubFeedbackOptions) error {
	log := klog.FromContext(ctx)

	model := opt.Model

	githubAPI, err := github.NewClient(ctx)
	if err != nil {
		return fmt.Errorf("failed to create github client: %w", err)
	}

	kube, err := clients.NewKubernetesClient()
	if err != nil {
		return err
	}

	if opt.PullRequest == "" {
		return fmt.Errorf("--pull-request is required")
	}

	pullRequest, err := github.ParsePullRequestURL(opt.PullRequest)
	if err != nil {
		return err
	}

	pullRequestID, err := github.ParsePullRequestURL(opt.PullRequest)
	if err != nil {
		return err
	}

	pullRequestData, _, err := githubAPI.PullRequests.Get(ctx, pullRequestID.Repo.Owner, pullRequestID.Repo.Name, pullRequestID.PullRequestNumber)
	if err != nil {
		return fmt.Errorf("getting pull request data: %w", err)
	}

	issueURL := opt.Issue
	if issueURL == "" {
		issueURL, err = findIssueFromPullRequest(ctx, pullRequestID.Repo, pullRequestData)
		if err != nil {
			return fmt.Errorf("finding issue from pull request: %w", err)
		}
		log.Info("inferred issue from pull request", "issueURL", issueURL)
	}

	issue, err := github.ParseIssueURL(issueURL)
	if err != nil {
		return fmt.Errorf("parsing issue URL %q: %w", issueURL, err)
	}

	repo := issue.Repo

	repoInfo, err := repo.FetchInfo(ctx, githubAPI)
	if err != nil {
		return fmt.Errorf("getting repo info: %w", err)
	}

	sb, found, err := sandbox.FindSandboxForIssue(ctx, kube, repo, issue)
	if err != nil {
		return fmt.Errorf("finding sandbox: %w", err)
	}

	if !found {
		sb, err = sandbox.LaunchSandboxForIssue(ctx, kube, repo, issue)
		if err != nil {
			return fmt.Errorf("launching sandbox for issue: %w", err)
		}
	}

	if err := sb.SetupGit(ctx); err != nil {
		return fmt.Errorf("setting up git in sandbox: %w", err)
	}

	if err := sb.SetupGitRepos(ctx); err != nil {
		return fmt.Errorf("setting up git branches in sandbox: %w", err)
	}

	// HACK: avoid .git/index.lock conflict with checkout
	time.Sleep(5 * time.Second)

	branchName := pullRequestData.GetHead().GetRef()
	if err := sb.CheckoutExistingBranch(ctx, branchName); err != nil {
		return err
	}

	threads, err := sb.ListThreads(ctx)
	if err != nil {
		return fmt.Errorf("listing threads in sandbox: %w", err)
	}

	log.Info("found threads in sandbox", "count", len(threads))

	haveIDs := make(map[string]bool)

	appendToThread := ""

	if len(threads) > 0 {
		if len(threads) > 1 {
			return fmt.Errorf("multiple threads found in sandbox %v; not yet supported", sb.GetPodID())
		}

		appendToThread = threads[0].SessionID

		messages, err := sb.GetThreadMessages(ctx, threads[0].SessionID)
		if err != nil {
			return fmt.Errorf("getting thread messages: %w", err)
		}

		for _, msg := range messages {
			if msg.Type == "gemini" {
				continue
			}
			for _, line := range strings.Split(msg.Content, "\n") {
				line = strings.TrimSpace(line)
				if strings.HasPrefix(line, "GITHUB_ID: ") {
					tokens := strings.Fields(line)
					if len(tokens) != 2 {
						return fmt.Errorf("unexpected ID line format: %q", line)
					}
					haveIDs[tokens[1]] = true
					continue
				}
			}
		}
	}

	prompt, err := prompts.FixPRFeedbackPrompt(ctx, githubAPI, repoInfo, pullRequest, haveIDs)
	if err != nil {
		return fmt.Errorf("failed to generate prompt for pull-request: %w", err)
	}
	log.Info("generated prompt for pull request feedback", "prompt", string(prompt))

	geminiAPIKey, err := GetGeminiAPIKey(sb.GetPodID().Namespace + "/" + sb.GetPodID().Name)
	if err != nil {
		return fmt.Errorf("getting gemini api key: %w", err)
	}

	if err := sandbox.ConfigureGemini(ctx, sb); err != nil {
		return fmt.Errorf("configuring gemini in sandbox: %w", err)
	}

	// Copy the prompt into the pod (for now)
	if len(prompt) > 0 {
		path := "/workspaces/prompt.txt"
		if err := sb.WriteFile(ctx, path, prompt); err != nil {
			return fmt.Errorf("copying prompt into sandbox pod: %w", err)
		}

		log.Info("wrote prompt into sandbox pod", "pod", sb.GetPodID(), "path", path)
	}

	// Run gemini with API key and prompt
	{
		log.Info("Running gemini in pod", "pod", sb.GetPodID())

		workdir := fmt.Sprintf("/workspaces/%s", pullRequest.Repo.FilesystemName())

		// TODO:
		// export GEMINI_TELEMETRY_ENABLED=true
		// export GEMINI_TELEMETRY_OTLP_ENDPOINT=http://otel-portal.otel-system:4317

		cmd := []string{"gemini", "--yolo", "--model", model}
		if appendToThread != "" {
			cmd = append(cmd, "--resume="+appendToThread)
		}
		opts := sandbox.ExecOptions{
			Command: []string{"sh", "-c", fmt.Sprintf("cd %s && export GEMINI_API_KEY=%s &&  %s < /workspaces/prompt.txt", workdir, geminiAPIKey, strings.Join(cmd, " "))},
			Stdout:  os.Stdout,
			Stderr:  os.Stderr,
		}

		opts.Secrets = []string{geminiAPIKey}

		if err := sb.Exec(ctx, opts); err != nil {
			return fmt.Errorf("running gemini in pod: %w", err)
		}
	}

	return nil
}

// findIssueFromPullRequest attempts to find a linked issue from the pull request description or comments.
func findIssueFromPullRequest(ctx context.Context, repo github.Repo, pullRequest *githubapi.PullRequest) (string, error) {
	log := klog.FromContext(ctx)

	toURL := func(s string) string {
		if !strings.HasPrefix(s, "#") {
			return ""
		}
		s = strings.TrimPrefix(s, "#")
		s = strings.TrimSuffix(s, ":")
		s = strings.TrimSuffix(s, ".")
		number, err := strconv.Atoi(s)
		if err != nil {
			return ""
		}
		u := fmt.Sprintf("https://%s/%s/%s/issues/%d", repo.Host, repo.Owner, repo.Name, number)
		return u
	}

	// First, check the pull request body for "Fixes: <issue-url>" or "Resolves: <issue-url>"
	pullRequestBody := pullRequest.GetBody()
	log.Info("searching pull request body for linked issue", "body", pullRequestBody)
	for _, line := range strings.Split(pullRequestBody, "\n") {
		line = strings.TrimSpace(line)
		tokens := strings.Fields(line)
		if len(tokens) >= 2 && (tokens[0] == "Fixes:" || tokens[0] == "Resolves:" || tokens[0] == "Fixes" || tokens[0] == "Resolves") {
			issueURL := toURL(tokens[1])
			if issueURL != "" {
				return issueURL, nil
			}
		}
	}

	// Check the title
	pullRequestTitle := pullRequest.GetTitle()
	log.Info("searching pull request title for linked issue", "title", pullRequestTitle)
	tokens := strings.Fields(pullRequestTitle)
	if len(tokens) >= 2 && (tokens[0] == "Fixes" || tokens[0] == "Resolves") {
		issueURL := toURL(tokens[1])
		if issueURL != "" {
			return issueURL, nil
		}
	}

	return "", fmt.Errorf("no linked issue found in pull request description or title")
}
