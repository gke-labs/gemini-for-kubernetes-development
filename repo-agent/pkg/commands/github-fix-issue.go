package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	reviewv1alpha1 "github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/api/repowatch/v1alpha1"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/github"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/sandbox"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/tasks"
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
	Model           string
	ExtensionsJSON  string
	DraftPR         bool

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
	cmd.Flags().StringVar(&fixCommand.Model, "model", os.Getenv("MODEL"), "Model to use")
	cmd.Flags().StringVar(&fixCommand.ExtensionsJSON, "extensions", os.Getenv("AGENT_LLM_EXTENSIONS"), "Extensions JSON")
	cmd.Flags().BoolVar(&fixCommand.InPod, "in-pod", false, "Whether running inside the pod")
	cmd.Flags().BoolVar(&fixCommand.DraftPR, "draft-pr", os.Getenv("DRAFT_PR") == "true", "Create a draft PR")
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

	if c.Model == "" {
		c.Model = "gemini-3.1-pro-preview"
	}
}

func (c *GithubFixIssueCommand) taskPath(name string, args ...interface{}) string {
	// Ensure the task path is correctly joined
	file := fmt.Sprintf(name, args...)
	return filepath.Join(c.TaskDir, file)
}

func (c *GithubFixIssueCommand) loadGithubObjects(ctx context.Context) error {
	// Get github token
	token, err := github.GetGithubToken(ctx)
	if err != nil {
		return err
	}
	c.GithubUserToken = token

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
	sb, err := sandbox.NewIssueSandbox(ctx, c.InPod, c.repo, c.issue, "")
	if err != nil {
		return err
	}
	c.sandbox = sb
	c.sandboxID = sb.GetSandboxID()
	return nil
}

// Run launches the fix-issue task.
func (c *GithubFixIssueCommand) Run(ctx context.Context) error {
	log := klog.FromContext(ctx)
	log.Info("Starting github-fix-issue task", "taskdir", c.TaskDir)
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

	promptPath := c.taskPath("agent-prompt.txt")
	task := tasks.FixIssueModel{
		Issue:         c.issue,
		Repo:          c.repo,
		User:          c.user,
		IssueComments: c.issue.IssueComments,
		PromptFile:    promptPath,
		Models:        strings.Split(c.Model, ","),
		Branch:        os.Getenv("ISSUE_BRANCH"),
		PRLabel:       os.Getenv("PR_LABEL"),
		DraftPR:       c.DraftPR,
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
		return fmt.Errorf("running fix-issue task: %w", err)
	}

	return nil
}
