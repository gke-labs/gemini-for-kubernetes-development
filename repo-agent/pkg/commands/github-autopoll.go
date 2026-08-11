package commands

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/clients"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/github"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/sandbox"
	gogithub "github.com/google/go-github/v39/github"
	"github.com/spf13/cobra"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/klog/v2"
)

// GithubAutopollOptions holds options for the autopoll function.
type GithubAutopollOptions struct {
	Repos        []string
	Allowlist    []string
	PollInterval time.Duration
	AssignedTo   string
}

// BuildGithubAutopollCommand creates a new cobra command for autopolling github issues
func BuildGithubAutopollCommand() *cobra.Command {
	var opt GithubAutopollOptions

	cmd := &cobra.Command{
		Use:   "github-autopoll",
		Short: "Continuously poll GitHub for issues assigned to a bot and automatically create sandboxes",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) != 0 {
				return fmt.Errorf("command does not take positional arguments")
			}

			return RunGithubAutopoll(cmd.Context(), opt)
		},
	}

	cmd.Flags().StringSliceVar(&opt.Repos, "repos", nil, "GitHub repositories to monitor (e.g., GoogleCloudPlatform/k8s-config-connector)")
	cmd.Flags().StringSliceVar(&opt.Allowlist, "allowlist", opt.Allowlist, "Comma-separated list of GitHub users whose issues will be processed")
	cmd.Flags().DurationVar(&opt.PollInterval, "poll-interval", 60*time.Second, "How often to poll GitHub")
	cmd.Flags().StringVar(&opt.AssignedTo, "assigned-to", "codebot-robot", "GitHub user to check for assigned issues")

	return cmd
}

// RunGithubAutopoll continuously polls GitHub for issues and creates sandboxes as needed.
func RunGithubAutopoll(ctx context.Context, opt GithubAutopollOptions) error {
	log := klog.FromContext(ctx)

	if len(opt.Repos) == 0 {
		return fmt.Errorf("--repos is required (e.g., --repos=GoogleCloudPlatform/k8s-config-connector)")
	}

	if len(opt.Allowlist) == 0 {
		return fmt.Errorf("--allowlist is required (e.g., --allowlist=user1,user2)")
	}

	githubAPI, err := github.NewClient(ctx)
	if err != nil {
		return fmt.Errorf("failed to create github client: %w", err)
	}

	kube, err := clients.NewKubernetesClient()
	if err != nil {
		return fmt.Errorf("failed to create kubernetes client: %w", err)
	}

	// Convert allowlist to map for faster lookups
	allowlistMap := make(map[string]bool)
	for _, user := range opt.Allowlist {
		allowlistMap[strings.TrimSpace(user)] = true
	}

	log.Info("Starting GitHub autopoll", "repos", opt.Repos, "allowlist", opt.Allowlist, "assignedTo", opt.AssignedTo, "pollInterval", opt.PollInterval)

	ticker := time.NewTicker(opt.PollInterval)
	defer ticker.Stop()

	// Track processed issues to avoid reprocessing in the same run
	poller := &AutoPoller{
		githubAPI:       githubAPI,
		kube:            kube,
		opt:             opt,
		allowlistMap:    allowlistMap,
		processedIssues: make(map[string]*Info),
	}

	// Do an initial poll immediately
	if err := poller.pollOnce(ctx); err != nil {
		log.Error(err, "error during initial poll")
	}

	for {
		select {
		case <-ctx.Done():
			log.Info("Context cancelled, stopping autopoll")
			return ctx.Err()
		case <-ticker.C:
			if err := poller.pollOnce(ctx); err != nil {
				log.Error(err, "error during poll")
			}
		}
	}
}

type AutoPoller struct {
	githubAPI        *github.Client
	kube             *clients.KubernetesClient
	opt              GithubAutopollOptions
	allowlistMap     map[string]bool
	processedIssues  map[string]*Info
	findSandboxPodFn func(ctx context.Context, name string) (*types.NamespacedName, error)
}

func (p *AutoPoller) findSandboxPod(ctx context.Context, name string) (*types.NamespacedName, error) {
	if p.findSandboxPodFn != nil {
		return p.findSandboxPodFn(ctx, name)
	}
	return sandbox.FindSandboxPod(ctx, name)
}

type Info struct {
	Reason string
}

