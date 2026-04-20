// Package sandbox manages the lifecycle of the agent's execution environment.
// It handles setting up the git repository, configuring the agent, and running the LLM workflow.
package sandbox

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/klog/v2"

	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/agentoutput"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/gitcli"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/llm"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/tokens"
)

// Config holds the configuration for running an agent in a sandbox.
type Config struct {
	AgentName        string
	AgentPrompt      string
	BranchName       string
	PushEnabled      bool
	GithubUserOrigin string
	GithubUserLogin  string
	GithubUserEmail  string
	GithubUserName   string
	GVR              schema.GroupVersionResource

	// Directory paths
	TaskDir       string
	RepoDir       string
	WorkspacesDir string
	TokensDir     string
	AgentOutput   *agentoutput.AgentOutput
}

func (c *Config) taskPath(name string, args ...interface{}) string {
	// Ensure the task path is correctly joined
	file := fmt.Sprintf(name, args...)
	return filepath.Join(c.TaskDir, file)
}

// PrepareGitBranch sets up the git environment for the agent.
// It configures the user, remote, and checks out the target branch.
func PrepareGitBranch(cfg Config) (string, error) {
	githubToken := tokens.GetGitHubToken()

	oldCommitID, err := gitcli.GetHeadCommitID()
	if err != nil {
		return "", fmt.Errorf("failed to get old commit id: %w", err)
	}

	// Typically origin would be the upstream repo and not the user's fork
	// Removing origin to prevent accidental pushes to upstream
	if err := gitcli.RemoveRemote("origin"); err != nil {
		klog.Infof("could not remove origin, probably because it does not exist: %v", err)
	}

	// Configure the origin remote to point to the user's fork if push is enabled.
	if cfg.PushEnabled && cfg.GithubUserOrigin != "" {
		if githubToken == "" {
			return oldCommitID, fmt.Errorf("GITHUB_TOKEN not found in environment variables (tried MANUAL_PAT, OAUTH_PAT, and GITHUB_TOKEN)")
		}
		originURL := fmt.Sprintf("https://%s:%s@%s", cfg.GithubUserLogin, githubToken, cfg.GithubUserOrigin)
		if err := gitcli.AddRemote("origin", originURL); err != nil {
			return oldCommitID, fmt.Errorf("failed to add origin: %w", err)
		}
	}

	// Configure git user identity for commits.
	if cfg.GithubUserEmail != "" {
		if err := gitcli.SetGlobalUserEmail(cfg.GithubUserEmail); err != nil {
			return oldCommitID, fmt.Errorf("failed to set git user email: %w", err)
		}
	}

	if cfg.GithubUserName != "" {
		if err := gitcli.SetGlobalUserName(cfg.GithubUserName); err != nil {
			return oldCommitID, fmt.Errorf("failed to set git user name: %w", err)
		}
	}

	// Checkout or create the working branch.
	if cfg.BranchName != "" {
		if err := gitcli.CheckoutOrCreateBranch(cfg.BranchName); err != nil {
			return oldCommitID, err
		}
	}

	return oldCommitID, nil
}

// RunAgent executes the agent workflow.
// It initializes the LLM provider, runs the agent with the prompt, and handles the output.
func RunAgent(ctx context.Context, cfg Config) error {
	log := klog.FromContext(ctx)
	log.Info("Starting agent", "agentName", cfg.AgentName)

	provider, err := llm.NewLLMProvider(llm.ProviderConfig{
		Name:          cfg.AgentName,
		WorkspacesDir: cfg.WorkspacesDir,
		TokensDir:     cfg.TokensDir,
		RepoDir:       cfg.RepoDir,
	})
	if err != nil {
		return err
	}

	// Setup provider-specific environment (e.g., copying .gemini folder).
	if err := provider.Setup(); err != nil {
		return err
	}

	// Update status to indicate the agent is running.
	_ = cfg.AgentOutput.SetAgentState(ctx, "running agent", "")

	if err := os.WriteFile(cfg.taskPath("agent-prompt.txt"), []byte(cfg.AgentPrompt), 0644); err != nil {
		return fmt.Errorf("failed to write agent-prompt.txt: %w", err)
	}

	// Execute the LLM agent.
	output, usage, err := provider.Run(cfg.AgentPrompt)
	if err != nil {
		var quotaErr *llm.QuotaError
		if errors.As(err, &quotaErr) {
			_ = cfg.AgentOutput.SetAgentState(ctx, "QUOTA ERROR", err.Error())
			log.Info("Agent run failed due to quota", "err", err)
			return err
		}
		log.Info("Agent run failed", "err", err, "output", string(output))
		return err
	}
	if err := os.WriteFile(cfg.taskPath("agent-output.txt"), output, 0644); err != nil {
		return fmt.Errorf("failed to write agent-output.txt: %w", err)
	}

	// Write stats for the task runner to pick up.
	if usage != nil {
		usageJSON, err := json.Marshal(usage)
		if err != nil {
			log.Error(err, "Failed to marshal stats")
		} else {
			if writeErr := os.WriteFile(cfg.taskPath("llm-usage.json"), usageJSON, 0644); writeErr != nil {
				log.Error(writeErr, "Failed to write llm-usage.json")
			}
		}
	}

	// Report the agent's output as a draft response.
	if err := cfg.AgentOutput.SetAgentDraft(ctx, string(output)); err != nil {
		return fmt.Errorf("failed to set agent draft: %w", err)
	}
	log.Info("Agent run completed successfully")
	// Cleanup resources (e.g., remove temporary files).
	if err := provider.Cleanup(); err != nil {
		return err
	}

	return nil
}

// ProcessGitChanges handles the post-agent execution git operations.
// It commits changes made by the agent and pushes them if enabled.
func ProcessGitChanges(ctx context.Context, cfg Config, oldCommitID string, commitMessage string) error {
	log := klog.FromContext(ctx)
	// Commit and push
	if cfg.GithubUserEmail != "" {
		if err := gitcli.CommitAllChanges(commitMessage); err != nil {
			return fmt.Errorf("failed to commit changes: %w", err)
		}
	}

	newCommitID, err := gitcli.GetHeadCommitID()
	if err != nil {
		return fmt.Errorf("failed to get new commit id: %w", err)
	}

	// Only push if there are new commits.
	if newCommitID != oldCommitID {
		log.Info("New changes being committed")
		if cfg.PushEnabled {
			if err := gitcli.Push("origin", cfg.BranchName, true); err != nil {
				return fmt.Errorf("failed to push changes: %w", err)
			}
			log.Info("New changes pushed")
		} else {
			log.Info("New changes not pushed. Git push not enabled")
		}
	}
	return nil
}
