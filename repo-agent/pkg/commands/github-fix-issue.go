package commands

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/github"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/prompts"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/sandbox"
	"github.com/spf13/cobra"
	"k8s.io/klog/v2"
)

// GithubFixIssueCommand holds options for the RunCode function.
type GithubFixIssueCommand struct {
	// Configurable options
	URL             string
	AgentName       string
	GithubUserLogin string
	GithubUserEmail string
	GithubUserName  string
	GithubUserToken string
	InPod           bool
	WorkspaceDir    string
	TaskDir         string

	// loaded objects
	issue     *github.Issue
	repo      *github.Repository
	user      *github.User
	sandbox   *sandbox.IssueSandbox
	sandboxID string
}

// BuildGithubFixIssueCommand creates a new cobra command for using a dev sandbox to solve a github issue
func BuildGithubFixIssueCommand() *cobra.Command {
	fixCommand := GithubFixIssueCommand{}

	cmd := &cobra.Command{
		Use:   "github-fix-issue",
		Short: "Fix a github issue using an LLM in a dev sandbox",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) != 0 {
				return fmt.Errorf("command does not take positional arguments")
			}
			if fixCommand.URL == "" {
				return fmt.Errorf("--issue-url is required")
			}
			fixCommand.InitDefaults()
			return fixCommand.Run(cmd.Context())
		},
	}

	cmd.Flags().StringVar(&fixCommand.URL, "issue-url", os.Getenv("ISSUE_URL"), "GitHub issue URL")
	cmd.Flags().StringVar(&fixCommand.AgentName, "agent-name", os.Getenv("AGENT_NAME"), "Agent name")
	cmd.Flags().StringVar(&fixCommand.GithubUserLogin, "github-user-login", os.Getenv("GITHUB_USER_LOGIN"), "Github user login")
	cmd.Flags().StringVar(&fixCommand.GithubUserEmail, "github-user-email", os.Getenv("GITHUB_USER_EMAIL"), "Github user email")
	cmd.Flags().StringVar(&fixCommand.GithubUserName, "github-user-name", os.Getenv("GITHUB_USER_NAME"), "Github user name")
	cmd.Flags().BoolVar(&fixCommand.InPod, "in-pod", false, "Whether running inside the pod")
	return cmd
}

func (c *GithubFixIssueCommand) InitDefaults() {
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
	if c.GithubUserToken == "" {
		c.GithubUserToken = os.Getenv("GITHUB_USER_TOKEN")
	}
}

func (c *GithubFixIssueCommand) taskPath(name string, args ...interface{}) string {
	// Ensure the task path is correctly joined
	file := fmt.Sprintf(name, args...)
	return filepath.Join(c.TaskDir, file)
}

func (c *GithubFixIssueCommand) loadGithubObjects(ctx context.Context) error {
	githubAPI, err := github.NewClient(context.Background())
	if err != nil {
		return err
	}

	c.issue, err = githubAPI.GetIssue(ctx, c.URL, true)
	if err != nil {
		return err
	}

	c.repo, err = githubAPI.GetRepositoryFromIssueURL(ctx, c.URL)
	if err != nil {
		return err
	}

	user := github.User{
		UserID: c.GithubUserLogin,
		Email:  c.GithubUserEmail,
		Name:   c.GithubUserName,
		Token:  c.GithubUserToken,
	}

	c.user = &user
	return nil
}

func (c *GithubFixIssueCommand) loadSandbox(ctx context.Context) error {
	sb, err := sandbox.NewIssueSandbox(ctx, c.InPod, c.repo, c.issue, *c.user)
	if err != nil {
		return err
	}
	c.sandbox = sb
	c.sandboxID = sb.GetSandboxID()
	return nil
}

func (c *GithubFixIssueCommand) SetupGit() error {
	//log := klog.FromContext(ctx)

	{
		config := `github.com:
    users:
        {{UserID}}:
            oauth_token: {{Token}}
    git_protocol: https
    oauth_token: {{Token}}
    user: {{UserID}}
`

		if c.user.Token == "" {
			return fmt.Errorf("user token is not set")
		}

		config = strings.ReplaceAll(config, "{{Token}}", c.user.Token)
		config = strings.ReplaceAll(config, "{{UserID}}", c.user.UserID)

		opts := sandbox.ExecOptions{
			Command: []string{"mkdir", "-p", "/root/.config/gh"},
		}
		if err := c.sandbox.Exec(opts); err != nil {
			return fmt.Errorf("creating /root/.config/gh directory: %w", err)
		}

		if err := c.sandbox.WriteFile("/root/.config/gh/hosts.yml", []byte(config)); err != nil {
			return fmt.Errorf("writing gh config: %w", err)
		}
	}

	// Run git config
	{
		opts := sandbox.ExecOptions{
			Command: []string{"git", "config", "--global", "user.email", c.user.Email},
		}
		if err := c.sandbox.Exec(opts); err != nil {
			return fmt.Errorf("running git config user.email: %w", err)
		}
		opts = sandbox.ExecOptions{
			Command: []string{"git", "config", "--global", "user.name", c.user.Name},
		}
		if err := c.sandbox.Exec(opts); err != nil {
			return fmt.Errorf("running git config user.name: %w", err)
		}
	}

	// Run gh auth setup-git
	{
		opts := sandbox.ExecOptions{
			Command: []string{"gh", "auth", "setup-git"},
		}
		if err := c.sandbox.Exec(opts); err != nil {
			return fmt.Errorf("running gh auth setup-git: %w", err)
		}
	}

	return nil
}

