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
	"encoding/json"
	"fmt"

	"github.com/gke-labs/gemini-for-kubernetes-development/agentsandboxes"
	"github.com/gke-labs/gemini-for-kubernetes-development/agentsandboxes/pkg/threads"
	"github.com/spf13/cobra"
	"k8s.io/apimachinery/pkg/types"
)

// ThreadsGetOptions holds options for the threads get command.
type ThreadsGetOptions struct {
	SandboxName string
	ThreadID    string
}

// InitDefaults initializes default values for ThreadsGetOptions.
func (o *ThreadsGetOptions) InitDefaults() {
}

// BuildThreadsGetCommand builds the cobra command for getting a thread.
func BuildThreadsGetCommand() *cobra.Command {
	var opt ThreadsGetOptions
	opt.InitDefaults()

	cmd := &cobra.Command{
		Use:   "get <sandbox-name> <thread-id>",
		Short: "Get a specific LLM thread in a sandbox",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			opt.SandboxName = args[0]
			opt.ThreadID = args[1]
			return RunThreadsGet(cmd.Context(), opt)
		},
	}
	return cmd
}

// RunThreadsGet executes the threads get logic.
func RunThreadsGet(ctx context.Context, opt ThreadsGetOptions) error {
	client, err := agentsandboxes.NewClient()
	if err != nil {
		return err
	}

	podID := types.NamespacedName{
		Namespace: client.Namespace(),
		Name:      opt.SandboxName,
	}

	executor := client.Executor(podID)
	thread, err := threads.GetThread(ctx, executor, opt.ThreadID, true)
	if err != nil {
		return err
	}

	b, err := json.MarshalIndent(thread, "", "  ")
	if err != nil {
		return err
	}
	fmt.Println(string(b))

	return nil
}