func (p *AutoPoller) pollOnce(ctx context.Context) error {
	log := klog.FromContext(ctx)

	for _, repoStr := range p.opt.Repos {
		repo, err := github.ParseRepo(repoStr)
		if err != nil {
			log.Error(err, "failed to parse repo", "repo", repoStr)
			continue
		}

		log.V(2).Info("Polling repository", "repo", repoStr)

		// Query GitHub for issues assigned to the bot
		issues, _, err := p.githubAPI.Issues.ListByRepo(ctx, repo.Owner, repo.Name, &gogithub.IssueListByRepoOptions{
			State:     "open",
			Assignee:  p.opt.AssignedTo,
			Sort:      "updated",
			Direction: "desc",
		})
		if err != nil {
			return fmt.Errorf("failed to list issues for %s: %w", repoStr, err)
		}

		log.V(2).Info("Found issues assigned to bot", "repo", repoStr, "count", len(issues))

		for _, issue := range issues {
			if issue.PullRequestLinks != nil {
				prID := issue.GetNumber()
				prKey := fmt.Sprintf("%s/%s#%d", repo.Owner, repo.Name, prID)

				// Skip if already processed in this run
				if info := p.processedIssues[prKey]; info != nil {
					log.V(2).Info("Skipping pull request, already processed", "pr", prKey, "reason", info.Reason)
					continue
				}

				// Check if author is in allowlist (or if it's the bot itself)
				author := issue.GetUser().GetLogin()
				if author != p.opt.AssignedTo && !p.allowlistMap[author] {
					log.Info("Skipping pull request, author not in allowlist", "pr", prKey, "author", author)
					continue
				}

				log.Info("Checking pull request for processing", "pr", prKey, "author", author)

				// Find the sandbox for the PR
				sandboxName, err := p.findSandboxForPR(ctx, repo, prID)
				if err != nil {
					log.Error(err, "error finding sandbox for pull request", "pr", prKey)
					p.processedIssues[prKey] = &Info{Reason: fmt.Sprintf("no sandbox found: %v", err)}
					continue
				}

				log.Info("Processing pull request", "pr", prKey, "sandbox", sandboxName)

				// Mark as processed
				p.processedIssues[prKey] = &Info{Reason: "processed"}

				// Create the PR URL and invoke github-feedback logic
				prURL := fmt.Sprintf("https://github.com/%s/%s/pull/%d", repo.Owner, repo.Name, prID)

				go func(prURL string, prID int, prKey string, sandboxName string) {
					log.Info("Removing bot assignment from pull request", "pr", prKey)
					if _, _, err := p.githubAPI.Issues.RemoveAssignees(ctx, repo.Owner, repo.Name, prID, []string{p.opt.AssignedTo}); err != nil {
						log.Error(err, "failed to remove bot assignment from pull request", "pr", prKey)
					}

					feedbackCmd := GithubFeedbackCommand{
						URL:             prURL,
						PullRequestID:   prID,
						Sandbox:         sandboxName,
						InPod:           false,
						GithubUserLogin: "codebot-robot",
						GithubUserEmail: "codebot-robot@google.com",
						GithubUserName:  "codebot-robot",
						GithubUserToken: os.Getenv("CODEBOT_ROBOT_GITHUB_TOKEN"),
					}
					feedbackCmd.InitDefaults()

					if err := feedbackCmd.Run(ctx); err != nil {
						log.Error(err, "failed to process pull request feedback", "pr", prKey)
					}
				}(prURL, prID, prKey, sandboxName)

				continue
			}

			issueKey := fmt.Sprintf("%s/%s#%d", repo.Owner, repo.Name, issue.GetNumber())

			// Skip if already processed in this run
			if info := p.processedIssues[issueKey]; info != nil {
				log.V(2).Info("Skipping issue, already processed", "issue", issueKey, "reason", info.Reason)
				continue
			}

			// Check if issue author is in allowlist
			author := issue.GetUser().GetLogin()
			if !p.allowlistMap[author] {
				log.Info("Skipping issue, author not in allowlist", "issue", issueKey, "author", author)
				continue
			}

			log.Info("Checking issue for processing", "issue", issueKey, "author", author)

			// Check if issue should be processed
			shouldProcess, reason, err := p.shouldProcessIssue(ctx, repo, issue)
			if err != nil {
				log.Error(err, "error checking if issue should be processed", "issue", issueKey)
				continue
			}
			if !shouldProcess {
				log.Info("Skipping issue", "issue", issueKey, "reason", reason)
				p.processedIssues[issueKey] = &Info{Reason: reason}
				continue
			}

			log.Info("Processing issue", "issue", issueKey)

			// Mark as processed
			p.processedIssues[issueKey] = &Info{Reason: "processed"}

			// Create the issue URL and invoke github-fix-issue logic
			issueURL := fmt.Sprintf("https://github.com/%s/%s/issues/%d", repo.Owner, repo.Name, issue.GetNumber())

			go func(issueURL string) {
				// TODO (barney-s): set additional fields
				fixIssue := GithubFixIssueCommand{
					URL:             issueURL,
					InPod:           false,
					GithubUserLogin: "codebot-robot",
					GithubUserEmail: "codebot-robot@google.com",
					GithubUserName:  "codebot-robot",
					GithubUserToken: os.Getenv("CODEBOT_ROBOT_GITHUB_TOKEN"),
				}
				// fixIssue.InitDefaults()

				if err := fixIssue.Run(ctx); err != nil {
					log.Error(err, "failed to process issue", "issue", issueKey)
					// Don't return error, continue processing other issues
				}
			}(issueURL)
		}
	}

	return nil
}

