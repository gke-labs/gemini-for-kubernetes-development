package commands

import (
	"context"
	"fmt"
	"strings"

	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/clients"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/sandbox"
	"github.com/spf13/cobra"
)

// GetThreadsOptions holds options for the GetThreads function.
type GetThreadsOptions struct {
	SandboxName string
	ThreadID    string

	// Whether to include messages in the output
	IncludeMessages bool
}

func (o *GetThreadsOptions) InitDefaults() {
	o.IncludeMessages = true
}

// NewThreadsGetCommand creates a new cobra command for getting LLM threads/chats in the dev sandbox.
func NewThreadsGetCommand() *cobra.Command {
	var opt GetThreadsOptions

	opt.InitDefaults()

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

	cmd.Flags().BoolVar(&opt.IncludeMessages, "include-messages", opt.IncludeMessages, "Whether to include messages in the output")

	return cmd
}

// RunGetThreads gets LLM thread/chat in the specified dev sandbox.
func RunGetThreads(ctx context.Context, opt GetThreadsOptions) error {
	kube, err := clients.NewKubernetesClient()
	if err != nil {
		return err
	}

	// 1. Find the pod
	podID, err := sandbox.FindSandboxPod(ctx, opt.SandboxName)
	if err != nil {
		return err
	}
	if podID == nil {
		return fmt.Errorf("sandbox %q not found", opt.SandboxName)
	}

	executor := &sandbox.PodExecutor{
		Kube:  kube,
		PodID: *podID,
	}

	thread, err := sandbox.GetThread(ctx, executor, opt.ThreadID, opt.IncludeMessages)
	if err != nil {
		return fmt.Errorf("failed to get thread: %w", err)
	}

	for _, msg := range thread.Messages {
		fmt.Printf("%v\t%v\t%v\n", msg.Type, msg.Content, msg.Timestamp)
		for _, toolCall := range msg.ToolCalls {
			var args []string
			for k, v := range toolCall.Arguments {
				args = append(args, fmt.Sprintf("%v=%v", k, v))
			}
			fmt.Printf("\tTool Call: %v(%v)\n", toolCall.Name, strings.Join(args, ","))
		}
	}

	return nil
}
