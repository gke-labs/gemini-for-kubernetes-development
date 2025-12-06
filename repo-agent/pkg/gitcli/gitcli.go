package gitcli

import (
	"fmt"
	"log"
	"os/exec"
	"strings"
)

var execCommand = exec.Command

func runCommand(args ...string) ([]byte, error) {
	name := "git"
	logArgs := make([]string, len(args))
	copy(logArgs, args)
	if len(args) > 3 && args[0] == "remote" && args[1] == "add" {
		logArgs[3] = "*****"
	}
	log.Printf("Running command: %s %v", name, logArgs)
	cmd := execCommand(name, args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return output, fmt.Errorf("command %s %v failed with output %s: %w", name, logArgs, string(output), err)
	}
	return output, nil
}

func GetHeadCommitID() (string, error) {
	out, err := runCommand("rev-parse", "HEAD")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

func RemoveRemote(name string) error {
	_, err := runCommand("remote", "remove", name)
	return err
}

func AddRemote(name, url string) error {
	_, err := runCommand("remote", "add", name, url)
	return err
}

func SetGlobalUserEmail(email string) error {
	if email == "" {
		return nil
	}
	_, err := runCommand("config", "--global", "user.email", email)
	return err
}

func SetGlobalUserName(name string) error {
	if name == "" {
		return nil
	}
	_, err := runCommand("config", "--global", "user.name", name)
	return err
}

func CheckoutOrCreateBranch(branch string) error {
	exists, err := BranchExists(branch)
	if err != nil {
		return fmt.Errorf("failed to list git branches: %w", err)
	}
	if exists {
		log.Printf("branch %s already exists, checking it out", branch)
		if err := CheckoutBranch(branch); err != nil {
			return fmt.Errorf("failed to checkout existing branch: %w", err)
		}
	} else {
		log.Printf("branch %s does not exist, creating it", branch)
		if err := CreateAndCheckoutBranch(branch); err != nil {
			return fmt.Errorf("failed to create issue branch: %w", err)
		}
	}
	return nil
}

func BranchExists(branch string) (bool, error) {
	out, err := runCommand("branch", "--list", branch)
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(string(out)) != "", nil
}

func CheckoutBranch(branch string) error {
	_, err := runCommand("checkout", branch)
	return err
}

func CreateAndCheckoutBranch(branch string) error {
	_, err := runCommand("checkout", "-b", branch)
	return err
}

func CommitAllChanges(message string) error {
	// Check if there are any changes to commit
	hasChanges, err := HasChanges()
	if err != nil {
		return fmt.Errorf("failed to get git status: %w", err)
	}
	if hasChanges {
		log.Println("Changes detected, committing")
		if err := AddAll(); err != nil {
			return fmt.Errorf("failed to git add: %v", err)
		}
		if err := Commit(message); err != nil {
			return fmt.Errorf("failed to git commit: %v", err)
		}
	}
	return nil
}

func HasChanges() (bool, error) {
	out, err := runCommand("status", "--porcelain")
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(string(out)) != "", nil
}

func AddAll() error {
	_, err := runCommand("add", ".")
	return err
}

func Commit(message string) error {
	_, err := runCommand("commit", "-m", message)
	return err
}

func Push(remote, branch string, force bool) error {
	args := []string{"push", "--set-upstream", remote, branch}
	if force {
		args = append(args, "--force")
	}
	_, err := runCommand(args...)
	return err
}
