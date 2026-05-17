package commands

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/spf13/cobra"
)

func NewUpCommand(ctx context.Context) *cobra.Command {
	var clusterName string
	var currentContext bool
	var recreate bool

	cmd := &cobra.Command{
		Use:   "up",
		Short: "Install required agent-sandbox CRDs and operator components into the Kubernetes cluster",
		Args:  cobra.NoArgs,
		RunE: func(_ *cobra.Command, args []string) error {
			if !currentContext {
				getCmd := exec.CommandContext(ctx, "kind", "get", "clusters")
				output, err := getCmd.Output()
				if err != nil {
					return fmt.Errorf("checking existing kind clusters: %w", err)
				}

				clusters := strings.Split(strings.TrimSpace(string(output)), "\n")
				exists := false
				for _, c := range clusters {
					if strings.TrimSpace(c) == clusterName {
						exists = true
						break
					}
				}

				if recreate && exists {
					fmt.Printf("Deleting existing kind cluster '%s'...\n", clusterName)
					delCmd := exec.CommandContext(ctx, "kind", "delete", "cluster", "--name", clusterName)
					delCmd.Stdout = os.Stdout
					delCmd.Stderr = os.Stderr
					if err := delCmd.Run(); err != nil {
						return fmt.Errorf("deleting kind cluster '%s': %w", clusterName, err)
					}
					exists = false
				}

				if exists {
					fmt.Printf("Kind cluster '%s' already exists, switching context to kind-%s...\n", clusterName, clusterName)
					switchCmd := exec.CommandContext(ctx, "kubectl", "config", "use-context", fmt.Sprintf("kind-%s", clusterName))
					if err := switchCmd.Run(); err != nil {
						return fmt.Errorf("switching to context kind-%s: %w", clusterName, err)
					}
				} else {
					fmt.Printf("Creating new kind cluster '%s'...\n", clusterName)
					createCmd := exec.CommandContext(ctx, "kind", "create", "cluster", "--name", clusterName)
					createCmd.Stdout = os.Stdout
					createCmd.Stderr = os.Stderr
					if err := createCmd.Run(); err != nil {
						return fmt.Errorf("creating kind cluster '%s': %w", clusterName, err)
					}
				}
			} else {
				fmt.Println("Using current kubectl context...")
			}

			return runInstall(ctx)
		},
	}

	cmd.Flags().StringVar(&clusterName, "cluster-name", "factory", "Name of the kind cluster to create or use")
	cmd.Flags().BoolVar(&currentContext, "current-context", false, "Use current kubectl context instead of creating/switching to a kind cluster")
	cmd.Flags().BoolVar(&recreate, "recreate", false, "Delete existing kind cluster and create a new one")

	return cmd
}

func runInstall(ctx context.Context) error {
	fmt.Println("Installing agent-sandbox...")

	cmd := exec.CommandContext(ctx, "kubectl", "cluster-info")
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("verifying cluster connection: %w (make sure KUBECONFIG is set and valid)", err)
	}

	manifestPath := "https://github.com/kubernetes-sigs/agent-sandbox/releases/download/v0.4.5/manifest.yaml"

	applyCmd := exec.CommandContext(ctx, "kubectl", "apply", "-f", manifestPath)
	applyCmd.Stdout = os.Stdout
	applyCmd.Stderr = os.Stderr
	if err := applyCmd.Run(); err != nil {
		return fmt.Errorf("applying agent-sandbox manifest %s: %w", manifestPath, err)
	}

	fmt.Println("Successfully installed agent-sandbox.")

	fmt.Println("\nChecking required parameters for user onboarding...")
	if err := RunUserOnboard(ctx, "", "", "", "", true); err != nil {
		fmt.Printf("Skipping user onboarding (%s).\n", err)
	}

	return nil
}
