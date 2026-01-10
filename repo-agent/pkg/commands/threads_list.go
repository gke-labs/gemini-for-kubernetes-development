package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"

	"github.com/spf13/cobra"
	"k8s.io/apimachinery/pkg/types"
)

// ListThreadsOptions holds options for the ListThreads function.
type ListThreadsOptions struct {
	SandboxName string
}

// NewThreadsListCommand creates a new cobra command for listing LLM threads/chats in the dev sandbox.
func NewThreadsListCommand() *cobra.Command {
	var opt ListThreadsOptions

	cmd := &cobra.Command{
		Use:   "list [sandbox-name]",
		Short: "List LLM threads/chats in the dev sandbox",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) != 1 {
				return fmt.Errorf("threads list command requires exactly one argument: the sandbox name")
			}
			opt.SandboxName = args[0]

			return RunListThreads(cmd.Context(), opt)
		},
	}
	return cmd
}

// RunListThreads lists LLM threads/chats in the specified dev sandbox.
func RunListThreads(ctx context.Context, opt ListThreadsOptions) error {
	// 1. Find the pod
	podID, err := findSandboxPod(ctx, opt.SandboxName)
	if err != nil {
		return err
	}
	if podID == nil {
		return fmt.Errorf("sandbox %q not found", opt.SandboxName)
	}

	threads, err := listThreads(ctx, *podID)
	if err != nil {
		return fmt.Errorf("failed to list threads: %w", err)
	}

	for _, thread := range threads {
		fmt.Fprintf(os.Stdout, "%v\t%v\t%v\t%v\t%v\n", thread.Workspace, thread.SessionID, thread.ProjectHash, thread.StartTime, thread.TotalTokens)
	}

	return nil
}

// listThreads runs the agent to list threads in the given dev sandbox pod.
func listThreads(ctx context.Context, podID types.NamespacedName) ([]ThreadInfo, error) {
	cmd := exec.CommandContext(ctx, "kubectl", "exec", "--namespace", podID.Namespace, podID.Name, "--", "/repo-agent/dev-sandbox", "threads", "agent")
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("failed to launch dev-sandbox agent via kubectl: %w", err)
	}

	var threads []ThreadInfo
	if err := json.Unmarshal(stdout.Bytes(), &threads); err != nil {
		return nil, fmt.Errorf("failed to parse threads agent output: %w", err)
	}
	return threads, nil
}
