package imagebuilder

import (
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
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("error running dotfiles entrypoint %q from repo %q: %w", strings.Join(cmd.Args, " "), b.DotFilesRepo, err)
	}

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
			cmd := exec.CommandContext(ctx, "git", args...)
			cmd.Dir = dest
			cmd.Stdout = os.Stdout
			cmd.Stderr = os.Stderr
			if err := cmd.Run(); err != nil {
				return fmt.Errorf("git %s failed: %w", strings.Join(args, " "), err)
			}
			return nil
		}

		if err := run("init"); err != nil {
			return err
		}
		if err := run("remote", "add", "origin", source); err != nil {
			return err
		}
		// fetch only the specific SHA with depth 1
		if err := run("fetch", "--depth", "1", "origin", ref); err != nil {
			return err
		}
		if err := run("checkout", "FETCH_HEAD"); err != nil {
			return err
		}
		return nil
	}

	args := []string{
		"git",
		"clone",
	}

	if ref != "" {
		args = append(args, "-b", ref)
	}

	args = append(args, source, dest)

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

func isSHA(s string) bool {
	if len(s) != 40 && len(s) != 64 {
		return false
	}
	for _, r := range s {
		if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F')) {
			return false
		}
	}
	return true
}
