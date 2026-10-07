package factorycli

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

type fakeProber struct {
	probe TaskProbe
	// What it was last asked to probe.
	taskType string
}

func (f *fakeProber) Probe(_ context.Context, _, _, taskType string) (TaskProbe, error) {
	f.taskType = taskType
	return f.probe, nil
}

func waitResult(t *testing.T, r *Runner, key string) Result {
	t.Helper()
	for i := 0; i < 100; i++ {
		if res, ok := r.LastResult(key); ok {
			return res
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("no result for %s", key)
	return Result{}
}

// A busy sandbox skips the launch entirely — the next reconcile is the
// requeue (watch dispatchTask: "sandbox is currently busy running
// another task").
func TestRunnerSkipsBusySandbox(t *testing.T) {
	r := &Runner{Binary: "/nonexistent-factory", Prober: &fakeProber{probe: TaskProbe{State: ProbeRunning}},
		running: map[string]struct{}{}, results: map[string]Result{}}

	if r.StartRun("alice/run-1", RunOptions{Namespace: "alice", SandboxName: "run-1", Mode: "plan", Name: "r1"}) {
		t.Fatal("busy sandbox must not launch")
	}
	if _, ok := r.LastResult("alice/run-1"); ok {
		t.Fatal("no result should be recorded for a skipped launch")
	}
	if r.IsRunning("alice/run-1") {
		t.Fatal("skip must not hold the running slot")
	}
}

// Recipe runs are not probed: their run name is the run, and factory
// follows or reads it when invoked with the name again.
func TestRecipeRunsAreNotProbed(t *testing.T) {
	p := &fakeProber{probe: TaskProbe{State: ProbeRunning}}
	r := &Runner{Binary: "/nonexistent-factory", Prober: p,
		running: map[string]struct{}{}, results: map[string]Result{}}

	if !r.StartRecipe("alice/triage-repo-3", RecipeOptions{Recipe: "triage", Namespace: "alice", SandboxName: "fix-repo-3", URL: "u", RunName: "auto/b/3/1"}) {
		t.Fatal("triage did not launch")
	}
	if !r.StartRecipe("alice/plan-repo-3", RecipeOptions{Recipe: "plan", Namespace: "alice", SandboxName: "fix-repo-3", URL: "u", RunName: "plan/b/3/1"}) {
		t.Fatal("plan did not launch")
	}
	waitResult(t, r, "alice/triage-repo-3")
	waitResult(t, r, "alice/plan-repo-3")
	if p.taskType != "" {
		t.Errorf("probed %q", p.taskType)
	}
}

// Nothing in flight: factory spawns normally (failing fast here on the
// nonexistent binary proves the exec path ran).
func TestRunnerLaunchesWhenIdle(t *testing.T) {
	r := &Runner{Binary: "/nonexistent-factory", Prober: &fakeProber{probe: TaskProbe{State: ProbeNone}},
		running: map[string]struct{}{}, results: map[string]Result{}}

	r.StartRecipe("alice/plan-repo-4", RecipeOptions{Recipe: "plan", Namespace: "alice", SandboxName: "fix-repo-4", URL: "u"})
	res := waitResult(t, r, "alice/plan-repo-4")
	if res.Err == nil {
		t.Error("expected exec error from nonexistent binary")
	}
}

// Every invocation tells factory who is launching it, so the sandboxes it
// creates carry the launcher label the board filters on.
func TestRunnerPassesLauncher(t *testing.T) {
	bin := t.TempDir() + "/factory"
	if err := os.WriteFile(bin, []byte("#!/bin/sh\necho \"$@\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	r := &Runner{Binary: bin, Prober: &fakeProber{probe: TaskProbe{State: ProbeNone}},
		running: map[string]struct{}{}, results: map[string]Result{}}

	r.StartPRWatch("alice/watch-repo-5", PRWatchOptions{Namespace: "alice", PRURL: "u"})
	res := waitResult(t, r, "alice/watch-repo-5")
	if res.Err != nil {
		t.Fatalf("run: %v", res.Err)
	}
	if !strings.HasPrefix(res.Output, "pr watch ") || !strings.Contains(res.Output, "--launcher=repo-agent") {
		t.Errorf("args = %q, want the subcommand first and --launcher=repo-agent", res.Output)
	}
}

func TestLaunchedElsewhere(t *testing.T) {
	for labels, want := range map[string]bool{"": false, "repo-agent": false, "factory": true} {
		l := map[string]string{}
		if labels != "" {
			l[LabelLauncher] = labels
		}
		if got := LaunchedElsewhere(l); got != want {
			t.Errorf("LaunchedElsewhere(launcher=%q) = %v, want %v", labels, got, want)
		}
	}
}
