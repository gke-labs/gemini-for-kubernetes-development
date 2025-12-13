package main

import (
	"fmt"
	"log"
	"os"

	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/agentoutput"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/codeserver"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/gitcli"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/llm"
)

var (
	gvr = schema.GroupVersionResource{
		Group:    "custom.agents.x-k8s.io",
		Version:  "v1alpha1",
		Resource: "issuesandboxes",
	}
)

func main() {
	go agentoutput.Run("issue", gvr)

	cmdCodeSrv, err := codeserver.Start()
	if err != nil {
		log.Fatalf("failed to start code-server: %v", err)
	}
	defer func() {
		if cmdCodeSrv.Process != nil {
			_ = cmdCodeSrv.Process.Kill()
		}
	}()

	// Prepare git branch
	oldCommitID, err := prepareGitBranch()
	if err != nil {
		log.Fatalf("failed to prepare git branch: %v", err)
	}

	if _, err := os.Stat("../agent-prompt.txt"); os.IsNotExist(err) {
		// Try solving the issue
		if err := runIssueSolver(); err != nil {
			log.Fatalf("failed solving issue: %v", err)
		}

		// Push the changes
		if err := processGitChanges(oldCommitID); err != nil {
			log.Fatalf("failed to process git changes: %v", err)
		}
	} else {
		log.Println("agent-prompt.txt exists, skipping code generation")
	}

	// Wait for code-server to exit
	err = cmdCodeSrv.Wait()
	if err != nil {
		log.Printf("Code Server exited with error: %v", err)
	} else {
		log.Println("Code Server exited with no error")
	}
}

func prepareGitBranch() (string, error) {
	// Environment variables
	gitPushEnabled := os.Getenv("GIT_PUSH_ENABLED") == "true"
	githubUserOrigin := os.Getenv("GITHUB_USER_ORIGIN")
	githubUserLogin := os.Getenv("GITHUB_USER_LOGIN")
	githubToken := os.Getenv("GITHUB_TOKEN")
	githubUserEmail := os.Getenv("GITHUB_USER_EMAIL")
	githubUserName := os.Getenv("GITHUB_USER_NAME")
	issueBranch := os.Getenv("ISSUE_BRANCH")

	oldCommitID, err := gitcli.GetHeadCommitID()
	if err != nil {
		return "", fmt.Errorf("failed to get old commit id: %w", err)
	}

	// Typically origin would be the upstream repo and not the user's fork
	// Removing origin to prevent accidental pushes to upstream
	if err := gitcli.RemoveRemote("origin"); err != nil {
		log.Printf("could not remove origin, probably because it does not exist: %v", err)
	}

	if gitPushEnabled && githubUserOrigin != "" {
		originURL := fmt.Sprintf("https://%s:%s@%s", githubUserLogin, githubToken, githubUserOrigin)
		if err := gitcli.AddRemote("origin", originURL); err != nil {
			return oldCommitID, fmt.Errorf("failed to add origin: %w", err)
		}
	}

	if err := gitcli.SetGlobalUserEmail(githubUserEmail); err != nil {
		return oldCommitID, fmt.Errorf("failed to set git user email: %w", err)
	}

	if err := gitcli.SetGlobalUserName(githubUserName); err != nil {
		return oldCommitID, fmt.Errorf("failed to set git user name: %w", err)
	}

	if err := gitcli.CheckoutOrCreateBranch(issueBranch); err != nil {
		return oldCommitID, err
	}

	return oldCommitID, nil
}

func processGitChanges(oldCommitID string) error {
	// Environment variables
	gitPushEnabled := os.Getenv("GIT_PUSH_ENABLED") == "true"
	githubUserEmail := os.Getenv("GITHUB_USER_EMAIL")
	issueBranch := os.Getenv("ISSUE_BRANCH")
	issueID := os.Getenv("ISSUEID")

	// Commit and push
	if githubUserEmail != "" {
		if err := gitcli.CommitAllChanges("fix for issue # " + issueID); err != nil {
			return fmt.Errorf("failed to commit changes: %w", err)
		}
	}

	newCommitID, err := gitcli.GetHeadCommitID()
	if err != nil {
		return fmt.Errorf("failed to get new commit id: %w", err)
	}

	if newCommitID != oldCommitID {
		log.Println("New changes being committed")
		if gitPushEnabled {
			if err := gitcli.Push("origin", issueBranch, true); err != nil {
				return fmt.Errorf("failed to push changes: %w", err)
			}
			log.Println("New changes pushed")
		} else {
			log.Println("New changes not pushed. Git push not enabled")
		}
	}
	return nil
}

func runIssueSolver() error {
	agentName := os.Getenv("AGENT_NAME")
	log.Printf("Starting issue solver with AGENT_NAME: %s", agentName)

	// Environment variables
	agentPrompt := os.Getenv("AGENT_PROMPT")

	provider, err := llm.NewLLMProvider(agentName)
	if err != nil {
		return err
	}

	if err := provider.Setup("/workspaces", "/tokens"); err != nil {
		return err
	}

	// Run gemini
	log.Println("agent-prompt.txt does not exist, running gemini")
	if err := os.WriteFile("../agent-prompt.txt", []byte(agentPrompt), 0644); err != nil {
		return fmt.Errorf("failed to write agent-prompt.txt: %w", err)
	}

	output, err := provider.Run(agentPrompt)
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
