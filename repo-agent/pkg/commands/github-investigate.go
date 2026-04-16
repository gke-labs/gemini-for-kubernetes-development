package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	reviewv1alpha1 "github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/api/repowatch/v1alpha1"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/github"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/sandbox"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/tasks"
	"github.com/spf13/cobra"
	"k8s.io/klog/v2"
)

// GithubInvestigateCommand holds options for the RunCode function.
type GithubInvestigateCommand struct {
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
	repo        *github.Repository
	pullRequest *github.PullRequest
	user        *github.User
	sandbox     *sandbox.IssueSandbox
	sandboxID   string
	failedRuns  []tasks.FailedRun
	githubAPI   *github.Client
}

// BuildGithubInvestigateCommand creates a new cobra command for using a dev sandbox to investigate github failures
func BuildGithubInvestigateCommand() *cobra.Command {
	c := GithubInvestigateCommand{}

	cmd := &cobra.Command{
		Use:   "github-investigate",
		Short: "Investigate github workflow failures using an LLM in a dev sandbox",
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

func (c *GithubInvestigateCommand) InitDefaults() {
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
		prid := os.Getenv("PULL_REQUEST_ID")
		if prid != "" {
			if _, err := fmt.Sscanf(prid, "%d", &c.PullRequestID); err != nil {
				c.PullRequestID = 0
			}
		}
	}
}

func (c *GithubInvestigateCommand) taskPath(name string, args ...interface{}) string {
	file := fmt.Sprintf(name, args...)
	return filepath.Join(c.TaskDir, file)
}

func (c *GithubInvestigateCommand) loadGithubObjects(ctx context.Context) error {
	token, err := github.GetGithubToken(ctx)
	if err != nil {
		return err
	}
	c.GithubUserToken = token

	githubAPI, err := github.NewClient(ctx)
	if err != nil {
		return err
	}
	c.githubAPI = githubAPI

	c.repo, err = c.githubAPI.GetRepositoryFromIssueURL(ctx, c.URL)
	if err != nil {
		return err
	}

	c.pullRequest, err = c.githubAPI.GetPullRequest(ctx, c.repo.Owner(), c.repo.Name(), c.PullRequestID)
	if err != nil {
		return fmt.Errorf("failed to get github pull request: %w", err)
	}

	// Fetch failed runs
	runs, err := c.githubAPI.ListWorkflowRunsByBranch(ctx, c.repo.Owner(), c.repo.Name(), c.pullRequest.HeadRef())
	if err != nil {
		return fmt.Errorf("failed to list workflow runs: %w", err)
	}

	for _, run := range runs {
		if run.GetConclusion() == "failure" {
			c.failedRuns = append(c.failedRuns, tasks.FailedRun{
				ID:      run.GetID(),
				Name:    run.GetName(),
				URL:     run.GetHTMLURL(),
				HeadSHA: run.GetHeadSHA(),
			})
		}
	}

	c.user = &github.User{
		UserID: c.GithubUserLogin,
		Email:  c.GithubUserEmail,
		Name:   c.GithubUserName,
		Token:  c.GithubUserToken,
	}

	return nil
}

func (c *GithubInvestigateCommand) loadSandbox(ctx context.Context) error {
	if c.InPod {
		var err error
		// Note: Using GetIssue is not possible here as we don't necessarily have an issue object fully populated or needed
		// But NewIssueSandbox expects an issue.
		// In existing code (github-feedback), it loads issue from URL.
		// Here we might just need the repo and PR.
		// However, sandbox creation logic seems tied to issues.
		// If we are reusing a sandbox, we might not need to create it.
		// Let's assume we are reusing or running in existing sandbox context.

		// If c.URL is set, we can try to load the issue.
		// But c.issue is not in our struct yet.
		// Let's rely on what NewIssueSandbox needs.
		// It needs repo and issue.
		// Let's skip issue loading for now and pass nil if possible, or try to load if URL is present.

		var issue *github.Issue
		if c.URL != "" {
			githubAPI, _ := github.NewClient(context.Background())
			issue, _ = githubAPI.GetIssue(ctx, c.URL, false)
		}

		sb, err := sandbox.NewIssueSandbox(ctx, c.InPod, c.repo, issue, "")
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

// Run launches gemini-cli to investigate failures.
func (c *GithubInvestigateCommand) Run(ctx context.Context) error {
	log := klog.FromContext(ctx)
	log.Info("Starting github-investigate task", "taskdir", c.TaskDir)

	err := c.loadGithubObjects(ctx)
	if err != nil {
		return err
	}

	err = c.loadSandbox(ctx)
	if err != nil {
		return err
	}

	if len(c.failedRuns) == 0 {
		log.Info("No failed runs found for this PR.")
		// Maybe we should report this back?
		// For now, let's just exit or run the task with empty list (which will result in empty prompt about failures).
	}

	comments, err := c.githubAPI.GetIssueCommentsByNumber(ctx, c.repo.Owner(), c.repo.Name(), c.pullRequest.Number())
	if err != nil {
		log.Error(err, "failed to get issue comments")
	}

	commits, err := c.githubAPI.GetPullRequestCommits(ctx, c.repo.Owner(), c.repo.Name(), c.pullRequest.Number())
	if err != nil {
		log.Error(err, "failed to get pull request commits")
	}
	var lastCommitAt time.Time
	for _, commit := range commits {
		if commit.CommittedAt().After(lastCommitAt) {
			lastCommitAt = commit.CommittedAt()
		}
	}

	var filteredComments []github.IssueComment
	for _, comment := range comments {
		if comment.CreatedAt().After(lastCommitAt) {
			filteredComments = append(filteredComments, comment)
		}
	}

	promptPath := c.taskPath("agent-prompt.txt")
	task := tasks.InvestigateFailuresModel{
		Repo:              c.repo,
		RepoOwner:         c.repo.Owner(),
		RepoName:          c.repo.Name(),
		PullRequest:       c.pullRequest,
		PromptFile:        promptPath,
		User:              c.user,
		Models:            strings.Split(c.Model, ","),
		FailedRuns:        c.failedRuns,
		IssueComments:     filteredComments,
		RepositoryCommits: commits,
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
		return fmt.Errorf("running investigate-failures task: %w", err)
	}

	return nil
}
