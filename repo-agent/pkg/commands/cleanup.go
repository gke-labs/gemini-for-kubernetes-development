package commands

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"k8s.io/klog/v2"
)

func startPeriodicCleanup(ctx context.Context) {
	log := klog.FromContext(ctx)
	log.Info("Starting periodic cleanup task")
	// Ticker for tmp directory cleanup (every hour)
	tmpTicker := time.NewTicker(1 * time.Hour)
	defer tmpTicker.Stop()

	// Ticker for go cache cleanup (every 6 hours)
	goTicker := time.NewTicker(6 * time.Hour)
	defer goTicker.Stop()

	// Initial cleanup
	performTmpCleanup(ctx)
	performGoCleanup(ctx)

	for {
		select {
		case <-tmpTicker.C:
			performTmpCleanup(ctx)
		case <-goTicker.C:
			performGoCleanup(ctx)
		case <-ctx.Done():
			return
		}
	}
}

func performTmpCleanup(ctx context.Context) {
	log := klog.FromContext(ctx)
	log.Info("Running periodic TMPDIR cleanup")

	// 1. Clean up TMPDIR
	tmpDir := os.Getenv("TMPDIR")
	if tmpDir != "" {
		cleanOldFiles(ctx, tmpDir, 24*time.Hour)
	}

	// 2. Clean up GOTMPDIR
	goTmpDir := os.Getenv("GOTMPDIR")
	if goTmpDir != "" && goTmpDir != tmpDir {
		cleanOldFiles(ctx, goTmpDir, 24*time.Hour)
	}
}

func performGoCleanup(ctx context.Context) {
	log := klog.FromContext(ctx)

	// Check if 'go' command exists
	if _, err := exec.LookPath("go"); err != nil {
		return
	}

	log.Info("Running periodic Go cache cleanup")

	// Clean up Go build cache
	cmd := exec.CommandContext(ctx, "go", "clean", "-cache")
	if err := cmd.Run(); err != nil {
		log.Error(err, "failed to run go clean -cache")
	}

	// Clean up Go test cache
	cmd = exec.CommandContext(ctx, "go", "clean", "-testcache")
	if err := cmd.Run(); err != nil {
		log.Error(err, "failed to run go clean -testcache")
	}

	// We don't clean modcache by default as it's very disruptive (requires re-downloading)
	// but we could add it if needed: 'go clean -modcache'
}

func cleanOldFiles(ctx context.Context, dir string, maxAge time.Duration) {
	log := klog.FromContext(ctx)

	entries, err := os.ReadDir(dir)
	if err != nil {
		if !os.IsNotExist(err) {
			log.Error(err, "failed to read directory for cleanup", "path", dir)
		}
		return
	}

	now := time.Now()
	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil {
			continue
		}

		// Don't delete very recent files
		if now.Sub(info.ModTime()) > maxAge {
			path := filepath.Join(dir, entry.Name())
			log.Info("Removing old file/dir", "path", path, "age", now.Sub(info.ModTime()))
			if err := os.RemoveAll(path); err != nil {
				log.Error(err, "failed to remove old file/dir", "path", path)
			}
		}
	}
}
