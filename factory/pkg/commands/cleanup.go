package commands

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/clients"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/commands/common"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/k8s"
	"github.com/spf13/cobra"
	"k8s.io/klog/v2"
)

func startPeriodicCleanup(ctx context.Context) {
	log := klog.FromContext(ctx)
	log.Info("Starting periodic cleanup task")

	tmpInterval := common.GetEnvDuration("CLEANUP_TMP_INTERVAL", 1*time.Hour)
	goInterval := common.GetEnvDuration("CLEANUP_GO_INTERVAL", 6*time.Hour)
	diskCheckInterval := common.GetEnvDuration("CLEANUP_DISK_CHECK_INTERVAL", 2*time.Minute)

	// Ticker for tmp directory cleanup
	tmpTicker := time.NewTicker(tmpInterval)
	defer tmpTicker.Stop()

	// Ticker for go cache cleanup
	var goTickerChan <-chan time.Time
	if goInterval > 0 {
		goTicker := time.NewTicker(goInterval)
		defer goTicker.Stop()
		goTickerChan = goTicker.C
	}

	// Ticker for disk usage check
	diskTicker := time.NewTicker(diskCheckInterval)
	defer diskTicker.Stop()

	// Initial cleanup - only for TMP, not Go (to avoid wiping cache on every restart)
	performTmpCleanup(ctx)
	checkDiskUsageAndCleanup(ctx)

	for {
		select {
		case <-tmpTicker.C:
			performTmpCleanup(ctx)
		case <-goTickerChan:
			performGoCleanup(ctx)
		case <-diskTicker.C:
			checkDiskUsageAndCleanup(ctx)
		case <-ctx.Done():
			return
		}
	}
}

// getDiskUsagePercent returns the disk usage percentage for the filesystem containing path.
func getDiskUsagePercent(path string) (float64, error) {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(path, &stat); err != nil {
		return 0, err
	}
	if stat.Blocks == 0 {
		return 0, nil
	}
	usedBlocks := stat.Blocks - stat.Bfree
	return (float64(usedBlocks) / float64(stat.Blocks)) * 100.0, nil
}

// checkDiskUsageAndCleanup monitors /workspaces disk space and triggers aggressive cache cleanup if threshold is exceeded.
func checkDiskUsageAndCleanup(ctx context.Context) {
	workspaceDir := "/workspaces"
	if _, err := os.Stat(workspaceDir); os.IsNotExist(err) {
		return
	}

	usage, err := getDiskUsagePercent(workspaceDir)
	if err != nil {
		return
	}

	threshold := float64(common.GetEnvInt("CLEANUP_DISK_USAGE_THRESHOLD_PERCENT", 85))
	if usage >= threshold {
		log := klog.FromContext(ctx)
		log.Info("Disk usage on /workspaces exceeded threshold, triggering cache cleanup", "usagePercent", fmt.Sprintf("%.1f%%", usage), "thresholdPercent", fmt.Sprintf("%.0f%%", threshold))

		// 1. Clean TMPDIR with zero maxAge (clean all old temp files)
		if tmpDir := os.Getenv("TMPDIR"); tmpDir != "" {
			cleanOldFiles(ctx, tmpDir, 0)
		}
		if goTmpDir := os.Getenv("GOTMPDIR"); goTmpDir != "" {
			cleanOldFiles(ctx, goTmpDir, 0)
		}

		// 2. Clean Go build cache
		if _, err := exec.LookPath("go"); err == nil {
			log.Info("Running emergency go clean -cache to free /workspaces disk space")
			cmd := exec.CommandContext(ctx, "go", "clean", "-cache")
			if err := cmd.Run(); err != nil {
				log.Error(err, "failed to run go clean -cache during emergency cleanup")
			}
		}
	}
}

func performTmpCleanup(ctx context.Context) {
	log := klog.FromContext(ctx)
	log.Info("Running periodic TMPDIR cleanup")

	maxAge := common.GetEnvDuration("CLEANUP_TMP_MAX_AGE", 24*time.Hour)

	// 1. Clean up TMPDIR
	tmpDir := os.Getenv("TMPDIR")
	if tmpDir != "" {
		cleanOldFiles(ctx, tmpDir, maxAge)
	}

	// 2. Clean up GOTMPDIR
	goTmpDir := os.Getenv("GOTMPDIR")
	if goTmpDir != "" {
		// Normalize paths before comparison
		cleanTmp := filepath.Clean(tmpDir)
		cleanGoTmp := filepath.Clean(goTmpDir)
		if cleanGoTmp != cleanTmp {
			cleanOldFiles(ctx, goTmpDir, maxAge)
		}
	}
}

