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
	"os/exec"
	"strings"

	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/sandbox"
	"github.com/spf13/cobra"
	"sigs.k8s.io/yaml"
)

// CreateOptions holds options for the Create command.
type CreateOptions struct {
	Name                  string
	Repo                  string
	Branch                string
	Dotfiles              string
	Namespace             string
	LLMProvider           string
	LLMSecret             string
	DevcontainerConfigRef string
	GithubLogin           string
	Image                 string
}

// InitDefaults initializes default values for CreateOptions.
func (o *CreateOptions) InitDefaults() {
	// No defaults to set currently
}

// BuildCreateCommand builds the cobra command for creating a dev sandbox.
func BuildCreateCommand() *cobra.Command {
	var opt CreateOptions
	opt.InitDefaults()

	cmd := &cobra.Command{
		Use:   "create [NAME]",
		Short: "Create a new dev sandbox",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			opt.Name = args[0]
			return RunCreate(cmd.Context(), opt)
		},
	}
	cmd.Flags().StringVar(&opt.Repo, "repo", "", "URL of the repository")
	cmd.Flags().StringVar(&opt.Branch, "branch", "", "Branch to checkout")
	cmd.Flags().StringVar(&opt.Dotfiles, "dotfiles", "", "URL of the dotfiles repository")
	cmd.Flags().StringVar(&opt.Namespace, "namespace", "default", "Namespace to create the sandbox in")
	cmd.Flags().StringVar(&opt.LLMProvider, "llm-provider", "gemini-cli", "LLM provider to use")
	cmd.Flags().StringVar(&opt.LLMSecret, "llm-secret", "", "LLM k8s secret to use")
	cmd.Flags().StringVar(&opt.DevcontainerConfigRef, "devcontainer-config-ref", "devcontainer-json", "Devcontainer config ref to use")
	cmd.Flags().StringVar(&opt.GithubLogin, "github-login", "", "GitHub login to use")
	cmd.Flags().StringVar(&opt.Image, "image", "", "Custom Docker image to use instead of devcontainer-config-ref")

	// Mark required flags using : _ = cmd.MarkFlagRequired("branch")

	return cmd
}

// From CreateOptions, return the HTML URL, clone URL, and origin URL
func urls(opt CreateOptions) (string, string, string) {
	repo := opt.Repo
	if repo == "" {
		// Try this command "git config --get remote.origin.url"
		out, err := exec.Command("git", "config", "--get", "remote.origin.url").Output()
		if err != nil {
			panic("repo URL must be provided")
		}
		repo = strings.TrimSpace(string(out))
	}

	htmlURL := strings.TrimSuffix(repo, ".git")
	cloneURL := htmlURL + ".git"
	if opt.Branch != "" {
		cloneURL += "#refs/heads/" + opt.Branch
	}
	originURL := htmlURL + ".git"
	return htmlURL, cloneURL, originURL
}

func userInfo(opt CreateOptions) (string, string, string) {
	// Try to get user info from git config
	nameOut, err := exec.Command("git", "config", "--get", "user.name").Output()
	if err != nil {
		panic("user name must be set in git config")
	}
	name := strings.TrimSpace(string(nameOut))

	emailOut, err := exec.Command("git", "config", "--get", "user.email").Output()
	if err != nil {
		panic("user email must be set in git config")
	}
	email := strings.TrimSpace(string(emailOut))

	return name, email, opt.GithubLogin
}

// RunCreate executes the create logic.
func RunCreate(ctx context.Context, opt CreateOptions) error {
	htmlURL, cloneURL, originURL := urls(opt)
	userName, userEmail, userLogin := userInfo(opt)

	secretName := opt.LLMSecret
	if secretName == "" {
		if opt.LLMProvider == "gemini-cli" {
			secretName = "gemini-vscode-tokens"
		} else if opt.LLMProvider == "claude" {
			secretName = "anthropic-api-key"
		} else {
			return fmt.Errorf("llm-secret must be provided for llm-provider %q", opt.LLMProvider)
		}
	}

	sandboxOpt := sandbox.DevSandboxOptions{
		Name:      opt.Name,
		Namespace: opt.Namespace,
		Labels: map[string]string{
			"createdBy": "repo-sandbox-cli",
		},
		CloneURL: cloneURL,
		HTMLURL:  htmlURL,
		Branch:   opt.Branch,
		// Default service account for CLI created sandboxes
		ServiceAccountName: "issue-sandbox",
		DotFilesRepo:       opt.Dotfiles,

		Origin:      originURL,
		PushEnabled: true,

		UserLogin: userLogin,
		UserName:  userName,
		UserEmail: userEmail,

		LLMProvider: opt.LLMProvider,
		// TODO: LLMConfigdirRef:     repoWatch.Spec.Dev.LLM.ConfigdirRef,
		LLMAPIKeySecretName: secretName,

		GithubSecretName:      "github-pat",
		DevcontainerConfigRef: opt.DevcontainerConfigRef,
		Image:                 opt.Image,

		HTTPEnabled: true,
		Replicas:    1,
	}

	if opt.Branch != "" {
		sandboxOpt.Branch = opt.Branch
	}

	sb, svc := sandbox.NewDevSandbox(sandboxOpt)

	sbData, err := yaml.Marshal(sb.Object)
	if err != nil {
		return fmt.Errorf("marshalling sandbox yaml: %w", err)
	}

	svcData, err := yaml.Marshal(svc)
	if err != nil {
		return fmt.Errorf("marshalling service yaml: %w", err)
	}

	data := append(sbData, []byte("\n---\n")...)
	data = append(data, svcData...)

	kubectlCmd := exec.CommandContext(ctx, "kubectl", "apply", "-f", "-")
	kubectlCmd.Stdin = strings.NewReader(string(data))
	kubectlCmd.Stdout = os.Stdout
	kubectlCmd.Stderr = os.Stderr

	if err := kubectlCmd.Run(); err != nil {
		return fmt.Errorf("running kubectl: %w", err)
	}

	fmt.Printf("DevSandbox %q created\n", opt.Name)
	return nil
}
