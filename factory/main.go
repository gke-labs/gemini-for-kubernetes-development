package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/commands"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/github"
	"k8s.io/klog/v2"
)

func main() {
	klog.InitFlags(nil)
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	// A task's child process reports its GitHub requests to its watch
	// (FACTORY_GITHUB_USAGE_FILE): every minute, and before it exits.
	stopUsage := github.ReportUsage(time.Minute)

	rootCmd := commands.NewRootCommand(ctx)
	err := rootCmd.ExecuteContext(ctx)
	stopUsage()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}
