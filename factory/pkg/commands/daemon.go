package commands

import (
	"context"
	"fmt"
	"os"
	"os/exec"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/sandbox"
	"github.com/spf13/cobra"
	"k8s.io/klog/v2"
)

func NewDaemonCommand(ctx context.Context) *cobra.Command {
	cmd := &cobra.Command{
		Use:    "daemon",
		Short:  "Run the factory sandbox daemon to initialize workspace storage and start envd",
		Hidden: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) != 0 {
				return fmt.Errorf("daemon command does not take any arguments")
			}
			return runDaemon(cmd.Context())
		},
	}
	return cmd
}

func runDaemon(ctx context.Context) error {
	log := klog.FromContext(ctx)

	// Ensure cache and temporary directories exist on /workspaces
	// This is important for Go builds to avoid ephemeral storage exhaustion.
	dirs := []string{
		sandbox.GoCachePath,
		sandbox.GoModCachePath,
		sandbox.TmpDirPath,
		os.Getenv("GOCACHE"),
		os.Getenv("GOMODCACHE"),
		os.Getenv("TMPDIR"),
		os.Getenv("GOTMPDIR"),
	}
	for _, dir := range dirs {
		if dir == "" {
			continue
		}
		if err := os.MkdirAll(dir, 0755); err != nil {
			log.Error(err, "failed to create directory", "path", dir)
		} else {
			log.Info("Initialized directory", "path", dir)
		}
	}

	// Start periodic cleanup in background
	go startPeriodicCleanup(ctx)

	log.Info("Starting envd daemon...")

	cmd := exec.CommandContext(ctx, "envd", "--isnotfc")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		log.Error(err, "envd daemon exited with error")
		return fmt.Errorf("envd daemon exited: %w", err)
	}

	log.Info("envd daemon exited successfully")
	return nil
}
