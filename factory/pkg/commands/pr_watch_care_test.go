package commands

import (
	"errors"
	"os"
	"testing"

	factorysandbox "github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/sandbox"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/spool"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/taskoutput"
)

// care's replies and reports are not comments for care to address.
func TestFactoryPosted(t *testing.T) {
	if !factoryPosted("Done.\n\n<!-- factory:task-output kind=Change task=fix-2 reply=1 -->") || factoryPosted("please fix") {
		t.Error("factoryPosted")
	}
}

// The watch revises care's run where the PR's sandbox records one on
// the PR.
func TestCareRunOn(t *testing.T) {
	pr := "https://github.com/o/r/pull/12"
	run := factorysandbox.RunAnnotation(careTaskType)
	if !careRunOn(map[string]string{"htmlURL": "https://github.com/O/r/pull/12/", run: `{"task":"c-1"}`}, pr) {
		t.Error("care's run on the PR not found")
	}
	if careRunOn(map[string]string{"htmlURL": pr}, pr) || careRunOn(map[string]string{"htmlURL": "https://github.com/o/r/pull/13", run: "{}"}, pr) {
		t.Error("found a care run where there is none on the PR")
	}
}

// A Change revise gets the push its session last made, and that task's
// title and body, skipping tasks that pushed nothing and other sessions.
func TestChangeInputs(t *testing.T) {
	files := map[string]string{
		"fix-1/" + taskoutput.PushedFile: `{"fork":"me/r","branch":"issue-7-1","base":"b0","head":"h1"}`,
		"fix-1/" + taskoutput.File:       "apiVersion: " + taskoutput.APIVersion + "\nkind: Change\ntarget: {url: https://github.com/o/r/issues/7}\nspec: {title: t1, body: b1}\n",
		"fix-2/" + taskoutput.PushedFile: `{"fork":"me/r","branch":"issue-7-1","base":"b0","head":"h2"}`,
		"other/" + taskoutput.PushedFile: `{"fork":"me/r","branch":"x","base":"b0","head":"hx"}`,
	}
	read := func(id, name string) ([]byte, error) {
		if v, ok := files[id+"/"+name]; ok {
			return []byte(v), nil
		}
		return nil, os.ErrNotExist
	}
	entries := []spool.Entry{
		{Task: spool.Task{ID: "other", Recipe: "fix"}},
		{Task: spool.Task{ID: "fix-3", Recipe: "fix", Session: "fix-1"}}, // failed before its push
		{Task: spool.Task{ID: "fix-2", Recipe: "fix", Session: "fix-1"}},
		{Task: spool.Task{ID: "fix-1", Recipe: "fix"}},
	}
	inputs := map[string]string{}
	if err := changeInputs(inputs, entries, "fix-1", "fix", read); err != nil {
		t.Fatal(err)
	}
	if inputs["pushed_branch"] != "issue-7-1" || inputs["pushed_head"] != "h2" || inputs["pushed_fork"] != "me/r" || inputs["pushed_base"] != "b0" {
		t.Errorf("inputs = %v", inputs)
	}
	// fix-2 left no task output: no title to keep.
	if inputs["pushed_title"] != "" {
		t.Errorf("title = %q", inputs["pushed_title"])
	}
	delete(files, "fix-2/"+taskoutput.PushedFile)
	if err := changeInputs(inputs, entries, "fix-1", "fix", read); err != nil {
		t.Fatal(err)
	}
	if inputs["pushed_head"] != "h1" || inputs["pushed_title"] != "t1" || inputs["pushed_body"] != "b1" {
		t.Errorf("inputs = %v", inputs)
	}
	if err := changeInputs(map[string]string{}, entries, "fix-9", "fix", read); err == nil {
		t.Error("a session that pushed nothing was revised")
	}
	if err := changeInputs(map[string]string{}, entries[:1], "other", "fix", func(string, string) ([]byte, error) { return []byte(`{"branch":""}`), nil }); err == nil || errors.Is(err, os.ErrNotExist) {
		t.Errorf("a push with no branch: %v", err)
	}

	it := githubItem{Owner: "o", Repo: "r", Number: 7}
	for want, a := range map[string]map[string]string{
		"https://github.com/o/r/pull/12": {"htmlURL": "https://github.com/o/r/pull/12"},
		"https://github.com/o/r/pull/13": {"htmlURL": "https://github.com/o/r/issues/7", "pr": "13"},
		"":                               {"htmlURL": "https://github.com/o/r/issues/7"},
	} {
		if got := prURLOf(a, it); got != want {
			t.Errorf("prURLOf(%v) = %q, want %q", a, got, want)
		}
	}
}
