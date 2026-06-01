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
	"k8s.io/klog/v2"
)

func NewPRCommand(ctx context.Context) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "pr",
		Short: "Manage GitHub pull request workflows",
	}
	cmd.AddCommand(NewReviewCommand(ctx))
	cmd.AddCommand(NewInvestigateCommand(ctx))
	cmd.AddCommand(NewAddressCommentsCommand(ctx))
	cmd.AddCommand(NewPRWatchCommand(ctx))
	return cmd
}

type InvestigateFlags struct {
	PRURL           string
	Prompt          string
	ContinueSession bool
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
			return runInvestigate(ctx, flags.PRURL, flags.Prompt, flags.ContinueSession)
		},
	}

	cmd.Flags().StringVar(&flags.PRURL, "pr-url", "", "GitHub PR URL (e.g. https://github.com/owner/repo/pull/123)")
	cmd.Flags().StringVar(&flags.Prompt, "prompt", "Investigate check failures for this PR", "Custom prompt for the investigate task")
	cmd.Flags().BoolVar(&flags.ContinueSession, "continue-session", false, "Continue the Gemini session from previous runs in the sandbox")

	return cmd
}

func runInvestigate(ctx context.Context, prURL, prompt string, continueSession bool) error {
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

	var failedProwRuns []string
	statuses, _, err := ghClient.Repositories.ListStatuses(ctx, owner, repo, headSHA, nil)
	if err == nil {
		for _, status := range statuses {
			if status.GetState() == "failure" || status.GetState() == "error" {
				failedRuns = append(failedRuns, tasks.FailedRun{
					ID:   status.GetID(),
					Name: status.GetContext(),
					URL:  status.GetTargetURL(),
				})
				failedProwRuns = append(failedProwRuns, fmt.Sprintf("%d|%s", status.GetID(), status.GetTargetURL()))
			}
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
			ID:        c.GetID(),
			UserLogin: c.GetUser().GetLogin(),
			CreatedAt: c.GetCreatedAt().Format(time.RFC3339),
			Body:      c.GetBody(),
		})
	}

	kubeClient, err := clients.NewKubernetesClient()
	if err != nil {
		return fmt.Errorf("creating k8s client: %w", err)
	}

	cloneURL := pr.GetBase().GetRepo().GetCloneURL()
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
		Models:        []string{"gemini-3.5-flash", "gemini-3-flash-preview", "gemini-3.1-pro-preview", "gemini-2.5-pro"},
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
		"GEMINI_API_KEY":             getGeminiAPIKey(secret),
		"GEMINI_CLI_TRUST_WORKSPACE": "true",
		"REPO_NAME":                  repo,
		"CLONE_URL":                  cloneURL,
		"PROMPT_FILE":                promptPath,
		"GITHUB_USER_ID":             githubLogin,
		"GITHUB_USER_EMAIL":          githubEmail,
		"GITHUB_USER_NAME":           githubLogin,
		"PR_NUMBER":                  strconv.Itoa(prNum),
		"FAILED_RUNS":                strings.Join(failedRunIDs, " "),
		"FAILED_PROW_RUNS":           strings.Join(failedProwRuns, " "),
		"MODELS":                     "gemini-3.5-flash gemini-3-flash-preview gemini-3.1-pro-preview gemini-2.5-pro",
		"GEMINI_CONTINUE_SESSION":    strconv.FormatBool(continueSession),
	}

	fmt.Println("Running investigate task via envd...")
	cmdStr := fmt.Sprintf("bash -c 'set -o pipefail; bash %s 2>&1 | tee %s/execution.log'", scriptPath, taskDir)
	if rootFlags.Tmux {
		fmt.Printf("Running task inside tmux session '%s'...\n", sandboxName)
		cmdStr = wrapWithTmux(cmdStr, sandboxName)
	}
	if err := client.RunTask(ctx, cmdStr, envMap); err != nil {
		return fmt.Errorf("running task: %w", err)
	}

	fmt.Println("\nInvestigate execution completed.")
	return nil
}

type AddressCommentsFlags struct {
	PRURL           string
	Prompt          string
	ContinueSession bool
}

