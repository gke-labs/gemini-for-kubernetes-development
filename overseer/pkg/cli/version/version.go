// Copyright 2026 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package version

import (
	"context"
	"log"

	"github.com/gke-labs/gemini-for-kubernetes-development/overseer/pkg/version"
	"github.com/spf13/cobra"
)

const (
	examples = `
	# version returns the version of the CLI tool.
	codebot version
	`
)

type Options struct {
}

func BuildVersionCmd() *cobra.Command {
	var opts Options

	cmd := &cobra.Command{
		Use:     "version",
		Short:   "version of the CLI tool",
		Example: examples,
		RunE: func(cmd *cobra.Command, args []string) error {
			return RunVersion(cmd.Context(), &opts)
		},
		Args: cobra.ExactArgs(0),
	}

	return cmd
}

func (opts *Options) validateFlags() error {
	return nil
}

func RunVersion(ctx context.Context, opts *Options) error {
	log.Printf("Running codebot version %s.", version.GetVersion())

	if err := opts.validateFlags(); err != nil {
		return err
	}

	return nil
}
