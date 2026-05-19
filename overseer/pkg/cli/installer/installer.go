// Copyright 2026 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package installer

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"

	"github.com/gke-labs/gemini-for-kubernetes-development/overseer/pkg/installer"
	"github.com/spf13/cobra"
)

const (
	examples = `
	# installer is used to install codebot on a Kubernetes cluster
	codebot installer
	`

	// flag names.
	flagProject     = "project"
	flagCluster     = "cluster"
	flagRegion      = "region"
	flagRepo        = "repo"
	flagWatchNs     = "watch-namespace"
	flagGeminiKey   = "gemini-api-key"
	flagGhPat       = "github-pat"
	flagOauthID     = "oauth-client-id"
	flagOauthSecret = "oauth-client-secret"
	flagBotName     = "bot-name"
	flagBotEmail    = "bot-email"
	flagAdmins      = "admin-users"
	flagKyverno     = "install-kyverno"
)

type Options struct {
	// GCP / GKE
	Project string
	Cluster string
	Region  string

	// Repository to watch
	RepoURL string
	// Namespace the RepoWatch CR lives in (defaults to a slug of the repo name)
	WatchNamespace string

	// Credentials
	GeminiAPIKey        string
	GitHubPAT           string
	GitHubOAuthClientID string // empty → single-user mode
	GitHubOAuthSecret   string

	// Git identity used by the bot for commits
	BotGitName  string
	BotGitEmail string

	// GitHub logins that may administer the UI (comma-separated)
	AdminUsers string

	// Whether to install Kyverno (required on GKE; can skip if already present)
	InstallKyverno bool
}

func BuildInstallerCmd() *cobra.Command {
	var opts Options

	cmd := &cobra.Command{
		Use:     "installer",
		Short:   "installer installs codebot on a Kubernetes cluster",
		Example: examples,
		RunE: func(cmd *cobra.Command, args []string) error {
			return RunInstaller(cmd.Context(), &opts)
		},
		Args: cobra.ExactArgs(0),
	}

	cmd.Flags().StringVar(&opts.Project, flagProject, "", "GCP project ID. (or set PROJECT env)")
	cmd.Flags().StringVar(&opts.Cluster, flagCluster, "", "GKE cluster name. (or set CLUSTER env)")
	cmd.Flags().StringVar(&opts.Region, flagRegion, "", "GKE cluster region/zone. (or set REGION env)")
	cmd.Flags().StringVar(&opts.RepoURL, flagRepo, "", "GitHub repository URL to watch (e.g. https://github.com/org/repo)")
	cmd.Flags().StringVar(&opts.WatchNamespace, flagWatchNs, "", "Namespace for RepoWatch CR (default: derived from repo name)")
	cmd.Flags().StringVar(&opts.GeminiAPIKey, flagGeminiKey, "", "Google Gemini API key. (or set GEMINI_API_KEY)")
	cmd.Flags().StringVar(&opts.GitHubPAT, flagGhPat, "", "GitHub Personal Access Token for the bot account")
	cmd.Flags().StringVar(&opts.GitHubOAuthClientID, flagOauthID, "", "GitHub OAuth App client ID (omit for single-user mode)")
	cmd.Flags().StringVar(&opts.GitHubOAuthSecret, flagOauthSecret, "", "GitHub OAuth App client secret")
	cmd.Flags().StringVar(&opts.BotGitName, flagBotName, "", "Git author name for bot commits")
	cmd.Flags().StringVar(&opts.BotGitEmail, flagBotEmail, "", "Git author email for bot commits")
	cmd.Flags().StringVar(&opts.AdminUsers, flagAdmins, "", "Comma-separated GitHub logins allowed to administer the UI")
	cmd.Flags().BoolVar(&opts.InstallKyverno, flagKyverno, true, "Install Kyverno (required on GKE for image-reference rewriting)")

	return cmd
}

