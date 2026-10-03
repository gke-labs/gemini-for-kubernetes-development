package spool

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/envd"
)

// Remote is the part of an envd client the spool's client side uses.
type Remote interface {
	WriteFile(ctx context.Context, destPath string, data []byte) error
	Exec(ctx context.Context, cmdStr, cwd string, envs map[string]string, stdin io.Reader, stdout, stderr io.Writer) error
}

// TaskDir is where a claimed task runs.
func TaskDir(id string) string {
	return envd.DefaultTasksDir + "/" + id
}

// Submit leaves a task in the sandbox's incoming directory. It is written
// under a dot-name and renamed into place, so the daemon never sees half
// of it.
func Submit(ctx context.Context, r Remote, task Task, recipeYAML []byte, inputs, env map[string]string) error {
	taskJSON, err := json.Marshal(task)
	if err != nil {
		return err
	}
	inputsJSON, err := json.Marshal(inputs)
	if err != nil {
		return err
	}
	envJSON, err := json.Marshal(env)
	if err != nil {
		return err
	}
	staging := IncomingDir + "/." + task.ID
	if err := run(ctx, r, fmt.Sprintf("umask 077 && mkdir -p %s", staging)); err != nil {
		return fmt.Errorf("creating the task in the spool: %w", err)
	}
	for _, f := range []struct {
		name string
		data []byte
	}{{RecipeFile, recipeYAML}, {InputsFile, inputsJSON}, {EnvFile, envJSON}, {TaskFile, taskJSON}} {
		if err := r.WriteFile(ctx, staging+"/"+f.name, f.data); err != nil {
			return fmt.Errorf("writing %s: %w", f.name, err)
		}
	}
	if err := run(ctx, r, fmt.Sprintf("chmod 600 %s/%s && mv %s %s/%s", staging, EnvFile, staging, IncomingDir, task.ID)); err != nil {
		return fmt.Errorf("spooling the task: %w", err)
	}
	return nil
}

// State is where a task is in its life.
type State string

const (
	Pending State = "pending" // in incoming, not claimed
	Claimed State = "claimed" // claimed, not started yet
	Running State = "running" // started, no exit code yet
	Exited  State = "exited"  // has an exit code
	Missing State = "missing" // nowhere
)

// Status reports where the task id is.
func Status(ctx context.Context, r Remote, id string) (State, error) {
	tf := envd.NewTaskFiles(TaskDir(id))
	script := fmt.Sprintf(`if [ -s %s ]; then echo exited
elif [ -s %s ]; then echo running
elif [ -d %s ]; then echo claimed
elif [ -d %s/%s ]; then echo pending
else echo missing; fi`, tf.ExitCodeFile, tf.PIDFile, tf.TaskDir, IncomingDir, id)
	var out bytes.Buffer
	if err := r.Exec(ctx, script, "/workspaces", nil, nil, &out, nil); err != nil {
		return "", err
	}
	return State(strings.TrimSpace(out.String())), nil
}

// ErrNotClaimed means no daemon took the task in time, and it was taken
// back: the sandbox's image predates the spool. Nothing of it runs.
var ErrNotClaimed = fmt.Errorf("no spool daemon claimed the task")

