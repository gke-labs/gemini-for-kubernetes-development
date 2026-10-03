package commands

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/clients"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/envd"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/k8s"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/recipe"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/spool"
)

// NewTaskCommand groups the commands that look at the tasks in a sandbox,
// whether the sandbox's daemon took them from the spool or envd started
// them: they all run in /workspaces/tasks/<id> with the same files.
func NewTaskCommand(ctx context.Context) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "task",
		Short: "List, follow and read the tasks in a sandbox",
	}
	cmd.AddCommand(newTaskListCommand(ctx))
	cmd.AddCommand(newTaskAttachCommand(ctx))
	cmd.AddCommand(newTaskLogsCommand(ctx))
	return cmd
}

// taskSandboxFlags picks the sandbox a task command talks to: named, or
// the one working on an issue or PR.
type taskSandboxFlags struct {
	sandbox, url string
}

func (f *taskSandboxFlags) add(cmd *cobra.Command) {
	cmd.Flags().StringVar(&f.sandbox, "sandbox", "", "Sandbox name")
	cmd.Flags().StringVar(&f.url, "url", "", "The issue or PR URL the sandbox works on")
}

// name resolves the sandbox. By URL it is the sandbox whose htmlURL
// annotation is that URL; when several are (an issue's fix and triage
// sandboxes), it asks for --sandbox rather than connect to — and so wake —
// all of them.
func (f *taskSandboxFlags) name(ctx context.Context) (string, error) {
	if f.sandbox != "" {
		return f.sandbox, nil
	}
	if f.url == "" {
		return "", fmt.Errorf("--sandbox or --url is required")
	}
	kubeClient, err := clients.NewKubernetesClient()
	if err != nil {
		return "", fmt.Errorf("creating k8s client: %w", err)
	}
	list, err := k8s.NewManager(kubeClient).ListSandboxes(ctx, rootFlags.Namespace)
	if err != nil {
		return "", fmt.Errorf("listing sandboxes: %w", err)
	}
	want := normalizeItemURL(f.url)
	var names []string
	for _, item := range list.Items {
		if normalizeItemURL(item.GetAnnotations()["htmlURL"]) == want {
			names = append(names, item.GetName())
		}
	}
	sort.Strings(names)
	switch len(names) {
	case 0:
		return "", fmt.Errorf("no sandbox in namespace %s works on %s", rootFlags.Namespace, f.url)
	case 1:
		return names[0], nil
	}
	return "", fmt.Errorf("several sandboxes work on %s; pick one with --sandbox: %s", f.url, strings.Join(names, ", "))
}

func normalizeItemURL(u string) string {
	u, _, _ = strings.Cut(u, "#")
	return strings.ToLower(strings.TrimSuffix(strings.TrimSpace(u), "/"))
}

// connect resolves the root flags and the sandbox and connects to it.
func (f *taskSandboxFlags) connect(ctx context.Context, c *cobra.Command) (*envd.Client, error) {
	if _, err := ResolveRootFlags(c); err != nil {
		return nil, err
	}
	name, err := f.name(ctx)
	if err != nil {
		return nil, err
	}
	client, err := envd.Connect(ctx, rootFlags.Namespace, name)
	if err != nil {
		return nil, fmt.Errorf("connecting to sandbox %s: %w", name, err)
	}
	return client, nil
}

// taskSelectFlags picks a task in the sandbox.
type taskSelectFlags struct {
	id, clientID string
}

func (f *taskSelectFlags) add(cmd *cobra.Command) {
	cmd.Flags().StringVar(&f.id, "task", "", "Task id (default: the newest task, or the newest with --client-id)")
	cmd.Flags().StringVar(&f.clientID, "client-id", "", "The --client-id the task was run with")
}

func (f *taskSelectFlags) find(ctx context.Context, client *envd.Client) (spool.Entry, error) {
	entries, err := spool.List(ctx, client)
	if err != nil {
		return spool.Entry{}, err
	}
	return spool.Find(entries, f.id, f.clientID)
}

