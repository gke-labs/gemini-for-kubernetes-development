package commands

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/clients"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/constants"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/github"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/k8s"
	"github.com/spf13/cobra"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/klog/v2"
)

func NewPRCommand(ctx context.Context) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "pr",
		Short: "Manage GitHub pull request workflows",
	}
	cmd.AddCommand(NewPRWatchCommand(ctx))
	cmd.AddCommand(NewAdoptCommand(ctx))
	return cmd
}

type PRWatchFlags struct {
	PRURL        string
	PollInterval time.Duration
	DryRun       bool
	WatchTimeout time.Duration
}

func NewPRWatchCommand(ctx context.Context) *cobra.Command {
	var flags PRWatchFlags

	cmd := &cobra.Command{
		Use:   "watch",
		Short: "Watch a pull request of yours and follow up failed checks and new review comments with care",
		Example: `  # Watch a PR: care fixes failed checks and addresses new review comments
  factory pr watch --pr-url https://github.com/owner/repo/pull/1`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			_, err := ResolveRootFlags(cmd)
			if err != nil {
				return err
			}

			if flags.PRURL == "" {
				return fmt.Errorf("--pr-url is required")
			}

			err = verifyPROwnership(ctx, flags.PRURL)
			if err != nil {
				return err
			}

			return runPRWatch(ctx, flags.PRURL, flags.PollInterval, flags.DryRun, flags.WatchTimeout)
		},
	}

	cmd.Flags().StringVar(&flags.PRURL, "pr-url", "", "GitHub PR URL (e.g. https://github.com/owner/repo/pull/123)")
	cmd.Flags().DurationVar(&flags.PollInterval, "poll-interval", 2*time.Minute, "Polling interval")
	cmd.Flags().BoolVar(&flags.DryRun, "dryrun", false, "Print actions without creating sandboxes or executing tasks")
	cmd.Flags().DurationVar(&flags.WatchTimeout, "watch-timeout", 0, "Timeout for watching (default forever)")

	return cmd
}

