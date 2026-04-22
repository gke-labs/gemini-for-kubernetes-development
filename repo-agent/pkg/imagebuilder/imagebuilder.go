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
		// Clone then checkout
		args := []string{"git", "clone", source, dest}
		cmdString := strings.Join(args, " ")
		log.Info("cloning git repo for SHA checkout", "source", source, "command", cmdString)
		cmd := exec.CommandContext(ctx, args[0], args[1:]...)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("cloning git repo %q with %q: %w", source, cmdString, err)
		}

		checkoutArgs := []string{"git", "-C", dest, "checkout", ref}
		checkoutCmdString := strings.Join(checkoutArgs, " ")
		log.Info("checking out specific SHA", "sha", ref, "command", checkoutCmdString)
		checkoutCmd := exec.CommandContext(ctx, checkoutArgs[0], checkoutArgs[1:]...)
		checkoutCmd.Stdout = os.Stdout
		checkoutCmd.Stderr = os.Stderr
		if err := checkoutCmd.Run(); err != nil {
			return fmt.Errorf("checking out SHA %q with %q: %w", ref, checkoutCmdString, err)
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
