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

package main

import (
	"fmt"
	"os"

	"github.com/gke-labs/gemini-for-kubernetes-development/agentsandboxes"
	"github.com/spf13/cobra"
)

func main() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

var rootCmd = &cobra.Command{
	Use:   "agentsandboxes",
	Short: "CLI for managing agent sandboxes",
}

func init() {
	rootCmd.AddCommand(listCmd)
	rootCmd.AddCommand(createCmd)
	rootCmd.AddCommand(deleteCmd)
}

var listCmd = &cobra.Command{
	Use:   "list",
	Short: "List agent sandboxes",
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx := cmd.Context()
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
	},
}

var createCmd = &cobra.Command{
	Use:   "create [name]",
	Short: "Create an agent sandbox",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		name := args[0]
		image, _ := cmd.Flags().GetString("image")

		ctx := cmd.Context()
		client, err := agentsandboxes.NewClient()
		if err != nil {
			return err
		}
		builder := client.New(name)

		if image != "" {
			builder.Image(image)
		}

		sandbox, err := builder.Create(ctx)
		if err != nil {
			return err
		}

		fmt.Printf("Created sandbox: %s/%s\n", sandbox.Namespace, sandbox.Name)
		return nil
	},
}

var deleteCmd = &cobra.Command{
	Use:   "delete [name]",
	Short: "Delete an agent sandbox",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		name := args[0]
		ctx := cmd.Context()
		client, err := agentsandboxes.NewClient()
		if err != nil {
			return err
		}

		if err := client.Delete(ctx, name); err != nil {
			return err
		}

		fmt.Printf("Deleted sandbox: %s\n", name)
		return nil
	},
}

func init() {
	createCmd.Flags().String("image", "gcr.io/justinsb-knotai-dev/generic-golang:latest", "Container image for the sandbox")
}
