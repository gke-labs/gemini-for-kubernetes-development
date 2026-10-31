package commands

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

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

// A result of a run in a session names the conversation it continued
// beside itself.
func TestWriteTaskOutputInASession(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "recipe-care-ci-20261005-101010-abcd")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeTaskFiles(t, dir, spool.Task{ID: filepath.Base(dir), Recipe: "summarize", Session: "recipe-care-20261004-101010-0001",
		Output: &taskoutput.Decl{Kind: "Summary", From: "summary-output.md", Actions: []taskoutput.Action{{Verb: "comment"}}}},
		map[string]string{"summary-output.md": "It is about x.\n"})
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
	if d.Source.Task != filepath.Base(dir) || d.Source.Session != "recipe-care-20261004-101010-0001" {
		t.Errorf("source = %+v", d.Source)
	}
	if _, err := d.Offer("comment", ""); err != nil {
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
func TestCompleteTaskOutputActions(t *testing.T) {
	doc, err := taskoutput.Wrap("Plan", "## Summary\nDo it.", taskoutput.Target{URL: "https://github.com/o/r/issues/1"}, taskoutput.Source{Sandbox: "sb", Task: "t"})
	if err != nil {
		t.Fatal(err)
	}
	bare, _ := taskoutput.Marshal(doc)
	decl := &taskoutput.Decl{Kind: "Plan", From: "plan-output.md", Actions: []taskoutput.Action{{Verb: "comment"}}}

	out, err := completeTaskOutput(bare, decl, "sb")
	if err != nil {
		t.Fatal(err)
	}
	docs, err := taskoutput.Parse(out)
	if err != nil || len(docs[0].Actions) != 1 || docs[0].Actions[0].Verb != "comment" {
		t.Errorf("backfilled = %s (%v)", out, err)
	}
	if out, _ := completeTaskOutput(bare, &taskoutput.Decl{Kind: "Plan"}, "sb"); string(out) != string(bare) {
		t.Errorf("no declared actions changed the document:\n%s", out)
	}
	doc.Actions = []taskoutput.Action{{Verb: "reject"}}
	own, _ := taskoutput.Marshal(doc)
	if out, _ := completeTaskOutput(own, decl, "sb"); string(out) != string(own) {
		t.Errorf("a document's own actions were replaced:\n%s", out)
	}
}

// A document its runner wrote without the sandbox - a retro's Change on its
// repository - gets the sandbox it was read from, so apply aliases that
// one to the PR it opens rather than looking it up by the repository's
// URL, which every sandbox working on the repository matches
// (GoogleCloudPlatform/k8s-config-connector#14722, 2026-10-30). A sandbox
// it names already is kept.
func TestCompleteTaskOutputSandbox(t *testing.T) {
	change := "change:\n  fork: bot/r\n  branch: retro-1\n  base: abc\n  title: Retro\n  body: Lessons.\n"
	doc, err := taskoutput.Wrap("Change", change, taskoutput.Target{URL: "https://github.com/o/r"}, taskoutput.Source{Task: "recipe-retro-1", Recipe: "retro"})
	if err != nil {
		t.Fatal(err)
	}
	bare, _ := taskoutput.Marshal(doc)
	out, err := completeTaskOutput(bare, nil, "retro-r-1")
	if err != nil {
		t.Fatal(err)
	}
	docs, err := taskoutput.Parse(out)
	if err != nil || len(docs) != 1 || docs[0].Source.Sandbox != "retro-r-1" || docs[0].Source.Task != "recipe-retro-1" {
		t.Errorf("source = %+v (%v):\n%s", docs[0].Source, err, out)
	}

	doc.Source.Sandbox = "other"
	named, _ := taskoutput.Marshal(doc)
	if out, _ := completeTaskOutput(named, nil, "retro-r-1"); string(out) != string(named) {
		t.Errorf("the sandbox the document names was replaced:\n%s", out)
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

// An optional result may be missing (an agent chore that changed
// nothing); a required one may not.
func TestWriteTaskOutputOptional(t *testing.T) {
	for _, optional := range []bool{true, false} {
		dir := t.TempDir()
		writeTaskFiles(t, dir, spool.Task{ID: "x", Output: &taskoutput.Decl{Kind: "Change", From: "change.yaml", Optional: optional}}, map[string]string{})
		err := writeTaskOutput(dir, dir, nil, "gemini")
		if optional != (err == nil) {
			t.Errorf("optional %v: %v", optional, err)
		}
		if _, err := os.Stat(filepath.Join(dir, taskoutput.File)); !os.IsNotExist(err) {
			t.Errorf("optional %v: a task output was written with no result", optional)
		}
	}
}

// A result the agent rewrites after the task, in a later turn of the
// conversation, is wrapped again when it is read (taskoutput.Refresh),
// about the same target and from the same task; one that no longer
// parses leaves the last good one.
func TestTaskOutputFollowsARewrittenResult(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "recipe-plan-20261013-101010-abcd")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeTaskFiles(t, dir, spool.Task{ID: filepath.Base(dir), Recipe: "plan", Output: &taskoutput.Decl{Kind: "Plan", From: "plan-output.md", Actions: []taskoutput.Action{{Verb: "comment"}}}},
		map[string]string{"plan-output.md": "## Summary\nFirst.\n"})
	if err := writeTaskOutput(dir, dir, map[string]string{"issue_url": "https://github.com/o/r/issues/1"}, "gemini"); err != nil {
		t.Fatal(err)
	}
	rewrite := func(content string) {
		t.Helper()
		path := filepath.Join(dir, "plan-output.md")
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		later := time.Now().Add(time.Minute)
		if err := os.Chtimes(path, later, later); err != nil {
			t.Fatal(err)
		}
	}
	rewrite("## Summary\nSecond.\n")
	if changed, err := taskoutput.Refresh(dir, "plan-output.md"); err != nil || !changed {
		t.Fatalf("Refresh = %v, %v; want rewrapped", changed, err)
	}
	if changed, _ := taskoutput.Refresh(dir, "plan-output.md"); changed {
		t.Error("Refresh rewrapped a result it had already wrapped")
	}
	data, _ := os.ReadFile(filepath.Join(dir, taskoutput.File))
	docs, err := taskoutput.Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	d := docs[0]
	if !strings.Contains(string(data), "Second.") || d.Target.URL != "https://github.com/o/r/issues/1" || d.Source.Task != filepath.Base(dir) || len(d.Actions) != 1 {
		t.Errorf("rewrapped = %s", data)
	}

	writeTaskFiles(t, dir, spool.Task{ID: filepath.Base(dir), Recipe: "triage", Output: &taskoutput.Decl{Kind: "Triage", From: "triage-output.yaml"}},
		map[string]string{"triage-output.yaml": "triage:\n  assessment: x\n"})
	if err := writeTaskOutput(dir, dir, nil, "gemini"); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "triage-output.yaml")
	later := time.Now().Add(2 * time.Minute)
	_ = os.WriteFile(path, []byte("not a triage"), 0o644)
	_ = os.Chtimes(path, later, later)
	if _, err := taskoutput.Refresh(dir, "triage-output.yaml"); err == nil {
		t.Error("a rewritten result that does not parse was accepted")
	}
	if data, _ := os.ReadFile(filepath.Join(dir, taskoutput.File)); !strings.Contains(string(data), "assessment: x") {
		t.Errorf("the last good result was not kept: %s", data)
	}
}

// An optional result the task did not leave is wrapped once the agent
// writes it: research's notes, written when someone asks for them.
func TestTaskOutputOfAnOptionalResultWrittenLater(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "recipe-research-20261013-101010-abcd")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeTaskFiles(t, dir, spool.Task{ID: filepath.Base(dir), Recipe: "research", URL: "https://github.com/o/r", Output: &taskoutput.Decl{Kind: "Notes", From: "notes.md", Optional: true}}, map[string]string{})
	if err := writeTaskOutput(dir, dir, nil, "gemini"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, taskoutput.File)); !os.IsNotExist(err) {
		t.Fatal("a task output was written with no notes")
	}
	if err := os.WriteFile(filepath.Join(dir, "notes.md"), []byte("# Notes\nFound it.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if changed, err := taskoutput.Refresh(dir, "notes.md"); err != nil || !changed {
		t.Fatalf("Refresh = %v, %v; want the notes wrapped", changed, err)
	}
	data, _ := os.ReadFile(filepath.Join(dir, taskoutput.File))
	if docs, err := taskoutput.Parse(data); err != nil || docs[0].Kind != "Notes" || docs[0].Target.URL != "https://github.com/o/r" {
		t.Errorf("notes = %s (%v)", data, err)
	}
}