func NewAddressCommentsCommand(ctx context.Context) *cobra.Command {
	var flags AddressCommentsFlags

	cmd := &cobra.Command{
		Use:   "address-comments",
		Short: "Address review feedback and comments for a GitHub pull request in a sandbox",
		Example: `  # Address PR review feedback
  factory pr address-comments --pr-url https://github.com/owner/repo/pull/1`,
		RunE: func(_ *cobra.Command, _ []string) error {
			if flags.PRURL == "" {
				return fmt.Errorf("--pr-url is required")
			}
			ctx, cancel := context.WithTimeout(ctx, rootFlags.Timeout)
			defer cancel()
			return runAddressComments(ctx, flags.PRURL, flags.Prompt, flags.ContinueSession)
		},
	}

	cmd.Flags().StringVar(&flags.PRURL, "pr-url", "", "GitHub PR URL (e.g. https://github.com/owner/repo/pull/123)")
	cmd.Flags().StringVar(&flags.Prompt, "prompt", "Address review feedback for this PR", "Custom prompt for the address-comments task")
	cmd.Flags().BoolVar(&flags.ContinueSession, "continue-session", false, "Continue the Gemini session from previous runs in the sandbox")

	return cmd
}

func runAddressComments(ctx context.Context, prURL, prompt string, continueSession bool) error {
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

	// Fetch PR commits
	prCommits, _, err := ghClient.PullRequests.ListCommits(ctx, owner, repo, prNum, nil)
	if err != nil {
		return fmt.Errorf("listing PR commits: %w", err)
	}
	var repoCommits []tasks.RepositoryCommit
	var lastCommitTime time.Time
	for _, c := range prCommits {
		repoCommits = append(repoCommits, tasks.RepositoryCommit{
			SHA:     c.GetSHA(),
			Message: c.GetCommit().GetMessage(),
		})
		if c.GetCommit().GetAuthor().GetDate().After(lastCommitTime) {
			lastCommitTime = c.GetCommit().GetAuthor().GetDate()
		}
	}

	// Fetch PR comments
	comments, _, err := ghClient.Issues.ListComments(ctx, owner, repo, prNum, nil)
	if err != nil {
		return fmt.Errorf("listing PR comments: %w", err)
	}
	var oldComments []tasks.PRComment
	var newComments []tasks.PRComment
	for _, c := range comments {
		cmt := tasks.PRComment{
			ID:        c.GetID(),
			UserLogin: c.GetUser().GetLogin(),
			CreatedAt: c.GetCreatedAt().Format(time.RFC3339),
			Body:      c.GetBody(),
		}
		if c.GetCreatedAt().After(lastCommitTime) {
			newComments = append(newComments, cmt)
		} else {
			oldComments = append(oldComments, cmt)
		}
	}

	// Fetch PR reviews
	reviews, _, err := ghClient.PullRequests.ListReviews(ctx, owner, repo, prNum, nil)
	if err != nil {
		return fmt.Errorf("listing PR reviews: %w", err)
	}
	var oldReviews []tasks.PRReview
	var newReviews []tasks.PRReview
	for _, r := range reviews {
		rev := tasks.PRReview{
			ID:        r.GetID(),
			UserLogin: r.GetUser().GetLogin(),
			Body:      r.GetBody(),
		}
		// Fetch review comments for this review
		revComments, _, err := ghClient.PullRequests.ListReviewComments(ctx, owner, repo, prNum, r.GetID(), nil)
		if err == nil {
			for _, rc := range revComments {
				rev.PullRequestComments = append(rev.PullRequestComments, tasks.PullRequestComment{
					Path:     rc.GetPath(),
					DiffHunk: rc.GetDiffHunk(),
					Body:     rc.GetBody(),
				})
			}
		}
		if r.GetSubmittedAt().After(lastCommitTime) {
			newReviews = append(newReviews, rev)
		} else {
			oldReviews = append(oldReviews, rev)
		}
	}

	if len(newComments) == 0 && len(newReviews) == 0 {
		fmt.Printf("No new comments or reviews found for PR #%d since last commit.\n", prNum)
		return nil
	}

	kubeClient, err := clients.NewKubernetesClient()
	if err != nil {
		return fmt.Errorf("creating k8s client: %w", err)
	}

	cloneURL := pr.GetBase().GetRepo().GetCloneURL()
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

	params := tasks.AddressFeedbackParams{
		PullRequest: tasks.PullRequest{
			Number: prNum,
			URL:    prURL,
			Title:  pr.GetTitle(),
			Body:   pr.GetBody(),
		},
		RepositoryCommits:     repoCommits,
		OldIssueComments:      oldComments,
		IssueComments:         newComments,
		OldPullRequestReviews: oldReviews,
		PullRequestReviews:    newReviews,
		Models:                []string{"gemini-3.5-flash", "gemini-3-flash-preview", "gemini-3.1-pro-preview", "gemini-2.5-pro"},
	}

	scriptBytes, err := tasks.GetAddressFeedbackScript()
	if err != nil {
		return fmt.Errorf("getting address-feedback script: %w", err)
	}

	promptBytes, err := tasks.RenderAddressFeedbackPrompt(params)
	if err != nil {
		return fmt.Errorf("rendering address-feedback prompt: %w", err)
	}

	fmt.Printf("Connecting to sandbox %s via envd...\n", sandboxName)
	client, err := envd.Connect(ctx, rootFlags.Namespace, sandboxName)
	if err != nil {
		return fmt.Errorf("connecting to sandbox: %w", err)
	}
	defer client.Close()

	taskDir := fmt.Sprintf("/workspaces/tasks/address-%s", time.Now().Format("20060102-150405"))
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
		"GEMINI_API_KEY":             getGeminiAPIKey(secret),
		"GEMINI_CLI_TRUST_WORKSPACE": "true",
		"REPO_NAME":                  repo,
		"CLONE_URL":                  cloneURL,
		"PROMPT_FILE":                promptPath,
		"GITHUB_USER_ID":             githubLogin,
		"GITHUB_USER_EMAIL":          githubEmail,
		"GITHUB_USER_NAME":           githubLogin,
		"PR_NUMBER":                  strconv.Itoa(prNum),
		"MODELS":                     "gemini-3.5-flash gemini-3-flash-preview gemini-3.1-pro-preview gemini-2.5-pro",
		"GEMINI_CONTINUE_SESSION":    strconv.FormatBool(continueSession),
	}

	fmt.Println("Running address-comments task via envd...")
	cmdStr := fmt.Sprintf("bash -c 'set -o pipefail; bash %s 2>&1 | tee %s/execution.log'", scriptPath, taskDir)
	if rootFlags.Tmux {
		fmt.Printf("Running task inside tmux session '%s'...\n", sandboxName)
		cmdStr = wrapWithTmux(cmdStr, sandboxName)
	}
	if err := client.RunTask(ctx, cmdStr, envMap); err != nil {
		return fmt.Errorf("running task: %w", err)
	}

	fmt.Println("\nAddress-comments execution completed.")
	return nil
}

