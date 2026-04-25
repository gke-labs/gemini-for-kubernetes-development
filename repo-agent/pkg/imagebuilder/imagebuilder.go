package imagebuilder

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"k8s.io/klog/v2"
)

// ImageBuilder is responsible for initializing the container, e.g. installing dotfiles
type ImageBuilder struct {
	DotFilesRepo string
	CloneURL     string
	Destination  string
}

// CloneRepo clones source repo to the dest directory.
func (b *ImageBuilder) CloneRepo(ctx context.Context) error {
	if b.CloneURL == "" {
		return fmt.Errorf("CloneURL is not set")
	}

	if err := b.GitClone(ctx, b.CloneURL, b.Destination); err != nil {
		return err
	}

	return nil
}

// InstallDotfilesRepo clones and install a dotfiles repo
func (b *ImageBuilder) InstallDotfilesRepo(ctx context.Context) error {
	log := klog.FromContext(ctx)

	if b.DotFilesRepo == "" {
		return nil
	}

	// Get the user's cache directory
	cacheDir, err := os.UserCacheDir()
	if err != nil {
		return fmt.Errorf("getting user cache dir: %w", err)
	}

	dotfilesDir := filepath.Join(cacheDir, "repo-sandbox", "dotfiles")

	if err := b.GitClone(ctx, b.DotFilesRepo, dotfilesDir); err != nil {
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
		return fmt.Errorf("unable to find entrypoint in dotfiles repo %q", b.DotFilesRepo)
	}

	cmd := exec.CommandContext(ctx, foundEntrypoint)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		log.Error(err, "error running dotfiles entrypoint", "command", strings.Join(cmd.Args, " "), "stdout", stdout.String(), "stderr", stderr.String())
		return fmt.Errorf("error running dotfiles entrypoint %q from repo %q: %w", strings.Join(cmd.Args, " "), b.DotFilesRepo, err)
	}
	log.V(2).Info("successfully ran dotfiles entrypoint", "command", strings.Join(cmd.Args, " "), "stdout", stdout.String())

	return nil
}

// GitClone clones a git repo to the dest directory.
func (b *ImageBuilder) GitClone(ctx context.Context, source string, dest string) error {
	log := klog.FromContext(ctx)

	var ref string
	if strings.Contains(source, "#") {
		parts := strings.SplitN(source, "#", 2)
		source = parts[0]
		ref = parts[1]
	}

	// Strip refs/heads/ prefix if present
	ref = strings.TrimPrefix(ref, "refs/heads/")

	if ref != "" && isSHA(ref) {
		// Efficient clone of a single SHA to reduce startup time
		if err := os.MkdirAll(dest, 0755); err != nil {
			return fmt.Errorf("failed to create destination directory %q: %w", dest, err)
		}

		run := func(args ...string) error {
			var stdout, stderr bytes.Buffer
			cmd := exec.CommandContext(ctx, "git", args...)
			cmd.Dir = dest
			cmd.Stdout = &stdout
			cmd.Stderr = &stderr
			if err := cmd.Run(); err != nil {
				log.Error(err, "git command failed", "args", args, "stdout", stdout.String(), "stderr", stderr.String())
				return fmt.Errorf("git %s failed: %w", strings.Join(args, " "), err)
			}
			log.V(2).Info("git command succeeded", "args", args)
			return nil
		}

		if err := run("init"); err == nil {
			if err := run("remote", "add", "origin", source); err == nil {
				// fetch only the specific SHA with depth 1.
				// Note: this might fail if the server doesn't allow fetching specific SHAs.
				if err := run("fetch", "--depth", "1", "origin", ref); err == nil {
					if err := run("checkout", "FETCH_HEAD"); err == nil {
						return nil
					}
				}
			}
		}

		// If efficient fetch failed, fall back to standard clone and then checkout the SHA
		log.Info("efficient SHA fetch failed, falling back to standard clone and checkout", "source", source, "ref", ref)
		if err := os.RemoveAll(dest); err != nil {
			log.Error(err, "failed to clean up destination after failed efficient fetch", "dest", dest)
		}

		// Standard clone without -b (since -b doesn't work with SHAs)
		if err := b.runGitClone(ctx, source, dest, ""); err != nil {
			return err
		}

		// Now checkout the SHA
		cmd := exec.CommandContext(ctx, "git", "checkout", ref)
		cmd.Dir = dest
		var stdout, stderr bytes.Buffer
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr
		if err := cmd.Run(); err != nil {
			log.Error(err, "failed to checkout SHA after clone", "ref", ref, "stdout", stdout.String(), "stderr", stderr.String())
			return fmt.Errorf("git checkout %s failed: %w", ref, err)
		}
		return nil
	}

	return b.runGitClone(ctx, source, dest, ref)
}

func (b *ImageBuilder) runGitClone(ctx context.Context, source, dest, ref string) error {
	log := klog.FromContext(ctx)
	args := []string{
		"clone",
	}

	if ref != "" {
		args = append(args, "-b", ref)
	}

	args = append(args, source, dest)

	cmdString := "git " + strings.Join(args, " ")
	log.Info("cloning git repo", "source", source, "command", cmdString)

	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		log.Error(err, "git clone failed", "command", cmdString, "stdout", stdout.String(), "stderr", stderr.String())
		return fmt.Errorf("git clone failed: %w", err)
	}
	return nil
}

func isSHA(s string) bool {
	if len(s) < 7 || len(s) > 64 {
		return false
	}
	for _, r := range s {
		if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F')) {
			return false
		}
	}
	return true
}
