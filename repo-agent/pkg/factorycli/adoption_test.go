package factorycli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type fakeProber struct {
	probe TaskProbe
	// What it was last asked to probe.
	taskType string
}

func (f *fakeProber) Probe(_ context.Context, _, _, taskType, _ string) (TaskProbe, error) {
	f.taskType = taskType
	return f.probe, nil
}

func waitResult(t *testing.T, r *Runner, key string) Result {
	t.Helper()
	// Generous: a loaded machine takes seconds to start a process.
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		if res, ok := r.LastResult(key); ok {
			return res
		}
	}
	t.Fatalf("no result for %s", key)
	return Result{}
}

// A busy sandbox skips the launch entirely — the next reconcile is the
// requeue (watch dispatchTask: "sandbox is currently busy running
// another task").
func TestRunnerSkipsBusySandbox(t *testing.T) {
	r := &Runner{Binary: "/nonexistent-factory", Prober: &fakeProber{probe: TaskProbe{State: ProbeRunning}},
		running: map[string]context.CancelFunc{}, results: map[string]Result{}}

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
		running: map[string]context.CancelFunc{}, results: map[string]Result{}}

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
		running: map[string]context.CancelFunc{}, results: map[string]Result{}}

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
		running: map[string]context.CancelFunc{}, results: map[string]Result{}}

	r.StartPRWatch("alice/watch-repo-5", PRWatchOptions{Namespace: "alice", PRURL: "u"})
	res := waitResult(t, r, "alice/watch-repo-5")
	if res.Err != nil {
		t.Fatalf("run: %v", res.Err)
	}
	if !strings.HasPrefix(res.Output, "pr watch ") || !strings.Contains(res.Output, "--launcher=repo-agent") {
		t.Errorf("args = %q, want the subcommand first and --launcher=repo-agent", res.Output)
	}
}

// An auto watch runs until it is stopped, and lists as running until
// then.
func TestPRWatchStop(t *testing.T) {
	dir := t.TempDir()
	bin, args := dir+"/factory", dir+"/args"
	if err := os.WriteFile(bin, []byte("#!/bin/sh\necho \"$@\" > "+args+"\nexec sleep 60\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	r := &Runner{Binary: bin, running: map[string]context.CancelFunc{}, results: map[string]Result{}}

	r.StartPRWatch("alice/prwatch-repo-5", PRWatchOptions{Namespace: "alice", PRURL: "u"})
	var got []byte
	for deadline := time.Now().Add(10 * time.Second); !strings.HasSuffix(string(got), "\n") && time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		got, _ = os.ReadFile(args)
	}
	if !strings.HasPrefix(string(got), "pr watch ") || strings.Contains(string(got), "--continue-session") {
		t.Errorf("args = %q, want pr watch with no --continue-session", got)
	}
	if got := r.Running("alice/prwatch-"); len(got) != 1 || got[0] != "alice/prwatch-repo-5" {
		t.Fatalf("Running = %v, want the watch", got)
	}
	r.Stop("alice/prwatch-repo-5")
	res := waitResult(t, r, "alice/prwatch-repo-5")
	if res.Err == nil {
		t.Errorf("a stopped watch ended with no error")
	}
	if got := r.Running("alice/"); len(got) != 0 {
		t.Errorf("Running = %v after Stop, want none", got)
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

// A run's phase is the run-<mode> recipe, named by --run, each phase only
// what its recipe takes: deploy no intent, and only a plan a runbook and
// a target.
func TestStartRunInvokesTheRunRecipe(t *testing.T) {
	dir := t.TempDir()
	argv := filepath.Join(dir, "argv")
	bin := filepath.Join(dir, "factory")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nprintf '%s\\n' \"$@\" > "+argv+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		mode string
		want string
	}{
		{"plan", "recipe run-plan --url https://github.com/o/r --namespace alice --timeout 1h0m0s --abort-on-cancel=false --run r1 --sandbox-key r1 --intent 3 nodes --runbook gke --target-pr 42"},
		{"deploy", "recipe run-deploy --url https://github.com/o/r --namespace alice --timeout 1h0m0s --abort-on-cancel=false --run r1 --sandbox-key r1"},
		{"teardown", "recipe run-teardown --url https://github.com/o/r --namespace alice --timeout 1h0m0s --abort-on-cancel=false --run r1 --sandbox-key r1 --intent 3 nodes"},
	} {
		r := &Runner{Binary: bin, Prober: &fakeProber{probe: TaskProbe{State: ProbeNone}},
			running: map[string]context.CancelFunc{}, results: map[string]Result{}}
		key := "alice/" + tc.mode
		if !r.StartRun(key, RunOptions{Namespace: "alice", SandboxName: RunSandboxName("r", "r1"), Mode: tc.mode, Name: "r1", Intent: "3 nodes", Runbook: "gke", Target: 42, RepoURL: "https://github.com/o/r"}) {
			t.Fatalf("%s did not launch", tc.mode)
		}
		waitResult(t, r, key)
		b, err := os.ReadFile(argv)
		if err != nil {
			t.Fatal(err)
		}
		if got := strings.Join(strings.Fields(strings.ReplaceAll(string(b), "\n", " ")), " "); !strings.HasPrefix(got, tc.want) {
			t.Errorf("%s: factory %s\nwant %s", tc.mode, got, tc.want)
		}
	}
	// factory's sandbox.ScopedSandboxName: run-<repo>-<short hash of the key>.
	if got := RunSandboxName("k8s-config-connector", "gke-std"); got != ScopedSandboxName(RunPrefix, "k8s-config-connector", "gke-std") || !strings.HasPrefix(got, "run-k8s-config-connector-") || strings.Contains(got, "gke-std") {
		t.Errorf("RunSandboxName = %s", got)
	}
}
