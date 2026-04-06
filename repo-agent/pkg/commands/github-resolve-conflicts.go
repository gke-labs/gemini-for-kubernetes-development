package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	reviewv1alpha1 "github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/api/repowatch/v1alpha1"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/github"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/sandbox"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/tasks"
	"github.com/spf13/cobra"
	"k8s.io/klog/v2"
)

// GithubResolveConflictsCommand holds options for the resolve-conflicts task.
type GithubResolveConflictsCommand struct {
	// Configurable options
	PRNumber        int
	RepoURL         string
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
	BaseRef         string
	HeadRef         string

	// loaded objects
	pr        *github.PullRequest
	repo      *github.Repository
	user      *github.User
	sandbox   *sandbox.IssueSandbox
	sandboxID string
}

// BuildGithubResolveConflictsCommand creates a new cobra command for automated merge conflict resolution.
func BuildGithubResolveConflictsCommand() *cobra.Command {
	resolveCommand := GithubResolveConflictsCommand{}

	cmd := &cobra.Command{
		Use:   "github-resolve-conflicts",
		Short: "Resolve merge conflicts in a pull request using an LLM",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) != 0 {
				return fmt.Errorf("command does not take positional arguments")
			}
			if resolveCommand.PRNumber == 0 {
				return fmt.Errorf("--pr-number is required")
			}
			resolveCommand.InitDefaults()
			return resolveCommand.Run(cmd.Context())
		},
	}

	cmd.Flags().IntVar(&resolveCommand.PRNumber, "pr-number", 0, "Pull request number")
	if prStr := os.Getenv("PR_NUMBER"); prStr != "" {
		if val, err := strconv.Atoi(prStr); err == nil {
			resolveCommand.PRNumber = val
		}
	}

	cmd.Flags().StringVar(&resolveCommand.RepoURL, "repo-url", os.Getenv("GIT_HTML_URL"), "GitHub repository URL")
	cmd.Flags().StringVar(&resolveCommand.AgentName, "agent-name", os.Getenv("AGENT_NAME"), "Agent name")
	cmd.Flags().StringVar(&resolveCommand.GithubUserLogin, "github-user-login", os.Getenv("GITHUB_USER_LOGIN"), "Github user login")
	cmd.Flags().StringVar(&resolveCommand.GithubUserEmail, "github-user-email", os.Getenv("GITHUB_USER_EMAIL"), "Github user email")
	cmd.Flags().StringVar(&resolveCommand.GithubUserName, "github-user-name", os.Getenv("GITHUB_USER_NAME"), "Github user name")
	cmd.Flags().StringVar(&resolveCommand.Model, "model", os.Getenv("MODEL"), "Model to use")
	cmd.Flags().StringVar(&resolveCommand.ExtensionsJSON, "extensions", os.Getenv("AGENT_LLM_EXTENSIONS"), "Extensions JSON")
	cmd.Flags().StringVar(&resolveCommand.BaseRef, "base-ref", os.Getenv("BASE_REF"), "Base branch ref")
	cmd.Flags().StringVar(&resolveCommand.HeadRef, "head-ref", os.Getenv("HEAD_REF"), "Head branch ref")
	cmd.Flags().BoolVar(&resolveCommand.InPod, "in-pod", false, "Whether running inside the pod")

	return cmd
}

func (c *GithubResolveConflictsCommand) InitDefaults() {
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

func (c *GithubResolveConflictsCommand) taskPath(name string, args ...interface{}) string {
	file := fmt.Sprintf(name, args...)
	return filepath.Join(c.TaskDir, file)
}

func (c *GithubResolveConflictsCommand) loadGithubObjects(ctx context.Context) error {
	token, err := github.GetGithubToken(ctx)
	if err != nil {
		return err
	}
	c.GithubUserToken = token

	githubAPI, err := github.NewClient(context.Background())
	if err != nil {
		return err
	}

	// We need the repo name from the URL
	if c.RepoURL == "" {
		return fmt.Errorf("GIT_HTML_URL (or --repo-url) not set")
	}
	parts := strings.Split(strings.TrimPrefix(c.RepoURL, "https://github.com/"), "/")
	if len(parts) < 2 {
		return fmt.Errorf("invalid GIT_HTML_URL format: %s", c.RepoURL)
	}
	owner := parts[0]
	repoName := parts[1]

	c.pr, err = githubAPI.GetPullRequest(ctx, owner, repoName, c.PRNumber)
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

func (c *GithubResolveConflictsCommand) loadSandbox(ctx context.Context) error {
	// Re-use NewIssueSandbox. If in pod, it uses LocalExecutor which is what we want.
	sb, err := sandbox.NewIssueSandbox(ctx, c.InPod, c.repo, nil, "")
	if err != nil {
		return err
	}
	c.sandbox = sb
	c.sandboxID = sb.GetSandboxID()
	return nil
}

func (c *GithubResolveConflictsCommand) Run(ctx context.Context) error {
	log := klog.FromContext(ctx)
	log.Info("Starting github-resolve-conflicts task", "taskdir", c.TaskDir)

	err := c.loadGithubObjects(ctx)
	if err != nil {
		return err
	}

	err = c.loadSandbox(ctx)
	if err != nil {
		return err
	}

	promptPath := c.taskPath("agent-prompt.txt")
	task := tasks.ResolveConflictsModel{
		PullRequest: c.pr,
		Repo:        c.repo,
		User:        c.user,
		PromptFile:  promptPath,
		Models:      strings.Split(c.Model, ","),
		BaseRef:     c.BaseRef,
		HeadRef:     c.HeadRef,
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

	// Re-use RunTask.
	err = tasks.RunTask(ctx, &task, c.sandbox, c.TaskDir, env)
	if err != nil {
		return fmt.Errorf("running resolve-conflicts task: %w", err)
	}

	return nil
}
