package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"strings"

	"k8s.io/klog/v2"
)

func main() {
	ctx := context.Background()

	if err := run(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	fmt.Fprintf(os.Stderr, "Completed successfully\n")
}

func run(ctx context.Context) error {
	log := klog.FromContext(ctx)

	cmdCodeSrv, err := startCodeServer(ctx)
	if err != nil {
		return fmt.Errorf("failed to start code-server: %w", err)
	}
	defer func() {
		if cmdCodeSrv.Process != nil {
			if err := cmdCodeSrv.Process.Kill(); err != nil {
				log.Error(err, "killing process")
			}
		}
	}()

	// Wait for code-server to exit
	if err := cmdCodeSrv.Wait(); err != nil {
		return fmt.Errorf("code-server process exited with error: %w", err)
	}

	return nil
}

func startCodeServer(ctx context.Context) (*exec.Cmd, error) {
	log.Println("starting code-server")
	repoURL := os.Getenv("GIT_HTML_URL")
	parts := strings.Split(strings.TrimPrefix(repoURL, "https://github.com/"), "/")
	if len(parts) != 2 {
		return nil, fmt.Errorf("invalid GIT_HTML_URL: %s", repoURL)
	}
	repo := parts[1]
	codeServerPath := "/usr/bin/code-server"
	args := []string{"--auth=none", "--bind-addr=0.0.0.0:13337", "/workspaces/" + repo}
	cmd := exec.CommandContext(ctx, codeServerPath, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("running code-server command failed: %w", err)
	}
	log.Printf("Running code-server in subprocess %d\n", cmd.Process.Pid)
	return cmd, nil
}