func (c *GithubFixIssueCommand) SetupGitRepos(ctx context.Context) error {
	log := klog.FromContext(ctx)

	workdir := fmt.Sprintf("/workspaces/%s", c.repo.Name())

	// Run gh repo fork
	log.Info("Forking repository", "sandbox", c.sandboxID, "repo", c.repo.CloneURL())
	{
		// TODO: Does gh support -C ?
		opts := sandbox.ExecOptions{
			Command: []string{"sh", "-c", fmt.Sprintf("cd %s && gh repo fork --remote", workdir)},
		}
		if err := c.sandbox.Exec(opts); err != nil {
			return fmt.Errorf("running gh repo fork: %w", err)
		}
	}

	// Setup default remote
	{
		defaultRepo := c.repo.CloneURL()

		// TODO: Does gh support -C ?
		opts := sandbox.ExecOptions{
			Command: []string{"sh", "-c", fmt.Sprintf("cd %s && gh repo set-default %s", workdir, defaultRepo)},
		}
		if err := c.sandbox.Exec(opts); err != nil {
			return fmt.Errorf("running gh repo fork: %w", err)
		}

	}

	// Wait for checkout to complete
	{
		timeoutAt := time.Now().Add(time.Minute)
		for {
			log.Info("Waiting for checkout to be ready")

			var stdout bytes.Buffer
			opts := sandbox.ExecOptions{
				Command: []string{"git", "-C", workdir, "branch", "--show-current"},
				Stdout:  &stdout,
			}
			if err := c.sandbox.Exec(opts); err != nil {
				klog.Infof("stdout: %v", stdout.String())
				if time.Now().After(timeoutAt) {
					return fmt.Errorf("timed out waiting for initial checkout to complete: %w", err)
				}
			} else {
				klog.Infof("current branch: %v", stdout.String())
				break
			}

			time.Sleep(2 * time.Second)
		}
	}

	return nil
}

func (c *GithubFixIssueCommand) CheckoutNewBranch(ctx context.Context) error {
	log := klog.FromContext(ctx)

	workdir := fmt.Sprintf("/workspaces/%s", c.repo.Name())

	branchName := fmt.Sprintf("issue_%d", c.issue.Number())

	// Create a new branch
	log.Info("Creating new branch", "sandbox", c.sandboxID, "branch", branchName)

	opts := sandbox.ExecOptions{
		Command: []string{"git", "-C", workdir, "checkout", "-b", branchName},
	}
	if err := c.sandbox.Exec(opts); err != nil {
		return fmt.Errorf("creating new branch: %w", err)
	}

	return nil
}

// RunGithubFixIssue launches VS Code connected to the specified dev sandbox.
func (c *GithubFixIssueCommand) Run(ctx context.Context) error {
	log := klog.FromContext(ctx)

	// Load data from github.com
	err := c.loadGithubObjects(ctx)
	if err != nil {
		return err
	}

	// get sandbox
	err = c.loadSandbox(ctx)
	if err != nil {
		return err
	}

	// setup git repo clone in sandbox
	if err := c.SetupGit(); err != nil {
		return fmt.Errorf("setting up git in sandbox: %w", err)
	}

	if err := c.SetupGitRepos(ctx); err != nil {
		return fmt.Errorf("setting up git branches in sandbox: %w", err)
	}

	// HACK: Avoid git lock issues
	time.Sleep(5 * time.Second)

	if err := c.CheckoutNewBranch(ctx); err != nil {
		return fmt.Errorf("checking out branch: %w", err)
	}

	// Prepare prompt
	model := prompts.FixIssueModel{
		Issue:         c.issue,
		IssueComments: c.issue.IssueComments,
		Repo:          c.repo,
	}
	prompt, err := prompts.FixIssuePrompt(model)
	if err != nil {
		return fmt.Errorf("failed to generate prompt for issue: %w", err)
	}

	log.Info("copying prompt into sandbox", "sandbox", c.sandboxID)

	promptPath := c.taskPath("agent-prompt.txt")
	if err := c.sandbox.WriteFile(promptPath, prompt); err != nil {
		return fmt.Errorf("copying prompt into sandbox: %w", err)
	}

	log.Info("Copied prompt into sandbox", "sandbox", c.sandboxID, "path", promptPath)

	if err := c.sandbox.ConfigureGemini(ctx); err != nil {
		return fmt.Errorf("configuring gemini in sandbox: %w", err)
	}

	apikey, err := GetGeminiAPIKey(c.sandboxID)
	if err != nil {
		return err
	}
	log.Info("Running gemini in sandbox", "sandbox", c.sandboxID)

	workdir := fmt.Sprintf("/workspaces/%s", c.repo.Name())

	opts := sandbox.ExecOptions{
		Command: []string{"sh", "-c", fmt.Sprintf("cd %s && export GEMINI_API_KEY=%s && gemini --yolo --model gemini-3-pro-preview < %s", workdir, apikey, promptPath)},
		Stdout:  os.Stdout,
		Stderr:  os.Stderr,
	}
	opts.Secrets = []string{apikey}

	if err := c.sandbox.Exec(opts); err != nil {
		return fmt.Errorf("running gemini: %w", err)
	}

	return nil
}
