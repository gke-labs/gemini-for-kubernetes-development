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

// Package gitcli provides a wrapper around the git command line interface.
// It simplifies common git operations and handles command execution and logging.
package gitcli

import (
	"fmt"
	"os/exec"
	"strings"

	"k8s.io/klog/v2"
)

var execCommand = exec.Command

// runCommand executes a git command with the given arguments.
// It logs the command (sanitizing sensitive information) and returns the combined output.
func runCommand(args ...string) ([]byte, error) {
	name := "git"
	logArgs := make([]string, len(args))
	copy(logArgs, args)
	// Sanitize sensitive information in logs (e.g., tokens in URL)
	if len(args) > 3 && args[0] == "remote" && args[1] == "add" {
		logArgs[3] = "*****"
	}
	klog.Infof("Running command: %s %v", name, logArgs)
	cmd := execCommand(name, args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return output, fmt.Errorf("command %s %v failed with output %s: %w", name, logArgs, string(output), err)
	}
	return output, nil
}

// GetHeadCommitID returns the current HEAD commit hash.
func GetHeadCommitID() (string, error) {
	out, err := runCommand("rev-parse", "HEAD")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// RemoveRemote removes a git remote by name.
func RemoveRemote(name string) error {
	_, err := runCommand("remote", "remove", name)
	return err
}

// AddRemote adds a new git remote.
func AddRemote(name, url string) error {
	_, err := runCommand("remote", "add", name, url)
	return err
}

// SetGlobalUserEmail sets the global git user email.
func SetGlobalUserEmail(email string) error {
	if email == "" {
		return nil
	}
	_, err := runCommand("config", "--global", "user.email", email)
	return err
}

// SetGlobalUserName sets the global git user name.
func SetGlobalUserName(name string) error {
	if name == "" {
		return nil
	}
	_, err := runCommand("config", "--global", "user.name", name)
	return err
}

// CheckoutOrCreateBranch checks out an existing branch or creates a new one if it doesn't exist.
func CheckoutOrCreateBranch(branch string) error {
	exists, err := BranchExists(branch)
	if err != nil {
		return fmt.Errorf("failed to list git branches: %w", err)
	}
	if exists {
		klog.Infof("branch %s already exists, checking it out", branch)
		if err := CheckoutBranch(branch); err != nil {
			return fmt.Errorf("failed to checkout existing branch: %w", err)
		}
	} else {
		klog.Infof("branch %s does not exist, creating it", branch)
		if err := CreateAndCheckoutBranch(branch); err != nil {
			return fmt.Errorf("failed to create issue branch: %w", err)
		}
	}
	return nil
}

// BranchExists checks if a branch exists locally.
func BranchExists(branch string) (bool, error) {
	out, err := runCommand("branch", "--list", branch)
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(string(out)) != "", nil
}

// CheckoutBranch checks out an existing branch.
func CheckoutBranch(branch string) error {
	_, err := runCommand("checkout", branch)
	return err
}

// CreateAndCheckoutBranch creates a new branch and checks it out.
func CreateAndCheckoutBranch(branch string) error {
	_, err := runCommand("checkout", "-b", branch)
	return err
}

// CommitAllChanges stages all changes (git add .) and commits them with the given message.
// It checks if there are changes before attempting to commit.
func CommitAllChanges(message string) error {
	// Check if there are any changes to commit
	hasChanges, err := HasChanges()
	if err != nil {
		return fmt.Errorf("failed to get git status: %w", err)
	}
	if hasChanges {
		klog.Info("Changes detected, committing")
		if err := AddAll(); err != nil {
			return fmt.Errorf("failed to git add: %v", err)
		}
		if err := Commit(message); err != nil {
			return fmt.Errorf("failed to git commit: %v", err)
		}
	}
	return nil
}

// HasChanges checks if there are any modified files (staged or unstaged).
func HasChanges() (bool, error) {
	out, err := runCommand("status", "--porcelain")
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(string(out)) != "", nil
}

// AddAll stages all changes in the current directory.
func AddAll() error {
	_, err := runCommand("add", ".")
	return err
}

// Commit creates a commit with the given message.
func Commit(message string) error {
	_, err := runCommand("commit", "-m", message)
	return err
}

// Push pushes the branch to the remote. If force is true, it uses --force.
func Push(remote, branch string, force bool) error {
	args := []string{"push", "--set-upstream", remote, branch}
	if force {
		args = append(args, "--force")
	}
	_, err := runCommand(args...)
	return err
}

// ResetHard performs a git reset --hard to the specified target (commit hash or branch).
func ResetHard(target string) error {
	_, err := runCommand("reset", "--hard", target)
	return err
}
