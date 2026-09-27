package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// contentsURL is the fake GitHub's key for reading a directory on the
// notes branch.
func contentsURL(path string) string {
	return "https://api.github.com/repos/alice/repo/contents/" + path + "?ref=exploration%2Fnotes"
}

// Remove has to find the run wherever it is. Runs moved from
// runbook-deployments/ to agent-runs/ and a legacy run is adopted only
// when something touches it, so both layouts are on screen at once;
// the handler looked in one of them and reported "not found" — or
// worse, 404ed silently behind a button that gave no feedback — for
// every run written since the move.
func TestRemoveFindsARunUnderTheCurrentLayout(t *testing.T) {
	gh := map[string]string{
		contentsURL("docs-exploration/agent-runs/instance1"): `[
			{"type": "file", "name": "runbook.md", "path": "docs-exploration/agent-runs/instance1/runbook.md", "sha": "aaa"},
			{"type": "file", "name": "receipt-1.md", "path": "docs-exploration/agent-runs/instance1/receipt-1.md", "sha": "bbb"}
		]`,
	}
	_, r, _, rt := boardTestServerWithRT(t, gh, boardCR())

	req, _ := http.NewRequest(http.MethodDelete, "/board/myboard/runbook/instance/instance1", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("remove returned %d, want 200: %s", w.Code, w.Body.String())
	}
	want := []string{
		"DELETE /repos/alice/repo/contents/docs-exploration/agent-runs/instance1/runbook.md",
		"DELETE /repos/alice/repo/contents/docs-exploration/agent-runs/instance1/receipt-1.md",
	}
	assertWrites(t, rt, want)
}

// A run's directory can hold a subdirectory — generated scripts, a
// plan's attachments. The Contents API deletes files, never
// directories, so anything left behind keeps the directory alive and
// the run on screen: the button appears to do nothing.
func TestRemoveDescendsIntoASubdirectory(t *testing.T) {
	gh := map[string]string{
		contentsURL("docs-exploration/agent-runs/instance1"): `[
			{"type": "file", "name": "runbook.md", "path": "docs-exploration/agent-runs/instance1/runbook.md", "sha": "aaa"},
			{"type": "dir", "name": "scripts", "path": "docs-exploration/agent-runs/instance1/scripts"}
		]`,
		contentsURL("docs-exploration/agent-runs/instance1/scripts"): `[
			{"type": "file", "name": "apply.sh", "path": "docs-exploration/agent-runs/instance1/scripts/apply.sh", "sha": "ccc"}
		]`,
	}
	_, r, _, rt := boardTestServerWithRT(t, gh, boardCR())

	req, _ := http.NewRequest(http.MethodDelete, "/board/myboard/runbook/instance/instance1", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("remove returned %d, want 200: %s", w.Code, w.Body.String())
	}
	assertWrites(t, rt, []string{
		"DELETE /repos/alice/repo/contents/docs-exploration/agent-runs/instance1/runbook.md",
		"DELETE /repos/alice/repo/contents/docs-exploration/agent-runs/instance1/scripts/apply.sh",
	})
}

// The older layouts still have live runs in them, and a run that was
// half-adopted has files in two places at once. Remove clears every
// copy, or the row comes back on the next reload.
func TestRemoveClearsEveryLayoutTheRunAppearsIn(t *testing.T) {
	gh := map[string]string{
		contentsURL("docs-exploration/agent-runs/rel1"): `[
			{"type": "file", "name": "runbook.md", "path": "docs-exploration/agent-runs/rel1/runbook.md", "sha": "aaa"}
		]`,
		contentsURL("docs-exploration/runbook-deployments/rel1"): `[
			{"type": "file", "name": "receipt-0.md", "path": "docs-exploration/runbook-deployments/rel1/receipt-0.md", "sha": "bbb"}
		]`,
	}
	_, r, _, rt := boardTestServerWithRT(t, gh, boardCR())

	req, _ := http.NewRequest(http.MethodDelete, "/board/myboard/runbook/instance/rel1", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("remove returned %d, want 200: %s", w.Code, w.Body.String())
	}
	assertWrites(t, rt, []string{
		"DELETE /repos/alice/repo/contents/docs-exploration/agent-runs/rel1/runbook.md",
		"DELETE /repos/alice/repo/contents/docs-exploration/runbook-deployments/rel1/receipt-0.md",
	})
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

func assertWrites(t *testing.T, rt *boardMockRT, want []string) {
	t.Helper()
	rt.mu.Lock()
	got := append([]string{}, rt.writes...)
	rt.mu.Unlock()
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("wrote:\n  %s\nwant:\n  %s", strings.Join(got, "\n  "), strings.Join(want, "\n  "))
	}
}
