package commands

import (
	"context"
	"fmt"

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
	clientset, _, err := GetClientset()
	if err != nil {
		return err
	}

	if err := k8s.BootstrapNamespace(ctx, clientset, opt.Namespace); err != nil {
		return fmt.Errorf("bootstrapping namespace %q: %w", opt.Namespace, err)
	}

	fmt.Printf("Namespace %q bootstrapped successfully\n", opt.Namespace)
	return nil
}
