package commands

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/clients"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/commands/common"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/constants"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/envd"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/github"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/k8s"
	factorysandbox "github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/sandbox"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/tasks"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/usagereport"
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
	cmd.AddCommand(NewIterateCommand(ctx))
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

type IterateFlags struct {
	PRURL           string
	Prompt          string
	ContinueSession bool
}

func NewIterateCommand(ctx context.Context) *cobra.Command {
	var flags IterateFlags

	cmd := &cobra.Command{
		Use:   "iterate",
		Short: "Iterate on code / resolve merge conflicts for a GitHub pull request in a sandbox",
		Example: `  # Iterate on PR / rebase PR
  factory pr iterate --pr-url https://github.com/owner/repo/pull/1 --prompt "Please rebase onto latest master"`,
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

			sessionName := "factory-pr-unknown-iterate"
			u, err := url.Parse(flags.PRURL)
			if err == nil {
				parts := strings.Split(strings.TrimPrefix(u.Path, "/"), "/")
				if len(parts) >= 4 && parts[2] == "pull" {
					sessionName = fmt.Sprintf("factory-pr-%s-iterate", parts[3])
				}
			}

			if rootFlags.Background {
				ran, err := checkAndRunInBackground(sessionName)
				if err != nil {
					return err
				}
				if ran {
					return nil // Parent exits
				}
			}

			ctx, cancel := context.WithTimeout(ctx, rootFlags.Timeout)
			defer cancel()
			return runIterate(ctx, flags.PRURL, flags.Prompt, flags.ContinueSession, rootFlags.EphemeralStorage, rootFlags.ResolvedSecrets)
		},
	}

	cmd.Flags().StringVar(&flags.PRURL, "pr-url", "", "GitHub PR URL (e.g. https://github.com/owner/repo/pull/123)")
	cmd.Flags().StringVar(&flags.Prompt, "prompt", "Resolve merge conflicts and iterate on code", "Custom prompt for the iterate task")
	cmd.Flags().BoolVar(&flags.ContinueSession, "continue-session", false, "Continue the Gemini session from previous runs in the sandbox")

	return cmd
}

func runIterate(ctx context.Context, prURL, prompt string, continueSession bool, ephemeralStorage string, secrets []factorysandbox.SecretMount) error {
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

	cloneURL := pr.GetBase().GetRepo().GetCloneURL()
	fmt.Printf("Ensuring review sandbox for PR #%d...\n", prNum)
	sandboxName, err := factorysandbox.EnsureReviewSandbox(ctx, kubeClient, rootFlags.Namespace, prNum, pr.GetTitle(), pr.GetHTMLURL(), pr.GetDiffURL(), cloneURL, rootFlags.Image, rootFlags.DiskSize, rootFlags.StorageClass, ephemeralStorage, secrets, rootFlags.ResolvedEnvs, rootFlags.User)
	if err != nil {
		return fmt.Errorf("ensuring review sandbox: %w", err)
	}

	secret, err := kubeClient.Clientset.CoreV1().Secrets(rootFlags.Namespace).Get(ctx, rootFlags.SecretName, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("fetching %s secret in namespace %s: %w (make sure to run 'factory user onboard' first)", rootFlags.SecretName, rootFlags.Namespace, err)
	}
	githubLogin := string(secret.Data[constants.KeyGithubLogin])
	githubEmail := string(secret.Data[constants.KeyGithubEmail])

	params := tasks.IterateParams{
		Repo: tasks.Repo{
			CloneURL: cloneURL,
		},
		Instruction: prompt,
		Branch:      pr.GetHead().GetRef(),
		PRNumber:    prNum,
		Models:      tasks.DefaultModels,
	}

	scriptBytes, err := tasks.GetIterateScript()
	if err != nil {
		return fmt.Errorf("getting iterate script: %w", err)
	}

	promptBytes, err := tasks.RenderIteratePrompt(params)
	if err != nil {
		return fmt.Errorf("rendering iterate prompt: %w", err)
	}

	fmt.Printf("Connecting to sandbox %s via envd...\n", sandboxName)
	client, err := envd.Connect(ctx, rootFlags.Namespace, sandboxName)
	if err != nil {
		return fmt.Errorf("connecting to sandbox: %w", err)
	}
	defer client.Close()

	taskDir := fmt.Sprintf("/workspaces/tasks/iterate-%s", time.Now().Format("20060102-150405"))
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
		"HOME":                       "/workspaces/.home",
		"GITHUB_TOKEN":               string(secret.Data[constants.KeyGithubToken]),
		"GEMINI_CLI_TRUST_WORKSPACE": "true",
		"REPO_OWNER":                 owner,
		"REPO_NAME":                  repo,
		"CLONE_URL":                  cloneURL,
		"PROMPT_FILE":                promptPath,
		"GITHUB_USER_ID":             githubLogin,
		"GITHUB_USER_EMAIL":          githubEmail,
		"GITHUB_USER_NAME":           githubLogin,
		"PR_NUMBER":                  strconv.Itoa(prNum),
		"BRANCH_NAME":                pr.GetHead().GetRef(),
		"GEMINI_CONTINUE_SESSION":    strconv.FormatBool(continueSession),
	}
	if err := applyEngineEnv(envMap, secret); err != nil {
		return err
	}

	fmt.Println("Running iterate task via envd...")
	cmdStr := fmt.Sprintf("bash -c 'set -o pipefail; bash %s'", scriptPath)
	_ = factorysandbox.MarkSandboxTaskRunning(ctx, kubeClient, rootFlags.Namespace, sandboxName, "iterate", rootFlags.Engine)
	if err := client.RunTaskResilient(ctx, cmdStr, envMap, taskDir, rootFlags.Detached, rootFlags.AbortOnCancel); err != nil {
		_ = factorysandbox.UpdateSandboxTaskAnnotation(ctx, kubeClient, rootFlags.Namespace, sandboxName, "iterate", "Failed")
		return fmt.Errorf("running task: %w", err)
	}
	if rootFlags.Detached {
		return nil
	}
	_ = factorysandbox.UpdateSandboxTaskAnnotation(ctx, kubeClient, rootFlags.Namespace, sandboxName, "iterate", "Completed")

	usagereport.HarvestTask(ctx, client, taskDir, usagereport.Meta{
		Repo:     owner + "/" + repo,
		TaskType: "iterate",
		Sandbox:  sandboxName,
		PR:       prNum,
		Issues:   common.ReferencedIssueList(pr),
	})
	usagereport.ReportPRSubject(ctx, owner+"/"+repo, pr)

	fmt.Println("\nIterate execution completed.")
	return nil
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

			return runAdopt(ctx, flags.PRURL, adoptAction, flags.Strategy, rootFlags.EphemeralStorage, rootFlags.ResolvedSecrets)
		},
	}

	cmd.Flags().StringVar(&flags.PRURL, "pr-url", "", "GitHub PR URL (e.g. https://github.com/owner/repo/pull/123)")
	cmd.Flags().StringVar(&flags.Strategy, "strategy", "reuse", "Adoption strategy: 'reuse' (git-based) or 'reimplement' (LLM-based)")

	return cmd
}

