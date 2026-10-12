package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/factorycli"
)

// workSandbox is a sandbox in overseer-kcc as the staging overseer's are.
func overseerWorkSandbox(name string, labels, annotations map[string]string, replicas int64) unstructured.Unstructured {
	l := map[string]interface{}{"factory.gemini.google.com/managed": "true"}
	for k, v := range labels {
		l[k] = v
	}
	a := map[string]interface{}{"repo": "k8s-config-connector"}
	for k, v := range annotations {
		a[k] = v
	}
	return unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "agents.x-k8s.io/v1alpha1", "kind": "Sandbox",
		"metadata": map[string]interface{}{"name": name, "namespace": "overseer-kcc", "labels": l, "annotations": a},
		"spec":     map[string]interface{}{"replicas": replicas},
	}}
}

const kccURL = "https://github.com/GoogleCloudPlatform/k8s-config-connector"

func runAnno(recipe, task, state, started string) string {
	b, _ := json.Marshal(map[string]string{"name": recipe + "-run", "task": task, "recipe": recipe, "state": state, "startedAt": started})
	return string(b)
}

func overseerWorkPod(sandbox string, phase corev1.PodPhase, reason string) corev1.Pod {
	return corev1.Pod{
		ObjectMeta: v1.ObjectMeta{Name: sandbox, Namespace: "overseer-kcc", Labels: map[string]string{"sandbox": sandbox}},
		Status:     corev1.PodStatus{Phase: phase, Reason: reason},
	}
}

// rowsByKey are the rows by key.
func rowsByKey(items []OverseerWorkItem) map[string]OverseerWorkItem {
	out := map[string]OverseerWorkItem{}
	for _, it := range items {
		out[it.Key] = it
	}
	return out
}

func chips(it OverseerWorkItem) string {
	var out []string
	for _, s := range it.Sessions {
		out = append(out, s.Recipe+":"+s.Status)
	}
	for _, q := range it.Queued {
		out = append(out, q.Recipe+":"+q.State)
	}
	return strings.Join(out, " ")
}

func sandboxNames(it OverseerWorkItem) string {
	var out []string
	for _, s := range it.Sandboxes {
		out = append(out, s.Name+"("+s.Pod+")")
	}
	return strings.Join(out, " ")
}