func runPRWatch(ctx context.Context, prURL string, interval time.Duration, dryRun bool, watchTimeout time.Duration) error {
	fmt.Printf("Starting PR watch for %s (poll interval: %s, dryRun: %v, watchTimeout: %s)...\n", prURL, interval, dryRun, watchTimeout)

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
	repoClient := github.ForRepo(ghClient, owner, repo)

	kubeClient, err := clients.NewKubernetesClient()
	if err != nil {
		return fmt.Errorf("creating k8s client: %w", err)
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	var timeoutChan <-chan time.Time
	if watchTimeout > 0 {
		timeoutChan = time.After(watchTimeout)
	}

	cleanup := func() {
		if rootFlags.Cleanup {
			manager := k8s.NewManager(kubeClient)

			// Find the sandbox by label first
			listOpts := metav1.ListOptions{
				LabelSelector: fmt.Sprintf("factory.gemini.google.com/pr=%d", prNum),
			}
			sbs, err := kubeClient.DynamicClient.Resource(k8s.SandboxGVR).Namespace(rootFlags.Namespace).List(ctx, listOpts)

			targetSandboxName := fmt.Sprintf("factory-pr-%d", prNum)
			if err == nil && len(sbs.Items) > 0 {
				targetSandboxName = sbs.Items[0].GetName()
			}

			fmt.Printf("Cleaning up sandbox '%s'...\n", targetSandboxName)
			if err := manager.DeleteSandbox(ctx, rootFlags.Namespace, targetSandboxName); err != nil {
				klog.Errorf("Failed to cleanup sandbox '%s': %v", targetSandboxName, err)
			}
		}
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

		// Care follows the PR up (design/recipe-sessions.md): care-ci on
		// failed checks, care-comments on new comments, in care's session.

		// Check 1: Check CI check runs and commit statuses
		headSHA := pr.GetHead().GetSHA()
		hasFailure := false

		checkRuns, err := repoClient.ListCheckRuns(ctx, headSHA)
		if err == nil {
			for _, run := range checkRuns {
				c := run.GetConclusion()
				if c == "failure" || c == "timed_out" || c == "cancelled" {
					hasFailure = true
					break
				}
			}
		}

		statuses, err := repoClient.ListStatuses(ctx, headSHA)
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
				fmt.Printf("\nFound failing checks for PR #%d (SHA: %s). Running care...\n", prNum, headSHA[:7])
				lastInvestigatedSHA = headSHA
				lastInvestigatedTime = time.Now()
				if dryRun {
					fmt.Printf("[DRYRUN] Would run care-ci for PR #%d\n", prNum)
				} else if err := careFollowUp(ctx, ghClient, kubeClient, prURL, "care-ci"); err != nil {
					klog.Errorf("Fix CI failed: %v", err)
				}
			}
		}

		// Check 2: Check new comments/reviews after latest commit
		prCommits, err := repoClient.ListCommits(ctx, prNum)
		if err == nil {
			var lastCommitTime time.Time
			for _, c := range prCommits {
				if c.GetCommit().GetCommitter().GetDate().After(lastCommitTime) {
					lastCommitTime = c.GetCommit().GetCommitter().GetDate()
				}
			}

			comments, err := repoClient.ListIssueComments(ctx, prNum)
			if err == nil {
				hasNewComments := false
				for _, c := range comments {
					// Ignore comments from bot
					if strings.Contains(c.GetUser().GetLogin(), "bot") {
						continue
					}
					// care's own replies and reports.
					if factoryPosted(c.GetBody()) {
						continue
					}
					if c.GetCreatedAt().After(lastCommitTime) && c.GetCreatedAt().After(lastCommentAddressedTime) {
						hasNewComments = true
						break
					}
				}

				if hasNewComments {
					fmt.Printf("\nFound new review comments for PR #%d. Running care...\n", prNum)
					lastCommentAddressedTime = time.Now()
					if dryRun {
						fmt.Printf("[DRYRUN] Would run care-comments for PR #%d\n", prNum)
					} else if err := careFollowUp(ctx, ghClient, kubeClient, prURL, "care-comments"); err != nil {
						klog.Errorf("Address comments failed: %v", err)
					}
				}
			}
		}

		return false
	}

	// Run first check immediately
	if checkPR() {
		cleanup()
		return nil
	}

	for {
		fmt.Printf("Sleeping for %s...\n", interval)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timeoutChan:
			fmt.Printf("\nWatch timeout of %s expired. Stopping watch.\n", watchTimeout)
			cleanup()
			return nil
		case <-ticker.C:
			if checkPR() {
				cleanup()
				return nil
			}
		}
	}
}

func verifyPROwnership(ctx context.Context, prURL string) error {
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

	prAuthor := pr.GetUser().GetLogin()
	user := rootFlags.User
	if user == "" {
		user = prAuthor
	}

	if user != "" && rootFlags.SecretName == constants.SecretFactoryUser {
		secretName := fmt.Sprintf("user-%s", user)
		_, err = kubeClient.Clientset.CoreV1().Secrets(rootFlags.Namespace).Get(ctx, secretName, metav1.GetOptions{})
		if err == nil {
			klog.Infof("Auto-resolved user secret for %s: %s", user, secretName)
			rootFlags.SecretName = secretName
			if rootFlags.User == "" {
				rootFlags.User = user
			}
		} else if !apierrors.IsNotFound(err) {
			return fmt.Errorf("checking user secret: %w", err)
		}
	}

	secret, err := kubeClient.Clientset.CoreV1().Secrets(rootFlags.Namespace).Get(ctx, rootFlags.SecretName, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("fetching %s secret in namespace %s: %w (make sure to run 'factory user onboard' first)", rootFlags.SecretName, rootFlags.Namespace, err)
	}
	githubLogin := string(secret.Data[constants.KeyGithubLogin])

	if !strings.EqualFold(prAuthor, githubLogin) {
		return fmt.Errorf("PR is not owned by the factory user (%s). It is owned by %s. Please run 'factory pr adopt <open|close> --pr-url %s' to adopt it first", githubLogin, prAuthor, prURL)
	}

	return nil
}

