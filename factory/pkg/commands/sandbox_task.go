package commands

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/clients"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/k8s"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/recipe"
	factorysandbox "github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/sandbox"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/spool"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/taskapi"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/taskoutput"
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
// annotation is that URL, or for an issue, the one labelled with it. When
// several are it asks for the name rather than connect to — and so wake —
// all of them.
func connectTaskSandbox(ctx context.Context, c *cobra.Command, arg string) (taskapi.Sandbox, error) {
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
	sb, err := taskapi.Connect(ctx, rootFlags.Namespace, name)
	if err != nil {
		return nil, fmt.Errorf("connecting to sandbox %s: %w", name, err)
	}
	return sb, nil
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
	it, err := parseGitHubItemURL(itemURL)
	issue := err == nil && !it.IsPR
	var names []string
	for i := range list.Items {
		item := &list.Items[i]
		if normalizeItemURL(item.GetAnnotations()["htmlURL"]) == want ||
			(issue && factorysandbox.IsIssueSandbox(item, it.Owner, it.Repo, it.Number)) {
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
	id, runName string
}

func (f *taskSelectFlags) add(cmd *cobra.Command) {
	cmd.Flags().StringVar(&f.id, "task", "", "Task id (default: the newest task, or the newest with --run-name)")
	cmd.Flags().StringVar(&f.runName, "run-name", "", "The --run-name the task was run with")
}

func (f *taskSelectFlags) find(ctx context.Context, sb taskapi.Sandbox) (spool.Entry, error) {
	entries, err := sb.List(ctx)
	if err != nil {
		return spool.Entry{}, err
	}
	e, err := spool.Find(entries, f.id, f.runName)
	if err == nil {
		settleTaskState(ctx, sb.Name(), entries, e)
	}
	return e, err
}

// settleTaskState records on the sandbox how a task found exited ended,
// when the sandbox still says it is running: nothing else does for a task
// run --detached, whose CLI returned when it started. A side task left
// "running" in an issue's sandbox would keep it from being suspended, or
// paused by repo-agent's board, for good.
func settleTaskState(ctx context.Context, sandboxName string, entries []spool.Entry, e spool.Entry) {
	if e.State != spool.Exited || e.Kind == "" {
		return
	}
	for _, other := range entries {
		if other.Kind == e.Kind && other.State != spool.Exited {
			return // the running one of its kind holds the claim
		}
	}
	kubeClient, err := clients.NewKubernetesClient()
	if err != nil {
		return
	}
	sb, err := k8s.NewManager(kubeClient).GetSandbox(ctx, rootFlags.Namespace, sandboxName)
	if err != nil {
		return
	}
	state := "Completed"
	if e.ExitCode != "0" {
		state = "Failed"
	}
	a := sb.GetAnnotations()
	switch {
	case a[factorysandbox.SideTaskStateAnnotation(e.Kind)] == "Running":
		_ = factorysandbox.UpdateSandboxSideTaskAnnotation(ctx, kubeClient, rootFlags.Namespace, sandboxName, e.Kind, state)
	case a["sandbox.gemini.google.com/last-task-type"] == e.Kind && a["sandbox.gemini.google.com/last-task-state"] == "Running":
		_ = factorysandbox.UpdateSandboxTaskAnnotation(ctx, kubeClient, rootFlags.Namespace, sandboxName, e.Kind, state)
	}
}

func newTaskListCommand(ctx context.Context) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "list <sandbox-name | issue/PR URL>",
		Short: "List the tasks in a sandbox, newest first",
		Example: `  factory sandbox task list https://github.com/owner/repo/issues/123
  factory sandbox task list fix-repo-123`,
		Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			sb, err := connectTaskSandbox(ctx, c, args[0])
			if err != nil {
				return err
			}
			defer sb.Close()
			entries, err := sb.List(ctx)
			if err != nil {
				return err
			}
			w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
			fmt.Fprintln(w, "TASK\tKIND\tRUN NAME\tSTATE\tSTARTED")
			for _, e := range entries {
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", e.ID, e.Kind, orDash(e.RunName), taskState(e), taskStarted(e))
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
		Example: `  factory sandbox task status recipe-repo-123 --run-name my-run-7
  factory sandbox task status https://github.com/owner/repo/issues/123 -o json`,
		Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			if output != "" && output != "json" {
				return fmt.Errorf("--output must be json or empty, not %q", output)
			}
			sb, err := connectTaskSandbox(ctx, c, args[0])
			if err != nil {
				return err
			}
			defer sb.Close()
			e, err := sel.find(ctx, sb)
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
			fmt.Fprintf(w, "Run name:\t%s\n", orDash(e.RunName))
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
		Short: "Print the result of a finished task",
		Long: `Print the result of a finished task.

With no file, the task's result is printed as a task output document
(task-output.yaml), for ` + "`factory apply`" + ` to act on; a task with none
gets its recipe's declared outputs, each under a banner with its name.
With a file, that file of the task directory is printed as it is, for a
program to read.`,
		Example: `  factory sandbox task output recipe-repo-123 --run-name my-run-7 | factory apply -f - --dry-run
  factory sandbox task output recipe-repo-123 --run-name my-run-7 triage-output.yaml`,
		Args: cobra.RangeArgs(1, 2),
		RunE: func(c *cobra.Command, args []string) error {
			if len(args) == 2 && !outputFileName.MatchString(args[1]) {
				return fmt.Errorf("%q is not a file name in the task directory", args[1])
			}
			sb, err := connectTaskSandbox(ctx, c, args[0])
			if err != nil {
				return err
			}
			defer sb.Close()
			e, err := sel.find(ctx, sb)
			if err != nil {
				return err
			}
			if e.State != spool.Exited {
				return fmt.Errorf("task %s is %s; its outputs are not final (wait for it with: factory sandbox task attach %s --task %s)", e.ID, e.State, args[0], e.ID)
			}
			if len(args) == 2 {
				return printTaskFile(ctx, sb, e.ID, args[1])
			}
			if doc, err := readTaskOutput(ctx, sb, e); err != nil || doc != nil {
				if err != nil {
					return err
				}
				_, err = os.Stdout.Write(doc)
				return err
			}
			rec, err := taskRecipe(ctx, sb, e.ID)
			if err != nil {
				return err
			}
			if rec == nil {
				files, _ := sb.Files(ctx, e.ID)
				return fmt.Errorf("task %s is not a recipe task; name the file to print, one of: %s", e.ID, strings.Join(files, ", "))
			}
			return printRecipeOutputs(ctx, sb, rec, e.ID)
		},
	}
	sel.add(cmd)
	return cmd
}

// readTaskOutput is a finished task's task output document: the one it
// wrote, or else one made here from the result it declared — sandboxes
// whose runner predates task outputs leave none. Nil when the task has no
// result of a known kind.
func readTaskOutput(ctx context.Context, sb taskapi.Sandbox, e spool.Entry) ([]byte, error) {
	sandboxName := sb.Name()
	out, err := sb.ReadFile(ctx, e.ID, taskoutput.File)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("reading task %s's %s: %w", e.ID, taskoutput.File, err)
	}
	decl := e.Output
	if len(out) > 0 {
		return withDeclaredActions(out, decl)
	}
	if decl == nil {
		return nil, nil
	}
	raw, err := sb.ReadFile(ctx, e.ID, decl.From)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("reading task %s's %s: %w", e.ID, decl.From, err)
	}
	if len(raw) == 0 {
		return nil, fmt.Errorf("task %s left no %s, its %s result", e.ID, decl.From, decl.Kind)
	}
	target := e.URL
	if target == "" {
		target = sandboxHTMLURL(ctx, sandboxName)
	}
	doc, err := taskoutput.Wrap(decl.Kind, string(raw), taskoutput.Target{URL: target}, taskoutput.Source{Sandbox: sandboxName, Task: e.ID, Recipe: e.Recipe})
	if err != nil {
		return nil, fmt.Errorf("task %s: %w", e.ID, err)
	}
	doc.Actions = decl.Actions
	return taskoutput.Marshal(doc)
}

