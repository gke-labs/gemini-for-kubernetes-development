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
	githubv39 "github.com/google/go-github/v39/github"
	"github.com/spf13/cobra"
	"k8s.io/klog/v2"
)

// DevInitCommand holds options for the dev initialization.
type DevInitCommand struct {
	// Configurable options
	RepoURL         string
	BranchName      string
	SourceBranch    string
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

// BuildDevInitCommand creates a new cobra command for initializing a dev sandbox
func BuildDevInitCommand() *cobra.Command {
	initCommand := DevInitCommand{}

	cmd := &cobra.Command{
		Use:   "dev-init",
		Short: "Initialize a dev sandbox environment",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) != 0 {
				return fmt.Errorf("command does not take positional arguments")
			}
			initCommand.InitDefaults()
			return initCommand.Run(cmd.Context())
		},
	}

	cmd.Flags().StringVar(&initCommand.RepoURL, "repo-url", os.Getenv("REPO_URL"), "GitHub repo URL")
	cmd.Flags().StringVar(&initCommand.BranchName, "branch-name", os.Getenv("BRANCH_NAME"), "Branch name")
	cmd.Flags().StringVar(&initCommand.SourceBranch, "source-branch", os.Getenv("SOURCE_BRANCH"), "Source branch name to fork from")
	cmd.Flags().StringVar(&initCommand.AgentPrompt, "agent-prompt", os.Getenv("AGENT_PROMPT"), "Agent prompt")
	cmd.Flags().StringVar(&initCommand.GithubUserLogin, "github-user-login", os.Getenv("GITHUB_USER_LOGIN"), "Github user login")
	cmd.Flags().StringVar(&initCommand.GithubUserEmail, "github-user-email", os.Getenv("GITHUB_USER_EMAIL"), "Github user email")
	cmd.Flags().StringVar(&initCommand.GithubUserName, "github-user-name", os.Getenv("GITHUB_USER_NAME"), "Github user name")
	cmd.Flags().StringVar(&initCommand.Model, "model", os.Getenv("MODEL"), "Model to use")
	cmd.Flags().StringVar(&initCommand.ExtensionsJSON, "extensions", os.Getenv("AGENT_LLM_EXTENSIONS"), "Extensions JSON")
	cmd.Flags().BoolVar(&initCommand.InPod, "in-pod", false, "Whether running inside the pod")
	return cmd
}

func (c *DevInitCommand) InitDefaults() {
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

func (c *DevInitCommand) taskPath(name string, args ...interface{}) string {
	// Ensure the task path is correctly joined
	file := fmt.Sprintf(name, args...)
	return filepath.Join(c.TaskDir, file)
}

func (c *DevInitCommand) loadGithubObjects(ctx context.Context) error {
	// Get github token
	token, err := github.GetGithubToken(ctx)
	if err != nil {
		return err
	}
	c.GithubUserToken = token

	// Let's parse the name from URL for directory naming
	// e.g. https://github.com/owner/repo
	cleanURL := strings.Split(c.RepoURL, "#")[0]
	cleanURL = strings.TrimSuffix(cleanURL, "/")
	base := filepath.Base(cleanURL)
	if ext := filepath.Ext(base); ext == ".git" {
		base = base[:len(base)-len(ext)]
	}

	// Construct basic repo object
	innerRepo := &githubv39.Repository{
		CloneURL: githubv39.String(strings.TrimSuffix(cleanURL, ".git") + ".git"),
		Name:     githubv39.String(base),
	}
	c.repo = github.NewRepository(innerRepo)

	user := github.User{
		UserID: c.GithubUserLogin,
		Email:  c.GithubUserEmail,
		Name:   c.GithubUserName,
		Token:  c.GithubUserToken,
	}

	c.user = &user
	return nil
}

func (c *DevInitCommand) loadSandbox(ctx context.Context) error {
	// Let's pass nil for issue.
	sb, err := sandbox.NewIssueSandbox(ctx, c.InPod, c.repo, nil, c.BranchName)
	if err != nil {
		return err
	}
	c.sandbox = sb
	c.sandboxID = sb.GetSandboxID()
	return nil
}

// Run launches the dev initialization task.
func (c *DevInitCommand) Run(ctx context.Context) error {
	log := klog.FromContext(ctx)
	log.Info("Starting dev init task", "taskdir", c.TaskDir)

	err := c.loadGithubObjects(ctx)
	if err != nil {
		return err
	}

	err = c.loadSandbox(ctx)
	if err != nil {
		return err
	}

	promptPath := c.taskPath("agent-prompt.txt")
	task := tasks.DevSetupModel{
		Repo:         c.repo,
		User:         c.user,
		BranchName:   c.BranchName,
		SourceBranch: c.SourceBranch,
		AgentPrompt:  c.AgentPrompt,
		PromptFile:   promptPath,
		Models:       strings.Split(c.Model, ","),
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
		log.Info("Gemini API Key not found, agent execution will likely fail if prompt is provided", "err", err)
		apikey = ""
	}

	env := map[string]string{
		"GEMINI_API_KEY":    apikey,
		"GITHUB_USER_TOKEN": c.GithubUserToken,
	}
	err = tasks.RunTask(ctx, &task, c.sandbox, c.TaskDir, env)
	if err != nil {
		return fmt.Errorf("running dev-setup task: %w", err)
	}

	return nil
}
