// Copyright 2026 The Kubernetes Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package commands

import (
	"context"
	"fmt"
	"os"

	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/clients"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/sandbox"
	"github.com/spf13/cobra"
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

	threads, err := sandbox.ListThreads(ctx, executor)
	if err != nil {
		return fmt.Errorf("failed to list threads: %w", err)
	}

	for _, thread := range threads {
		fmt.Fprintf(os.Stdout, "%v\t%v\t%v\t%v\t%v\n", thread.Workspace, thread.SessionID, thread.ProjectHash, thread.StartTime, thread.TotalTokens)
	}

	return nil
}