func (opts *Options) validateFlags() []error {
	var errs []error
	if opts.Project == "" {
		opts.Project = os.Getenv("PROJECT")
	}
	if len(opts.Project) < 3 {
		errs = append(errs, fmt.Errorf("Unset or invalid project %q", opts.Project))
	}
	if opts.Cluster == "" {
		opts.Cluster = os.Getenv("CLUSTER")
	}
	if len(opts.Cluster) < 3 {
		errs = append(errs, fmt.Errorf("Unset or invalid cluster %q", opts.Cluster))
	}
	if opts.Region == "" {
		opts.Region = os.Getenv("REGION")
	}
	if len(opts.Region) < 3 {
		errs = append(errs, fmt.Errorf("Unset or invalid region %q", opts.Region))
	}
	if opts.GeminiAPIKey == "" {
		opts.GeminiAPIKey = os.Getenv("GEMINI_API_KEY")
	}
	if opts.GeminiAPIKey == "" {
		errs = append(errs, fmt.Errorf("Unset or invalid gemini API key %q", opts.GeminiAPIKey))
	}
	if opts.GitHubPAT == "" {
		opts.GitHubPAT = os.Getenv("GITHUB_PAT")
	}
	if opts.GitHubPAT == "" {
		errs = append(errs, fmt.Errorf("Unset or invalid github PAT %q", opts.GitHubPAT))
	}
	// if opts.GitHubOAuthClientID == "" {
	// 	opts.GitHubOAuthClientID = os.Getenv("GEMINI_OAUTH_ID")
	// }
	if opts.GitHubOAuthClientID != "" {
		// if opts.GitHubOAuthSecret == "" {
		// 	opts.Project = os.Getenv("GEMINI_OAUTH_SECRET")
		// }
		if opts.GitHubOAuthSecret == "" {
			errs = append(errs, fmt.Errorf("Unset or invalid github oauth secret %q", opts.GitHubOAuthSecret))
		}
	}
	if opts.WatchNamespace != "" && opts.RepoURL == "" {
		errs = append(errs, fmt.Errorf("Cannot set watch space %q, without a repo URL", opts.WatchNamespace))
	}
	if opts.WatchNamespace == "" && opts.RepoURL != "" {
		errs = append(errs, fmt.Errorf("Cannot set repo URL %q, without a watch spave", opts.RepoURL))
	}

	if len(errs) > 0 {
		return errs
	}
	return nil
}

func printErrors(errs []error) error {
	var result error
	for _, err := range errs {
		log.Printf("	Received error: %v", err)
		result = errors.Join(result, err)
	}
	return result
}

func RunInstaller(ctx context.Context, opts *Options) error {
	log.Printf("Running installer.")

	if errs := opts.validateFlags(); errs != nil {
		return printErrors(errs)
	}
	if errs := installer.CheckTools(); errs != nil {
		return printErrors(errs)
	}
	if err := installer.CheckGKEConnection(opts.Project, opts.Cluster, opts.Region); err != nil {
		return err
	}
	if err := installer.InstallEnvoyProxy("v1.5.2"); err != nil {
		log.Printf("*Hint*: Try running `gcloud auth login; gcloud auth application-default login`")
		return err
	}
	if err := installer.InstallAgentSandbox("v0.4.6"); err != nil {
		log.Printf("*Hint*: Try running `gcloud auth login; gcloud auth application-default login`")
		return err
	}
	if err := installer.InstallKRO("0.9.1"); err != nil {
		return err
	}
	if err := installer.InstallCRDs(); err != nil {
		return err
	}
	if err := installer.InstallSecrets("repo-agent-system", opts.GeminiAPIKey, opts.GitHubPAT, opts.GitHubOAuthClientID, opts.GitHubOAuthSecret); err != nil {
		return err
	}
	if err := installer.InstallRepoWatch(opts.WatchNamespace, opts.GeminiAPIKey, opts.GitHubPAT, opts.GitHubOAuthClientID, opts.GitHubOAuthSecret, opts.RepoURL); err != nil {
		return err
	}

	return nil
}
