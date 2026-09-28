package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func resetRepoRunbookCache() {
	repoRunbookCache.Lock()
	repoRunbookCache.entries = map[string]repoRunbookEntry{}
	repoRunbookCache.Unlock()
}

func repoRunbooksOf(t *testing.T, r http.Handler) []string {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, "/board/myboard/runbook", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("runbook returned %d: %s", w.Code, w.Body.String())
	}
	var body struct {
		RepoRunbooks []string `json:"repoRunbooks"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return body.RepoRunbooks
}

// The composer offers the repository's runbooks: the directories under
// .agents/runbooks/ on the default branch — what factory reads — and
// nothing else that happens to be there.
func TestRunsTabListsTheRepositorysRunbooks(t *testing.T) {
	resetRepoRunbookCache()
	gh := map[string]string{
		"https://api.github.com/repos/test/repo/contents/.agents/runbooks": `[
			{"type": "dir", "name": "gke", "path": ".agents/runbooks/gke"},
			{"type": "dir", "name": "gce-vm", "path": ".agents/runbooks/gce-vm"},
			{"type": "file", "name": "README.md", "path": ".agents/runbooks/README.md"},
			{"type": "dir", "name": "Not_A_Name", "path": ".agents/runbooks/Not_A_Name"}
		]`,
	}
	_, r, _ := boardTestServer(t, gh, boardCR())
	got := repoRunbooksOf(t, r)
	if len(got) != 2 || got[0] != "gce-vm" || got[1] != "gke" {
		t.Errorf("repoRunbooks = %v, want [gce-vm gke]", got)
	}
}

// Most repositories have no .agents/runbooks/ at all: that is an empty
// list, not an error, and not a missing key the UI has to guess about.
func TestRunsTabWithNoRepositoryRunbooks(t *testing.T) {
	resetRepoRunbookCache()
	_, r, _ := boardTestServer(t, map[string]string{}, boardCR())
	if got := repoRunbooksOf(t, r); got == nil || len(got) != 0 {
		t.Errorf("repoRunbooks = %#v, want an empty list", got)
	}
}

func postRun(t *testing.T, r http.Handler, body map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	b, _ := json.Marshal(body)
	req, _ := http.NewRequest(http.MethodPost, "/board/myboard/runbook", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

// The runbook a run starts from rides the Request to the controller,
// which hands it to `factory run plan --runbook`.
func TestAPlanCarriesItsRunbook(t *testing.T) {
	_, r, dyn := boardTestServer(t, map[string]string{}, boardCR())
	// -in-pod needs no GCP project, which this server has none of.
	w := postRun(t, r, map[string]string{"mode": "plan", "name": "pr-42-in-pod", "runbook": "gke"})
	if w.Code != http.StatusOK {
		t.Fatalf("plan returned %d: %s", w.Code, w.Body.String())
	}
	filed := filedRequests(t, dyn, "alice")
	if len(filed) != 1 || filed[0].Spec.Run == nil {
		t.Fatalf("requests = %+v; want one run", filed)
	}
	if got := filed[0].Spec.Run.Runbook; got != "gke" {
		t.Errorf("spec.run.runbook = %q, want gke", got)
	}
}

// A runbook starts a plan and nothing else, has the shape factory
// accepts, and is not the run itself: each is refused here, where the
// owner sees why, rather than by factory, where the click would strand.
func TestARunbookIsRefusedWhereItCannotApply(t *testing.T) {
	for _, tc := range []struct {
		why  string
		body map[string]string
	}{
		{"deploy", map[string]string{"mode": "deploy", "name": "pr-42-in-pod", "runbook": "gke"}},
		{"teardown", map[string]string{"mode": "teardown", "name": "pr-42-in-pod", "runbook": "gke"}},
		{"shape", map[string]string{"mode": "plan", "name": "pr-42-in-pod", "runbook": "../gke"}},
		{"itself", map[string]string{"mode": "plan", "name": "pr-42-in-pod", "runbook": "pr-42-in-pod"}},
	} {
		t.Run(tc.why, func(t *testing.T) {
			_, r, dyn := boardTestServer(t, map[string]string{}, boardCR())
			if w := postRun(t, r, tc.body); w.Code != http.StatusBadRequest {
				t.Errorf("returned %d, want 400: %s", w.Code, w.Body.String())
			}
			if filed := filedRequests(t, dyn, "alice"); len(filed) != 0 {
				t.Errorf("a Request was filed anyway: %+v", filed)
			}
		})
	}
}
