package codeserver

import (
	"os"
	"os/exec"
	"testing"
)

func TestHelperProcess(_ *testing.T) {
	if os.Getenv("GO_WANT_HELPER_PROCESS") != "1" {
		return
	}
	os.Exit(0)
}

func fakeExecCommand(command string, args ...string) *exec.Cmd {
	cs := []string{"-test.run=TestHelperProcess", "--", command}
	cs = append(cs, args...)
	cmd := exec.Command(os.Args[0], cs...)
	cmd.Env = []string{"GO_WANT_HELPER_PROCESS=1"}
	return cmd
}

func TestStart(t *testing.T) {
	execCommand = fakeExecCommand
	defer func() { execCommand = exec.Command }()

	// Case 1: Invalid GIT_HTML_URL
	os.Setenv("GIT_HTML_URL", "invalid-url")
	cmd, err := Start()
	if err == nil {
		t.Error("Expected error for invalid GIT_HTML_URL")
	}
	if cmd != nil {
		t.Error("Expected nil cmd")
	}

	// Case 2: Valid GIT_HTML_URL
	os.Setenv("GIT_HTML_URL", "https://github.com/user/repo/pull/123")
	cmd, err = Start()
	if err != nil {
		t.Errorf("Expected no error, got %v", err)
	}
	if cmd == nil {
		t.Error("Expected cmd to be returned")
	}
}
