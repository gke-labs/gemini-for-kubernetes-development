package commands

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/spool"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/taskoutput"
)

func writeTaskFiles(t *testing.T, dir string, task spool.Task, files map[string]string) {
	t.Helper()
	b, _ := json.Marshal(task)
	files[spool.TaskFile] = string(b)
	for name, data := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(data), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestWriteTaskOutput(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "recipe-triage-20261003-101010-abcd")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeTaskFiles(t, dir, spool.Task{ID: filepath.Base(dir), Recipe: "triage", Output: &taskoutput.Decl{Kind: "Triage", From: "triage-output.yaml"}},
		map[string]string{"triage-output.yaml": "triage:\n  labels: [bug]\n  assessment: x\n"})
	if err := writeTaskOutput(dir, dir, map[string]string{"issue_url": "https://github.com/o/r/issues/1"}, "gemini"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, taskoutput.File))
	if err != nil {
		t.Fatal(err)
	}
	docs, err := taskoutput.Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	d := docs[0]
	if d.Target.URL != "https://github.com/o/r/issues/1" || d.Source.Task != filepath.Base(dir) || d.Source.Recipe != "triage" || d.Source.Engine != "gemini" {
		t.Errorf("document = %+v", d)
	}
}

// A revise's result names the conversation it came from beside itself.
func TestWriteTaskOutputOfARevise(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "recipe-plan-20261005-101010-abcd")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	actions := []taskoutput.Action{{Verb: "comment"}, {Verb: "revise", Revise: "plan", Label: "Update plan"}}
	writeTaskFiles(t, dir, spool.Task{ID: filepath.Base(dir), Recipe: "plan", Revise: "plan", Session: "recipe-plan-20261004-101010-0001",
		Output: &taskoutput.Decl{Kind: "Plan", From: "plan-output.md", Actions: actions}},
		map[string]string{"plan-output.md": "## Summary\nDo it.\n"})
	if err := writeTaskOutput(dir, dir, map[string]string{"issue_url": "https://github.com/o/r/issues/1"}, "gemini"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, taskoutput.File))
	if err != nil {
		t.Fatal(err)
	}
	docs, err := taskoutput.Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	d := docs[0]
	if d.Source.Task != filepath.Base(dir) || d.Source.Session != "recipe-plan-20261004-101010-0001" {
		t.Errorf("source = %+v", d.Source)
	}
	if _, err := d.Offer("revise", "plan"); err != nil {
		t.Error(err)
	}
}

// A result apply could not act on fails the task.
func TestWriteTaskOutputFailsOnABadResult(t *testing.T) {
	dir := t.TempDir()
	writeTaskFiles(t, dir, spool.Task{ID: "x", Output: &taskoutput.Decl{Kind: "Triage", From: "triage-output.yaml"}},
		map[string]string{"triage-output.yaml": "I could not decide."})
	if err := writeTaskOutput(dir, dir, nil, "gemini"); err == nil {
		t.Error("a result that is not a triage was accepted")
	}
}

// No declaration, nothing to write: a recipe without task-output.
func TestWriteTaskOutputWithoutDeclaration(t *testing.T) {
	dir := t.TempDir()
	writeTaskFiles(t, dir, spool.Task{ID: "x"}, map[string]string{})
	if err := writeTaskOutput(dir, dir, nil, "gemini"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, taskoutput.File)); !os.IsNotExist(err) {
		t.Error("a task output was written without a declaration")
	}
}

// A kind newer than the sandbox's runner does not fail the task: the
// client, which knows it, wraps the result when it reads it.
func TestWriteTaskOutputLeavesAnUnknownKind(t *testing.T) {
	dir := t.TempDir()
	writeTaskFiles(t, dir, spool.Task{ID: "x", Output: &taskoutput.Decl{Kind: "Later", From: "later-output.md"}},
		map[string]string{"later-output.md": "something"})
	if err := writeTaskOutput(dir, dir, nil, "gemini"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, taskoutput.File)); !os.IsNotExist(err) {
		t.Error("a task output was written for an unknown kind")
	}
}