// withDeclaredActions is a task output with the actions its task declared,
// when its runner, older than actions, left them out. Anything else is
// returned as it is.
func withDeclaredActions(data []byte, decl *taskoutput.Decl) ([]byte, error) {
	if decl == nil || len(decl.Actions) == 0 {
		return data, nil
	}
	docs, err := taskoutput.Parse(data)
	if err != nil || len(docs) != 1 || len(docs[0].Actions) > 0 || docs[0].Kind != decl.Kind {
		return data, nil
	}
	docs[0].Actions = decl.Actions
	return taskoutput.Marshal(docs[0])
}

// sandboxHTMLURL is the issue or PR a sandbox works on, or "".
func sandboxHTMLURL(ctx context.Context, name string) string {
	kubeClient, err := clients.NewKubernetesClient()
	if err != nil {
		return ""
	}
	sb, err := k8s.NewManager(kubeClient).GetSandbox(ctx, rootFlags.Namespace, name)
	if err != nil {
		return ""
	}
	return sb.GetAnnotations()["htmlURL"]
}

func printTaskFile(ctx context.Context, sb taskapi.Sandbox, id, name string) error {
	out, err := sb.ReadFile(ctx, id, name)
	if errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("the task left no %s", name)
	}
	if err != nil {
		return fmt.Errorf("reading %s: %w", name, err)
	}
	_, err = os.Stdout.Write(out)
	return err
}