type PRWatchFlags struct {
	PRURL           string
	PollInterval    time.Duration
	DryRun          bool
	ContinueSession bool
	WatchTimeout    time.Duration
}

func NewPRWatchCommand(ctx context.Context) *cobra.Command {
	var flags PRWatchFlags

	cmd := &cobra.Command{
		Use:   "watch",
		Short: "Watch a GitHub pull request for check failures and new review comments to automatically react",
		Example: `  # Watch a PR and automatically investigate failures or address feedback
  factory pr watch --pr-url https://github.com/owner/repo/pull/1`,
		RunE: func(_ *cobra.Command, _ []string) error {
			if flags.PRURL == "" {
				return fmt.Errorf("--pr-url is required")
			}
			return runPRWatch(ctx, flags.PRURL, flags.PollInterval, flags.DryRun, flags.ContinueSession, flags.WatchTimeout)
		},
	}

	cmd.Flags().StringVar(&flags.PRURL, "pr-url", "", "GitHub PR URL (e.g. https://github.com/owner/repo/pull/123)")
	cmd.Flags().DurationVar(&flags.PollInterval, "poll-interval", 2*time.Minute, "Polling interval")
	cmd.Flags().BoolVar(&flags.DryRun, "dryrun", false, "Print actions without creating sandboxes or executing tasks")
	cmd.Flags().BoolVar(&flags.ContinueSession, "continue-session", false, "Continue the Gemini session from previous runs in the sandbox")
	cmd.Flags().DurationVar(&flags.WatchTimeout, "watch-timeout", 0, "Timeout for watching (default forever)")

	return cmd
}

