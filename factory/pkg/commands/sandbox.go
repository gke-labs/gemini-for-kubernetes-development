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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func validateSandboxName(name string) error {
	if strings.HasPrefix(name, "-") {
		return fmt.Errorf("invalid sandbox name '%s': cannot begin with '-' (make sure flags precede positional arguments)", name)
	}
	return nil
}

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
	cmd.AddCommand(NewSandboxConnectCommand(ctx))
	cmd.AddCommand(NewSandboxChatCommand(ctx))

	return cmd
}

func NewSandboxInspectCommand(ctx context.Context) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "inspect [sandbox-name]",
		Short: "Inspect sandbox status, PVC usage, and active configuration",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			sandboxName := args[0]
			if err := validateSandboxName(sandboxName); err != nil {
				return err
			}

			kubeClient, err := clients.NewKubernetesClient()
			if err != nil {
				return fmt.Errorf("creating k8s client: %w", err)
			}
			manager := k8s.NewManager(kubeClient)

			sb, err := manager.GetSandbox(ctx, rootFlags.Namespace, sandboxName)
			if err != nil {
				return fmt.Errorf("getting sandbox '%s': %w", sandboxName, err)
			}

			fmt.Printf("Sandbox: %s\n", sb.GetName())
			fmt.Printf("Namespace: %s\n", sb.GetNamespace())
			fmt.Printf("Created: %s\n", sb.GetCreationTimestamp().Time.Format(time.RFC3339))

			if labels := sb.GetLabels(); labels != nil {
				fmt.Printf("Type: %s\n", labels["sandbox.gemini.google.com/type"])
			}

			podName, err := envd.GetSandboxPodName(ctx, rootFlags.Namespace, sandboxName)
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

type SandboxLogsFlags struct {
	Daemon bool
}