// The runner copies the actions and the preview the task declared into
// the document.
func TestWriteTaskOutputCopiesActions(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "recipe-plan-20261003-101010-abcd")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	actions := []taskoutput.Action{{Verb: "comment"}, {Verb: "run", Run: "fix", Label: "Fix"}}
	writeTaskFiles(t, dir, spool.Task{ID: filepath.Base(dir), Recipe: "plan", Output: &taskoutput.Decl{Kind: "Plan", From: "plan-output.md", Actions: actions, Preview: "spec.markdown"}},
		map[string]string{"plan-output.md": "## Summary\nDo it.\n"})
	if err := writeTaskOutput(dir, dir, map[string]string{"issue_url": "https://github.com/o/r/issues/1"}, "gemini"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, taskoutput.File))
	if err != nil {
		t.Fatal(err)
	}
	docs, err := taskoutput.Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	if got := docs[0].Actions; len(got) != 2 || got[1].Run != "fix" || got[1].Label != "Fix" {
		t.Errorf("actions = %+v", got)
	}
	if docs[0].Preview != "spec.markdown" {
		t.Errorf("preview = %q", docs[0].Preview)
	}
}

// A document from a runner older than actions gets the ones its task
// declared; one that has actions, or a declaration without any, is left
// as it is.
func TestWithDeclaredActions(t *testing.T) {
	doc, err := taskoutput.Wrap("Plan", "## Summary\nDo it.", taskoutput.Target{URL: "https://github.com/o/r/issues/1"}, taskoutput.Source{Task: "t"})
	if err != nil {
		t.Fatal(err)
	}
	bare, _ := taskoutput.Marshal(doc)
	decl := &taskoutput.Decl{Kind: "Plan", From: "plan-output.md", Actions: []taskoutput.Action{{Verb: "comment"}}}

	out, err := withDeclaredActions(bare, decl)
	if err != nil {
		t.Fatal(err)
	}
	docs, err := taskoutput.Parse(out)
	if err != nil || len(docs[0].Actions) != 1 || docs[0].Actions[0].Verb != "comment" {
		t.Errorf("backfilled = %s (%v)", out, err)
	}
	if out, _ := withDeclaredActions(bare, &taskoutput.Decl{Kind: "Plan"}); string(out) != string(bare) {
		t.Errorf("no declared actions changed the document:\n%s", out)
	}
	doc.Actions = []taskoutput.Action{{Verb: "reject"}}
	own, _ := taskoutput.Marshal(doc)
	if out, _ := withDeclaredActions(own, decl); string(out) != string(own) {
		t.Errorf("a document's own actions were replaced:\n%s", out)
	}
}

// A PR's result is pinned to the head the clone step checked out.
func TestWriteTaskOutputPinsThePRHead(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git required")
	}
	repo := t.TempDir()
	git := func(args ...string) string {
		out, err := exec.Command("git", append([]string{"-C", repo, "-c", "user.email=x@x", "-c", "user.name=x"}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	git("init", "-q")
	git("commit", "-q", "--allow-empty", "-m", "head")
	head := git("rev-parse", "HEAD")
	git("update-ref", "refs/factory/pr/head", head)
	git("commit", "-q", "--allow-empty", "-m", "moved by the agent")

	dir := filepath.Join(t.TempDir(), "recipe-review-20261005-101010-abcd")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeTaskFiles(t, dir, spool.Task{ID: filepath.Base(dir), Recipe: "review", Output: &taskoutput.Decl{Kind: "Review", From: "review.yaml"}},
		map[string]string{"review.yaml": "review:\n  body: Fine.\n"})
	if err := writeTaskOutput(dir, repo, map[string]string{"pr_url": "https://github.com/o/r/pull/2"}, "gemini"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, taskoutput.File))
	if err != nil {
		t.Fatal(err)
	}
	docs, err := taskoutput.Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	if got := docs[0].Target; got.URL != "https://github.com/o/r/pull/2" || got.Commit != head {
		t.Errorf("target = %+v, want commit %s", got, head)
	}
	if prHeadCommit(dir) != "" {
		t.Error("a directory without the ref has a PR head")
	}
}
