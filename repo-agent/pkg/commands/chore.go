// Copyright 2026 The Kubernetes Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package commands

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

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
	SkipPR       bool

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
			if err := choreCommand.InitDefaults(); err != nil {
				return err
			}
			return choreCommand.Run(cmd.Context())
		},
	}

	cmd.Flags().StringVar(&choreCommand.AgentPrompt, "prompt", "", "Chore prompt (falls back to AGENT_PROMPT_FILE or AGENT_PROMPT env vars)")
	cmd.Flags().StringVar(&choreCommand.ChoreName, "name", os.Getenv("CHORE_NAME"), "Chore name")
	cmd.Flags().StringVar(&choreCommand.ChoreFile, "file", os.Getenv("CHORE_FILE"), "Chore definition file path")
	cmd.Flags().StringVar(&choreCommand.RepoName, "repo", os.Getenv("REPO"), "Repository name")
	cmd.Flags().StringVar(&choreCommand.CloneURL, "clone-url", os.Getenv("CLONE_URL"), "Repository clone URL")
	cmd.Flags().StringVar(&choreCommand.RepoOwner, "repo-owner", os.Getenv("REPO_OWNER"), "Repository owner")
	cmd.Flags().BoolVar(&choreCommand.SkipPR, "skip-pr", os.Getenv("SKIP_PR") == "true", "Skip PR creation")
	cmd.Flags().BoolVar(&choreCommand.InPod, "in-pod", false, "Whether running inside the pod")
	return cmd
}

func (c *ChoreCommand) InitDefaults() error {
	prompt, err := resolveAgentPrompt(c.AgentPrompt)
	if err != nil {
		return err
	}
	c.AgentPrompt = prompt

	if c.WorkspaceDir == "" {
		c.WorkspaceDir = "/workspaces"
	}
	if c.TaskDir == "" {
		c.TaskDir = os.Getenv("TASKDIR")
	}
	if c.TaskDir == "" {
		c.TaskDir = c.WorkspaceDir
	}
	return nil
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
	repo, err := github.ParseRepo(c.CloneURL)
	if err != nil {
		// Fallback for tests or missing clone URL
		repo = &github.Repo{Host: "github.com"}
	}

	task := tasks.ChoreModel{
		AgentPrompt: c.AgentPrompt,
		ChoreName:   c.ChoreName,
		ChoreFile:   c.ChoreFile,
		RepoName:    c.RepoName,
		CloneURL:    c.CloneURL,
		RepoOwner:   c.RepoOwner,
		PromptFile:  promptPath,
		SkipPR:      c.SkipPR,
		Repo:        repo,
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
