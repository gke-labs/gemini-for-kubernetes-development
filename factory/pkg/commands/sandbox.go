package commands

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/clients"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/envd"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/k8s"
	"github.com/spf13/cobra"
)

func NewSandboxCommand(ctx context.Context) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "sandbox",
		Short: "Manage Kubernetes sandboxes",
	}

	cmd.AddCommand(NewSandboxListCommand(ctx))
	cmd.AddCommand(NewSandboxDeleteCommand(ctx))
	cmd.AddCommand(NewSandboxCpCommand(ctx))
	cmd.AddCommand(NewSandboxExecCommand(ctx))
	cmd.AddCommand(NewSandboxInspectCommand(ctx))
	cmd.AddCommand(NewSandboxLogsCommand(ctx))

	return cmd
}

func NewSandboxInspectCommand(ctx context.Context) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "inspect [sandbox-name]",
		Short: "Inspect sandbox status, PVC usage, and active configuration",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			sandboxName := args[0]
			kubeClient, err := clients.NewKubernetesClient()
			if err != nil {
				return fmt.Errorf("creating k8s client: %w", err)
			}
			manager := k8s.NewManager(kubeClient)

			sb, err := manager.GetSandbox(ctx, namespace, sandboxName)
			if err != nil {
				return fmt.Errorf("getting sandbox '%s': %w", sandboxName, err)
			}

			fmt.Printf("Sandbox: %s\n", sb.GetName())
			fmt.Printf("Namespace: %s\n", sb.GetNamespace())
			fmt.Printf("Created: %s\n", sb.GetCreationTimestamp().Time.Format(time.RFC3339))

			if labels := sb.GetLabels(); labels != nil {
				fmt.Printf("Type: %s\n", labels["sandbox.gemini.google.com/type"])
			}

			podName, err := envd.GetSandboxPodName(ctx, namespace, sandboxName)
			if err == nil {
				fmt.Printf("Active Pod: %s\n", podName)
			} else {
				fmt.Printf("Active Pod: [Not Running]\n")
			}

			return nil
		},
	}
	return cmd
}

func NewSandboxLogsCommand(ctx context.Context) *cobra.Command {
	var daemon bool
	cmd := &cobra.Command{
		Use:   "logs [sandbox-name]",
		Short: "Stream task execution logs or envd daemon logs",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			sandboxName := args[0]
			podName, err := envd.GetSandboxPodName(ctx, namespace, sandboxName)
			if err != nil {
				return fmt.Errorf("getting sandbox pod: %w", err)
			}

			if daemon {
				cmd := exec.CommandContext(ctx, "kubectl", "logs", "-f", "-n", namespace, podName, "-c", "sandbox")
				cmd.Stdout = os.Stdout
				cmd.Stderr = os.Stderr
				return cmd.Run()
			}

			client, err := envd.Connect(ctx, namespace, sandboxName)
			if err != nil {
				return fmt.Errorf("connecting to sandbox: %w", err)
			}
			defer client.Close()

			fmt.Println("Streaming latest execution log...")
			return client.RunTask(ctx, "tail -f $(ls -td /workspaces/tasks/fix-* 2>/dev/null | head -1)/execution.log", nil)
		},
	}
	cmd.Flags().BoolVar(&daemon, "daemon", false, "Stream envd daemon logs instead of task execution logs")
	return cmd
}

