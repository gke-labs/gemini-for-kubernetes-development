package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
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

// newSandboxTaskCommand groups the commands that look at the tasks in a sandbox,
// whether the sandbox's daemon took them from the spool or envd started
// them: they all run in /workspaces/tasks/<id> with the same files.
func newSandboxTaskCommand(ctx context.Context) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "task",
		Short: "List, follow and read the tasks in a sandbox",
	}
	cmd.AddCommand(newTaskListCommand(ctx))
	cmd.AddCommand(newTaskStatusCommand(ctx))
	cmd.AddCommand(newTaskOutputCommand(ctx))
	cmd.AddCommand(newTaskAttachCommand(ctx))
	cmd.AddCommand(newTaskLogsCommand(ctx))
	return cmd
}

// connectTaskSandbox connects to the sandbox a task command names: by
// name, or by the issue or PR URL it works on — the sandbox whose htmlURL
// annotation is that URL. When several are (an issue's fix and triage
// sandboxes) it asks for the name rather than connect to — and so wake —
// all of them.
func connectTaskSandbox(ctx context.Context, c *cobra.Command, arg string) (*envd.Client, error) {
	if _, err := ResolveRootFlags(c); err != nil {
		return nil, err
	}
	name := arg
	if strings.HasPrefix(arg, "https://") || strings.HasPrefix(arg, "http://") {
		var err error
		if name, err = sandboxForURL(ctx, arg); err != nil {
			return nil, err
		}
	} else if err := validateSandboxName(name); err != nil {
		return nil, err
	}
	client, err := envd.Connect(ctx, rootFlags.Namespace, name)
	if err != nil {
		return nil, fmt.Errorf("connecting to sandbox %s: %w", name, err)
	}
	return client, nil
}

func sandboxForURL(ctx context.Context, itemURL string) (string, error) {
	kubeClient, err := clients.NewKubernetesClient()
	if err != nil {
		return "", fmt.Errorf("creating k8s client: %w", err)
	}
	list, err := k8s.NewManager(kubeClient).ListSandboxes(ctx, rootFlags.Namespace)
	if err != nil {
		return "", fmt.Errorf("listing sandboxes: %w", err)
	}
	want := normalizeItemURL(itemURL)
	var names []string
	for _, item := range list.Items {
		if normalizeItemURL(item.GetAnnotations()["htmlURL"]) == want {
			names = append(names, item.GetName())
		}
	}
	sort.Strings(names)
	switch len(names) {
	case 0:
		return "", fmt.Errorf("no sandbox in namespace %s works on %s", rootFlags.Namespace, itemURL)
	case 1:
		return names[0], nil
	}
	return "", fmt.Errorf("several sandboxes work on %s; name one instead: %s", itemURL, strings.Join(names, ", "))
}

func normalizeItemURL(u string) string {
	u, _, _ = strings.Cut(u, "#")
	return strings.ToLower(strings.TrimSuffix(strings.TrimSpace(u), "/"))
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
	cmd := &cobra.Command{
		Use:   "list <sandbox-name | issue/PR URL>",
		Short: "List the tasks in a sandbox, newest first",
		Example: `  factory sandbox task list https://github.com/owner/repo/issues/123
  factory sandbox task list fix-repo-123`,
		Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			client, err := connectTaskSandbox(ctx, c, args[0])
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
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", e.ID, e.Kind, orDash(e.ClientID), taskState(e), taskStarted(e))
			}
			return w.Flush()
		},
	}
	return cmd
}

func taskState(e spool.Entry) string {
	if e.State == spool.Exited {
		return "exited " + e.ExitCode
	}
	return string(e.State)
}

func taskStarted(e spool.Entry) string {
	if e.Started.IsZero() {
		return "-"
	}
	return e.Started.Local().Format(time.DateTime)
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// newTaskStatusCommand reports where a task is without following it, so a
// caller that started a task detached can come back for it: pending,
// claimed, running, or exited with its exit code.
func newTaskStatusCommand(ctx context.Context) *cobra.Command {
	var sel taskSelectFlags
	var output string
	cmd := &cobra.Command{
		Use:   "status <sandbox-name | issue/PR URL>",
		Short: "Show whether a task is still running, and how it exited",
		Example: `  factory sandbox task status recipe-repo-123 --client-id my-run-7
  factory sandbox task status https://github.com/owner/repo/issues/123 -o json`,
		Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			if output != "" && output != "json" {
				return fmt.Errorf("--output must be json or empty, not %q", output)
			}
			client, err := connectTaskSandbox(ctx, c, args[0])
			if err != nil {
				return err
			}
			defer client.Close()
			e, err := sel.find(ctx, client)
			if err != nil {
				return err
			}
			if output == "json" {
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(e)
			}
			w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
			fmt.Fprintf(w, "Task:\t%s\n", e.ID)
			fmt.Fprintf(w, "Kind:\t%s\n", e.Kind)
			fmt.Fprintf(w, "Client ID:\t%s\n", orDash(e.ClientID))
			fmt.Fprintf(w, "State:\t%s\n", taskState(e))
			fmt.Fprintf(w, "Started:\t%s\n", taskStarted(e))
			return w.Flush()
		},
	}
	sel.add(cmd)
	cmd.Flags().StringVarP(&output, "output", "o", "", "Output format: json")
	return cmd
}

