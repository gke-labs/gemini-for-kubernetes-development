package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	boardv1alpha1 "github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/api/repoboard/v1alpha1"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/models"
)

// boardWithRecipes is the board once its controller has published the
// catalog: the built-ins and summarize, on issues and PRs.
func boardWithRecipes() map[string]interface{} {
	board := boardCR()
	board.Object["status"] = map[string]interface{}{"recipes": []interface{}{
		map[string]interface{}{"name": "triage", "label": "Triage", "on": []interface{}{"issue"}, "kind": "Triage"},
		map[string]interface{}{"name": "research", "label": "Research", "on": []interface{}{"repo"}, "kind": "Notes"},
		map[string]interface{}{"name": "summarize", "label": "Summarize", "on": []interface{}{"issue", "pr"}, "kind": "Summary"},
	}}
	return board.Object
}

func TestGetBoardRecipes(t *testing.T) {
	board := boardCR()
	board.Object = boardWithRecipes()
	_, r, _ := boardTestServer(t, nil, board)
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/board/myboard/recipes", nil)
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var recipes []boardv1alpha1.BoardRecipe
	if err := json.Unmarshal(w.Body.Bytes(), &recipes); err != nil {
		t.Fatal(err)
	}
	if len(recipes) != 3 || recipes[2].Name != "summarize" || recipes[2].Kind != "Summary" {
		t.Errorf("recipes = %+v, want the board's three, in its order", recipes)
	}

	// Before the controller has published any: none, not an error.
	_, r, _ = boardTestServer(t, nil, boardCR())
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK || strings.TrimSpace(w.Body.String()) != "[]" {
		t.Errorf("no catalog: %d %s, want 200 []", w.Code, w.Body.String())
	}
}

// Any recipe the board lists launches on a row it starts on, as one
// recipe Request with the member's inputs.
func TestLaunchRecipeFilesARequest(t *testing.T) {
	board := boardCR()
	board.Object = boardWithRecipes()
	_, r, dyn := boardTestServer(t, nil, board)
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("POST", "/board/myboard/prs/9/recipes/summarize", strings.NewReader(`{"inputs":{"length":"short"}}`))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	filed := theRequest(t, dyn, "alice")
	spec := filed.Spec
	if spec.Verb != boardv1alpha1.VerbRecipe || spec.Recipe != "summarize" || spec.Item != "pr" || spec.Number != 9 ||
		spec.Member != "alice" || spec.Inputs["length"] != "short" {
		t.Errorf("filed %+v, want alice's summarize on PR 9 with its input", spec)
	}
}

// A recipe the board does not list, or one that does not start on the
// row, is refused: nothing would ever serve the Request.
func TestLaunchRecipeRefusesWhatTheBoardCannotStart(t *testing.T) {
	for _, path := range []string{
		"/board/myboard/issues/9/recipes/nope",
		"/board/myboard/issues/9/recipes/research",
		"/board/myboard/issues/9/recipes/review",
		"/board/myboard/prs/9/recipes/triage",
	} {
		board := boardCR()
		board.Object = boardWithRecipes()
		_, r, _ := boardTestServer(t, nil, board)
		w := httptest.NewRecorder()
		req, _ := http.NewRequest("POST", path, nil)
		r.ServeHTTP(w, req)
		if w.Code != http.StatusBadRequest {
			t.Errorf("%s: %d %s, want 400", path, w.Code, w.Body.String())
		}
	}
}

func runSandbox(name, url string, annotations map[string]string) *unstructured.Unstructured {
	sb := &unstructured.Unstructured{Object: map[string]interface{}{}}
	sb.SetName(name)
	annotations["htmlURL"] = url
	sb.SetAnnotations(annotations)
	return sb
}

// Every run recorded on an item's sandboxes is one of its sessions, newest
// first, whichever recipe it is: an issue's sandbox's on the issue and on
// the PR its fix opened, a PR's on the PR.
func TestRunSessions(t *testing.T) {
	items := map[string]*models.WorkItem{"issue-12": {}, "pr-30": {}, "pr-9": {}}
	sandboxes := []*unstructured.Unstructured{
		runSandbox("fix-repo-12", "https://github.com/o/repo/pull/30", map[string]string{
			"sandbox.gemini.google.com/plan-run":   `{"name":"p","task":"plan-1","startedAt":"2026-10-01T10:00:00Z","recipe":"plan","kind":"Plan","state":"Completed","endedAt":"2026-10-01T10:05:00Z","revises":[{"id":"revise"}]}`,
			"sandbox.gemini.google.com/fix-run":    `{"name":"f","task":"fix-2","startedAt":"2026-10-02T10:00:00Z","recipe":"fix","kind":"Change","state":"Running"}`,
			"board.gemini.google.com/plan-output":  "kind: Plan\n",
			"board.gemini.google.com/plan-applied": `{"comment":"2026-10-01T11:00:00Z"}`,
		}),
		runSandbox("recipe-repo-9", "https://github.com/o/repo/pull/9", map[string]string{
			"sandbox.gemini.google.com/recipe-summarize-run": `{"task":"s-1","session":"s-0","startedAt":"2026-10-03T10:00:00Z","recipe":"summarize","kind":"Summary","state":"Completed"}`,
		}),
		runSandbox("unrelated", "https://github.com/o/repo/issues/77", map[string]string{
			"sandbox.gemini.google.com/recipe-summarize-run": `{"task":"x","startedAt":"2026-10-03T10:00:00Z"}`,
		}),
	}
	addRunSessions(items, sandboxes, "repo", nil)

	issue := items["issue-12"].Sessions
	if len(issue) != 2 || issue[0].Recipe != "fix" || issue[1].Recipe != "plan" {
		t.Fatalf("issue sessions = %+v, want fix then plan", issue)
	}
	plan := issue[1]
	if plan.Sandbox != "fix-repo-12" || plan.Task != "plan-1" || plan.Run != "sandbox.gemini.google.com/plan-run" ||
		plan.State != "Completed" || plan.EndedAt != "2026-10-01T10:05:00Z" || !plan.Output ||
		plan.Applied["comment"] == "" || len(plan.Revises) != 1 || plan.Revises[0] != "revise" {
		t.Errorf("plan session = %+v", plan)
	}
	if issue[0].Output || issue[0].EndedAt != "" || issue[0].Status != "running" {
		t.Errorf("fix session = %+v, want running with no output", issue[0])
	}
	if plan.Status != "done" {
		t.Errorf("plan status = %q, want done: its draft was posted", plan.Status)
	}
	if got := items["pr-30"].Sessions; len(got) != 2 {
		t.Errorf("PR 30 sessions = %+v, want the issue sandbox's two", got)
	}
	if got := items["pr-9"].Sessions; len(got) != 1 || got[0].Task != "s-0" {
		t.Errorf("PR 9 sessions = %+v, want summarize's, in the session it revised in", got)
	}
}

// A run's status is the same rule for every recipe: running, failed, or
// ended, and ready only while its draft waits for the member.
func TestRunStatus(t *testing.T) {
	for want, s := range map[string]models.RunSession{
		"running":     {State: "Running", Output: true},
		"failed":      {State: "Failed"},
		"done":        {State: "Completed"},
		"ready":       {State: "Completed", Output: true, Applied: map[string]string{"edit": "t"}},
		"done (post)": {State: "Completed", Output: true, Applied: map[string]string{"edit": "t", "comment": "t"}},
	} {
		if got := runStatus(s); got != strings.Fields(want)[0] {
			t.Errorf("%s: %+v → %q", want, s, got)
		}
	}
}
