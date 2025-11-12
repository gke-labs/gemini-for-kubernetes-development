package main

import (
	"fmt"
	"log"
	"os"
	"os/exec"
)

func main() {
	cmdCodeSrv, err := startCodeServer()
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

	// Try solving the issue
	if err := runIssueSolver(); err != nil {
		log.Fatalf("failed solving issue: %v", err)
	}

	// Push the changes
	if err := processGitChanges(oldCommitID); err != nil {
		log.Fatalf("failed to process git changes: %v", err)
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

	cmdop, err := runCommand("git", "rev-parse", "HEAD")
	if err != nil {
		return "", fmt.Errorf("failed to get old commit id: %w", err)
	}
	oldCommitID := string(cmdop)

	// Typically origin would be the upstream repo and not the user's fork
	// Removing origin to prevent accidental pushes to upstream
	if _, err := runCommand("git", "remote", "remove", "origin"); err != nil {
		log.Printf("could not remove origin, probably because it does not exist: %v", err)
	}

	if gitPushEnabled && githubUserOrigin != "" {
		originURL := fmt.Sprintf("https://%s:%s@%s", githubUserLogin, githubToken, githubUserOrigin)
		if _, err := runCommand("git", "remote", "add", "origin", originURL); err != nil {
			return oldCommitID, fmt.Errorf("failed to add origin: %w", err)
		}
	}

	if githubUserEmail != "" {
		if _, err := runCommand("git", "config", "--global", "user.email", githubUserEmail); err != nil {
			return oldCommitID, fmt.Errorf("failed to set git user email: %w", err)
		}
	}

	if githubUserName != "" {
		if _, err := runCommand("git", "config", "--global", "user.name", githubUserName); err != nil {
			return oldCommitID, fmt.Errorf("failed to set git user name: %w", err)
		}
	}

	if _, err := runCommand("git", "checkout", "-b", issueBranch); err != nil {
		return oldCommitID, fmt.Errorf("failed to create issue branch: %w", err)
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
		if _, err := runCommand("git", "add", "."); err != nil {
			return fmt.Errorf("failed to git add: %v", err)
		}
		commitMsg := fmt.Sprintf("fix for issue # %s", issueID)
		if _, err := runCommand("git", "commit", "-m", commitMsg); err != nil {
			return fmt.Errorf("failed to git commit: %v", err)
		}
	}

	newCommitID, err := runCommand("git", "rev-parse", "HEAD")
	if err != nil {
		return fmt.Errorf("failed to get new commit id: %w", err)
	}

	if string(newCommitID) != oldCommitID {
		log.Println("New changes being committed")
		if gitPushEnabled {
			if _, err := runCommand("git", "push", "--set-upstream", "origin", issueBranch, "--force"); err != nil {
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
	log.Println("Starting issue solver")

	// Environment variables
	agentPrompt := os.Getenv("AGENT_PROMPT")

	// Handle .gemini directory
	if _, err := os.Stat("/workspaces/.gemini"); err == nil {
		log.Println(".gemini directory exists in /workspaces, copying to repo directory")
		if _, err := os.Stat(".gemini"); err == nil {
			log.Println(".gemini directory exists in repo directory, moving to .gemini.bak")
			if err := os.Rename(".gemini", ".gemini.bak"); err != nil {
				return fmt.Errorf("failed to move .gemini to .gemini.bak: %w", err)
			}
		}
		if _, err := runCommand("cp", "-R", "/workspaces/.gemini", ".gemini"); err != nil {
			return fmt.Errorf("failed to copy .gemini directory: %w", err)
		}
	} else {
		log.Println(".gemini directory does not exist in /workspaces")
	}

	// Run gemini
	if _, err := os.Stat("../agent-prompt.txt"); os.IsNotExist(err) {
		log.Println("agent-prompt.txt does not exist, running gemini")
		if err := os.WriteFile("../agent-prompt.txt", []byte(agentPrompt), 0644); err != nil {
			return fmt.Errorf("failed to write agent-prompt.txt: %w", err)
		}
		geminiAPIKey := os.Getenv("GEMINI_API_KEY")
		if geminiAPIKey == "" {
			geminiAPIKeyBytes, err := os.ReadFile("/tokens/gemini")
			if err != nil {
				return fmt.Errorf("failed to read gemini token: %w", err)
			}
			geminiAPIKey = string(geminiAPIKeyBytes)
		}
		cmd := exec.Command("gemini", "-y", "-p", agentPrompt)
		cmd.Env = append(os.Environ(), "GEMINI_API_KEY="+geminiAPIKey)
		output, err := cmd.CombinedOutput()
		if err != nil {
			log.Printf("gemini command failed: %v, output: %s", err, string(output))
		}
		if err := os.WriteFile("../agent-output.txt", output, 0644); err != nil {
			return fmt.Errorf("failed to write agent-output.txt: %w", err)
		}
	} else {
		log.Println("agent-prompt.txt exists, skipping gemini generation")
	}

	// Cleanup .gemini
	if _, err := os.Stat(".gemini.bak"); err == nil {
		log.Println("moving .gemini.bak -> .gemini")
		if err := os.RemoveAll(".gemini"); err != nil {
			log.Printf("failed to remove .gemini directory: %v", err)
		}
		if err := os.Rename(".gemini.bak", ".gemini"); err != nil {
			return fmt.Errorf("failed to move .gemini.bak to .gemini: %w", err)
		}
	}

	return nil
}

func runCommand(name string, args ...string) ([]byte, error) {
	cmd := exec.Command(name, args...)
	log.Printf("Running command: %s %v", name, args)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return output, fmt.Errorf("command %s %v failed with output %s: %w", name, args, string(output), err)
	}
	return output, nil
}

func startCodeServer() (*exec.Cmd, error) {
	log.Println("starting code-server")
	codeServerPath := "/usr/bin/code-server"
	args := []string{"--auth=none", "--bind-addr=0.0.0.0:13337"}
	cmd := exec.Command(codeServerPath, args...)
	cmd.Stdout = os.Stdout
	err := cmd.Start()
	if err != nil {
		return nil, err
	}
	log.Printf("Running code-server in subprocess %d\n", cmd.Process.Pid)
	return cmd, nil
}