func runAdopt(ctx context.Context, prURL, adoptAction, strategy string, ephemeralStorage string, secrets []factorysandbox.SecretMount) error {
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

	secret, err := kubeClient.Clientset.CoreV1().Secrets(rootFlags.Namespace).Get(ctx, rootFlags.SecretName, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("fetching %s secret in namespace %s: %w (make sure to run 'factory user onboard' first)", rootFlags.SecretName, rootFlags.Namespace, err)
	}
	githubLogin := string(secret.Data[constants.KeyGithubLogin])
	githubEmail := string(secret.Data[constants.KeyGithubEmail])

	prAuthor := pr.GetUser().GetLogin()
	if strings.EqualFold(prAuthor, githubLogin) {
		return fmt.Errorf("PR is already owned by the factory user (%s)", githubLogin)
	}

	sandboxName := fmt.Sprintf("adopt-%s-%d", repo, prNum)
	cloneURL := fmt.Sprintf("https://github.com/%s/%s.git", owner, repo)

	fmt.Printf("Ensuring adopt sandbox '%s'...\n", sandboxName)
	sandboxName, err = factorysandbox.EnsureAdoptSandbox(ctx, kubeClient, rootFlags.Namespace, repo, prNum, cloneURL, prURL, rootFlags.Image, rootFlags.DiskSize, rootFlags.StorageClass, ephemeralStorage, secrets, rootFlags.ResolvedEnvs, rootFlags.User)
	if err != nil {
		return fmt.Errorf("ensuring adopt sandbox: %w", err)
	}

	fmt.Printf("Connecting to sandbox %s via envd...\n", sandboxName)
	client, err := envd.Connect(ctx, rootFlags.Namespace, sandboxName)
	if err != nil {
		return fmt.Errorf("connecting to sandbox: %w", err)
	}
	defer client.Close()

	taskDir := fmt.Sprintf("/workspaces/tasks/adopt-%s-%s", strategy, time.Now().Format("20060102-150405"))
	promptPath := fmt.Sprintf("%s/adopt-prompt.txt", taskDir)
	scriptPath := fmt.Sprintf("%s/pre-script.sh", taskDir)

	var promptBytes []byte
	var prDiff string

	if strategy == "reimplement" {
		fmt.Println("Downloading PR diff to construct prompt...")
		diffURL := pr.GetDiffURL()
		req, err := http.NewRequestWithContext(ctx, "GET", diffURL, nil)
		if err != nil {
			return fmt.Errorf("creating request for PR diff: %w", err)
		}
		req.Header.Set("Authorization", "Bearer "+string(secret.Data[constants.KeyGithubToken]))

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return fmt.Errorf("fetching PR diff: %w", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("failed to fetch PR diff: status code %d", resp.StatusCode)
		}
		var diffBuf bytes.Buffer
		if _, err := io.Copy(&diffBuf, resp.Body); err != nil {
			return fmt.Errorf("reading PR diff response: %w", err)
		}
		prDiff = diffBuf.String()
	}

	params := tasks.AdoptParams{
		RepoOwner: owner,
		RepoName:  repo,
		CloneURL:  cloneURL,
		PRNumber:  prNum,
		PRURL:     prURL,
		AdoptFlag: adoptAction,
		Strategy:  strategy,
		Title:     pr.GetTitle(),
		Body:      pr.GetBody(),
		Diff:      prDiff,
		Models:    tasks.DefaultModels,
	}

	promptBytes, err = tasks.RenderAdoptPrompt(params)
	if err != nil {
		return fmt.Errorf("rendering adopt prompt: %w", err)
	}

	scriptBytes, err := tasks.GetAdoptScript()
	if err != nil {
		return fmt.Errorf("getting adopt script: %w", err)
	}

	fmt.Println("Writing prompt and script into sandbox...")
	if err := client.WriteFile(ctx, promptPath, promptBytes); err != nil {
		return fmt.Errorf("writing prompt: %w", err)
	}
	if err := client.WriteFile(ctx, scriptPath, scriptBytes); err != nil {
		return fmt.Errorf("writing script: %w", err)
	}

	envMap := map[string]string{
		"HOME":                       "/workspaces/.home",
		"GITHUB_TOKEN":               string(secret.Data[constants.KeyGithubToken]),
		"GEMINI_CLI_TRUST_WORKSPACE": "true",
		"REPO_OWNER":                 owner,
		"REPO_NAME":                  repo,
		"CLONE_URL":                  cloneURL,
		"PROMPT_FILE":                promptPath,
		"GITHUB_USER_ID":             githubLogin,
		"GITHUB_USER_EMAIL":          githubEmail,
		"GITHUB_USER_NAME":           githubLogin,
		"PR_NUMBER":                  strconv.Itoa(prNum),
		"PR_URL":                     prURL,
		"ADOPT_FLAG":                 adoptAction,
		"STRATEGY":                   strategy,
	}
	if err := applyEngineEnv(envMap, secret); err != nil {
		return err
	}

	fmt.Println("Running adopt task via envd...")
	cmdStr := fmt.Sprintf("bash -c 'set -o pipefail; bash %s'", scriptPath)
	_ = factorysandbox.MarkSandboxTaskRunning(ctx, kubeClient, rootFlags.Namespace, sandboxName, "adopt", rootFlags.Engine)
	if err := client.RunTaskResilient(ctx, cmdStr, envMap, taskDir, rootFlags.Detached, rootFlags.AbortOnCancel); err != nil {
		_ = factorysandbox.UpdateSandboxTaskAnnotation(ctx, kubeClient, rootFlags.Namespace, sandboxName, "adopt", "Failed")
		return fmt.Errorf("running task: %w", err)
	}
	if rootFlags.Detached {
		return nil
	}
	_ = factorysandbox.UpdateSandboxTaskAnnotation(ctx, kubeClient, rootFlags.Namespace, sandboxName, "adopt", "Completed")

	usagereport.HarvestTask(ctx, client, taskDir, usagereport.Meta{
		Repo:     owner + "/" + repo,
		TaskType: "adopt",
		Sandbox:  sandboxName,
		PR:       prNum,
	})
	usagereport.ReportPRSubject(ctx, owner+"/"+repo, pr)

	fmt.Println("\nAdopt execution completed.")

	var buf bytes.Buffer
	var createdPRURL string
	if err := client.Exec(ctx, fmt.Sprintf("cat %s/agent-output.txt", taskDir), "/workspaces", nil, nil, &buf, os.Stderr); err != nil {
		klog.Warningf("Could not read agent-output.txt: %v", err)
	} else {
		for _, line := range strings.Split(buf.String(), "\n") {
			line = strings.TrimSpace(line)
			if strings.Contains(line, "/pull/") {
				createdPRURL = line
				break
			}
		}
	}

	if createdPRURL != "" {
		fmt.Printf("\nSuccessfully adopted PR! New PR URL: %s\n", createdPRURL)
		parts := strings.Split(createdPRURL, "/")
		if len(parts) > 0 {
			if createdPRNum, err := strconv.Atoi(parts[len(parts)-1]); err == nil {
				fmt.Printf("Aliasing sandbox %s to PR #%d...\n", sandboxName, createdPRNum)
				if err := factorysandbox.AliasSandboxToPR(ctx, kubeClient, rootFlags.Namespace, sandboxName, createdPRNum, createdPRURL); err != nil {
					klog.Warningf("Failed to alias sandbox to PR #%d: %v", createdPRNum, err)
				}
			}
		}
	} else {
		fmt.Printf("\nAdopted PR output:\n%s\n", buf.String())
	}

	return nil
}
