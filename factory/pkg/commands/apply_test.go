package commands

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"

	githubv39 "github.com/google/go-github/v39/github"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/spool"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/taskoutput"
)

// run:fix gives the plan, as edited, to fix as its input from a Plan.
func TestRunFixFollowUpPassesThePlan(t *testing.T) {
	d, err := taskoutput.Wrap("Plan", "## Summary\nDo it.\n", taskoutput.Target{URL: "https://github.com/o/r/issues/7"}, taskoutput.Source{Task: "t"})
	if err != nil {
		t.Fatal(err)
	}
	input, text, err := followUpInput(d, "fix")
	if err != nil || input != "plan" || text != "## Summary\nDo it.\n" {
		t.Errorf("followUpInput = %q %q %v, want fix's plan input with the plan", input, text, err)
	}
}

// A run of a recipe that takes no input from the result's kind starts
// nothing.
func TestRunFollowUpNeedsAnInputFromTheKind(t *testing.T) {
	d, err := taskoutput.Wrap("Summary", "Short.\n", taskoutput.Target{URL: "https://github.com/o/r/issues/7"}, taskoutput.Source{Task: "t"})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := followUpInput(d, "fix"); err == nil {
		t.Error("fix took a Summary")
	}
}

// A Change's open-pr that finds the branch's PR aliases the fix's sandbox
// to it.
func TestApplyDocumentAliasesTheSandbox(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/user", func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, `{"login":"me"}`) })
	mux.HandleFunc("/repos/o/r/pulls", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `[{"number":9,"html_url":"https://github.com/o/r/pull/9"}]`)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	gh := githubv39.NewClient(nil)
	gh.BaseURL, _ = url.Parse(srv.URL + "/")

	oldFind, oldAlias := findSandbox, aliasSandbox
	t.Cleanup(func() { findSandbox, aliasSandbox = oldFind, oldAlias })
	var aliased string
	aliasSandbox = func(_ context.Context, name string, prNum int, prURL string) error {
		aliased = fmt.Sprintf("%s %d %s", name, prNum, prURL)
		return nil
	}
	findSandbox = func(_ context.Context, u string) (string, error) { return "found-for-" + u, nil }

	change := func(sandbox string) *taskoutput.Document {
		d, err := taskoutput.Wrap("Change", "change:\n  title: t\n  body: b\n", taskoutput.Target{URL: "https://github.com/o/r/issues/7"}, taskoutput.Source{Sandbox: sandbox})
		if err != nil {
			t.Fatal(err)
		}
		if err := d.SetPushed(taskoutput.Pushed{Fork: "me/r", Branch: "issue-7-1", Head: "h"}); err != nil {
			t.Fatal(err)
		}
		return d
	}

	if err := applyDocument(context.Background(), gh, change("fix-r-7"), "open-pr", "", false); err != nil {
		t.Fatal(err)
	}
	if aliased != "fix-r-7 9 https://github.com/o/r/pull/9" {
		t.Errorf("aliased %q", aliased)
	}
	aliased = ""
	if err := applyDocument(context.Background(), gh, change(""), "open-pr", "", false); err != nil {
		t.Fatal(err)
	}
	if aliased != "found-for-https://github.com/o/r/issues/7 9 https://github.com/o/r/pull/9" {
		t.Errorf("aliased %q", aliased)
	}
	aliased = ""
	if err := applyDocument(context.Background(), gh, change(""), "open-pr", "", true); err != nil || aliased != "" {
		t.Errorf("dry run: %v, aliased %q", err, aliased)
	}
}

// The runner's Change gets the push's branch and head, not the agent's,
// and the task's labels.
func TestFillChange(t *testing.T) {
	dir := t.TempDir()
	d, err := taskoutput.Wrap("Change", "change:\n  title: t\n  branch: main\n  body: b\n  labels: [x]\n", taskoutput.Target{URL: "https://github.com/o/r/issues/7"}, taskoutput.Source{})
	if err != nil {
		t.Fatal(err)
	}
	if err := fillChange(d, spool.Task{}, dir, nil); err == nil {
		t.Error("a Change with no push was filled")
	}
	if err := os.WriteFile(filepath.Join(dir, taskoutput.PushedFile), []byte(`{"fork":"me/r","branch":"issue-7-1","base":"b0","head":"h1"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := fillChange(d, spool.Task{}, dir, map[string]string{"labels": " factory, x ,"}); err != nil {
		t.Fatal(err)
	}
	c, err := d.ChangeSpec()
	if err != nil {
		t.Fatal(err)
	}
	if c.Fork != "me/r" || c.Branch != "issue-7-1" || c.Base != "b0" || d.Target.Commit != "h1" || fmt.Sprint(c.Labels) != "[x factory]" {
		t.Errorf("filled %+v, commit %s", c, d.Target.Commit)
	}

	// A revise's is about the PR, and keeps the PR's title and body where
	// the agent wrote none.
	rev, err := taskoutput.Wrap("Change", "change:\n  report: fixed the lint\n", taskoutput.Target{URL: "https://github.com/o/r/issues/7"}, taskoutput.Source{})
	if err != nil {
		t.Fatal(err)
	}
	if err := fillChange(rev, spool.Task{}, dir, nil); err == nil {
		t.Error("a start's Change with no title was filled")
	}
	inputs := map[string]string{"pr_url": "https://github.com/o/r/pull/9", "pushed_title": "t0", "pushed_body": "b0"}
	if err := fillChange(rev, spool.Task{Revise: "fix-ci"}, dir, inputs); err != nil {
		t.Fatal(err)
	}
	if c, err = rev.ChangeSpec(); err != nil {
		t.Fatal(err)
	}
	if c.Title != "t0" || c.Body != "b0" || c.Report != "fixed the lint" || rev.Target.URL != "https://github.com/o/r/pull/9" || rev.Target.Commit != "h1" {
		t.Errorf("revise filled %+v, target %+v", c, rev.Target)
	}
	retitled, _ := taskoutput.Wrap("Change", "change:\n  title: t1\n", taskoutput.Target{}, taskoutput.Source{})
	if err := fillChange(retitled, spool.Task{Revise: "iterate"}, dir, inputs); err != nil {
		t.Fatal(err)
	}
	if c, _ = retitled.ChangeSpec(); c.Title != "t1" || c.Body != "b0" {
		t.Errorf("retitled %+v", c)
	}
}
