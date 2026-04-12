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

// IterateCommand holds options for the iterate command.
type IterateCommand struct {
	// Configurable options
	RepoURL         string
	BranchName      string
	PRID            string
	AgentPrompt     string
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
	repo      *github.Repository
	user      *github.User
	sandbox   *sandbox.IssueSandbox // Reusing IssueSandbox struct as it fits dev sandbox needs
	sandboxID string
}

// BuildIterateCommand creates a new cobra command for iterating on code
func BuildIterateCommand() *cobra.Command {
	iterCommand := IterateCommand{}

	cmd := &cobra.Command{
		Use:   "iterate",
		Short: "Iterate on code using an LLM in a dev sandbox",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) != 0 {
				return fmt.Errorf("command does not take positional arguments")
			}
			iterCommand.InitDefaults()
			return iterCommand.Run(cmd.Context())
		},
	}

	cmd.Flags().StringVar(&iterCommand.RepoURL, "repo-url", os.Getenv("GIT_HTML_URL"), "GitHub repo URL")
	cmd.Flags().StringVar(&iterCommand.BranchName, "branch-name", os.Getenv("BRANCH_NAME"), "Branch name")
	cmd.Flags().StringVar(&iterCommand.PRID, "pr-id", "", "PR ID")
	cmd.Flags().StringVar(&iterCommand.AgentPrompt, "agent-prompt", os.Getenv("AGENT_PROMPT"), "Agent prompt")
	cmd.Flags().StringVar(&iterCommand.GithubUserLogin, "github-user-login", os.Getenv("GITHUB_USER_LOGIN"), "Github user login")
	cmd.Flags().StringVar(&iterCommand.GithubUserEmail, "github-user-email", os.Getenv("GITHUB_USER_EMAIL"), "Github user email")
	cmd.Flags().StringVar(&iterCommand.GithubUserName, "github-user-name", os.Getenv("GITHUB_USER_NAME"), "Github user name")
	cmd.Flags().StringVar(&iterCommand.Model, "model", os.Getenv("MODEL"), "Model to use")
	cmd.Flags().StringVar(&iterCommand.ExtensionsJSON, "extensions", os.Getenv("AGENT_LLM_EXTENSIONS"), "Extensions JSON")
	cmd.Flags().BoolVar(&iterCommand.InPod, "in-pod", false, "Whether running inside the pod")
	return cmd
}

func (c *IterateCommand) InitDefaults() {
	c.AgentPrompt = resolveAgentPrompt(c.AgentPrompt)

	if c.PRID == "" {
		c.PRID = os.Getenv("PRID")
	}
	if c.PRID == "" {
		c.PRID = os.Getenv("PULL_REQUEST_ID")
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

func (c *IterateCommand) taskPath(name string, args ...interface{}) string {
	// Ensure the task path is correctly joined
	file := fmt.Sprintf(name, args...)
	return filepath.Join(c.TaskDir, file)
}

func (c *IterateCommand) loadGithubObjects(ctx context.Context) error {
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

	c.repo, err = githubAPI.GetRepositoryFromHTMLUrl(ctx, c.RepoURL)
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

func (c *IterateCommand) loadSandbox(ctx context.Context) error {
	// Let's pass nil for issue.
	sb, err := sandbox.NewIssueSandbox(ctx, c.InPod, c.repo, nil, c.BranchName)
	if err != nil {
		return err
	}
	c.sandbox = sb
	c.sandboxID = sb.GetSandboxID()
	return nil
}

// Run launches the iterate task.
func (c *IterateCommand) Run(ctx context.Context) error {
	log := klog.FromContext(ctx)
	log.Info("Starting iterate task", "taskdir", c.TaskDir)

	err := c.loadGithubObjects(ctx)
	if err != nil {
		return err
	}

	err = c.loadSandbox(ctx)
	if err != nil {
		return err
	}

	promptPath := c.taskPath("agent-prompt.txt")
	task := tasks.IterateModel{
		Repo:        c.repo,
		User:        c.user,
		AgentPrompt: c.AgentPrompt,
		BranchName:  c.BranchName,
		PRID:        c.PRID,
		PromptFile:  promptPath,
		Models:      strings.Split(c.Model, ","),
	}

	if c.ExtensionsJSON != "" {
		var extensions []reviewv1alpha1.Extension
		if err := json.Unmarshal([]byte(c.ExtensionsJSON), &extensions); err != nil {
			return fmt.Errorf("failed to unmarshal extensions JSON: %w", err)
		}
		task.Extensions = extensions
	}

	apikey, err := GetGeminiAPIKey(c.sandboxID)
	if err != nil {
		log.Info("Gemini API Key not found, agent execution will likely fail if prompt is provided", "err", err)
		apikey = ""
	}

	env := map[string]string{
		"GEMINI_API_KEY":    apikey,
		"GITHUB_USER_TOKEN": c.GithubUserToken,
	}
	err = tasks.RunTask(ctx, &task, c.sandbox, c.TaskDir, env)
	if err != nil {
		return fmt.Errorf("running iterate task: %w", err)
	}

	return nil
}
