package commands

import (
	"encoding/json"
	"os"
	"path/filepath"
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
	if err := writeTaskOutput(dir, map[string]string{"issue_url": "https://github.com/o/r/issues/1"}, "gemini"); err != nil {
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

// A result apply could not act on fails the task.
func TestWriteTaskOutputFailsOnABadResult(t *testing.T) {
	dir := t.TempDir()
	writeTaskFiles(t, dir, spool.Task{ID: "x", Output: &taskoutput.Decl{Kind: "Triage", From: "triage-output.yaml"}},
		map[string]string{"triage-output.yaml": "I could not decide."})
	if err := writeTaskOutput(dir, nil, "gemini"); err == nil {
		t.Error("a result that is not a triage was accepted")
	}
}

// No declaration, nothing to write: a recipe without task-output.
func TestWriteTaskOutputWithoutDeclaration(t *testing.T) {
	dir := t.TempDir()
	writeTaskFiles(t, dir, spool.Task{ID: "x"}, map[string]string{})
	if err := writeTaskOutput(dir, nil, "gemini"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, taskoutput.File)); !os.IsNotExist(err) {
		t.Error("a task output was written without a declaration")
	}
}
