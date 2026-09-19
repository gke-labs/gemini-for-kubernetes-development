package factorycli

import (
	"context"
	"strings"
	"testing"
	"time"
)

type fakeProber struct {
	probe TaskProbe
}

func (f *fakeProber) Probe(_ context.Context, _, _, _, _ string) (TaskProbe, error) {
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

	if r.StartPlan("alice/plan-repo-1", PlanOptions{Namespace: "alice", SandboxName: "fix-repo-1", IssueURL: "u"}) {
		t.Fatal("busy sandbox must not launch")
	}
	if _, ok := r.LastResult("alice/plan-repo-1"); ok {
		t.Fatal("no result should be recorded for a skipped launch")
	}
	if r.IsRunning("alice/plan-repo-1") {
		t.Fatal("skip must not hold the running slot")
	}
}

// An orphaned finished plan (annotation claimed Running, exit code
// present) is adopted: its output becomes the harvestable result and
// factory never spawns.
func TestRunnerAdoptsOrphanedPlan(t *testing.T) {
	r := &Runner{Binary: "/nonexistent-factory",
		Prober:  &fakeProber{probe: TaskProbe{State: ProbeOrphanCompleted, ExitCode: "0", Output: "## Summary\nadopted"}},
		running: map[string]struct{}{}, results: map[string]Result{}}

	if !r.StartPlan("alice/plan-repo-2", PlanOptions{Namespace: "alice", SandboxName: "fix-repo-2", IssueURL: "u"}) {
		t.Fatal("adoption should report started")
	}
	res := waitResult(t, r, "alice/plan-repo-2")
	if res.Err != nil {
		t.Fatalf("adoption should not error: %v", res.Err)
	}
	if ExtractPlan(res.Output) != "## Summary\nadopted" {
		t.Errorf("adopted output not harvestable: %q", res.Output)
	}
}

// An orphaned FAILED task records the failure without re-executing.
func TestRunnerAdoptsOrphanedFailure(t *testing.T) {
	r := &Runner{Binary: "/nonexistent-factory",
		Prober:  &fakeProber{probe: TaskProbe{State: ProbeOrphanCompleted, ExitCode: "1", Output: "boom"}},
		running: map[string]struct{}{}, results: map[string]Result{}}

	r.StartTriage("alice/triage-repo-3", TriageOptions{Namespace: "alice", SandboxName: "triage-repo-3", IssueURL: "u"})
	res := waitResult(t, r, "alice/triage-repo-3")
	if res.Err == nil || !strings.Contains(res.Err.Error(), "exited 1") {
		t.Errorf("expected adopted-failure error, got %+v", res)
	}
}

// Nothing in flight: factory spawns normally (failing fast here on the
// nonexistent binary proves the exec path ran).
func TestRunnerLaunchesWhenIdle(t *testing.T) {
	r := &Runner{Binary: "/nonexistent-factory", Prober: &fakeProber{probe: TaskProbe{State: ProbeNone}},
		running: map[string]struct{}{}, results: map[string]Result{}}

	r.StartPlan("alice/plan-repo-4", PlanOptions{Namespace: "alice", SandboxName: "fix-repo-4", IssueURL: "u"})
	res := waitResult(t, r, "alice/plan-repo-4")
	if res.Err == nil {
		t.Error("expected exec error from nonexistent binary")
	}
}
