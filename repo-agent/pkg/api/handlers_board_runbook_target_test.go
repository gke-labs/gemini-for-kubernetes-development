package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func postRunJSON(t *testing.T, r http.Handler, body map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	b, _ := json.Marshal(body)
	req, _ := http.NewRequest(http.MethodPost, "/board/myboard/runbook", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

// Deploy ▾ on a pull request row: a plan from a runbook, pinned to the
// pull request. Both ride the Request to the controller, which hands
// them to `factory run plan --runbook --target`.
func TestAPlanCarriesItsPullRequest(t *testing.T) {
	_, r, dyn := boardTestServer(t, map[string]string{}, boardCR())
	// -in-pod needs no GCP project, which this server has none of.
	w := postRunJSON(t, r, map[string]any{"mode": "plan", "name": "gke-pr42-in-pod", "runbook": "gke-in-pod", "target": 42})
	if w.Code != http.StatusOK {
		t.Fatalf("plan returned %d: %s", w.Code, w.Body.String())
	}
	filed := filedRequests(t, dyn, "alice")
	if len(filed) != 1 || filed[0].Spec.Run == nil {
		t.Fatalf("requests = %+v; want one run", filed)
	}
	if got := filed[0].Spec.Run.Target; got != 42 {
		t.Errorf("spec.run.target = %d, want 42", got)
	}
	if got := filed[0].Spec.Run.Runbook; got != "gke-in-pod" {
		t.Errorf("spec.run.runbook = %q, want gke-in-pod", got)
	}
}

// A pull request is pinned by a plan and nothing else, is a pull
// request number, and a run pinned to one keeps its name short enough
// for the sandbox name to hold it whole.
func TestAPullRequestIsRefusedWhereItCannotApply(t *testing.T) {
	for _, tc := range []struct {
		why  string
		body map[string]any
	}{
		{"deploy", map[string]any{"mode": "deploy", "name": "gke-pr42", "target": 42}},
		{"teardown", map[string]any{"mode": "teardown", "name": "gke-pr42", "target": 42}},
		{"negative", map[string]any{"mode": "plan", "name": "gke-pr42", "target": -1}},
		{"long name", map[string]any{"mode": "plan", "name": strings.Repeat("a", maxTargetRunName) + "-pr42", "target": 42}},
	} {
		t.Run(tc.why, func(t *testing.T) {
			_, r, dyn := boardTestServer(t, map[string]string{}, boardCR())
			if w := postRunJSON(t, r, tc.body); w.Code != http.StatusBadRequest {
				t.Errorf("returned %d, want 400: %s", w.Code, w.Body.String())
			}
			if filed := filedRequests(t, dyn, "alice"); len(filed) != 0 {
				t.Errorf("a Request was filed anyway: %+v", filed)
			}
		})
	}
}

// The runs listing says which pull request a run is pinned to, read
// from the target.env the plan wrote, so a pull request row can show
// its runs. A run with no pin says nothing.
func TestRunsListingShowsTheirPullRequest(t *testing.T) {
	resetRepoRunbookCache()
	sha := strings.Repeat("a", 40)
	gh := map[string]string{
		contentsURL("docs-exploration/agent-runs"): `[
			{"type": "dir", "name": "gke-pr42", "path": "docs-exploration/agent-runs/gke-pr42"},
			{"type": "dir", "name": "gke", "path": "docs-exploration/agent-runs/gke"}
		]`,
		contentsURL("docs-exploration/agent-runs/gke-pr42"): `[
			{"type": "file", "name": "runbook.md", "path": "docs-exploration/agent-runs/gke-pr42/runbook.md"},
			{"type": "file", "name": "target.env", "path": "docs-exploration/agent-runs/gke-pr42/target.env"}
		]`,
		contentsURL("docs-exploration/agent-runs/gke-pr42/target.env"): `{"type": "file", "name": "target.env", "content": "TARGET_PR=42\nTARGET_SHA=` + sha + `\n"}`,
		contentsURL("docs-exploration/agent-runs/gke"): `[
			{"type": "file", "name": "runbook.md", "path": "docs-exploration/agent-runs/gke/runbook.md"}
		]`,
	}
	_, r, _ := boardTestServer(t, gh, boardCR())
	req, _ := http.NewRequest(http.MethodGet, "/board/myboard/runbook", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("runbook returned %d: %s", w.Code, w.Body.String())
	}
	var body struct {
		Instances []struct {
			Name      string `json:"name"`
			Target    int    `json:"target"`
			TargetSHA string `json:"targetSHA"`
		} `json:"instances"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	got := map[string]int{}
	for _, in := range body.Instances {
		got[in.Name] = in.Target
		if in.Name == "gke-pr42" && in.TargetSHA != sha {
			t.Errorf("targetSHA = %q, want %q", in.TargetSHA, sha)
		}
	}
	if len(got) != 2 || got["gke-pr42"] != 42 || got["gke"] != 0 {
		t.Errorf("targets = %v, want gke-pr42:42 and gke:0", got)
	}
}