func NewSandboxLogsCommand(ctx context.Context) *cobra.Command {
	var flags SandboxLogsFlags
	cmd := &cobra.Command{
		Use:   "logs [sandbox-name]",
		Short: "Stream task execution logs or envd daemon logs",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			sandboxName := args[0]
			if err := validateSandboxName(sandboxName); err != nil {
				return err
			}

			podName, err := envd.GetSandboxPodName(ctx, rootFlags.Namespace, sandboxName)
			if err != nil {
				return fmt.Errorf("getting sandbox pod: %w", err)
			}

			if flags.Daemon {
				cmd := exec.CommandContext(ctx, "kubectl", "logs", "-f", "-n", rootFlags.Namespace, podName, "-c", "sandbox")
				cmd.Stdout = os.Stdout
				cmd.Stderr = os.Stderr
				return cmd.Run()
			}

			client, err := envd.Connect(ctx, rootFlags.Namespace, sandboxName)
			if err != nil {
				return fmt.Errorf("connecting to sandbox: %w", err)
			}
			defer client.Close()

			fmt.Println("Streaming latest execution log...")
			return client.RunTask(ctx, "tail -f $(ls -td /workspaces/tasks/* 2>/dev/null | head -1)/execution.log", nil)
		},
	}
	cmd.Flags().BoolVar(&flags.Daemon, "daemon", false, "Stream envd daemon logs instead of task execution logs")
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

			list, err := manager.ListSandboxes(ctx, rootFlags.Namespace)
			if err != nil {
				return fmt.Errorf("listing sandboxes: %w", err)
			}

			if len(list.Items) == 0 {
				fmt.Printf("No sandboxes found in namespace '%s'.\n", rootFlags.Namespace)
				return nil
			}

			podList, _ := kubeClient.Clientset.CoreV1().Pods(rootFlags.Namespace).List(ctx, metav1.ListOptions{})

			w := tabwriter.NewWriter(os.Stdout, 0, 0, 4, ' ', 0)
			fmt.Fprintln(w, "NAME\tTYPE\tSTATUS\tLAST TASK\tPR/ISSUE URL\tAGE")

			for _, item := range list.Items {
				name := item.GetName()
				if name == "" {
					continue
				}
				sbType := "unknown"
				if labels := item.GetLabels(); labels != nil {
					if t, ok := labels["sandbox.gemini.google.com/type"]; ok {
						sbType = t
					} else if t, ok := labels["sandbox-type"]; ok {
						sbType = t
					}
				}
				if sbType == "review" {
					sbType = "pr"
				}

				podStatusStr := ""
				if podList != nil {
					for _, pod := range podList.Items {
						if pod.Labels["sandbox"] == name && pod.DeletionTimestamp == nil {
							podStatusStr = string(pod.Status.Phase)
							if len(pod.Status.ContainerStatuses) > 0 {
								state := pod.Status.ContainerStatuses[0].State
								if state.Waiting != nil && state.Waiting.Reason != "" {
									podStatusStr = state.Waiting.Reason
								} else if state.Terminated != nil && state.Terminated.Reason != "" {
									podStatusStr = state.Terminated.Reason
								}
							}
							if podStatusStr == "Pending" {
								for _, cond := range pod.Status.Conditions {
									if cond.Type == "PodScheduled" && cond.Status == "False" && cond.Reason == "Unschedulable" {
										podStatusStr = "Unschedulable"
										break
									}
								}
							}
							break
						}
					}
				}

				if podStatusStr == "" {
					if ann := item.GetAnnotations(); ann != nil {
						if s, ok := ann["sandbox.gemini.google.com/pod-status"]; ok && s != "" {
							podStatusStr = s
						}
					}
				}
				if podStatusStr == "" {
					podStatusStr = "No Pod"
				}

				lastTaskStr := "-"
				htmlURL := "-"
				if ann := item.GetAnnotations(); ann != nil {
					tType := ann["sandbox.gemini.google.com/last-task-type"]
					tState := ann["sandbox.gemini.google.com/last-task-state"]
					if tType != "" && tState != "" {
						lastTaskStr = fmt.Sprintf("%s (%s)", tType, tState)
					}

					if u, ok := ann["sandbox.gemini.google.com/html-url"]; ok && u != "" {
						htmlURL = u
					} else if u, ok := ann["htmlURL"]; ok && u != "" {
						htmlURL = u
					}
				}

				creationTime := item.GetCreationTimestamp().Time
				age := time.Since(creationTime).Round(time.Second)

				fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n", name, sbType, podStatusStr, lastTaskStr, htmlURL, age)
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
			if err := validateSandboxName(sandboxName); err != nil {
				return err
			}

			kubeClient, err := clients.NewKubernetesClient()
			if err != nil {
				return fmt.Errorf("creating k8s client: %w", err)
			}
			manager := k8s.NewManager(kubeClient)

			fmt.Printf("Deleting sandbox '%s' and its service in namespace '%s'...\n", sandboxName, rootFlags.Namespace)
			if err := manager.DeleteSandbox(ctx, rootFlags.Namespace, sandboxName); err != nil {
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
			if err := validateSandboxName(sandboxName); err != nil {
				return err
			}

			podName, err := envd.GetSandboxPodName(ctx, rootFlags.Namespace, sandboxName)
			if err != nil {
				return fmt.Errorf("getting sandbox pod: %w", err)
			}

			cmd := exec.CommandContext(ctx, "kubectl", "cp", srcFile, fmt.Sprintf("%s/%s:%s", rootFlags.Namespace, podName, destPath), "-c", "sandbox")
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

type SandboxExecFlags struct {
	Envs []string
	Cwd  string
}

func NewSandboxExecCommand(ctx context.Context) *cobra.Command {
	var flags SandboxExecFlags
	cmd := &cobra.Command{
		Use:   "exec [sandbox-name] [command...]",
		Short: "Execute a command in the sandbox with stdin/stdout connected",
		Long: `Execute a command in the sandbox with stdin/stdout connected.
Note: factory flags (-e, -w) must precede positional arguments ([sandbox-name] [command...]).`,
		Args: cobra.MinimumNArgs(2),
		RunE: func(_ *cobra.Command, args []string) error {
			sandboxName := args[0]
			if err := validateSandboxName(sandboxName); err != nil {
				return err
			}
			execArgs := strings.Join(args[1:], " ")

			client, err := envd.Connect(ctx, rootFlags.Namespace, sandboxName)
			if err != nil {
				return fmt.Errorf("connecting to sandbox: %w", err)
			}
			defer client.Close()

			envMap := make(map[string]string)
			for _, e := range flags.Envs {
				parts := strings.SplitN(e, "=", 2)
				if len(parts) == 2 {
					envMap[parts[0]] = parts[1]
				}
			}

			return client.Exec(ctx, execArgs, flags.Cwd, envMap, os.Stdin, os.Stdout, os.Stderr)
		},
	}
	cmd.Flags().StringArrayVarP(&flags.Envs, "env", "e", nil, "Environment variables to set (e.g. -e KEY=VALUE)")
	cmd.Flags().StringVarP(&flags.Cwd, "cwd", "w", "/workspaces", "Working directory inside the container")
	return cmd
}

func NewSandboxConnectCommand(ctx context.Context) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "connect [sandbox-name]",
		Short: "Connect to a sandbox via interactive tmux session",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			sandboxName := args[0]
			if err := validateSandboxName(sandboxName); err != nil {
				return err
			}

			podName, err := envd.GetSandboxPodName(ctx, rootFlags.Namespace, sandboxName)
			if err != nil {
				return fmt.Errorf("getting sandbox pod: %w", err)
			}

			fmt.Printf("Connecting to tmux session in sandbox '%s'...\n", sandboxName)
			cmd := exec.CommandContext(ctx, "kubectl", "exec", "-it", "-n", rootFlags.Namespace, podName, "-c", "sandbox", "--", "sh", "-c", "tmux attach || tmux new-session")
			cmd.Stdin = os.Stdin
			cmd.Stdout = os.Stdout
			cmd.Stderr = os.Stderr
			if err := cmd.Run(); err != nil {
				return fmt.Errorf("tmux session exited: %w", err)
			}
			return nil
		},
	}
	return cmd
}

type SandboxChatFlags struct {
	ListSessions bool
	Resume       string
}