func newTaskListCommand(ctx context.Context) *cobra.Command {
	var sb taskSandboxFlags
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List the tasks in a sandbox, newest first",
		Example: `  factory task list --url https://github.com/owner/repo/issues/123
  factory task list --sandbox fix-repo-123`,
		RunE: func(c *cobra.Command, _ []string) error {
			client, err := sb.connect(ctx, c)
			if err != nil {
				return err
			}
			defer client.Close()
			entries, err := spool.List(ctx, client)
			if err != nil {
				return err
			}
			w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
			fmt.Fprintln(w, "TASK\tKIND\tCLIENT ID\tSTATE\tSTARTED")
			for _, e := range entries {
				state := string(e.State)
				if e.State == spool.Exited {
					state = "exited " + e.ExitCode
				}
				started := "-"
				if !e.Started.IsZero() {
					started = e.Started.Local().Format(time.DateTime)
				}
				clientID := e.ClientID
				if clientID == "" {
					clientID = "-"
				}
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", e.ID, e.Kind, clientID, state, started)
			}
			return w.Flush()
		},
	}
	sb.add(cmd)
	return cmd
}

// newTaskAttachCommand reconnects to a task: it follows the log to the end
// and returns the task's result; for a recipe it prints the outputs, as
// `recipe run` would have. Interrupting it leaves the task running.
func newTaskAttachCommand(ctx context.Context) *cobra.Command {
	var sb taskSandboxFlags
	var sel taskSelectFlags
	cmd := &cobra.Command{
		Use:   "attach",
		Short: "Follow a task in a sandbox to its end",
		Example: `  factory task attach --url https://github.com/owner/repo/issues/123
  factory task attach --sandbox recipe-repo-123 --client-id my-run-7`,
		RunE: func(c *cobra.Command, _ []string) error {
			client, err := sb.connect(ctx, c)
			if err != nil {
				return err
			}
			defer client.Close()
			e, err := sel.find(ctx, client)
			if err != nil {
				return err
			}
			if err := awaitTaskStart(ctx, client, e); err != nil {
				return err
			}
			taskDir := spool.TaskDir(e.ID)
			fmt.Printf("Attaching to task %s (%s)...\n", e.ID, taskDir)
			if err := client.AttachTask(ctx, taskDir, nil, false); err != nil {
				return fmt.Errorf("task %s: %w", e.ID, err)
			}
			var recipeYAML bytes.Buffer
			_ = client.Exec(ctx, "cat "+taskDir+"/"+spool.RecipeFile+" 2>/dev/null", "/workspaces", nil, nil, &recipeYAML, nil)
			if recipeYAML.Len() == 0 {
				return nil
			}
			rec, err := recipe.Parse(recipeYAML.Bytes())
			if err != nil {
				return fmt.Errorf("reading the task's recipe: %w", err)
			}
			return printRecipeOutputs(ctx, client, rec, taskDir)
		},
	}
	sb.add(cmd)
	sel.add(cmd)
	return cmd
}

func newTaskLogsCommand(ctx context.Context) *cobra.Command {
	var sb taskSandboxFlags
	var sel taskSelectFlags
	var follow bool
	cmd := &cobra.Command{
		Use:   "logs",
		Short: "Print a task's log",
		Example: `  factory task logs --url https://github.com/owner/repo/issues/123
  factory task logs --sandbox fix-repo-123 --task fix-20261002-150405 -f`,
		RunE: func(c *cobra.Command, _ []string) error {
			client, err := sb.connect(ctx, c)
			if err != nil {
				return err
			}
			defer client.Close()
			e, err := sel.find(ctx, client)
			if err != nil {
				return err
			}
			taskDir := spool.TaskDir(e.ID)
			if !follow {
				return client.Exec(ctx, "cat "+envd.NewTaskFiles(taskDir).LogFile+" 2>/dev/null", "/workspaces", nil, nil, os.Stdout, os.Stderr)
			}
			if err := awaitTaskStart(ctx, client, e); err != nil {
				return err
			}
			if err := client.AttachTask(ctx, taskDir, nil, false); err != nil {
				return fmt.Errorf("task %s: %w", e.ID, err)
			}
			return nil
		},
	}
	sb.add(cmd)
	sel.add(cmd)
	cmd.Flags().BoolVarP(&follow, "follow", "f", false, "Follow the log until the task exits")
	return cmd
}

// awaitTaskStart waits for a spooled task the daemon has not started yet,
// so that following it finds its pid. It never withdraws the task.
func awaitTaskStart(ctx context.Context, client *envd.Client, e spool.Entry) error {
	if e.State != spool.Pending && e.State != spool.Claimed {
		return nil
	}
	fmt.Printf("Task %s is %s; waiting for the sandbox to start it...\n", e.ID, e.State)
	return spool.AwaitStart(ctx, client, e.ID, 365*24*time.Hour, 2*time.Minute)
}