func NewSandboxListCommand(ctx context.Context) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List sandboxes in the current namespace",
		RunE: func(_ *cobra.Command, _ []string) error {
			kubeClient, err := clients.NewKubernetesClient()
			if err != nil {
				return fmt.Errorf("creating k8s client: %w", err)
			}
			manager := k8s.NewManager(kubeClient)

			list, err := manager.ListSandboxes(ctx, namespace)
			if err != nil {
				return fmt.Errorf("listing sandboxes: %w", err)
			}

			if len(list.Items) == 0 {
				fmt.Printf("No sandboxes found in namespace '%s'.\n", namespace)
				return nil
			}

			w := tabwriter.NewWriter(os.Stdout, 0, 0, 4, ' ', 0)
			fmt.Fprintln(w, "NAME\tTYPE\tAGE")

			for _, item := range list.Items {
				name := item.GetName()
				sbType := "unknown"
				if labels := item.GetLabels(); labels != nil {
					if t, ok := labels["sandbox.gemini.google.com/type"]; ok {
						sbType = t
					} else if t, ok := labels["sandbox-type"]; ok {
						sbType = t
					}
				}

				creationTime := item.GetCreationTimestamp().Time
				age := time.Since(creationTime).Round(time.Second)

				fmt.Fprintf(w, "%s\t%s\t%s\n", name, sbType, age)
			}
			w.Flush()

			return nil
		},
	}
	return cmd
}

func NewSandboxDeleteCommand(ctx context.Context) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "delete [sandbox-name]",
		Short: "Delete a sandbox and its -lb service",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			sandboxName := args[0]
			kubeClient, err := clients.NewKubernetesClient()
			if err != nil {
				return fmt.Errorf("creating k8s client: %w", err)
			}
			manager := k8s.NewManager(kubeClient)

			fmt.Printf("Deleting sandbox '%s' and its service in namespace '%s'...\n", sandboxName, namespace)
			if err := manager.DeleteSandbox(ctx, namespace, sandboxName); err != nil {
				return fmt.Errorf("deleting sandbox: %w", err)
			}

			fmt.Printf("Successfully deleted sandbox '%s'.\n", sandboxName)
			return nil
		},
	}
	return cmd
}

func NewSandboxCpCommand(ctx context.Context) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "cp [sandbox-name] [src-file] [dest-path]",
		Short: "Copy a script or file to a specific path in the sandbox",
		Args:  cobra.ExactArgs(3),
		RunE: func(_ *cobra.Command, args []string) error {
			sandboxName, srcFile, destPath := args[0], args[1], args[2]

			podName, err := envd.GetSandboxPodName(ctx, namespace, sandboxName)
			if err != nil {
				return fmt.Errorf("getting sandbox pod: %w", err)
			}

			cmd := exec.CommandContext(ctx, "kubectl", "cp", srcFile, fmt.Sprintf("%s/%s:%s", namespace, podName, destPath), "-c", "sandbox")
			cmd.Stdout = os.Stdout
			cmd.Stderr = os.Stderr
			if err := cmd.Run(); err != nil {
				return fmt.Errorf("kubectl cp failed: %w", err)
			}
			fmt.Printf("Successfully copied '%s' to '%s' in sandbox '%s'.\n", srcFile, destPath, sandboxName)
			return nil
		},
	}
	return cmd
}

func NewSandboxExecCommand(ctx context.Context) *cobra.Command {
	var envs []string
	var cwd string
	cmd := &cobra.Command{
		Use:   "exec [sandbox-name] [command...]",
		Short: "Execute a command in the sandbox with stdin/stdout connected",
		Args:  cobra.MinimumNArgs(2),
		RunE: func(_ *cobra.Command, args []string) error {
			sandboxName := args[0]
			execArgs := strings.Join(args[1:], " ")

			client, err := envd.Connect(ctx, namespace, sandboxName)
			if err != nil {
				return fmt.Errorf("connecting to sandbox: %w", err)
			}
			defer client.Close()

			envMap := make(map[string]string)
			for _, e := range envs {
				parts := strings.SplitN(e, "=", 2)
				if len(parts) == 2 {
					envMap[parts[0]] = parts[1]
				}
			}

			return client.Exec(ctx, execArgs, cwd, envMap, os.Stdin, os.Stdout, os.Stderr)
		},
	}
	cmd.Flags().StringArrayVarP(&envs, "env", "e", nil, "Environment variables to set (e.g. -e KEY=VALUE)")
	cmd.Flags().StringVarP(&cwd, "cwd", "w", "/workspaces", "Working directory inside the container")
	return cmd
}
