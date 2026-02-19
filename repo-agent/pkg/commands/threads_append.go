package commands

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"

	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/clients"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/sandbox"
	"github.com/spf13/cobra"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/klog/v2"
)

// AppendToThreadOptions holds options for the AppendToThread function.
type AppendToThreadOptions struct {
	// The name of the dev sandbox.
	SandboxName string

	// The ID of the thread to comment on.
	ThreadID string
}

// NewAppendToThreadCommand creates a new cobra command for adding a prompt to an LLM thread (chat session) in the dev sandbox.
func NewAppendToThreadCommand() *cobra.Command {
	var opt AppendToThreadOptions

	cmd := &cobra.Command{
		Use:   "comment --sandbox [sandbox-name] --thread [thread-id]",
		Short: "Comment on LLM thread/chat in the dev sandbox",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) != 0 {
				return fmt.Errorf("threads append command does not take any arguments")
			}
			return RunAppendToThread(cmd.Context(), opt)
		},
	}

	cmd.Flags().StringVar(&opt.SandboxName, "sandbox", opt.SandboxName, "name of sandbox")
	cmd.Flags().StringVar(&opt.ThreadID, "thread", opt.ThreadID, "ID of the thread to comment on.")

	return cmd
}

// RunAppendToThread will add a comment to an existing thread in the specified dev sandbox.
func RunAppendToThread(ctx context.Context, opt AppendToThreadOptions) error {
	log := klog.FromContext(ctx)

	kube, err := clients.NewKubernetesClient()
	if err != nil {
		return err
	}

	if opt.SandboxName == "" {
		return fmt.Errorf("--sandbox is required")
	}

	if opt.ThreadID == "" {
		return fmt.Errorf("--thread is required")
	}

	comment, err := io.ReadAll(os.Stdin)
	if err != nil {
		return fmt.Errorf("failed to read comment from stdin: %w", err)
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

	// We need to read the current thread both to validate it exists and to infer the workspace
	thread, err := sandbox.GetThread(ctx, executor, opt.ThreadID, false)
	if err != nil {
		return fmt.Errorf("failed to get thread: %w", err)
	}

	cwd := thread.Workspace
	if cwd == "" {
		return fmt.Errorf("thread %q does not have a workspace associated with it", opt.ThreadID)
	}

	updated, err := appendToThread(ctx, kube, *podID, opt.ThreadID, cwd, comment)
	if err != nil {
		return fmt.Errorf("failed to get thread: %w", err)
	}
	log.V(2).Info("appended to thread", "thread", updated)

	return nil
}

// appendToThread runs the agent to append a comment to a thread in the given dev sandbox pod.
func appendToThread(ctx context.Context, kube *clients.KubernetesClient, podID types.NamespacedName, threadID string, cwd string, stdin []byte) (*sandbox.ThreadInfo, error) {
	// TODO: This is a bit of a hack, would be great to use a service portal
	geminiAPIKey, err := GetGeminiAPIKey(podID.Namespace + "/" + podID.Name)
	if err != nil {
		return nil, err
	}

	command := fmt.Sprintf("export GEMINI_API_KEY=%s && %s threads agent", geminiAPIKey, sandbox.RepoSandboxBinary)
	command += fmt.Sprintf(" --thread-id=%s", threadID)
	command += " --action=append"
	command += " --cwd=" + cwd

	var stdout bytes.Buffer
	execOptions := sandbox.ExecOptions{
		Command: []string{"sh", "-c", command},
		Secrets: []string{geminiAPIKey},
		Stdin:   stdin,
		Stdout:  &stdout,
	}

	if err := sandbox.ExecInPod(ctx, kube, podID, execOptions); err != nil {
		return nil, fmt.Errorf("failed to execute repo-sandbox agent in pod: %w", err)
	}

	var thread sandbox.ThreadInfo
	// TODO: Parse output to get updated thread info, particularly for a new thread
	return &thread, nil
}
