package gitcli

import (
	"bytes"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"testing"

	"k8s.io/klog/v2"
)

// TestHelperProcess isn't a real test. It's used as a helper process
// for Test* functions that use execCommand.
func TestHelperProcess(_ *testing.T) {
	if os.Getenv("GO_WANT_HELPER_PROCESS") != "1" {
		return
	}
	// args[0] is the test binary name
	// args[1] is "-test.run=TestHelperProcess"
	// args[2] is "--"
	// args[3] is the command name (e.g. "git")
	// args[4...] are the arguments to the command
	args := os.Args
	for len(args) > 0 {
		if args[0] == "--" {
			args = args[1:]
			break
		}
		args = args[1:]
	}
	if len(args) == 0 {
		fmt.Fprintf(os.Stderr, "No command\n")
		os.Exit(2)
	}

	cmd, args := args[0], args[1:]
	switch cmd {
	case "git":
		handleGitCommand(args)
	default:
		fmt.Fprintf(os.Stderr, "Unknown command %q\n", cmd)
		os.Exit(2)
	}
	os.Exit(0)
}

func handleGitCommand(args []string) {
	if len(args) == 0 {
		return
	}
	subcmd := args[0]
	switch subcmd {
	case "rev-parse":
		fmt.Println("1234567890abcdef")
	case "remote":
		// Handle remote add/remove
	case "status":
		// Check for --porcelain
		if len(args) > 1 && args[1] == "--porcelain" {
			if os.Getenv("GIT_STATUS_DIRTY") == "1" {
				fmt.Println("M  file.txt")
			}
		}
	case "branch":
		if len(args) > 1 && args[1] == "--list" {
			if len(args) > 2 && args[2] == "existing-branch" {
				fmt.Println("  existing-branch")
			}
		}
	}
}

func fakeExecCommand(command string, args ...string) *exec.Cmd {
	cs := []string{"-test.run=TestHelperProcess", "--", command}
	cs = append(cs, args...)
	cmd := exec.Command(os.Args[0], cs...)
	cmd.Env = []string{"GO_WANT_HELPER_PROCESS=1"}
	if val := os.Getenv("GIT_STATUS_DIRTY"); val != "" {
		cmd.Env = append(cmd.Env, "GIT_STATUS_DIRTY="+val)
	}
	return cmd
}

func TestGetHeadCommitID(t *testing.T) {
	execCommand = fakeExecCommand
	defer func() { execCommand = exec.Command }()

	id, err := GetHeadCommitID()
	if err != nil {
		t.Errorf("Expected no error, got %v", err)
	}
	if id != "1234567890abcdef" {
		t.Errorf("Expected commit ID 1234567890abcdef, got %s", id)
	}
}

func TestHasChanges(t *testing.T) {
	execCommand = fakeExecCommand
	defer func() { execCommand = exec.Command }()

	// Clean
	os.Setenv("GIT_STATUS_DIRTY", "")
	dirty, err := HasChanges()
	if err != nil {
		t.Errorf("Expected no error, got %v", err)
	}
	if dirty {
		t.Errorf("Expected clean status")
	}

	// Dirty
	os.Setenv("GIT_STATUS_DIRTY", "1")
	dirty, err = HasChanges()
	if err != nil {
		t.Errorf("Expected no error, got %v", err)
	}
	if !dirty {
		t.Errorf("Expected dirty status")
	}
	os.Unsetenv("GIT_STATUS_DIRTY")
}

func TestBranchExists(t *testing.T) {
	execCommand = fakeExecCommand
	defer func() { execCommand = exec.Command }()

	exists, err := BranchExists("existing-branch")
	if err != nil {
		t.Errorf("Expected no error, got %v", err)
	}
	if !exists {
		t.Errorf("Expected branch to exist")
	}

	exists, err = BranchExists("non-existing-branch")
	if err != nil {
		t.Errorf("Expected no error, got %v", err)
	}
	if exists {
		t.Errorf("Expected branch not to exist")
	}
}

func TestAddRemoteRedaction(t *testing.T) {
	execCommand = fakeExecCommand
	defer func() { execCommand = exec.Command }()

	// Capture stderr
	r, w, _ := os.Pipe()
	origStderr := os.Stderr
	os.Stderr = w

	// Ensure klog logs to stderr
	_ = flag.Set("logtostderr", "true")

	defer func() {
		os.Stderr = origStderr
	}()

	// URL with sensitive info
	err := AddRemote("origin", "https://user:secret-token@github.com/repo.git")
	if err != nil {
		t.Errorf("Expected no error, got %v", err)
	}

	// Force flush klog
	klog.Flush()
	w.Close()

	var buf bytes.Buffer
	_, _ = io.Copy(&buf, r)
	output := buf.String()

	if strings.Contains(output, "secret-token") {
		t.Errorf("Log output contains token: %s", output)
	}
	if !strings.Contains(output, "*****") {
		t.Errorf("Log output does not contain redaction: %s", output)
	}
}