func TestBuildOverseerWork(t *testing.T) {
	sandboxes := []unstructured.Unstructured{
		// The overseer's controller: never a row.
		overseerWorkSandbox("overseer-kcc", map[string]string{"sandbox.gemini.google.com/type": "overseer"}, nil, 1),
		// A recipe fix aliased to its PR, whose care-ci then failed.
		overseerWorkSandbox("fix-k8s-config-connector-13870", map[string]string{factorycli.LabelIssue: "13870", "factory.gemini.google.com/pr": "13873", labelFactoryUser: "coder-bot"}, map[string]string{
			"htmlURL":                   kccURL + "/pull/13873",
			factorycli.AnnotationFixRun: runAnno("fix", "recipe-fix-1", "Completed", "2026-10-12T10:00:00Z"),
			"sandbox.gemini.google.com/recipe-care-run": runAnno("care-ci", "recipe-care-ci-2", "Failed", "2026-10-12T12:00:00Z"),
		}, 0),
		// The PR's review, by name only, folds into the issue's row.
		overseerWorkSandbox("review-k8s-config-connector-13873", nil, map[string]string{"htmlURL": kccURL + "/pull/13873",
			"sandbox.gemini.google.com/review-run": runAnno("review", "recipe-review-3", "Completed", "2026-10-12T11:00:00Z")}, 0),
		// A classic fix from before the issue label: the name is the
		// issue's, its URL the PR's; one chip from its last task.
		overseerWorkSandbox("fix-k8s-config-connector-13336", map[string]string{"factory.gemini.google.com/pr": "13789"}, map[string]string{
			"htmlURL": kccURL + "/pull/13789", "pr": "13789",
			annoLastTaskType: "investigate", annoTaskState: "Completed", "sandbox.gemini.google.com/completion-time": "2026-10-11T09:00:00Z",
		}, 0),
		// A PR's own fix sandbox (fix-<repo>-<pr>): the PR's row.
		overseerWorkSandbox("fix-k8s-config-connector-13900", map[string]string{"factory.gemini.google.com/pr": "13900"}, map[string]string{
			"htmlURL": kccURL + "/pull/13900", annoLastTaskType: "address-comments", annoTaskState: "Running",
		}, 1),
		// A legacy review sandbox with a title.
		overseerWorkSandbox("factory-pr-k8s-config-connector-13618", map[string]string{"sandbox.gemini.google.com/type": "review", "factory.gemini.google.com/pr": "13618"}, map[string]string{
			"htmlURL": kccURL + "/pull/13618", "title": "Greenfield: Implement direct KMS", annoLastTaskType: "address-comments", annoTaskState: "Failed",
		}, 0),
		// A workflow's sandbox.
		overseerWorkSandbox("wf-issue-10588", nil, map[string]string{"htmlURL": kccURL + "/issues/10588", annoLastTaskType: "agent", annoTaskState: "Completed"}, 0),
		// A scheduled agent on the repository: no issue.
		overseerWorkSandbox("agent-k8s-config-connector-kcc-release-scheduler", map[string]string{"sandbox.gemini.google.com/type": "agent"}, map[string]string{
			"htmlURL": kccURL, annoLastTaskType: "agent", annoTaskState: "Completed"}, 0),
		// A fix whose pod was evicted.
		overseerWorkSandbox("fix-k8s-config-connector-13916", map[string]string{factorycli.LabelIssue: "13916"}, map[string]string{
			"htmlURL": kccURL + "/issues/13916", factorycli.AnnotationFixRun: runAnno("fix", "recipe-fix-4", "Running", "2026-10-12T13:00:00Z"),
		}, 1),
	}
	pods := []corev1.Pod{
		overseerWorkPod("fix-k8s-config-connector-13900", corev1.PodRunning, ""),
		overseerWorkPod("fix-k8s-config-connector-13916", corev1.PodFailed, "Evicted"),
	}
	q := &QueueResponse{
		Incoming: []QueueTaskItem{
			{FileName: "task-pr-13873-iterate.yaml", Type: "pr-iterate", URL: kccURL + "/pull/13873", Number: 13873, Rank: 2, Priority: "high"},
			{FileName: "task-issue-13931.yaml", Type: "issue-fix", URL: kccURL + "/issues/13931", Number: 13931, Rank: 1},
		},
	}
	rows := rowsByKey(buildOverseerWork("overseer-kcc", sandboxes, pods, q))

	for key, want := range map[string]struct {
		kind, section, reason, chips, sandboxes string
		pr                                      int
	}{
		"issue-13870": {"issue", "attention", "care-ci failed", "care-ci:failed review:done fix:done care-rebase:incoming",
			"fix-k8s-config-connector-13870(paused) review-k8s-config-connector-13873(paused)", 13873},
		"issue-13336": {"issue", "idle", "", "care-ci:done", "fix-k8s-config-connector-13336(paused)", 13789},
		"pr-13900":    {"pr", "working", "", "care-comments:running", "fix-k8s-config-connector-13900(running)", 0},
		"pr-13618":    {"pr", "attention", "care-comments failed", "care-comments:failed", "factory-pr-k8s-config-connector-13618(paused)", 0},
		"issue-10588": {"issue", "idle", "", "workflow:done", "wf-issue-10588(paused)", 0},
		"sandbox-agent-k8s-config-connector-kcc-release-scheduler": {"agent", "agents", "", "agent:done", "agent-k8s-config-connector-kcc-release-scheduler(paused)", 0},
		"issue-13916": {"issue", "attention", "fix-k8s-config-connector-13916's pod evicted", "fix:running", "fix-k8s-config-connector-13916(evicted)", 0},
		"issue-13931": {"issue", "queued", "", "fix:incoming", "", 0},
	} {
		it, ok := rows[key]
		if !ok {
			t.Errorf("no row %s", key)
			continue
		}
		got := fmt.Sprintf("%s %s %q [%s] {%s} pr=%d", it.Kind, it.Section, it.Reason, chips(it), sandboxNames(it), it.PR)
		exp := fmt.Sprintf("%s %s %q [%s] {%s} pr=%d", want.kind, want.section, want.reason, want.chips, want.sandboxes, want.pr)
		if got != exp {
			t.Errorf("%s:\n got %s\nwant %s", key, got, exp)
		}
	}
	if len(rows) != 8 {
		t.Errorf("%d rows, want 8 (no controller row, the review folded): %v", len(rows), rows)
	}
	if it := rows["pr-13618"]; it.Title != "Greenfield: Implement direct KMS" || it.HTMLURL != kccURL+"/pull/13618" {
		t.Errorf("legacy PR row: title %q, url %q", it.Title, it.HTMLURL)
	}
	if it := rows["issue-13870"]; it.PRURL != kccURL+"/pull/13873" || it.Sessions[0].Task != "recipe-care-ci-2" || it.UpdatedAt != "2026-10-12T12:00:00Z" {
		t.Errorf("issue 13870: prUrl %q, first chip's task %q, updated %q", it.PRURL, it.Sessions[0].Task, it.UpdatedAt)
	}
	if it := rows["issue-10588"]; !it.Workflow {
		t.Errorf("issue 10588 is not marked a workflow")
	}
}

// The queue unreachable (the watch mid-cycle) is said, and the rows are
// the sandboxes'; the feed is admin-only like the rest.
func TestOverseerWorkWithoutTheQueue(t *testing.T) {
	prev := fetchOverseerQueue
	fetchOverseerQueue = func(context.Context, *Server, string) (*QueueResponse, error) {
		return nil, errors.New("the watch's queue is unreachable")
	}
	t.Cleanup(func() { fetchOverseerQueue = prev })

	r := overseerSessionServer(t, &fakeSessions{})
	if w := doAs(t, r, "alice", http.MethodGet, "/api/overseers/kcc/work", ""); w.Code != http.StatusForbidden {
		t.Errorf("alice: %d, want 403", w.Code)
	}
	w := doAs(t, r, "root", http.MethodGet, "/api/overseers/kcc/work", "")
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	var got OverseerWork
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.QueueError == "" || len(got.Items) != 1 || got.Items[0].Key != "issue-42" || len(got.Items[0].Sandboxes) != 2 {
		t.Errorf("work = %s", w.Body.String())
	}
}