// AwaitStart waits until the daemon has started the task, so that
// attaching finds its pid. A task still unclaimed after claimTimeout is
// withdrawn and ErrNotClaimed returned; withdrawing is a rename that
// fails once the daemon has claimed it, so the task never runs twice.
func AwaitStart(ctx context.Context, r Remote, id string, claimTimeout, startTimeout time.Duration) error {
	poll := time.Second
	claimDeadline := time.Now().Add(claimTimeout)
	startDeadline := time.Now().Add(startTimeout)
	for {
		state, err := Status(ctx, r, id)
		if err != nil {
			return err
		}
		switch state {
		case Running, Exited:
			return nil
		case Missing:
			return fmt.Errorf("task %s is neither in the spool nor in %s", id, envd.DefaultTasksDir)
		case Pending:
			if time.Now().After(claimDeadline) {
				withdrawn, err := withdraw(ctx, r, id)
				if err != nil {
					return err
				}
				if withdrawn {
					return ErrNotClaimed
				}
				// Claimed between the check and the rename.
			}
		case Claimed:
			if time.Now().After(startDeadline) {
				return fmt.Errorf("task %s was claimed but has not started after %s", id, startTimeout)
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(poll):
		}
	}
}

func withdraw(ctx context.Context, r Remote, id string) (bool, error) {
	var out bytes.Buffer
	script := fmt.Sprintf(`mkdir -p %[1]s && if mv %[2]s/%[3]s %[1]s/%[3]s 2>/dev/null; then rm -rf %[1]s/%[3]s; echo withdrawn; fi`, WithdrawnDir, IncomingDir, id)
	if err := r.Exec(ctx, script, "/workspaces", nil, nil, &out, nil); err != nil {
		return false, fmt.Errorf("withdrawing unclaimed task %s: %w", id, err)
	}
	return strings.TrimSpace(out.String()) == "withdrawn", nil
}

// Entry is a spooled task as List finds it.
type Entry struct {
	Task
	State    State
	ExitCode string
}

// List finds the spooled tasks in the sandbox, pending and claimed,
// newest first.
func List(ctx context.Context, r Remote) ([]Entry, error) {
	script := fmt.Sprintf(`for d in %s/*/ %s/*/; do
  [ -f "${d}%s" ] || continue
  case "$d" in %s/*) s=pending ;; *) if [ -s "${d}exit_code" ]; then s="exited:$(cat "${d}exit_code")"; elif [ -s "${d}pid" ]; then s=running; else s=claimed; fi ;; esac
  printf '%%s\t%%s\n' "$s" "$(tr -d '\n' < "${d}%s")"
done`, IncomingDir, envd.DefaultTasksDir, TaskFile, IncomingDir, TaskFile)
	var out bytes.Buffer
	if err := r.Exec(ctx, script, "/workspaces", nil, nil, &out, nil); err != nil {
		return nil, err
	}
	return parseList(out.String()), nil
}

func parseList(out string) []Entry {
	var entries []Entry
	for _, line := range strings.Split(out, "\n") {
		state, taskJSON, ok := strings.Cut(line, "\t")
		if !ok {
			continue
		}
		var e Entry
		if err := json.Unmarshal([]byte(taskJSON), &e.Task); err != nil || e.ID == "" {
			continue
		}
		e.State = State(state)
		if code, ok := strings.CutPrefix(state, "exited:"); ok {
			e.State, e.ExitCode = Exited, strings.TrimSpace(code)
		}
		entries = append(entries, e)
	}
	sort.SliceStable(entries, func(i, j int) bool { return entries[i].SubmittedAt.After(entries[j].SubmittedAt) })
	return entries
}

// Find picks the task to attach to: by id, else the newest with
// clientID, else the newest of all.
func Find(entries []Entry, id, clientID string) (Entry, error) {
	for _, e := range entries {
		if (id != "" && e.ID == id) || (id == "" && clientID != "" && e.ClientID == clientID) || (id == "" && clientID == "") {
			return e, nil
		}
	}
	switch {
	case id != "":
		return Entry{}, fmt.Errorf("no spooled task %s", id)
	case clientID != "":
		return Entry{}, fmt.Errorf("no spooled task with client id %s", clientID)
	}
	return Entry{}, fmt.Errorf("no spooled tasks")
}

// run runs script and fails unless it succeeded. envd's Exec does not
// report a command's exit status, so success is a line the script prints
// last.
func run(ctx context.Context, r Remote, script string) error {
	var out, errOut bytes.Buffer
	if err := r.Exec(ctx, "("+script+") && echo spool-ok", "/workspaces", nil, nil, &out, &errOut); err != nil {
		return fmt.Errorf("%w (stderr: %s)", err, strings.TrimSpace(errOut.String()))
	}
	if !strings.HasSuffix(strings.TrimSpace(out.String()), "spool-ok") {
		return fmt.Errorf("failed: %s", strings.TrimSpace(errOut.String()))
	}
	return nil
}
