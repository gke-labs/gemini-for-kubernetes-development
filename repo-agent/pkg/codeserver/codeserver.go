package codeserver

import (
	"fmt"
	"os"
	"os/exec"
	"strings"

	"k8s.io/klog/v2"
)

const (
	CodeServerPort = 13337
	WorkspacePath  = "/workspaces"
)

var execCommand = exec.Command

func runDummyCommand() (*exec.Cmd, error) {
	cmd := execCommand("sleep", "infinity")
	cmd.Stdout = os.Stdout
	err := cmd.Start()
	if err != nil {
		return nil, err
	}
	klog.Infof("Running dummy command in subprocess %d\n", cmd.Process.Pid)
	return cmd, nil
}

func Start() (*exec.Cmd, error) {
	repoURL := os.Getenv("GIT_HTML_URL")
	parts := strings.Split(strings.TrimPrefix(repoURL, "https://github.com/"), "/")
	if len(parts) < 4 {
		return nil, fmt.Errorf("invalid GIT_HTML_URL: %s", repoURL)
	}
	repo := parts[1]

	codeServerPath := "/usr/bin/code-server"
	// check if code-server exists
	if _, err := os.Stat(codeServerPath); err != nil {
		if os.IsNotExist(err) {
			// code-server not found.
			klog.Info("code-server not found, running dummy command instead")
			return runDummyCommand()
		}
		return nil, err
	}

	klog.Info("starting code-server")
	args := []string{"--auth=none", fmt.Sprintf("--bind-addr=0.0.0.0:%d", CodeServerPort), WorkspacePath + "/" + repo}
	cmd := execCommand(codeServerPath, args...)
	cmd.Stdout = os.Stdout
	err := cmd.Start()
	if err != nil {
		return nil, err
	}
	klog.Infof("Running code-server in subprocess %d\n", cmd.Process.Pid)
	return cmd, nil
}
