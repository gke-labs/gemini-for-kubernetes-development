package sandbox

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/k8s"
)

func recordedRunOn(t *testing.T, get func() *unstructured.Unstructured, key string) RecordedRun {
	t.Helper()
	var run RecordedRun
	if err := json.Unmarshal([]byte(get().GetAnnotations()[key]), &run); err != nil {
		t.Fatalf("%s: %v", key, err)
	}
	return run
}

// A run carries its own state, written with the task's: an issue's plan
// keeps Completed once its fix starts, and a side task's run reads the
// same way as a main one's.
func TestTheRecordedRunCarriesItsState(t *testing.T) {
	ctx, ns := context.Background(), "u"
	kc := fakeKube(t, ns, &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "agents.x-k8s.io/v1alpha1",
		"kind":       "Sandbox",
		"metadata":   map[string]interface{}{"name": "fix-r-7", "namespace": ns},
		"spec":       map[string]interface{}{"replicas": int64(1)},
	}})
	sb := func() *unstructured.Unstructured {
		o, err := kc.DynamicClient.Resource(k8s.SandboxGVR).Namespace(ns).Get(ctx, "fix-r-7", metav1.GetOptions{})
		if err != nil {
			t.Fatal(err)
		}
		return o
	}

	start := func(taskType, task string, side bool) {
		run := RecordedRun{Task: task, StartedAt: time.Now().UTC(), Recipe: taskType, Kind: "Plan", State: "Running",
			Revises: []RecordedRevise{{ID: "plan", Label: "Update plan"}}}
		if err := MarkSandboxRunStarted(ctx, kc, ns, "fix-r-7", taskType, "gemini", side, run); err != nil {
			t.Fatal(err)
		}
	}
	start("plan", "recipe-plan-1", false)
	if run := recordedRunOn(t, sb, RunAnnotation("plan")); run.State != "Running" || run.EndedAt != nil {
		t.Fatalf("started plan run = %+v", run)
	}
	if err := UpdateSandboxTaskAnnotation(ctx, kc, ns, "fix-r-7", "plan", "Completed"); err != nil {
		t.Fatal(err)
	}
	start("fix", "recipe-fix-1", false)
	if run := recordedRunOn(t, sb, RunAnnotation("plan")); run.State != "Completed" || run.EndedAt == nil || run.Recipe != "plan" || len(run.Revises) != 1 {
		t.Errorf("plan run once the fix started = %+v", run)
	}
	if got := sb().GetAnnotations()["sandbox.gemini.google.com/last-task-type"]; got != "fix" {
		t.Errorf("last-task-type = %q, want fix", got)
	}

	start("recipe-triage", "recipe-triage-1", true)
	if err := UpdateSandboxSideTaskAnnotation(ctx, kc, ns, "fix-r-7", "recipe-triage", "Failed"); err != nil {
		t.Fatal(err)
	}
	if run := recordedRunOn(t, sb, RunAnnotation("recipe-triage")); run.State != "Failed" || run.EndedAt == nil {
		t.Errorf("side run = %+v", run)
	}
	if run := recordedRunOn(t, sb, RunAnnotation("fix")); run.State != "Running" {
		t.Errorf("a side task's end changed the fix run: %+v", run)
	}

	// Settling: only the run's own task, and only once.
	if err := SettleRecordedRun(ctx, kc, ns, "fix-r-7", "fix", false, "recipe-fix-0", "Failed"); err != nil {
		t.Fatal(err)
	}
	if run := recordedRunOn(t, sb, RunAnnotation("fix")); run.State != "Running" {
		t.Errorf("an older task settled the newer run: %+v", run)
	}
	if err := SettleRecordedRun(ctx, kc, ns, "fix-r-7", "fix", false, "recipe-fix-1", "Completed"); err != nil {
		t.Fatal(err)
	}
	if run := recordedRunOn(t, sb, RunAnnotation("fix")); run.State != "Completed" || run.EndedAt == nil {
		t.Errorf("settled fix run = %+v", run)
	}
	if got := sb().GetAnnotations()["sandbox.gemini.google.com/last-task-state"]; got != "Completed" {
		t.Errorf("last-task-state = %q, want Completed", got)
	}
}
