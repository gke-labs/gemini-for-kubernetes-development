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

// CreateOptions holds options for the create command.
type CreateOptions struct {
	Name  string
	Image string
}

// InitDefaults initializes default values for CreateOptions.
func (o *CreateOptions) InitDefaults() error {
	if o.Image == "" {
		o.Image = "gcr.io/justinsb-knotai-dev/generic-golang:latest"
	}
	return nil
}

// BuildCreateCommand builds the cobra command for creating an agent sandbox.
func BuildCreateCommand() *cobra.Command {
	var opt CreateOptions

	cmd := &cobra.Command{
		Use:   "create [name]",
		Short: "Create an agent sandbox",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := opt.InitDefaults(); err != nil {
				return err
			}
			opt.Name = args[0]
			return RunCreate(cmd.Context(), opt)
		},
	}
	cmd.Flags().StringVar(&opt.Image, "image", "", "Container image for the sandbox")

	return cmd
}

// RunCreate executes the create logic.
func RunCreate(ctx context.Context, opt CreateOptions) error {
	client, err := agentsandboxes.NewClient()
	if err != nil {
		return err
	}
	builder := client.New(opt.Name)

	if opt.Image != "" {
		builder.Image(opt.Image)
	}

	sandbox, err := builder.Create(ctx)
	if err != nil {
		return err
	}

	fmt.Printf("Created sandbox: %s/%s\n", sandbox.Namespace, sandbox.Name)
	return nil
}
