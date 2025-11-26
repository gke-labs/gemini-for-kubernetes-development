package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"strings"

	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/dev-sandbox/sshd"
	"github.com/spf13/cobra"
	"k8s.io/klog/v2"
)

func main() {
	ctx := context.Background()

	if err := run(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	// fmt.Fprintf(os.Stderr, "Completed successfully\n")
}

func run(ctx context.Context) error {
	// log := klog.FromContext(ctx)

	rootCommand := &cobra.Command{}

	initCommand := &cobra.Command{
		Use: "init",
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) != 0 {
				return fmt.Errorf("init command does not take any arguments")
			}
			return InitContainer(cmd.Context())
		},
	}
	rootCommand.AddCommand(initCommand)

	sshdCommand := &cobra.Command{
		Use: "sshd",
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) != 0 {
				return fmt.Errorf("sshd command does not take any arguments")
			}
			return RunSSHD(cmd.Context())
		},
	}
	rootCommand.AddCommand(sshdCommand)

	return rootCommand.ExecuteContext(ctx)
}

func RunSSHD(ctx context.Context) error {
	log := klog.FromContext(ctx)

	conn := sshd.NewStdinStdoutConn(os.Stdin, os.Stdout)

	server := sshd.NewServer()

	if err := server.Start(ctx, conn); err != nil {
		log.Error(err, "SSH server exited with error")
		return fmt.Errorf("ssh server: %w", err)
	}

	// log.Info("SSH server exited successfully")
	return nil
}

func InitContainer(ctx context.Context) error {
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
