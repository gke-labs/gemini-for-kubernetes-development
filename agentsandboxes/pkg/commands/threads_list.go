/*
Copyright 2026 The Gemini Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package commands

import (
	"context"
	"fmt"

	"github.com/gke-labs/gemini-for-kubernetes-development/agentsandboxes"
	"github.com/gke-labs/gemini-for-kubernetes-development/agentsandboxes/pkg/threads"
	"github.com/spf13/cobra"
	"k8s.io/apimachinery/pkg/types"
)

// ThreadsListOptions holds options for the threads list command.
type ThreadsListOptions struct {
	SandboxName string
}

// InitDefaults initializes default values for ThreadsListOptions.
func (o *ThreadsListOptions) InitDefaults() error {
	return nil
}

// BuildThreadsListCommand builds the cobra command for listing threads.
func BuildThreadsListCommand() *cobra.Command {
	var opt ThreadsListOptions

	cmd := &cobra.Command{
		Use:   "list <sandbox-name>",
		Short: "List LLM threads in a sandbox",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := opt.InitDefaults(); err != nil {
				return err
			}
			opt.SandboxName = args[0]
			return RunThreadsList(cmd.Context(), opt)
		},
	}
	return cmd
}

// RunThreadsList executes the threads list logic.
func RunThreadsList(ctx context.Context, opt ThreadsListOptions) error {
	client, err := agentsandboxes.NewClient()
	if err != nil {
		return err
	}

	podID := types.NamespacedName{
		Namespace: client.Namespace(),
		Name:      opt.SandboxName,
	}

	executor := client.Executor(podID)
	list, err := threads.ListThreads(ctx, executor)
	if err != nil {
		return err
	}

	for _, t := range list {
		fmt.Printf("%s\n", t.SessionID)
	}
	return nil
}
