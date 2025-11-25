package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"k8s.io/klog/v2"
)

func main() {
	ctx := context.Background()

	// Listen to signals so we can gracefully shutdown
	ctx, stopListeningToSignals := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stopListeningToSignals() // Ensure we stop listening to signals.

	if err := run(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	fmt.Fprintf(os.Stderr, "Completed successfully\n")
}

func run(ctx context.Context) error {
	log := klog.FromContext(ctx)

	var b ImageBuilder
	if dotFilesRepo := os.Getenv("USER_DOTFILESREPO"); dotFilesRepo != "" {
		if err := b.InstallDotfilesRepo(ctx, dotFilesRepo); err != nil {
			// Note: we don't fail the entire startup if dotfiles installation fails
			log.Error(err, "installing dotfiles repo", "repo", dotFilesRepo)
		}
	}

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

// ImageBuilder is responsible for initializing the container, e.g. installing dotfiles
type ImageBuilder struct {
}

// InstallDotfilesRepo clones and install a dotfiles repo
func (b *ImageBuilder) InstallDotfilesRepo(ctx context.Context, dotFilesRepo string) error {
	log := klog.FromContext(ctx)

	// Get the user's cache directory
	cacheDir, err := os.UserCacheDir()
	if err != nil {
		return fmt.Errorf("getting user cache dir: %w", err)
	}

	dotfilesDir := filepath.Join(cacheDir, "dev-sandbox", "dotfiles")

	if err := b.GitClone(ctx, dotFilesRepo, dotfilesDir); err != nil {
		return err
	}

	// Well-known entrypoints
	entrypoints := []string{
		"setup",
	}

	var foundEntrypoint string
	for _, entrypoint := range entrypoints {
		p := filepath.Join(dotfilesDir, entrypoint)
		if _, err := os.Stat(p); err != nil {
			if !os.IsNotExist(err) {
				log.Error(err, "error checking for entrypoint", "entrypoint", p)
			}
			continue
		}
		foundEntrypoint = p
		break
	}

	if foundEntrypoint == "" {
		return fmt.Errorf("unable to find entrypoint in dotfiles repo %q", dotFilesRepo)
	}

	cmd := exec.CommandContext(ctx, foundEntrypoint)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("error running dotfiles entrypoint %q from repo %q: %w", strings.Join(cmd.Args, " "), dotFilesRepo, err)
	}

	return nil
}

// GitClone clones a git repo to the dest directory.
func (b *ImageBuilder) GitClone(ctx context.Context, source string, dest string) error {
	log := klog.FromContext(ctx)

	args := []string{
		"git",
		"clone",
		source,
		dest,
	}

	cmdString := strings.Join(args, " ")
	log.Info("cloning git repo", "source", source, "command", cmdString)

	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("cloning git repo %q with %q: %w", source, cmdString, err)
	}

	return nil
}
