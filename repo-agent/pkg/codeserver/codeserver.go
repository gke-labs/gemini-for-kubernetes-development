package codeserver

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"strings"
)

const (
	CodeServerPort = 13337
	WorkspacePath  = "/workspaces"
)

var execCommand = exec.Command

func Start() (*exec.Cmd, error) {
	log.Println("starting code-server")
	repoURL := os.Getenv("GIT_HTML_URL")
	parts := strings.Split(strings.TrimPrefix(repoURL, "https://github.com/"), "/")
	if len(parts) < 4 {
		return nil, fmt.Errorf("invalid GIT_HTML_URL: %s", repoURL)
	}
	repo := parts[1]
	codeServerPath := "/usr/bin/code-server"
	args := []string{"--auth=none", fmt.Sprintf("--bind-addr=0.0.0.0:%d", CodeServerPort), WorkspacePath + "/" + repo}
	cmd := execCommand(codeServerPath, args...)
	cmd.Stdout = os.Stdout
	err := cmd.Start()
	if err != nil {
		return nil, err
	}
	log.Printf("Running code-server in subprocess %d\n", cmd.Process.Pid)
	return cmd, nil
}
