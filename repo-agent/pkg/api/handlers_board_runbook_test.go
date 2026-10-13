package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/dynamic/fake"

	boardv1alpha1 "github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/api/repoboard/v1alpha1"
)

// Removing a run deletes its branch, and nothing else.
func TestRemoveDeletesTheRunsBranch(t *testing.T) {
	gh := map[string]string{
		"https://api.github.com/repos/alice/repo/git/ref/heads/runs/instance1": `{"ref": "refs/heads/runs/instance1", "object": {"sha": "aaa", "type": "commit"}}`,
	}
	_, r, _, rt := boardTestServerWithRT(t, gh, boardCR())

	req, _ := http.NewRequest(http.MethodDelete, "/board/myboard/runbook/instance/instance1", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("remove returned %d, want 200: %s", w.Code, w.Body.String())
	}
	assertWrites(t, rt, []string{"DELETE /repos/alice/repo/git/refs/heads/runs/instance1"})
}

// Nowhere at all is still a 404, and must not have deleted anything on
// the way to deciding that.
func TestRemoveReportsARunItCannotFind(t *testing.T) {
	_, r, _, rt := boardTestServerWithRT(t, map[string]string{}, boardCR())

	req, _ := http.NewRequest(http.MethodDelete, "/board/myboard/runbook/instance/ghost", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("remove returned %d, want 404: %s", w.Code, w.Body.String())
	}
	assertWrites(t, rt, nil)
}

// seedRunClick puts one settled-or-standing run click in the namespace,
// named and aged by the caller: what the Try tab shows depends entirely
// on which of several receipts for one (mode, run) is the newest.
func seedRunClick(t *testing.T, dyn *fake.FakeDynamicClient, name, mode, run, phase string, age time.Duration) {
	t.Helper()
	click := requestCR(boardv1alpha1.RequestSpec{
		Verb: boardv1alpha1.VerbRun,
		Run:  &boardv1alpha1.RunRequest{Mode: mode, Name: run},
	})
	click.SetName(name)
	at := v1.NewTime(time.Now().Add(-age))
	click.SetCreationTimestamp(at)
	status := map[string]interface{}{"phase": phase}
	if phase == boardv1alpha1.RequestFailed || phase == boardv1alpha1.RequestSucceeded {
		status["completedAt"] = at.UTC().Format(time.RFC3339)
	}
	click.Object["status"] = status
	if _, err := dyn.Resource(requestGVR).Namespace("alice").Create(context.Background(), click, v1.CreateOptions{}); err != nil {
		t.Fatalf("seed %s: %v", name, err)
	}
}

func runbookPending(t *testing.T, r http.Handler) []map[string]interface{} {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, "/board/myboard/runbook", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("runbook returned %d: %s", w.Code, w.Body.String())
	}
	var body struct {
		Pending []map[string]interface{} `json:"pending"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return body.Pending
}

// A failure is kept for a week so nobody has to be at their desk when
// it happens. That is only useful while it is still true: click deploy
// again and it works, and the week-old failure has to go — the newest
// receipt for a (mode, run) is the only one the tab speaks for.
func TestAFreshSuccessBuriesLastWeeksFailure(t *testing.T) {
	_, r, dyn := boardTestServer(t, map[string]string{}, boardCR())
	seedRunClick(t, dyn, "deploy-old-failure", "deploy", "gcevm", boardv1alpha1.RequestFailed, 6*24*time.Hour)
	seedRunClick(t, dyn, "deploy-fresh-success", "deploy", "gcevm", boardv1alpha1.RequestSucceeded, 10*time.Minute)

	if pending := runbookPending(t, r); len(pending) != 0 {
		t.Errorf("pending = %+v, want the failure buried by the newer success", pending)
	}
}

// Two modes of one run are two facts: a deploy that failed on Friday
// and a re-plan running now both belong on screen, and the row decides
// which of them it speaks for.
func TestModesOfOneRunDoNotBuryEachOther(t *testing.T) {
	_, r, dyn := boardTestServer(t, map[string]string{}, boardCR())
	seedRunClick(t, dyn, "deploy-failure", "deploy", "gcevm", boardv1alpha1.RequestFailed, 3*24*time.Hour)
	seedRunClick(t, dyn, "plan-running", "plan", "gcevm", boardv1alpha1.RequestRunning, time.Minute)

	pending := runbookPending(t, r)
	if len(pending) != 2 {
		t.Fatalf("pending = %+v, want both the failed deploy and the live plan", pending)
	}
	modes := map[string]string{}
	for _, p := range pending {
		modes[p["mode"].(string)] = p["phase"].(string)
	}
	if modes["deploy"] != boardv1alpha1.RequestFailed || modes["plan"] != boardv1alpha1.RequestRunning {
		t.Errorf("modes = %v", modes)
	}
}

func assertWrites(t *testing.T, rt *boardMockRT, want []string) {
	t.Helper()
	rt.mu.Lock()
	got := append([]string{}, rt.writes...)
	rt.mu.Unlock()
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("wrote:\n  %s\nwant:\n  %s", strings.Join(got, "\n  "), strings.Join(want, "\n  "))
	}
}
