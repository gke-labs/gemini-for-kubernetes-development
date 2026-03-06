package commands

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/github"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/sandbox"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/tasks"
	"github.com/spf13/cobra"
	"k8s.io/klog/v2"
)

// ChoreCommand holds options for the Run function.
type ChoreCommand struct {
	// Configurable options
	AgentPrompt  string
	ChoreName    string
	ChoreFile    string
	InPod        bool
	WorkspaceDir string
	TaskDir      string
	RepoName     string
	CloneURL     string
	RepoOwner    string

	// Traceability metadata
	TraceSandboxTask      string
	TraceSandboxTaskUID   string
	TraceSandbox          string
	TraceRepoWatch        string
	TraceTaskType         string
	TraceInstallationName string
	MetadataEnabled       bool

	// loaded objects
	sandbox   *sandbox.IssueSandbox
	sandboxID string
}

// BuildChoreCommand creates a new cobra command for running a chore in a sandbox
func BuildChoreCommand() *cobra.Command {
	choreCommand := ChoreCommand{}

	cmd := &cobra.Command{
		Use:   "chore",
		Short: "Run a chore using an LLM in a sandbox",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			choreCommand.InitDefaults()
			return choreCommand.Run(cmd.Context())
		},
	}

	cmd.Flags().StringVar(&choreCommand.AgentPrompt, "prompt", os.Getenv("AGENT_PROMPT"), "Chore prompt")
	cmd.Flags().StringVar(&choreCommand.ChoreName, "name", os.Getenv("CHORE_NAME"), "Chore name")
	cmd.Flags().StringVar(&choreCommand.ChoreFile, "file", os.Getenv("CHORE_FILE"), "Chore definition file path")
	cmd.Flags().StringVar(&choreCommand.RepoName, "repo", os.Getenv("REPO"), "Repository name")
	cmd.Flags().StringVar(&choreCommand.CloneURL, "clone-url", os.Getenv("CLONE_URL"), "Repository clone URL")
	cmd.Flags().StringVar(&choreCommand.RepoOwner, "repo-owner", os.Getenv("REPO_OWNER"), "Repository owner")
	cmd.Flags().BoolVar(&choreCommand.InPod, "in-pod", false, "Whether running inside the pod")

	// Traceability metadata flags
	cmd.Flags().StringVar(&choreCommand.TraceSandboxTask, "sandbox-task", os.Getenv("SANDBOX_TASK"), "Sandbox task name (namespace/name)")
	cmd.Flags().StringVar(&choreCommand.TraceSandboxTaskUID, "sandbox-task-uid", os.Getenv("SANDBOX_TASK_UID"), "Sandbox task UID")
	cmd.Flags().StringVar(&choreCommand.TraceSandbox, "trace-sandbox", os.Getenv("SANDBOX"), "Sandbox name for traceability")
	cmd.Flags().StringVar(&choreCommand.TraceRepoWatch, "repowatch", os.Getenv("REPOWATCH"), "RepoWatch name")
	cmd.Flags().StringVar(&choreCommand.TraceTaskType, "task-type", os.Getenv("TASK_TYPE"), "Task type")
	cmd.Flags().StringVar(&choreCommand.TraceInstallationName, "installation-name", os.Getenv("INSTALLATION_NAME"), "Installation name for traceability")
	cmd.Flags().BoolVar(&choreCommand.MetadataEnabled, "enable-traceability-metadata", os.Getenv("ENABLE_TRACEABILITY_METADATA") == "true", "Enable traceability metadata in GitHub artifacts")

	return cmd
}


func (c *ChoreCommand) InitDefaults() {
	if c.WorkspaceDir == "" {
		c.WorkspaceDir = "/workspaces"
	}
	if c.TaskDir == "" {
		c.TaskDir = os.Getenv("TASKDIR")
	}
	if c.TaskDir == "" {
		c.TaskDir = c.WorkspaceDir
	}
}

func (c *ChoreCommand) taskPath(name string, args ...interface{}) string {
	file := fmt.Sprintf(name, args...)
	return filepath.Join(c.TaskDir, file)
}

func (c *ChoreCommand) loadSandbox(ctx context.Context) error {
	// For chore, we might not have an issue or full repo info easily available
	// But IssueSandbox is what tasks.RunTask expects.
	// We can probably pass nil for repo and issue if the task doesn't use them.
	sb, err := sandbox.NewIssueSandbox(ctx, c.InPod, nil, nil, "")
	if err != nil {
		return err
	}
	c.sandbox = sb
	c.sandboxID = sb.GetSandboxID()
	return nil
}

// Run executes the chore.
func (c *ChoreCommand) Run(ctx context.Context) error {
	log := klog.FromContext(ctx)
	log.Info("Starting chore task", "taskdir", c.TaskDir)

	// get sandbox
	err := c.loadSandbox(ctx)
	if err != nil {
		return err
	}

	promptPath := c.taskPath("agent-prompt.txt")
	task := tasks.ChoreModel{
		AgentPrompt: c.AgentPrompt,
		ChoreName:   c.ChoreName,
		ChoreFile:   c.ChoreFile,
		RepoName:    c.RepoName,
		CloneURL:    c.CloneURL,
		RepoOwner:   c.RepoOwner,
		PromptFile:  promptPath,
		Metadata: github.TraceabilityMetadata{
			Enabled:          c.MetadataEnabled,
			SandboxTask:      c.TraceSandboxTask,
			SandboxTaskUID:   c.TraceSandboxTaskUID,
			Sandbox:          c.TraceSandbox,
			RepoWatch:        c.TraceRepoWatch,
			TaskType:         c.TraceTaskType,
			InstallationName: c.TraceInstallationName,
			Timestamp:        time.Now().UTC().Format(time.RFC3339),
		},
	}

	apikey, err := GetGeminiAPIKey(c.sandboxID)
	if err != nil {
		return err
	}

	env := map[string]string{
		"GEMINI_API_KEY": apikey,
	}
	err = tasks.RunTask(ctx, &task, c.sandbox, c.TaskDir, env)
	if err != nil {
		return fmt.Errorf("running chore task: %w", err)
	}

	return nil
}