// shouldProcessIssue checks if an issue should be processed based on:
// 1. Whether a PR is already linked
// 2. Whether a sandbox already exists
func (p *AutoPoller) shouldProcessIssue(ctx context.Context, repo *github.Repo, issue *gogithub.Issue) (bool, string, error) {
	log := klog.FromContext(ctx)

	// Check if a PR is linked to this issue
	linkedPR, err := hasLinkedPR(ctx, p.githubAPI, repo, issue)
	if err != nil {
		return false, "", fmt.Errorf("failed to check for linked PR: %w", err)
	}

	for _, pr := range linkedPR {
		prData, _, err := p.githubAPI.PullRequests.Get(ctx, pr.Repo.Owner, pr.Repo.Name, pr.PullRequestNumber)
		if err != nil {
			return false, "", fmt.Errorf("error fetching linked PR data: %v", err)
		}
		switch prData.GetState() {
		case "open":
			return false, fmt.Sprintf("issue has an open linked PR %v", prData.GetHTMLURL()), nil
		}
	}

	// Check if a sandbox already exists for this issue
	sandboxName := fmt.Sprintf("github-%s-%s-%d", repo.Owner, repo.Name, issue.GetNumber())
	sandboxName = strings.ToLower(sandboxName)

	podID, err := p.findSandboxPod(ctx, sandboxName)
	if err != nil {
		log.Error(err, "failed to check for existing sandbox", "sandboxName", sandboxName)
		// If we can't check, skip this issue for now
		return false, "", fmt.Errorf("error checking sandbox: %v", err)
	}
	if podID != nil {
		return false, "sandbox already exists for this issue", nil
	}

	return true, "", nil
}

// hasLinkedPR checks if the issue has any linked pull requests
func hasLinkedPR(ctx context.Context, githubAPI *github.Client, repo *github.Repo, issue *gogithub.Issue) ([]*github.PullRequestRef, error) {
	// Use the timeline API to check for linked PRs
	// GitHub's timeline API shows cross-references including linked PRs
	timeline, _, err := githubAPI.Issues.ListIssueTimeline(ctx, repo.Owner, repo.Name, issue.GetNumber(), nil)
	if err != nil {
		return nil, fmt.Errorf("failed to get issue timeline: %w", err)
	}

	var prs []*github.PullRequestRef

	for _, event := range timeline {
		// Check for cross-referenced events that link to PRs
		if event.GetEvent() == "cross-referenced" && event.Source != nil {
			if event.Source.Issue != nil {
				// We're looking for a PR, not another issue
				if event.GetSource().GetType() == "issue" {
					continue
				}
				if event.Source.Issue.PullRequestLinks != nil {
					u := event.Source.Issue.GetHTMLURL()
					parsedPR, err := github.ParsePullRequestURL(u)
					if err != nil {
						return nil, fmt.Errorf("failed to parse linked PR URL %q: %w", u, err)
					}
					prs = append(prs, parsedPR)
				}
			}
		}
		// Also check for connected events (newer GitHub feature for linking issues/PRs)
		if event.GetEvent() == "connected" {
			klog.Infof("Found connected event (not yet handled in hasLinkedPR): %+v", event)
			return nil, fmt.Errorf("connected events not yet supported in hasLinkedPR")
		}
	}

	return prs, nil
}

func ValueOf[T any](ptr *T) T {
	if ptr == nil {
		var zero T
		return zero
	}
	return *ptr
}

func (p *AutoPoller) findSandboxForPR(ctx context.Context, repo *github.Repo, prID int) (string, error) {
	log := klog.FromContext(ctx)

	// 1. Try sandbox name with PR number first: github-<owner>-<repo>-<prID>
	nameWithPR := fmt.Sprintf("github-%s-%s-%d", repo.Owner, repo.Name, prID)
	nameWithPR = strings.ToLower(nameWithPR)
	podID, err := p.findSandboxPod(ctx, nameWithPR)
	if err == nil && podID != nil {
		log.V(2).Info("Found sandbox named after PR", "sandbox", nameWithPR, "pr", prID)
		return nameWithPR, nil
	}

	// 2. If not found, look for linked issues in the timeline
	log.V(2).Info("Sandbox with PR number not found, checking timeline for linked issues", "pr", prID)
	timeline, _, err := p.githubAPI.Issues.ListIssueTimeline(ctx, repo.Owner, repo.Name, prID, nil)
	if err != nil {
		return "", fmt.Errorf("failed to get pull request timeline to find sandbox: %w", err)
	}

	for _, event := range timeline {
		// Look for cross-referenced issues
		if event.GetEvent() == "cross-referenced" && event.Source != nil {
			if event.Source.Issue != nil {
				// Ensure it is indeed an issue, not a pull request
				if event.GetSource().GetType() == "issue" && event.Source.Issue.PullRequestLinks == nil {
					issueNum := event.Source.Issue.GetNumber()
					nameWithIssue := fmt.Sprintf("github-%s-%s-%d", repo.Owner, repo.Name, issueNum)
					nameWithIssue = strings.ToLower(nameWithIssue)
					podID, err = p.findSandboxPod(ctx, nameWithIssue)
					if err == nil && podID != nil {
						log.Info("Found sandbox named after linked issue", "sandbox", nameWithIssue, "pr", prID, "issue", issueNum)
						return nameWithIssue, nil
					}
				}
			}
		}
	}

	return "", fmt.Errorf("no existing sandbox found for pull request #%d", prID)
}
