package commands

import (
	"context"
	"fmt"
	"net/http"
	"os"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/tokenusage"
	"github.com/spf13/cobra"
	"k8s.io/klog/v2"
)

func NewTokenDaemonCommand(ctx context.Context) *cobra.Command {
	cmd := &cobra.Command{
		Use:    "token-daemon",
		Short:  "Run the token-usage collector daemon that durably records gemini-cli usage pushed by factory tasks",
		Hidden: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) != 0 {
				return fmt.Errorf("token-daemon command does not take any arguments")
			}
			return runTokenDaemon(cmd.Context())
		},
	}
	return cmd
}

func runTokenDaemon(ctx context.Context) error {
	log := klog.FromContext(ctx)

	storageRoot := os.Getenv("STORAGE_ROOT")
	if storageRoot == "" {
		storageRoot = "/data"
	}

	addr := os.Getenv("ADDR")
	if addr == "" {
		addr = ":8080"
	}

	s, err := tokenusage.NewServer(storageRoot)
	if err != nil {
		return fmt.Errorf("creating token-usage server: %w", err)
	}

	log.Info("Starting token-usage collector", "addr", addr, "storageRoot", storageRoot)
	if err := http.ListenAndServe(addr, s.Handler()); err != nil {
		return fmt.Errorf("token-usage collector exited: %w", err)
	}
	return nil
}
