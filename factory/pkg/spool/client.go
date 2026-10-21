package spool

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/taskfiles"
)

// TaskDir is where a claimed task runs.
func TaskDir(id string) string {
	return taskfiles.DefaultTasksDir + "/" + id
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

// Entry is a task as List finds it. Kind and Started come from task.json
// when the task has one (spooled and recipe tasks) and from the directory
// name otherwise (<kind>-<YYYYMMDD-HHMMSS>, as every task command names
// its directory).
type Entry struct {
	Task
	Kind     string    `json:"kind"`
	Started  time.Time `json:"started,omitempty"`
	State    State     `json:"state"`
	ExitCode string    `json:"exit_code,omitempty"`
	// Ended and Reason are from the task's status, when it has one.
	Ended  time.Time `json:"ended,omitzero"`
	Reason string    `json:"reason,omitempty"`
	// EngineRetries are the model calls its engine retried, from the
	// agent session's engine log, as it runs.
	EngineRetries *EngineRetries `json:"engine_retries,omitempty"`
	// RequiresHuman is why the task failed in a way running it again would
	// repeat (RequiresHumanFile), when it did.
	RequiresHuman string `json:"requires_human,omitempty"`
	// StopReason is the ACP stop reason of the turn the task failed on,
	// when that turn ended otherwise than with the agent's answer
	// (StopReasonFile): "step 8:ask: max_turn_requests" is a task whose
	// agent ran out of turns.
	StopReason string `json:"stop_reason,omitempty"`
}

// OutOfTurns is whether the task failed because its agent ran out of
// turns: the engine cut a request off at its limit of model turns.
func (e Entry) OutOfTurns() bool {
	return strings.HasSuffix(e.StopReason, ": max_turn_requests")
}

// taskName splits a task directory name into its kind and the time in it.
var taskName = regexp.MustCompile(`^(.+)-(\d{8}-\d{6})(?:-[0-9a-f]+)?$`)

// ListScript is the shell script List runs, printing a line per task in
// incomingDir and tasksDir for ParseList; the daemon's task server runs
// it too, so both report a task the same way.
func ListScript(incomingDir, tasksDir string) string {
	return fmt.Sprintf(`for d in %s/*/ %s/*/; do
  [ -d "$d" ] || continue
  case "$d" in
  %s/*) s=pending ;;
  *)
    if [ -s "${d}exit_code" ]; then s="exited:$(cat "${d}exit_code")"
    elif [ -s "${d}pid" ]; then
      if [ -n "$(%s)" ]; then s=running
      elif [ -s "${d}exit_code" ]; then s="exited:$(cat "${d}exit_code")"
      else echo 137 > "${d}exit_code" 2>/dev/null; s="exited:137"; fi
    else s=claimed; fi ;;
  esac
  printf '%%s\t%%s\t%%s\n' "$s" "$(basename "$d")" "$(tr -d '\n' < "${d}%s" 2>/dev/null)"
done`, incomingDir, tasksDir, incomingDir, taskfiles.BuildCheckPidCmd(`"${d}pid"`, `"${d}start_time"`), TaskFile)
}

// ParseList reads ListScript's output, newest task first.
func ParseList(out string) []Entry {
	var entries []Entry
	for _, line := range strings.Split(out, "\n") {
		parts := strings.SplitN(line, "\t", 3)
		if len(parts) != 3 || parts[1] == "" {
			continue
		}
		state, name, taskJSON := parts[0], parts[1], parts[2]
		e := Entry{Kind: name}
		if m := taskName.FindStringSubmatch(name); m != nil {
			e.Kind = m[1]
			e.Started, _ = time.ParseInLocation("20060102-150405", m[2], time.Local)
		}
		if taskJSON != "" && json.Unmarshal([]byte(taskJSON), &e.Task) == nil && e.ID != "" {
			e.Kind = "recipe-" + e.Recipe
			if e.TaskType != "" {
				e.Kind = e.TaskType
			}
			e.Started = e.SubmittedAt
		}
		e.ID = name
		e.State = State(state)
		if code, ok := strings.CutPrefix(state, "exited:"); ok {
			e.State, e.ExitCode = Exited, strings.TrimSpace(code)
		}
		entries = append(entries, e)
	}
	sort.SliceStable(entries, func(i, j int) bool { return entries[i].Started.After(entries[j].Started) })
	return entries
}

// Find picks the task to attach to: by id, else the newest with
// runName, else the newest of all.
func Find(entries []Entry, id, runName string) (Entry, error) {
	for _, e := range entries {
		if (id != "" && e.ID == id) || (id == "" && runName != "" && e.RunName == runName) || (id == "" && runName == "") {
			return e, nil
		}
	}
	switch {
	case id != "":
		return Entry{}, fmt.Errorf("no task %s", id)
	case runName != "":
		return Entry{}, fmt.Errorf("no task with run name %s", runName)
	}
	return Entry{}, fmt.Errorf("no tasks")
}
