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

// GithubTriageIssueCommand holds options for the RunCode function.
type GithubTriageIssueCommand struct {
	// Configurable options
	URL             string
	AgentName       string
	InPod           bool
	WorkspaceDir    string
	TaskDir         string
	Model           string
	GithubUserToken string
	ExtensionsJSON  string

	// loaded objects
	issue *github.Issue
	// TODO(barney-s): do we need repo ?
	repo      *github.Repository
	sandbox   *sandbox.IssueSandbox
	sandboxID string
}

// BuildGithubTriageIssueCommand creates a new cobra command for using a dev sandbox to solve a github issue
func BuildGithubTriageIssueCommand() *cobra.Command {
	triageCommand := GithubTriageIssueCommand{}

	cmd := &cobra.Command{
		Use:   "github-triage-issue",
		Short: "Triage a github issue using an LLM in a dev sandbox",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) != 0 {
				return fmt.Errorf("command does not take positional arguments")
			}
			if triageCommand.URL == "" {
				return fmt.Errorf("--issue-url is required")
			}
			triageCommand.InitDefaults()
			return triageCommand.Run(cmd.Context())
		},
	}

	cmd.Flags().StringVar(&triageCommand.URL, "issue-url", os.Getenv("ISSUE_URL"), "GitHub issue URL")
	cmd.Flags().StringVar(&triageCommand.AgentName, "agent-name", os.Getenv("AGENT_NAME"), "Agent name")
	cmd.Flags().StringVar(&triageCommand.Model, "model", os.Getenv("MODEL"), "Model to use")
	cmd.Flags().StringVar(&triageCommand.ExtensionsJSON, "extensions", os.Getenv("AGENT_LLM_EXTENSIONS"), "Extensions JSON")
	cmd.Flags().BoolVar(&triageCommand.InPod, "in-pod", false, "Whether running inside the pod")
	return cmd
}

func (c *GithubTriageIssueCommand) InitDefaults() {
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

func (c *GithubTriageIssueCommand) taskPath(name string, args ...interface{}) string {
	// Ensure the task path is correctly joined
	file := fmt.Sprintf(name, args...)
	return filepath.Join(c.TaskDir, file)
}

func (c *GithubTriageIssueCommand) loadGithubObjects(ctx context.Context) error {
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

	return nil
}

func (c *GithubTriageIssueCommand) loadSandbox(ctx context.Context) error {
	sb, err := sandbox.NewIssueSandbox(ctx, c.InPod, c.repo, c.issue, "")
	if err != nil {
		return err
	}
	c.sandbox = sb
	c.sandboxID = sb.GetSandboxID()
	return nil
}

// RunGithubTriageIssue launches VS Code connected to the specified dev sandbox.
func (c *GithubTriageIssueCommand) Run(ctx context.Context) error {
	log := klog.FromContext(ctx)
	log.Info("Starting github-triage-issue task", "taskdir", c.TaskDir)
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
	task := tasks.TriageIssueModel{
		Issue:      c.issue,
		PromptFile: promptPath,
		Models:     strings.Split(c.Model, ","),
		AgentName:  c.AgentName,

		// Traceability metadata
		GithubTraceability: os.Getenv("GITHUB_TRACEABILITY") == "true",
		SandboxTaskName:    os.Getenv("SANDBOX_TASK_NAME"),
		SandboxTaskUID:     os.Getenv("SANDBOX_TASK_UID"),
		SandboxName:        os.Getenv("SANDBOX_NAME"),
		RepoWatchName:      os.Getenv("REPOWATCH_NAME"),
		Namespace:          os.Getenv("NAMESPACE"),
		Timestamp:          time.Now().UTC().Format(time.RFC3339),
	}

	if c.ExtensionsJSON != "" {
		var extensions []reviewv1alpha1.Extension
		if err := json.Unmarshal([]byte(c.ExtensionsJSON), &extensions); err != nil {
			log.Error(err, "failed to unmarshal extensions JSON")
		} else {
			task.Extensions = extensions
		}
	}

	var apikey string
	if c.AgentName == "dummy" {
		apikey = "dummy_key"
	} else {
		apikey, err = GetGeminiAPIKey(c.sandboxID)
		if err != nil {
			return err
		}
	}

	env := map[string]string{
		"GEMINI_API_KEY": apikey,
		// Dont need it for the script. leaving a comment here just in case we change the script
		//"GITHUB_USER_TOKEN": c.GithubUserToken,
	}
	err = tasks.RunTask(ctx, &task, c.sandbox, c.TaskDir, env)
	if err != nil {
		return fmt.Errorf("running triage-issue task: %w", err)
	}

	return nil
}