func performGoCleanup(ctx context.Context) {
	// The reviewer noted that Go 1.15+ automatically manages its build cache.
	// Unconditional 'go clean -cache' can break active builds.
	// We only run this if explicitly enabled via environment variable.
	if os.Getenv("CLEANUP_GO_CACHE_ENABLED") != "true" {
		return
	}

	log := klog.FromContext(ctx)

	// Check if 'go' command exists
	if _, err := exec.LookPath("go"); err != nil {
		return
	}

	log.Info("Running periodic Go cache cleanup")

	// Clean up Go build cache (this includes test cache)
	cmd := exec.CommandContext(ctx, "go", "clean", "-cache")
	if err := cmd.Run(); err != nil {
		log.Error(err, "failed to run go clean -cache")
	}
}

func cleanOldFiles(ctx context.Context, dir string, maxAge time.Duration) {
	log := klog.FromContext(ctx)

	dir = filepath.Clean(dir)
	// Safety check: don't clean root or very short paths
	if dir == "/" || dir == "" || len(dir) < 2 {
		log.Error(nil, "Refusing to clean directory (too dangerous or invalid)", "path", dir)
		return
	}

	now := time.Now()

	// We'll collect directories to try and remove them after their contents.
	var dirs []string

	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}

		// Don't delete the root directory being cleaned
		if path == dir {
			return nil
		}

		info, err := d.Info()
		if err != nil {
			return nil
		}

		age := now.Sub(info.ModTime())

		if d.IsDir() {
			dirs = append(dirs, path)
			return nil
		}

		// Don't delete very recent files
		if age > maxAge {
			log.Info("Removing old file", "path", path, "age", age)
			if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
				log.Error(err, "failed to remove old file", "path", path)
			}
		}
		return nil
	})

	if err != nil {
		log.Error(err, "error walking directory for cleanup", "path", dir)
	}

	// Remove empty directories in reverse order (bottom-up)
	for i := len(dirs) - 1; i >= 0; i-- {
		d := dirs[i]
		info, err := os.Stat(d)
		if err != nil {
			continue
		}
		if now.Sub(info.ModTime()) > maxAge {
			// os.Remove only succeeds if the directory is empty
			if err := os.Remove(d); err == nil {
				log.Info("Removed old empty directory", "path", d)
			}
		}
	}
}

type CleanupFlags struct {
	OlderThan time.Duration
	Overseer  bool
}

func NewCleanupCommand(ctx context.Context) *cobra.Command {
	var flags CleanupFlags

	cmd := &cobra.Command{
		Use:   "cleanup",
		Short: "Delete sandboxes older than a specified duration",
		RunE: func(_ *cobra.Command, _ []string) error {
			kubeClient, err := clients.NewKubernetesClient()
			if err != nil {
				return fmt.Errorf("creating k8s client: %w", err)
			}
			manager := k8s.NewManager(kubeClient)

			list, err := manager.ListSandboxes(ctx, rootFlags.Namespace)
			if err != nil {
				return fmt.Errorf("listing sandboxes: %w", err)
			}

			now := time.Now()
			deletedCount := 0

			for _, item := range list.Items {
				name := item.GetName()
				if !flags.Overseer {
					if strings.HasPrefix(name, "overseer-") {
						continue
					}
					labels := item.GetLabels()
					if labels != nil && labels["overseer.gemini.google.com/overseer"] != "" {
						continue
					}
				}
				creationTime := item.GetCreationTimestamp().Time
				if now.Sub(creationTime) > flags.OlderThan {
					fmt.Printf("Deleting sandbox '%s' (age: %s)...\n", name, now.Sub(creationTime).Round(time.Second))
					if err := manager.DeleteSandbox(ctx, rootFlags.Namespace, name); err != nil {
						klog.Errorf("Failed to delete sandbox '%s': %v", name, err)
					} else {
						deletedCount++
					}
				}
			}

			fmt.Printf("Successfully deleted %d sandboxes.\n", deletedCount)
			return nil
		},
	}

	cmd.Flags().DurationVar(&flags.OlderThan, "older-than", 24*time.Hour, "Delete sandboxes older than this duration")
	cmd.Flags().BoolVar(&flags.Overseer, "overseer", false, "Include overseer sandboxes in cleanup")

	return cmd
}
