// Copyright 2026 The Kubernetes Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// you may obtain a copy of the License at
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
	"strings"

	"github.com/spf13/cobra"
	"k8s.io/klog/v2"

	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/github"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/sandbox"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/tasks"
)

type RollbackOptions struct {
	RepoURL         string
	PullRequestID   int
	CommitSHA       string
	Branch          string
	Remote          string
	GithubUserLogin string
	GithubUserEmail string
	GithubUserName  string
	GithubUserToken string
	InPod           bool
	WorkspaceDir    string
	TaskDir         string

	// loaded objects
	repo      *github.Repository
	user      *github.User
	sandbox   *sandbox.IssueSandbox
	sandboxID string
}

func (o *RollbackOptions) InitDefaults() {
	if o.RepoURL == "" {
		o.RepoURL = os.Getenv("GIT_HTML_URL")
	}
	if o.RepoURL == "" {
		o.RepoURL = os.Getenv("REPO")
	}

	if o.CommitSHA == "" {
		o.CommitSHA = os.Getenv("COMMIT_SHA")
	}
	if o.Branch == "" {
		o.Branch = os.Getenv("BRANCH_NAME")
	}
	if o.Branch == "" {
		o.Branch = os.Getenv("ISSUE_BRANCH")
	}
	if o.Branch == "" {
		o.Branch = os.Getenv("DEV_BRANCH")
	}
	if o.Branch == "" {
		cloneURL := os.Getenv("GIT_CLONE_URL")
		if cloneURL != "" && strings.Contains(cloneURL, "#refs/heads/") {
			parts := strings.SplitN(cloneURL, "#refs/heads/", 2)
			o.Branch = parts[1]
		}
	}
	if o.PullRequestID == 0 {
		prid := os.Getenv("PULL_REQUEST_ID")
		if prid != "" {
			if _, err := fmt.Sscanf(prid, "%d", &o.PullRequestID); err != nil {
				o.PullRequestID = 0
			}
		}
	}
	if o.Remote == "" {
		o.Remote = "origin"
	}

	if o.WorkspaceDir == "" {
		o.WorkspaceDir = "/workspaces"
	}
	if o.TaskDir == "" {
		o.TaskDir = os.Getenv("TASKDIR")
	}
	if o.TaskDir == "" {
		o.TaskDir = o.WorkspaceDir
	}

	if o.GithubUserLogin == "" {
		o.GithubUserLogin = os.Getenv("GITHUB_USER_LOGIN")
	}
	if o.GithubUserEmail == "" {
		o.GithubUserEmail = os.Getenv("GITHUB_USER_EMAIL")
	}
	if o.GithubUserName == "" {
		o.GithubUserName = os.Getenv("GITHUB_USER_NAME")
	}
}

func BuildRollbackCommand() *cobra.Command {
	opts := RollbackOptions{}
	cmd := &cobra.Command{
		Use:   "rollback",
		Short: "Rollback to a previous commit",
		RunE: func(cmd *cobra.Command, args []string) error {
			opts.InitDefaults()
			if opts.PullRequestID == 0 {
				return fmt.Errorf("--pull-request is required")
			}
			return RunRollback(cmd.Context(), opts)
		},
	}
	cmd.Flags().StringVar(&opts.CommitSHA, "commit-sha", "", "Commit SHA to rollback to")
	cmd.Flags().StringVar(&opts.Branch, "branch", "", "Branch name")
	cmd.Flags().StringVar(&opts.RepoURL, "repo-url", "", "GitHub repo URL")
	cmd.Flags().BoolVar(&opts.InPod, "in-pod", false, "Whether running inside the pod")
	return cmd
}

func (o *RollbackOptions) loadGithubObjects(ctx context.Context) error {
	// Get github token
	token, err := github.GetGithubToken(ctx)
	if err != nil {
		return err
	}
	o.GithubUserToken = token

	githubAPI, err := github.NewClient(context.Background())
	if err != nil {
		return err
	}

	o.repo, err = githubAPI.GetRepositoryFromHTMLUrl(ctx, o.RepoURL)
	if err != nil {
		return err
	}

	user := github.User{
		UserID: o.GithubUserLogin,
		Email:  o.GithubUserEmail,
		Name:   o.GithubUserName,
		Token:  o.GithubUserToken,
	}

	o.user = &user
	return nil
}

func (o *RollbackOptions) loadSandbox(ctx context.Context) error {
	// Pass nil for issue as rollback doesn't need an issue.
	sb, err := sandbox.NewIssueSandbox(ctx, o.InPod, o.repo, nil, o.Branch)
	if err != nil {
		return err
	}
	o.sandbox = sb
	o.sandboxID = sb.GetSandboxID()
	return nil
}

func RunRollback(ctx context.Context, opts RollbackOptions) error {
	log := klog.FromContext(ctx)
	log.Info("Starting rollback task", "taskdir", opts.TaskDir)

	if opts.CommitSHA == "" {
		return fmt.Errorf("commit-sha is required")
	}
	if opts.Branch == "" {
		return fmt.Errorf("branch is required")
	}
	if opts.RepoURL == "" {
		return fmt.Errorf("repo-url is required")
	}

	err := opts.loadGithubObjects(ctx)
	if err != nil {
		return err
	}

	err = opts.loadSandbox(ctx)
	if err != nil {
		return err
	}

	task := tasks.RollbackModel{
		PullRequestID:               opts.PullRequestID,
		Repo:                        opts.repo,
		User:                        opts.user,
		CommitSHA:                   opts.CommitSHA,
		Branch:                      opts.Branch,
		Remote:                      opts.Remote,
		Metadata:                    tasks.GetMetadata(),
		TraceabilityMetadataEnabled: tasks.GetTraceabilityMetadataEnabled(),
	}

	env := tasks.GetMetadataEnv()
	env["GITHUB_USER_TOKEN"] = opts.GithubUserToken

	// Try to get Gemini API key, though rollback might not need it,
	// but it's good practice for tasks.
	if apikey, err := GetGeminiAPIKey(opts.sandboxID); err == nil {
		env["GEMINI_API_KEY"] = apikey
	}

	err = tasks.RunTask(ctx, &task, opts.sandbox, opts.TaskDir, env)
	if err != nil {
		return fmt.Errorf("running rollback task: %w", err)
	}

	fmt.Println("Rollback task completed successfully")
	return nil
}
