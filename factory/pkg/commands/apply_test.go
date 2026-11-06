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
	input, text, err := followUpInput(context.Background(), d, "fix", nil)
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
	if _, _, err := followUpInput(context.Background(), d, "fix", nil); err == nil {
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

// A Change's post-replies on its PR aliases no sandbox: several may work
// on the PR (a care run's and an old one), and none needs it.
func TestApplyDocumentPostRepliesAliasesNothing(t *testing.T) {
	oldFind, oldAlias := findSandbox, aliasSandbox
	t.Cleanup(func() { findSandbox, aliasSandbox = oldFind, oldAlias })
	findSandbox = func(_ context.Context, u string) (string, error) {
		return "", fmt.Errorf("several sandboxes work on %s", u)
	}
	aliasSandbox = func(_ context.Context, name string, _ int, _ string) error {
		t.Errorf("aliased %s", name)
		return nil
	}
	d, err := taskoutput.Wrap("Change", "change:\n  title: t\n  body: b\n", taskoutput.Target{URL: "https://github.com/o/r/pull/9"}, taskoutput.Source{Task: "t"})
	if err != nil {
		t.Fatal(err)
	}
	if err := applyDocument(context.Background(), githubv39.NewClient(nil), d, "post-replies", "", false); err != nil {
		t.Errorf("post-replies: %v", err)
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
	if c.Draft != nil {
		t.Errorf("no draft input, but draft = %v", *c.Draft)
	}
	// The draft input says whether open-pr opens a draft (the overseer's:
	// false); anything else than true or false is refused.
	if err := fillChange(d, spool.Task{}, dir, map[string]string{"draft": "false"}); err != nil {
		t.Fatal(err)
	}
	if c, err = d.ChangeSpec(); err != nil || c.Draft == nil || *c.Draft {
		t.Errorf("draft false: %+v, %v", c, err)
	}
	if err := fillChange(d, spool.Task{}, dir, map[string]string{"draft": "maybe"}); err == nil {
		t.Error("draft maybe was taken")
	}

	// A Change on a PR — such as care's — is about the PR, and keeps its title and body where the agent wrote
	// none.
	rev, err := taskoutput.Wrap("Change", "change:\n  report: fixed the lint\n", taskoutput.Target{URL: "https://github.com/o/r/issues/7"}, taskoutput.Source{})
	if err != nil {
		t.Fatal(err)
	}
	if err := fillChange(rev, spool.Task{}, dir, nil); err == nil {
		t.Error("a start's Change with no title was filled")
	}
	inputs := map[string]string{"pr_url": "https://github.com/o/r/pull/9", "pushed_title": "t0", "pushed_body": "b0"}
	if err := fillChange(rev, spool.Task{Recipe: "care-ci"}, dir, inputs); err != nil {
		t.Fatal(err)
	}
	if c, err = rev.ChangeSpec(); err != nil {
		t.Fatal(err)
	}
	if c.Title != "t0" || c.Body != "b0" || c.Report != "fixed the lint" || rev.Target.URL != "https://github.com/o/r/pull/9" || rev.Target.Commit != "h1" {
		t.Errorf("care's filled %+v, target %+v", c, rev.Target)
	}
	retitled, _ := taskoutput.Wrap("Change", "change:\n  title: t1\n", taskoutput.Target{}, taskoutput.Source{})
	if err := fillChange(retitled, spool.Task{Recipe: "care-iterate"}, dir, inputs); err != nil {
		t.Fatal(err)
	}
	if c, _ = retitled.ChangeSpec(); c.Title != "t1" || c.Body != "b0" || len(c.Feedback) != 0 {
		t.Errorf("retitled %+v", c)
	}
	started, _ := taskoutput.Wrap("Change", "change:\n  report: rebased\n", taskoutput.Target{}, taskoutput.Source{})
	if err := fillChange(started, spool.Task{}, dir, inputs); err != nil {
		t.Fatal(err)
	}
	if c, _ = started.ChangeSpec(); c.Title != "t0" || c.Body != "b0" || started.Target.URL != "https://github.com/o/r/pull/9" {
		t.Errorf("care start filled %+v, target %+v", c, started.Target)
	}

	// An address-comments task names the feedback it was handed.
	if err := os.WriteFile(filepath.Join(dir, taskoutput.FeedbackFile), []byte(`[{"kind":"review-comment","id":4,"author":"rev","body":"x"},{"kind":"review","id":5,"nodeId":"PRR_5","body":"y"}]`), 0o644); err != nil {
		t.Fatal(err)
	}
	addressed, _ := taskoutput.Wrap("Change", "change:\n  replies:\n    - inReplyTo: 4\n      body: done\n", taskoutput.Target{}, taskoutput.Source{})
	if err := fillChange(addressed, spool.Task{Recipe: "care-comments"}, dir, inputs); err != nil {
		t.Fatal(err)
	}
	if c, _ = addressed.ChangeSpec(); fmt.Sprint(c.Feedback) != "[{review-comment 4 } {review 5 PRR_5}]" {
		t.Errorf("feedback %v", c.Feedback)
	}
}

// The link to a run's conversation: the session it continued, else its
// own task, in the sandbox the apply names, else the document's.
func TestSessionLink(t *testing.T) {
	const tmpl = "https://board.example/#/overseers/kcc/task-session/{sandbox}/{task}"
	doc := func(sandbox, task, session string) *taskoutput.Document {
		return &taskoutput.Document{Source: taskoutput.Source{Sandbox: sandbox, Task: task, Session: session}}
	}
	disclose := rootFlags.Disclose
	t.Cleanup(func() { rootFlags.Disclose = disclose })
	rootFlags.Disclose = true
	for name, tc := range map[string]struct {
		tmpl    string
		doc     *taskoutput.Document
		sandbox string
		want    string
	}{
		"its own task":             {tmpl, doc("", "recipe-fix-1", ""), "fix-r-7", "https://board.example/#/overseers/kcc/task-session/fix-r-7/recipe-fix-1"},
		"the session it continued": {tmpl, doc("", "care-2", "care-1"), "fix-r-7", "https://board.example/#/overseers/kcc/task-session/fix-r-7/care-1"},
		"the document's sandbox":   {tmpl, doc("fix-r-8", "recipe-fix-1", ""), "", "https://board.example/#/overseers/kcc/task-session/fix-r-8/recipe-fix-1"},
		"no template":              {"", doc("", "recipe-fix-1", ""), "fix-r-7", ""},
		"no sandbox":               {tmpl, doc("", "recipe-fix-1", ""), "", ""},
		"no task":                  {tmpl, doc("", "", ""), "fix-r-7", ""},
	} {
		if got := sessionLink(tc.tmpl, tc.doc, tc.sandbox); got != tc.want {
			t.Errorf("%s: %q, want %q", name, got, tc.want)
		}
	}
	rootFlags.Disclose = false
	if got := sessionLink(tmpl, doc("", "recipe-fix-1", ""), "fix-r-7"); got != "" {
		t.Errorf("--disclose=false: %q, want no link", got)
	}
}