// outputFileName is a file directly in a task directory.
var outputFileName = regexp.MustCompile(`^[A-Za-z0-9_-][A-Za-z0-9._-]*$`)

// newTaskOutputCommand reads what a finished task left: a recipe's
// declared outputs, or a file named. It does not wait; a task still going
// is an error, so a caller never takes a half-written file for the result.
func newTaskOutputCommand(ctx context.Context) *cobra.Command {
	var sel taskSelectFlags
	cmd := &cobra.Command{
		Use:   "output <sandbox-name | issue/PR URL> [file]",
		Short: "Print the outputs of a finished task",
		Long: `Print the outputs of a finished task.

With no file, a recipe task's declared outputs are printed, each under a
banner with its name. With a file, that file of the task directory is
printed as it is, for a program to read.`,
		Example: `  factory sandbox task output recipe-repo-123 --client-id my-run-7
  factory sandbox task output recipe-repo-123 --client-id my-run-7 triage-output.yaml
  factory sandbox task output triage-repo-123 triage-output.txt`,
		Args: cobra.RangeArgs(1, 2),
		RunE: func(c *cobra.Command, args []string) error {
			if len(args) == 2 && !outputFileName.MatchString(args[1]) {
				return fmt.Errorf("%q is not a file name in the task directory", args[1])
			}
			client, err := connectTaskSandbox(ctx, c, args[0])
			if err != nil {
				return err
			}
			defer client.Close()
			e, err := sel.find(ctx, client)
			if err != nil {
				return err
			}
			if e.State != spool.Exited {
				return fmt.Errorf("task %s is %s; its outputs are not final (wait for it with: factory sandbox task attach %s --task %s)", e.ID, e.State, args[0], e.ID)
			}
			taskDir := spool.TaskDir(e.ID)
			if len(args) == 2 {
				return printTaskFile(ctx, client, taskDir, args[1])
			}
			rec, err := taskRecipe(ctx, client, taskDir)
			if err != nil {
				return err
			}
			if rec == nil {
				var ls bytes.Buffer
				_ = client.Exec(ctx, "ls "+taskDir, "/workspaces", nil, nil, &ls, nil)
				return fmt.Errorf("task %s is not a recipe task; name the file to print, one of: %s", e.ID, strings.Join(strings.Fields(ls.String()), ", "))
			}
			return printRecipeOutputs(ctx, client, rec, taskDir)
		},
	}
	sel.add(cmd)
	return cmd
}

func printTaskFile(ctx context.Context, client *envd.Client, taskDir, name string) error {
	path := taskDir + "/" + name
	var out, errOut bytes.Buffer
	if err := client.Exec(ctx, fmt.Sprintf("if [ -f %[1]s ]; then cat %[1]s; else echo missing >&2; fi", path), "/workspaces", nil, nil, &out, &errOut); err != nil {
		return fmt.Errorf("reading %s: %w", path, err)
	}
	if strings.TrimSpace(errOut.String()) == "missing" {
		return fmt.Errorf("the task left no %s", name)
	}
	_, err := os.Stdout.Write(out.Bytes())
	return err
}

// taskRecipe is the recipe a task ran, or nil for a task that is not one.
func taskRecipe(ctx context.Context, client *envd.Client, taskDir string) (*recipe.Recipe, error) {
	var recipeYAML bytes.Buffer
	_ = client.Exec(ctx, "cat "+taskDir+"/"+spool.RecipeFile+" 2>/dev/null", "/workspaces", nil, nil, &recipeYAML, nil)
	if recipeYAML.Len() == 0 {
		return nil, nil
	}
	rec, err := recipe.Parse(recipeYAML.Bytes())
	if err != nil {
		return nil, fmt.Errorf("reading the task's recipe: %w", err)
	}
	return rec, nil
}

// newTaskAttachCommand reconnects to a task: it follows the log to the end
// and returns the task's result; for a recipe it prints the outputs, as
// `recipe run` would have. Interrupting it leaves the task running.
func newTaskAttachCommand(ctx context.Context) *cobra.Command {
	var sel taskSelectFlags
	cmd := &cobra.Command{
		Use:   "attach <sandbox-name | issue/PR URL>",
		Short: "Follow a task in a sandbox to its end",
		Example: `  factory sandbox task attach https://github.com/owner/repo/issues/123
  factory sandbox task attach recipe-repo-123 --client-id my-run-7`,
		Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			client, err := connectTaskSandbox(ctx, c, args[0])
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
			rec, err := taskRecipe(ctx, client, taskDir)
			if err != nil || rec == nil {
				return err
			}
			return printRecipeOutputs(ctx, client, rec, taskDir)
		},
	}
	sel.add(cmd)
	return cmd
}

func newTaskLogsCommand(ctx context.Context) *cobra.Command {
	var sel taskSelectFlags
	var follow bool
	cmd := &cobra.Command{
		Use:   "logs <sandbox-name | issue/PR URL>",
		Short: "Print a task's log",
		Example: `  factory sandbox task logs https://github.com/owner/repo/issues/123
  factory sandbox task logs fix-repo-123 --task fix-20261002-150405 -f`,
		Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			client, err := connectTaskSandbox(ctx, c, args[0])
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