type AdoptFlags struct {
	PRURL    string
	Strategy string
}

func NewAdoptCommand(ctx context.Context) *cobra.Command {
	var flags AdoptFlags

	cmd := &cobra.Command{
		Use:   "adopt <open|close>",
		Short: "Adopt a third-party PR under the bot user identity in a sandbox",
		Args:  cobra.ExactArgs(1),
		Example: `  # Adopt a PR and comment on it, leaving it open
  factory pr adopt open --pr-url https://github.com/owner/repo/pull/1

  # Adopt a PR and comment/close it
  factory pr adopt close --pr-url https://github.com/owner/repo/pull/1 --strategy reimplement`,
		RunE: func(cmd *cobra.Command, args []string) error {
			_, err := ResolveRootFlags(cmd)
			if err != nil {
				return err
			}

			adoptAction := args[0]
			if adoptAction != "open" && adoptAction != "close" {
				return fmt.Errorf("invalid argument '%s'. Must be one of [open, close]", adoptAction)
			}

			if flags.PRURL == "" {
				return fmt.Errorf("--pr-url is required")
			}

			if flags.Strategy != "reuse" && flags.Strategy != "reimplement" {
				return fmt.Errorf("invalid strategy '%s'. Must be one of [reuse, reimplement]", flags.Strategy)
			}

			// The adopt recipes (recipe/recipes/adopt*.yaml): reuse takes the
			// PR's commits, reimplement has the agent redo them; open-pr
			// opens the adopted PR and comments on (and with close,
			// closes) the original.
			if err := refuseOwnPR(ctx, flags.PRURL); err != nil {
				return err
			}
			recipeName := "adopt"
			if flags.Strategy == "reimplement" {
				recipeName = "adopt-reimplement"
			}
			return runRecipe(ctx, recipeName, flags.PRURL, "", "", "", false,
				applyMode{on: true, verb: "open-pr", asUser: rootFlags.User != ""},
				map[string]string{"close": strconv.FormatBool(adoptAction == "close")}, nil)
		},
	}

	cmd.Flags().StringVar(&flags.PRURL, "pr-url", "", "GitHub PR URL (e.g. https://github.com/owner/repo/pull/123)")
	cmd.Flags().StringVar(&flags.Strategy, "strategy", "reuse", "Adoption strategy: 'reuse' (git-based) or 'reimplement' (LLM-based)")

	return cmd
}

// refuseOwnPR refuses to adopt a PR the adopting account (the secret the
// recipe runs with) already authored: there is nothing to adopt.
func refuseOwnPR(ctx context.Context, prURL string) error {
	it, err := parseGitHubItemURL(prURL)
	if err != nil || !it.IsPR {
		return fmt.Errorf("expected a PR URL (https://github.com/owner/repo/pull/123), got %s", prURL)
	}
	ghClient, err := github.NewClient(ctx)
	if err != nil {
		return fmt.Errorf("creating github client: %w", err)
	}
	pr, _, err := ghClient.PullRequests.Get(ctx, it.Owner, it.Repo, it.Number)
	if err != nil {
		return fmt.Errorf("fetching PR #%d: %w", it.Number, err)
	}
	kubeClient, err := clients.NewKubernetesClient()
	if err != nil {
		return fmt.Errorf("creating k8s client: %w", err)
	}
	secret, err := kubeClient.Clientset.CoreV1().Secrets(rootFlags.Namespace).Get(ctx, rootFlags.SecretName, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("fetching %s secret in namespace %s: %w (make sure to run 'factory user onboard' first)", rootFlags.SecretName, rootFlags.Namespace, err)
	}
	if login := string(secret.Data[constants.KeyGithubLogin]); strings.EqualFold(pr.GetUser().GetLogin(), login) {
		return fmt.Errorf("PR is already owned by the factory user (%s)", login)
	}
	return nil
}