func NewSandboxChatCommand(ctx context.Context) *cobra.Command {
	var flags SandboxChatFlags
	cmd := &cobra.Command{
		Use:   "chat [sandbox-name]",
		Short: "Connect to a sandbox and resume a Gemini CLI chat session",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			sandboxName := args[0]
			if err := validateSandboxName(sandboxName); err != nil {
				return err
			}

			kubeClient, err := clients.NewKubernetesClient()
			if err != nil {
				return fmt.Errorf("creating k8s client: %w", err)
			}

			secret, err := kubeClient.Clientset.CoreV1().Secrets(rootFlags.Namespace).Get(ctx, rootFlags.SecretName, metav1.GetOptions{})
			if err != nil {
				return fmt.Errorf("fetching %s secret in namespace %s: %w (make sure to run 'factory user onboard' first)", rootFlags.SecretName, rootFlags.Namespace, err)
			}
			geminiKey := getGeminiAPIKey(secret)
			if geminiKey == "" {
				return fmt.Errorf("GEMINI_API_KEY not found in secret %s and TOKENSCRIPT_DIR was not set or returned empty", rootFlags.SecretName)
			}

			podName, err := envd.GetSandboxPodName(ctx, rootFlags.Namespace, sandboxName)
			if err != nil {
				return fmt.Errorf("getting sandbox pod: %w", err)
			}

			manager := k8s.NewManager(kubeClient)
			sb, err := manager.GetSandbox(ctx, rootFlags.Namespace, sandboxName)
			if err != nil {
				return fmt.Errorf("getting sandbox '%s': %w", sandboxName, err)
			}

			var repoDir string
			if ann := sb.GetAnnotations(); ann != nil {
				if r, ok := ann["repo"]; ok && r != "" {
					repoDir = fmt.Sprintf("/workspaces/%s", r)
				}
			}

			// Extract repo directory from Sandbox annotations if available, falling back to auto-detecting via find
			detectRepoScript := `REPO_DIR=$(find /workspaces -maxdepth 2 -name .git 2>/dev/null | head -1 | sed 's|/.git||'); if [ -n "$REPO_DIR" ]; then cd "$REPO_DIR"; else cd /workspaces; fi;`
			if repoDir != "" {
				detectRepoScript = fmt.Sprintf(`if [ -d "%s" ]; then cd "%s"; else REPO_DIR=$(find /workspaces -maxdepth 2 -name .git 2>/dev/null | head -1 | sed 's|/.git||'); if [ -n "$REPO_DIR" ]; then cd "$REPO_DIR"; else cd /workspaces; fi; fi;`, repoDir, repoDir)
			}

			// Backup session files before running Gemini CLI, and restore them afterward if Gemini deletes them on exit due to inactivity
			backupScript := `CHAT_DIR="/root/.gemini/tmp/$(basename "$PWD")/chats"; if [ -d "$CHAT_DIR" ]; then mkdir -p "$CHAT_DIR/backup"; cp "$CHAT_DIR"/*.jsonl "$CHAT_DIR/backup/" 2>/dev/null || true; fi;`
			restoreScript := `if [ -d "$CHAT_DIR/backup" ]; then cp -n "$CHAT_DIR/backup"/*.jsonl "$CHAT_DIR/" 2>/dev/null || true; fi;`

			if flags.ListSessions {
				fmt.Printf("Listing Gemini sessions in sandbox '%s'...\n", sandboxName)
				execCmd := fmt.Sprintf("%s GEMINI_API_KEY='%s' gemini --list-sessions", detectRepoScript, geminiKey)
				cmd := exec.CommandContext(ctx, "kubectl", "exec", "-it", "-n", rootFlags.Namespace, podName, "-c", "sandbox", "--", "sh", "-c", execCmd)
				cmd.Stdin = os.Stdin
				cmd.Stdout = os.Stdout
				cmd.Stderr = os.Stderr
				if err := cmd.Run(); err != nil {
					return fmt.Errorf("listing sessions failed: %w", err)
				}
				return nil
			}

			fmt.Printf("Connecting to Gemini chat session (resume: %s) in sandbox '%s'...\n", flags.Resume, sandboxName)
			execCmd := fmt.Sprintf("%s %s GEMINI_API_KEY='%s' gemini --resume %s; %s", detectRepoScript, backupScript, geminiKey, flags.Resume, restoreScript)
			cmd := exec.CommandContext(ctx, "kubectl", "exec", "-it", "-n", rootFlags.Namespace, podName, "-c", "sandbox", "--", "sh", "-c", execCmd)
			cmd.Stdin = os.Stdin
			cmd.Stdout = os.Stdout
			cmd.Stderr = os.Stderr
			if err := cmd.Run(); err != nil {
				return fmt.Errorf("gemini session exited: %w", err)
			}
			return nil
		},
	}

	cmd.Flags().BoolVarP(&flags.ListSessions, "list-sessions", "l", false, "List available Gemini sessions in the sandbox and exit")
	cmd.Flags().StringVarP(&flags.Resume, "resume", "r", "latest", "Resume a previous session by index or 'latest'")

	return cmd
}
