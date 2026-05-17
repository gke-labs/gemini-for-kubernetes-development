package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/commands"
	"k8s.io/klog/v2"
)

func main() {
	klog.InitFlags(nil)
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	rootCmd := commands.NewRootCommand(ctx)
	if err := rootCmd.ExecuteContext(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}
