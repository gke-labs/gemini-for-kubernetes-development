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

	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/clients"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/k8s"
	"github.com/spf13/cobra"
)

// BootstrapOptions holds options for the Bootstrap command.
type BootstrapOptions struct {
	Namespace string
}

// BuildBootstrapCommand builds the cobra command for bootstrapping a namespace.
func BuildBootstrapCommand() *cobra.Command {
	var opt BootstrapOptions

	cmd := &cobra.Command{
		Use:   "bootstrap",
		Short: "Bootstrap a namespace for development",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if opt.Namespace == "" {
				return fmt.Errorf("namespace is required")
			}
			return RunBootstrap(cmd.Context(), opt)
		},
	}
	cmd.Flags().StringVar(&opt.Namespace, "namespace", "", "Namespace to bootstrap")
	_ = cmd.MarkFlagRequired("namespace")

	return cmd
}

// RunBootstrap executes the bootstrap logic.
func RunBootstrap(ctx context.Context, opt BootstrapOptions) error {
	kube, err := clients.NewKubernetesClient()
	if err != nil {
		return err
	}
	clientset := kube.Clientset

	if err := k8s.BootstrapNamespace(ctx, clientset, opt.Namespace); err != nil {
		return fmt.Errorf("bootstrapping namespace %q: %w", opt.Namespace, err)
	}

	fmt.Printf("Namespace %q bootstrapped successfully\n", opt.Namespace)
	return nil
}
