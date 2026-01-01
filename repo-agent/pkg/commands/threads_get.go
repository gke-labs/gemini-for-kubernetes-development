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

// GetThreadsOptions holds options for the GetThreads function.
type GetThreadsOptions struct {
	SandboxName string
	ThreadID    string
}

// NewThreadsGetCommand creates a new cobra command for getting LLM threads/chats in the dev sandbox.
func NewThreadsGetCommand() *cobra.Command {
	var opt GetThreadsOptions

	cmd := &cobra.Command{
		Use:   "get [sandbox-name] [thread-id]",
		Short: "Get LLM thread/chat in the dev sandbox",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) != 2 {
				return fmt.Errorf("threads get command requires exactly two arguments: the sandbox name and the thread ID")
			}
			opt.SandboxName = args[0]
			opt.ThreadID = args[1]

			return RunGetThreads(cmd.Context(), opt)
		},
	}
	return cmd
}

// RunGetThreads gets LLM thread/chat in the specified dev sandbox.
func RunGetThreads(ctx context.Context, opt GetThreadsOptions) error {
	// 1. Find the pod
	podID, err := findSandboxPod(ctx, opt.SandboxName)
	if err != nil {
		return err
	}

	thread, err := getThread(ctx, *podID, opt.ThreadID)
	if err != nil {
		return fmt.Errorf("failed to get thread: %w", err)
	}

	for _, msg := range thread.Messages {
		fmt.Printf("%v\t%v\t%v\n", msg.Type, msg.Content, msg.Timestamp)
		for _, toolCall := range msg.ToolCalls {
			fmt.Printf("\tTool Call: %v\n", toolCall.Name)
		}
	}

	return nil
}

// getThread runs the agent to get a thread in the given dev sandbox pod.
func getThread(ctx context.Context, podID types.NamespacedName, threadID string) (*ThreadInfo, error) {
	args := []string{
		"kubectl", "exec", "--namespace", podID.Namespace, podID.Name, "--", "/repo-agent/dev-sandbox", "threads", "agent",
	}
	args = append(args, "--include-messages=true")
	args = append(args, fmt.Sprintf("--thread-id=%s", threadID))
	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
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

	if len(threads) == 0 {
		return nil, fmt.Errorf("thread with ID %q not found", threadID)
	}

	thread := threads[0]
	return &thread, nil
}