func runPRWatch(ctx context.Context, prURL string, interval time.Duration, dryRun, continueSession bool, watchTimeout time.Duration) error {
	fmt.Printf("Starting PR watch for %s (poll interval: %s, dryRun: %v, continueSession: %v, watchTimeout: %s)...\n", prURL, interval, dryRun, continueSession, watchTimeout)

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

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	var timeoutChan <-chan time.Time
	if watchTimeout > 0 {
		timeoutChan = time.After(watchTimeout)
	}

	var lastInvestigatedSHA string
	var lastInvestigatedTime time.Time
	var lastCommentAddressedTime time.Time

	checkPR := func() bool {
		pr, _, err := ghClient.PullRequests.Get(ctx, owner, repo, prNum)
		if err != nil {
			klog.Errorf("Failed to fetch PR #%d: %v", prNum, err)
			return false
		}

		if pr.GetMerged() {
			fmt.Printf("\nPR #%d is merged. Stopping watch.\n", prNum)
			return true
		}
		if pr.GetState() == "closed" {
			fmt.Printf("\nPR #%d is closed. Stopping watch.\n", prNum)
			return true
		}

		// Check 1: Check CI check runs and commit statuses
		headSHA := pr.GetHead().GetSHA()
		hasFailure := false

		checks, _, err := ghClient.Checks.ListCheckRunsForRef(ctx, owner, repo, headSHA, nil)
		if err == nil {
			for _, run := range checks.CheckRuns {
				if run.GetConclusion() == "failure" {
					hasFailure = true
					break
				}
			}
		}

		statuses, _, err := ghClient.Repositories.ListStatuses(ctx, owner, repo, headSHA, nil)
		if err == nil {
			for _, status := range statuses {
				if status.GetState() == "failure" || status.GetState() == "error" {
					hasFailure = true
					break
				}
			}
		}

		if hasFailure {
			if headSHA != lastInvestigatedSHA || time.Since(lastInvestigatedTime) > 30*time.Minute {
				fmt.Printf("\nFound failing checks for PR #%d (SHA: %s). Triggering investigate...\n", prNum, headSHA[:7])
				lastInvestigatedSHA = headSHA
				lastInvestigatedTime = time.Now()
				if dryRun {
					fmt.Printf("[DRYRUN] Would trigger investigate for PR #%d\n", prNum)
				} else {
					if err := runInvestigate(ctx, prURL, "Investigate check failures for this PR", continueSession); err != nil {
						klog.Errorf("Investigate failed: %v", err)
					}
				}
			}
		}

		// Check 2: Check new comments/reviews after latest commit
		prCommits, _, err := ghClient.PullRequests.ListCommits(ctx, owner, repo, prNum, nil)
		if err == nil {
			var lastCommitTime time.Time
			for _, c := range prCommits {
				if c.GetCommit().GetAuthor().GetDate().After(lastCommitTime) {
					lastCommitTime = c.GetCommit().GetAuthor().GetDate()
				}
			}

			comments, _, err := ghClient.Issues.ListComments(ctx, owner, repo, prNum, nil)
			if err == nil {
				hasNewComments := false
				for _, c := range comments {
					// Ignore comments from bot
					if strings.Contains(c.GetUser().GetLogin(), "bot") {
						continue
					}
					if c.GetCreatedAt().After(lastCommitTime) && c.GetCreatedAt().After(lastCommentAddressedTime) {
						hasNewComments = true
						break
					}
				}

				if hasNewComments {
					fmt.Printf("\nFound new review comments for PR #%d. Triggering address-comments...\n", prNum)
					lastCommentAddressedTime = time.Now()
					if dryRun {
						fmt.Printf("[DRYRUN] Would trigger address-comments for PR #%d\n", prNum)
					} else {
						if err := runAddressComments(ctx, prURL, "Address review feedback for this PR", continueSession); err != nil {
							klog.Errorf("Address-comments failed: %v", err)
						}
					}
				}
			}
		}

		return false
	}

	// Run first check immediately
	if checkPR() {
		return nil
	}

	for {
		fmt.Printf("Sleeping for %s...\n", interval)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timeoutChan:
			fmt.Printf("\nWatch timeout of %s expired. Stopping watch.\n", watchTimeout)
			return nil
		case <-ticker.C:
			if checkPR() {
				return nil
			}
		}
	}
}
