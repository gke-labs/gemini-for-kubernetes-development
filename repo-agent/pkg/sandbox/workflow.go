package sandbox

import (
	"context"
	"fmt"
	"log"
	"os"

	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/agentoutput"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/gitcli"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/llm"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/tokens"
)

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
	ReportStatus     bool
}

func PrepareGitBranch(cfg Config) (string, error) {
	githubToken := tokens.GetGitHubToken()

	oldCommitID, err := gitcli.GetHeadCommitID()
	if err != nil {
		return "", fmt.Errorf("failed to get old commit id: %w", err)
	}

	// Typically origin would be the upstream repo and not the user's fork
	// Removing origin to prevent accidental pushes to upstream
	if err := gitcli.RemoveRemote("origin"); err != nil {
		log.Printf("could not remove origin, probably because it does not exist: %v", err)
	}

	if cfg.PushEnabled && cfg.GithubUserOrigin != "" {
		if githubToken == "" {
			return oldCommitID, fmt.Errorf("GITHUB_TOKEN not found in environment variables (tried MANUAL_PAT, OAUTH_PAT, and GITHUB_TOKEN)")
		}
		originURL := fmt.Sprintf("https://%s:%s@%s", cfg.GithubUserLogin, githubToken, cfg.GithubUserOrigin)
		if err := gitcli.AddRemote("origin", originURL); err != nil {
			return oldCommitID, fmt.Errorf("failed to add origin: %w", err)
		}
	}

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

	if cfg.BranchName != "" {
		if err := gitcli.CheckoutOrCreateBranch(cfg.BranchName); err != nil {
			return oldCommitID, err
		}
	}

	return oldCommitID, nil
}

func RunAgent(ctx context.Context, cfg Config) error {
	log.Printf("Starting agent with AGENT_NAME: %s", cfg.AgentName)

	provider, err := llm.NewLLMProvider(cfg.AgentName)
	if err != nil {
		return err
	}

	if err := provider.Setup("/workspaces", "/tokens"); err != nil {
		return err
	}

	// Run gemini
	if cfg.ReportStatus {
		_ = agentoutput.SetAgentState(ctx, cfg.GVR, "running agent", "")
	}

	// We assume we are in the repo directory or the prompt should be written where the agent can find it?
	// issue-sandbox writes to "../agent-prompt.txt".
	// Let's stick to that for now to avoid breaking things, or make it configurable.
	// For dev-sandbox, /workspaces/REPO is the CWD. So ".." is /workspaces.
	if err := os.WriteFile("../agent-prompt.txt", []byte(cfg.AgentPrompt), 0644); err != nil {
		return fmt.Errorf("failed to write agent-prompt.txt: %w", err)
	}

	output, err := provider.Run(cfg.AgentPrompt)
	if err != nil {
		log.Printf("Agent run failed: %v, output: %s", err, string(output))
	}
	if err := os.WriteFile("../agent-output.txt", output, 0644); err != nil {
		return fmt.Errorf("failed to write agent-output.txt: %w", err)
	}

	// Cleanup
	if err := provider.Cleanup("/workspaces"); err != nil {
		return err
	}

	return nil
}

func ProcessGitChanges(ctx context.Context, cfg Config, oldCommitID string, commitMessage string) error {
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

	if newCommitID != oldCommitID {
		log.Println("New changes being committed")
		if cfg.PushEnabled {
			if cfg.ReportStatus {
				_ = agentoutput.SetAgentState(ctx, cfg.GVR, "pushing changes", "")
			}
			if err := gitcli.Push("origin", cfg.BranchName, true); err != nil {
				return fmt.Errorf("failed to push changes: %w", err)
			}
			log.Println("New changes pushed")
		} else {
			log.Println("New changes not pushed. Git push not enabled")
		}
	}
	return nil
}
