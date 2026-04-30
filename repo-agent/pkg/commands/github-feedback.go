package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	reviewv1alpha1 "github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/api/repowatch/v1alpha1"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/github"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/sandbox"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/tasks"
	"github.com/spf13/cobra"
	"k8s.io/klog/v2"
)

// GithubFeedbackCommand holds options for the RunCode function.
type GithubFeedbackCommand struct {
	URL             string
	PullRequestID   int
	Sandbox         string
	AgentName       string
	GithubUserLogin string
	GithubUserEmail string
	GithubUserName  string
	GithubUserToken string
	InPod           bool
	WorkspaceDir    string
	TaskDir         string
	Model           string
	ExtensionsJSON  string

	// loaded objects
	issue         *github.Issue
	repo          *github.Repository
	pullRequest   *github.PullRequest
	repoCommits   []github.RepositoryCommit
	issueComments []github.IssueComment
	prComments    []github.PullRequestComment
	prReviews     []github.PullRequestReview
	user          *github.User
	sandbox       *sandbox.IssueSandbox
	sandboxID     string
}

// BuildGithubFeedbackCommand creates a new cobra command for using a dev sandbox to address github feedback
func BuildGithubFeedbackCommand() *cobra.Command {
	c := GithubFeedbackCommand{}

	cmd := &cobra.Command{
		Use:   "github-feedback",
		Short: "Address github pull request feedback using an LLM in a dev sandbox",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) != 0 {
				return fmt.Errorf("command does not take positional arguments")
			}
			c.InitDefaults()
			if c.PullRequestID == 0 {
				return fmt.Errorf("--pull-request is required")
			}
			if !c.InPod && c.Sandbox == "" {
				return fmt.Errorf("--sandbox is required")
			}
			return c.Run(cmd.Context())
		},
	}

	cmd.Flags().StringVar(&c.Sandbox, "sandbox", "", "Name of existing sandbox to reuse")
	cmd.Flags().IntVar(&c.PullRequestID, "pull-request", 0, "GitHub pull request number")
	cmd.Flags().StringVar(&c.URL, "issue-url", os.Getenv("ISSUE_URL"), "GitHub issue URL")
	cmd.Flags().StringVar(&c.AgentName, "agent-name", os.Getenv("AGENT_NAME"), "Agent name")
	cmd.Flags().StringVar(&c.GithubUserLogin, "github-user-login", os.Getenv("GITHUB_USER_LOGIN"), "Github user login")
	cmd.Flags().StringVar(&c.GithubUserEmail, "github-user-email", os.Getenv("GITHUB_USER_EMAIL"), "Github user email")
	cmd.Flags().StringVar(&c.GithubUserName, "github-user-name", os.Getenv("GITHUB_USER_NAME"), "Github user name")
	cmd.Flags().StringVar(&c.Model, "model", os.Getenv("MODEL"), "Model to use")
	cmd.Flags().StringVar(&c.ExtensionsJSON, "extensions", os.Getenv("AGENT_LLM_EXTENSIONS"), "Extensions JSON")
	cmd.Flags().BoolVar(&c.InPod, "in-pod", false, "Whether running inside the pod")

	return cmd
}

func (c *GithubFeedbackCommand) InitDefaults() {
	if c.AgentName == "" {
		c.AgentName = "gemini-cli"
	}

	if c.WorkspaceDir == "" {
		c.WorkspaceDir = "/workspaces"
	}
	if c.TaskDir == "" {
		c.TaskDir = os.Getenv("TASKDIR")
	}
	if c.TaskDir == "" {
		c.TaskDir = c.WorkspaceDir
	}

	if c.Model == "" {
		c.Model = "gemini-3.1-pro-preview"
	}

	if c.PullRequestID == 0 {
		c.PullRequestID = GetPullRequestIDFromEnv()
	}
}

func (c *GithubFeedbackCommand) taskPath(name string, args ...interface{}) string {
	file := fmt.Sprintf(name, args...)
	return filepath.Join(c.TaskDir, file)
}

