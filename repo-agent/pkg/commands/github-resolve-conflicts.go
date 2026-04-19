/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
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
	CustomPrompt    string

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
			if err := resolveCommand.InitDefaults(); err != nil {
				return err
			}
			if resolveCommand.PRNumber == 0 {
				return fmt.Errorf("--pr-number is required")
			}
			return resolveCommand.Run(cmd.Context())
		},
	}

	prDefault := 0
	if prStr := os.Getenv("PR_NUMBER"); prStr != "" {
		if val, err := strconv.Atoi(strings.TrimSpace(prStr)); err == nil {
			prDefault = val
		}
	}
	cmd.Flags().IntVar(&resolveCommand.PRNumber, "pr-number", prDefault, "Pull request number")

	cmd.Flags().StringVar(&resolveCommand.RepoURL, "repo-url", os.Getenv("GIT_HTML_URL"), "GitHub repository URL")
	cmd.Flags().StringVar(&resolveCommand.GithubUserLogin, "github-user-login", os.Getenv("GITHUB_USER_LOGIN"), "Github user login")
	cmd.Flags().StringVar(&resolveCommand.GithubUserEmail, "github-user-email", os.Getenv("GITHUB_USER_EMAIL"), "Github user email")
	cmd.Flags().StringVar(&resolveCommand.GithubUserName, "github-user-name", os.Getenv("GITHUB_USER_NAME"), "Github user name")
	cmd.Flags().StringVar(&resolveCommand.Model, "model", os.Getenv("MODEL"), "Model to use")
	cmd.Flags().StringVar(&resolveCommand.ExtensionsJSON, "extensions", os.Getenv("AGENT_LLM_EXTENSIONS"), "Extensions JSON")
	cmd.Flags().StringVar(&resolveCommand.BaseRef, "base-ref", os.Getenv("BASE_REF"), "Base branch ref")
	cmd.Flags().StringVar(&resolveCommand.HeadRef, "head-ref", os.Getenv("HEAD_REF"), "Head branch ref")
	cmd.Flags().StringVar(&resolveCommand.CustomPrompt, "custom-prompt", "", "Custom prompt instructions (falls back to AGENT_PROMPT_FILE or AGENT_PROMPT env vars)")
	cmd.Flags().BoolVar(&resolveCommand.InPod, "in-pod", false, "Whether running inside the pod")

	return cmd
}

func (c *GithubResolveConflictsCommand) InitDefaults() error {
	prompt, err := resolveAgentPrompt(c.CustomPrompt)
	if err != nil {
		return err
	}
	c.CustomPrompt = prompt

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
	return nil
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

	githubAPI, err := github.NewClient(ctx)
	if err != nil {
		return err
	}

	// We need the repo name from the URL
	if c.RepoURL == "" {
		return fmt.Errorf("GIT_HTML_URL (or --repo-url) not set")
	}
	u, err := url.Parse(c.RepoURL)
	if err != nil {
		return fmt.Errorf("invalid RepoURL %q: %w", c.RepoURL, err)
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) < 2 {
		return fmt.Errorf("invalid repository path in URL %q", c.RepoURL)
	}
	owner := parts[0]
	repoName := strings.TrimSuffix(parts[1], ".git")

	c.pr, err = githubAPI.GetPullRequest(ctx, owner, repoName, c.PRNumber)
	if err != nil {
		return err
	}

	if c.BaseRef == "" && c.pr != nil {
		c.BaseRef = c.pr.BaseRef()
	}
	if c.HeadRef == "" && c.pr != nil {
		c.HeadRef = c.pr.HeadRef()
	}

	if c.BaseRef == "" || c.HeadRef == "" {
		state := "unknown"
		if c.pr != nil {
			state = c.pr.State()
		}
		klog.V(4).Infof("PR object: %+v", c.pr)
		return fmt.Errorf("BaseRef or HeadRef is empty for PR %d (state: %s) in %s/%s", c.PRNumber, state, owner, repoName)
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
	if c.InPod {
		name := "local"
		if c.repo != nil {
			name = fmt.Sprintf("local-%s-pr-%d", c.repo.Name(), c.PRNumber)
		}
		c.sandbox = sandbox.NewLocalSandbox(ctx, c.repo, name)
		c.sandboxID = c.sandbox.GetSandboxID()
		return nil
	}

	// For non-pod execution, we still need NewIssueSandbox to find/launch a real sandbox
	sb, err := sandbox.NewIssueSandbox(ctx, false, c.repo, nil, fmt.Sprintf("pr-%d", c.PRNumber))
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

	var models []string
	modelSeen := make(map[string]bool)
	for _, m := range strings.Split(c.Model, ",") {
		if trimmed := strings.TrimSpace(m); trimmed != "" {
			if !modelSeen[trimmed] {
				models = append(models, trimmed)
				modelSeen[trimmed] = true
			}
		}
	}
	if len(models) == 0 {
		return fmt.Errorf("no models provided for conflict resolution")
	}

	promptPath := c.taskPath("agent-prompt.txt")
	task := tasks.ResolveConflictsModel{
		PullRequest:  c.pr,
		Repo:         c.repo,
		RepoOwner:    c.repo.Owner(),
		RepoName:     c.repo.Name(),
		User:         c.user,
		PromptFile:   promptPath,
		Models:       models,
		BaseRef:      c.BaseRef,
		HeadRef:      c.HeadRef,
		CustomPrompt: c.CustomPrompt,
	}

	if c.ExtensionsJSON != "" {
		var extensions []reviewv1alpha1.Extension
		if err := json.Unmarshal([]byte(c.ExtensionsJSON), &extensions); err != nil {
			klog.Warningf("failed to unmarshal extensions JSON (skipping): %v", err)
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
