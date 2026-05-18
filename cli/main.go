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

package main

import (
	"fmt"
	"os"

	"github.com/gke-labs/gemini-for-kubernetes-development/cli/cmd/installer"
	"github.com/gke-labs/gemini-for-kubernetes-development/cli/cmd/version"
	"github.com/spf13/cobra"
)

func BuildRootCommand() *cobra.Command {
	rootCmd := &cobra.Command{
		Use:   "codebot-cli subcommand",
		Short: "codebot-cli is an alpha cli tool for codebot.",
	}

	rootCmd.AddCommand(installer.BuildInstallerCmd())
	rootCmd.AddCommand(version.BuildVersionCmd())

	rootCmd.CompletionOptions.DisableDefaultCmd = true

	return rootCmd
}

func main() {
	rootCmd := BuildRootCommand()
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		os.Exit(1)
	}
}
