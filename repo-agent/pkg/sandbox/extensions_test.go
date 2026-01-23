package sandbox_test

import (
	"os"
	"os/exec"
	"reflect"
	"testing"

	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/sandbox"
)

func TestInstallExtensions(t *testing.T) {
	// Create a temporary extensions.txt file for testing
	content := `
	https://github.com/google-gemini/skill-kubernetes
	https://github.com/google-gemini/skill-kubernetes v1.2.0
	`
	tmpfile, err := os.CreateTemp("", "extensions.txt")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(tmpfile.Name())
	if _, err := tmpfile.Write([]byte(content)); err != nil {
		t.Fatal(err)
	}
	if err := tmpfile.Close(); err != nil {
		t.Fatal(err)
	}

	// Rename the temporary file to extensions.txt
	if err := os.Rename(tmpfile.Name(), "extensions.txt"); err != nil {
		t.Fatal(err)
	}
	defer os.Remove("extensions.txt")

	// Mock the exec.Command function
	var commands [][]string
	sandbox.ExecCommand = func(name string, arg ...string) *exec.Cmd {
		commands = append(commands, append([]string{name}, arg...))
		cmd := &exec.Cmd{
			Path: name,
			Args: append([]string{name}, arg...),
		}
		return cmd
	}

	// Call the InstallExtensions function
	if err := sandbox.InstallExtensions(); err != nil {
		t.Fatalf("InstallExtensions() failed: %v", err)
	}

	// Verify the commands that were executed
	expectedCommands := [][]string{
		{"gemini", "extensions", "install", "--auto-update", "--consent", "https://github.com/google-gemini/skill-kubernetes"},
		{"gemini", "extensions", "install", "--auto-update", "--consent", "https://github.com/google-gemini/skill-kubernetes", "v1.2.0"},
	}

	if !reflect.DeepEqual(commands, expectedCommands) {
		t.Errorf("expected commands %v, but got %v", expectedCommands, commands)
	}
}
