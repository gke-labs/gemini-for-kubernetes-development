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
	"fmt"

	"github.com/gke-labs/gemini-for-kubernetes-development/agentsandboxes"
	"github.com/spf13/cobra"
)

// ListOptions holds options for the list command.
type ListOptions struct {
}

// InitDefaults initializes default values for ListOptions.
func (o *ListOptions) InitDefaults() {
}

// BuildListCommand builds the cobra command for listing agent sandboxes.
func BuildListCommand() *cobra.Command {
	var opt ListOptions
	opt.InitDefaults()

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List agent sandboxes",
		RunE: func(cmd *cobra.Command, args []string) error {
			return RunList(cmd.Context(), opt)
		},
	}
	return cmd
}

// RunList executes the list logic.
func RunList(ctx context.Context, opt ListOptions) error {
	client, err := agentsandboxes.NewClient()
	if err != nil {
		return err
	}
	sandboxes, err := client.List(ctx)
	if err != nil {
		return err
	}

	for _, s := range sandboxes {
		fmt.Printf("%s/%s\n", s.Namespace, s.Name)
	}
	return nil
}