// taskRecipe is the recipe a task ran, or nil for a task that is not one.
func taskRecipe(ctx context.Context, sb taskapi.Sandbox, id string) (*recipe.Recipe, error) {
	recipeYAML, err := sb.ReadFile(ctx, id, spool.RecipeFile)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("reading the task's recipe: %w", err)
	}
	if len(recipeYAML) == 0 {
		return nil, nil
	}
	rec, err := recipe.Parse(recipeYAML)
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
  factory sandbox task attach recipe-repo-123 --run-name my-run-7`,
		Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			sb, err := connectTaskSandbox(ctx, c, args[0])
			if err != nil {
				return err
			}
			defer sb.Close()
			e, err := sel.find(ctx, sb)
			if err != nil {
				return err
			}
			if err := awaitTaskStart(ctx, sb, e); err != nil {
				return err
			}
			fmt.Printf("Attaching to task %s (%s)...\n", e.ID, spool.TaskDir(e.ID))
			if err := sb.Attach(ctx, e.ID, nil, false); err != nil {
				return fmt.Errorf("task %s: %w", e.ID, err)
			}
			_, _ = (&taskSelectFlags{id: e.ID}).find(ctx, sb)
			rec, err := taskRecipe(ctx, sb, e.ID)
			if err != nil || rec == nil {
				return err
			}
			return printRecipeOutputs(ctx, sb, rec, e.ID)
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
			sb, err := connectTaskSandbox(ctx, c, args[0])
			if err != nil {
				return err
			}
			defer sb.Close()
			e, err := sel.find(ctx, sb)
			if err != nil {
				return err
			}
			if !follow {
				return sb.Log(ctx, e.ID, os.Stdout)
			}
			if err := awaitTaskStart(ctx, sb, e); err != nil {
				return err
			}
			if err := sb.Attach(ctx, e.ID, nil, false); err != nil {
				return fmt.Errorf("task %s: %w", e.ID, err)
			}
			_, _ = (&taskSelectFlags{id: e.ID}).find(ctx, sb)
			return nil
		},
	}
	sel.add(cmd)
	cmd.Flags().BoolVarP(&follow, "follow", "f", false, "Follow the log until the task exits")
	return cmd
}

// awaitTaskStart waits for a spooled task the daemon has not started yet,
// so that following it finds its pid. It never withdraws the task.
func awaitTaskStart(ctx context.Context, sb taskapi.Sandbox, e spool.Entry) error {
	if e.State != spool.Pending && e.State != spool.Claimed {
		return nil
	}
	fmt.Printf("Task %s is %s; waiting for the sandbox to start it...\n", e.ID, e.State)
	return sb.AwaitStart(ctx, e.ID, 365*24*time.Hour, 2*time.Minute)
}