func (c *GithubFeedbackCommand) loadGithubObjects(ctx context.Context) error {
	token, err := github.GetGithubToken(ctx)
	if err != nil {
		return err
	}
	c.GithubUserToken = token

	githubAPI, err := github.NewClient(context.Background())
	if err != nil {
		return err
	}

	c.repo, err = githubAPI.GetRepositoryFromIssueURL(ctx, c.URL)
	if err != nil {
		return err
	}

	if c.URL != "" {
		c.issue, err = githubAPI.GetIssue(ctx, c.URL, true)
		if err != nil {
			return err
		}
	}

	c.pullRequest, err = githubAPI.GetPullRequest(ctx, c.repo.Owner(), c.repo.Name(), c.PullRequestID)
	if err != nil {
		return fmt.Errorf("failed to get github pull request: %w", err)
	}

	c.repoCommits, err = githubAPI.GetPullRequestCommits(ctx, c.repo.Owner(), c.repo.Name(), c.PullRequestID)
	if err != nil {
		return fmt.Errorf("failed to list github pull request commits: %w", err)
	}

	// General issue comments (conversation)
	c.issueComments, err = githubAPI.GetIssueCommentsByNumber(ctx, c.repo.Owner(), c.repo.Name(), c.PullRequestID)
	if err != nil {
		return fmt.Errorf("failed to list github issue comments: %w", err)
	}
	sort.Slice(c.issueComments, func(i, j int) bool {
		return c.issueComments[i].CreatedAt().Before(c.issueComments[j].CreatedAt())
	})

	// Code comments (associated with reviews)
	c.prComments, err = githubAPI.GetPullRequestComments(ctx, c.repo.Owner(), c.repo.Name(), c.PullRequestID)
	if err != nil {
		return fmt.Errorf("failed to list github pull request code comments: %w", err)
	}

	// Reviews
	c.prReviews, err = githubAPI.GetPullRequestReviews(ctx, c.repo.Owner(), c.repo.Name(), c.PullRequestID)
	if err != nil {
		return fmt.Errorf("failed to list github pull request reviews: %w", err)
	}

	github.MapPRCommentsToReview(c.prComments, c.prReviews)

	sort.Slice(c.prReviews, func(i, j int) bool {
		return c.prReviews[i].SubmittedAt().Before(c.prReviews[j].SubmittedAt())
	})

	c.user = &github.User{
		UserID: c.GithubUserLogin,
		Email:  c.GithubUserEmail,
		Name:   c.GithubUserName,
		Token:  c.GithubUserToken,
	}

	return nil
}

func (c *GithubFeedbackCommand) loadSandbox(ctx context.Context) error {
	if c.InPod {
		var err error
		sb, err := sandbox.NewIssueSandbox(ctx, c.InPod, c.repo, c.issue, "")
		if err != nil {
			return err
		}
		c.sandbox = sb
		c.sandboxID = sb.GetSandboxID()
		return nil
	}

	// Reusing existing sandbox
	podID, err := sandbox.FindSandboxPod(ctx, c.Sandbox)
	if err != nil {
		return err
	}
	if podID == nil {
		return fmt.Errorf("sandbox %q not found", c.Sandbox)
	}

	sb, err := sandbox.NewSandboxFromPodID(ctx, *podID)
	if err != nil {
		return err
	}
	c.sandbox = sb
	c.sandboxID = sb.GetSandboxID()
	return nil
}

// RunGithubFeedback launches gemini-cli to respond to the specified GitHub pull request feedback.
func (c *GithubFeedbackCommand) Run(ctx context.Context) error {
	log := klog.FromContext(ctx)
	log.Info("Starting github-feedback task", "taskdir", c.TaskDir)

	err := c.loadGithubObjects(ctx)
	if err != nil {
		return err
	}

	err = c.loadSandbox(ctx)
	if err != nil {
		return err
	}

	// Filter comments and reviews based on last commit time
	var lastCommitTime time.Time
	for _, commit := range c.repoCommits {
		if t := commit.CommittedAt(); t.After(lastCommitTime) {
			lastCommitTime = t
		}
	}

	var newIssueComments, oldIssueComments []github.IssueComment
	for _, comment := range c.issueComments {
		if comment.CreatedAt().Before(lastCommitTime) {
			oldIssueComments = append(oldIssueComments, comment)
		} else {
			newIssueComments = append(newIssueComments, comment)
		}
	}

	var newPrReviews, oldPrReviews []github.PullRequestReview
	for _, review := range c.prReviews {
		if review.SubmittedAt().Before(lastCommitTime) {
			oldPrReviews = append(oldPrReviews, review)
		} else {
			newPrReviews = append(newPrReviews, review)
		}
	}

	promptPath := c.taskPath("agent-prompt.txt")
	task := tasks.AddressFeedbackModel{
		Repo:                  c.repo,
		PullRequest:           c.pullRequest,
		RepositoryCommits:     c.repoCommits,
		IssueComments:         newIssueComments,
		OldIssueComments:      oldIssueComments,
		PullRequestReviews:    newPrReviews,
		OldPullRequestReviews: oldPrReviews,
		PromptFile:            promptPath,
		User:                  c.user,
		Models:                strings.Split(c.Model, ","),
	}

	if c.ExtensionsJSON != "" {
		var extensions []reviewv1alpha1.Extension
		if err := json.Unmarshal([]byte(c.ExtensionsJSON), &extensions); err != nil {
			log.Error(err, "failed to unmarshal extensions JSON")
		} else {
			task.Extensions = extensions
		}
	}

	apikey, err := GetGeminiAPIKey(c.sandboxID)
	if err != nil {
		return err
	}

	env := map[string]string{
		"GEMINI_API_KEY":    apikey,
		"GITHUB_USER_TOKEN": c.GithubUserToken,
	}

	err = tasks.RunTask(ctx, &task, c.sandbox, c.TaskDir, env)
	if err != nil {
		return fmt.Errorf("running address-feedback task: %w", err)
	}

	return nil
}
