package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	boardv1alpha1 "github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/api/repoboard/v1alpha1"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/models"
)

// boardWithRecipes is the board once its controller has published the
// catalog: the built-ins and summarize, on issues and PRs.
func boardWithRecipes() map[string]interface{} {
	board := bareBoardCR()
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
	_, r, _ = boardTestServer(t, nil, bareBoardCR())
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
			"sandbox.gemini.google.com/last-task-engine":     "claude",
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
	// A run's engine is its sandbox's; one never stamped ran gemini.
	if got := items["pr-9"].Sessions[0].Engine; got != "claude" {
		t.Errorf("PR 9 session engine = %q, want claude", got)
	}
	if plan.Engine != "gemini" {
		t.Errorf("plan session engine = %q, want gemini (unstamped sandbox)", plan.Engine)
	}
}

// An issue folded into the PR that fixes it shows its runs on the PR row,
// once, whether its sandbox still names the issue or already the PR.
func TestRunSessionsOfFoldedIssue(t *testing.T) {
	items := map[string]*models.WorkItem{"pr-30": {Fixes: []int{12, 13}}}
	sandboxes := []*unstructured.Unstructured{
		runSandbox("fix-repo-12", "https://github.com/o/repo/issues/12", map[string]string{
			"sandbox.gemini.google.com/recipe-summarize-run": `{"task":"s-1","startedAt":"2026-10-03T10:00:00Z","recipe":"summarize","kind":"Summary","state":"Completed"}`,
		}),
		runSandbox("fix-repo-13", "https://github.com/o/repo/pull/30", map[string]string{
			"sandbox.gemini.google.com/plan-run": `{"task":"p-1","startedAt":"2026-10-01T10:00:00Z","recipe":"plan","kind":"Plan","state":"Completed"}`,
		}),
	}
	addRunSessions(items, sandboxes, "repo", nil)
	got := items["pr-30"].Sessions
	if len(got) != 2 || got[0].Recipe != "summarize" || got[1].Recipe != "plan" {
		t.Errorf("PR 30 sessions = %+v, want summarize then plan, once each", got)
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

// A PR the member authored offers no Review: GitHub takes no verdict from
// a PR's author. Care runs on one whose head is on their fork alone.
func TestOwnPROffersCareNotReview(t *testing.T) {
	catalog, err := boardRecipes(boardCR())
	if err != nil {
		t.Fatal(err)
	}
	items := map[string]*models.WorkItem{
		"pr-1": {Type: "pr", Mine: true, MyPR: true},
		"pr-2": {Type: "pr"},
		"pr-3": {Type: "pr", Mine: true},
	}
	applyRowRules(items, catalog, nil, false, time.Now())
	offers := func(key, name string) bool {
		return slices.ContainsFunc(items[key].Recipes, func(rec models.RowRecipe) bool { return rec.Name == name })
	}
	for key, want := range map[string][2]bool{"pr-1": {false, true}, "pr-2": {true, false}, "pr-3": {false, false}} {
		if got := [2]bool{offers(key, "review"), offers(key, "care")}; got != want {
			t.Errorf("%s offers review, care = %v, want %v", key, got, want)
		}
	}
}

// The rows offer what the catalog says, built-ins included: fix on issues,
// review on PRs, care on a PR of the member's from their fork only, and
// nothing before the controller has published.
func TestRowRecipesFollowTheCatalog(t *testing.T) {
	catalog, err := boardRecipes(boardCR())
	if err != nil {
		t.Fatal(err)
	}
	names := func(item string, catalog []boardv1alpha1.BoardRecipe, myPR bool) (out []string) {
		for _, rec := range rowRecipes(catalog, item, myPR) {
			out = append(out, rec.Name)
		}
		return out
	}
	if got := strings.Join(names("issue", catalog, false), ","); got != "fix,plan,summarize,triage" {
		t.Errorf("issue row = %s", got)
	}
	if got := strings.Join(names("pr", catalog, false), ","); got != "review" {
		t.Errorf("PR row = %s", got)
	}
	if got := strings.Join(names("pr", catalog, true), ","); got != "care,review" {
		t.Errorf("my PR row = %s", got)
	}
	if got := names("issue", nil, false); len(got) != 0 {
		t.Errorf("no catalog: %v, want no buttons", got)
	}
}

// Auto on a PR of the member's files their watch Request; off deletes it.
func TestAutoTogglesTheWatchRequest(t *testing.T) {
	_, r, dyn := boardTestServer(t, nil, boardCR())
	post := func(body string) int {
		w := httptest.NewRecorder()
		req, _ := http.NewRequest("POST", "/board/myboard/prs/9/auto", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		r.ServeHTTP(w, req)
		return w.Code
	}
	if code := post(`{"on":true}`); code != http.StatusOK {
		t.Fatalf("on: %d", code)
	}
	if code := post(`{"on":true}`); code != http.StatusOK {
		t.Fatalf("on again: %d", code)
	}
	spec := theRequest(t, dyn, "alice").Spec
	if spec.Verb != boardv1alpha1.VerbWatch || spec.Item != "pr" || spec.Number != 9 || spec.Member != "alice" {
		t.Errorf("filed %+v, want alice's watch of PR 9", spec)
	}
	if code := post(`{"on":false}`); code != http.StatusOK {
		t.Fatalf("off: %d", code)
	}
	if reqs := filedRequests(t, dyn, "alice"); len(reqs) != 0 {
		t.Errorf("after off: %+v, want no watch", reqs)
	}
	if code := post(`{}`); code != http.StatusBadRequest {
		t.Errorf("no on: %d, want 400", code)
	}
}

// Every PR row of the member's has an auto, on while their watch stands.
func TestMarkAutos(t *testing.T) {
	items := map[string]*models.WorkItem{
		"pr-1": {Type: "pr", MyPR: true},
		"pr-2": {Type: "pr", MyPR: true},
		"pr-3": {Type: "pr"},
	}
	watch := func(n int, member string) boardv1alpha1.Request {
		req := boardv1alpha1.Request{Spec: boardv1alpha1.RequestSpec{Verb: boardv1alpha1.VerbWatch, Item: "pr", Number: n, Member: member}}
		req.Status.Phase = boardv1alpha1.RequestRunning
		req.Status.Message = "no token"
		return req
	}
	markAutos(items, []boardv1alpha1.Request{watch(1, "alice"), watch(2, "bob")}, "alice")
	if a := items["pr-1"].Auto; a == nil || !a.On || a.Message != "no token" {
		t.Errorf("pr-1 auto = %+v, want on, with why its watch failed", a)
	}
	if a := items["pr-2"].Auto; a == nil || a.On {
		t.Errorf("pr-2 auto = %+v, want off: the watch is bob's", a)
	}
	if items["pr-3"].Auto != nil {
		t.Errorf("pr-3 auto = %+v, want none: not alice's", items["pr-3"].Auto)
	}
}
