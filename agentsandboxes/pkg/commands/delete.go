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

// DeleteOptions holds options for the delete command.
type DeleteOptions struct {
	Name string
}

// InitDefaults initializes default values for DeleteOptions.
func (o *DeleteOptions) InitDefaults() error {
	return nil
}

// BuildDeleteCommand builds the cobra command for deleting an agent sandbox.
func BuildDeleteCommand() *cobra.Command {
	var opt DeleteOptions

	cmd := &cobra.Command{
		Use:   "delete [name]",
		Short: "Delete an agent sandbox",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := opt.InitDefaults(); err != nil {
				return err
			}
			opt.Name = args[0]
			return RunDelete(cmd.Context(), opt)
		},
	}
	return cmd
}

// RunDelete executes the delete logic.
func RunDelete(ctx context.Context, opt DeleteOptions) error {
	client, err := agentsandboxes.NewClient()
	if err != nil {
		return err
	}

	if err := client.Delete(ctx, opt.Name); err != nil {
		return err
	}

	fmt.Printf("Deleted sandbox: %s\n", opt.Name)
	return nil
}
