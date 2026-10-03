package taskoutput

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	githubv39 "github.com/google/go-github/v39/github"
)

const agentReply = "Here it is:\n```yaml\ntriage:\n  labels: [bug, area/x]\n  priority: high\n  duplicates: [7]\n  assessment: It breaks.\n```"

func triageDoc(t *testing.T, task string) *Document {
	t.Helper()
	doc, err := Wrap("Triage", agentReply, Target{URL: "https://github.com/o/r/issues/12"}, Source{Task: task, Recipe: "triage"})
	if err != nil {
		t.Fatalf("Wrap: %v", err)
	}
	return doc
}

func TestWrapMarshalParse(t *testing.T) {
	data, err := Marshal(triageDoc(t, "recipe-triage-1"), triageDoc(t, "recipe-triage-2"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(data), "apiVersion: "+APIVersion+"\nkind: Triage\n") {
		t.Errorf("document starts:\n%s", data)
	}
	docs, err := Parse(data)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(docs) != 2 || docs[1].Source.Task != "recipe-triage-2" {
		t.Fatalf("parsed %d documents: %+v", len(docs), docs)
	}
	tr, err := docs[0].TriageSpec()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(tr.Labels, ",") != "bug,area/x" || tr.Priority != "high" || tr.Duplicates[0] != 7 || tr.Assessment != "It breaks." {
		t.Errorf("spec = %+v", tr)
	}
}

func TestWrapRejectsWhatCannotBeApplied(t *testing.T) {
	for _, raw := range []string{"no yaml here: [", "labels: [x]"} {
		if _, err := Wrap("Triage", raw, Target{}, Source{}); err == nil {
			t.Errorf("Wrap(%q) succeeded", raw)
		}
	}
	if _, err := Wrap("Poem", "x", Target{}, Source{}); err == nil {
		t.Error("an unknown kind was wrapped")
	}
}

func TestParseRejects(t *testing.T) {
	for name, doc := range map[string]string{
		"apiVersion": "apiVersion: v0\nkind: Triage\ntarget: {url: u}\nspec: {}\n",
		"kind":       "apiVersion: " + APIVersion + "\nkind: Poem\ntarget: {url: u}\nspec: {}\n",
		"target":     "apiVersion: " + APIVersion + "\nkind: Triage\nspec: {}\n",
		"empty":      "",
	} {
		if _, err := Parse([]byte(doc)); err == nil {
			t.Errorf("%s: Parse succeeded", name)
		}
	}
}

// fakeGitHub records what is written and serves the comments posted so far.
type fakeGitHub struct {
	labels   [][]string
	comments []string
}

func (f *fakeGitHub) client(t *testing.T) *githubv39.Client {
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/o/r/issues/12/labels", func(w http.ResponseWriter, r *http.Request) {
		var l []string
		_ = json.NewDecoder(r.Body).Decode(&l)
		f.labels = append(f.labels, l)
		fmt.Fprint(w, "[]")
	})
	mux.HandleFunc("/repos/o/r/issues/12/comments", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			var c githubv39.IssueComment
			_ = json.NewDecoder(r.Body).Decode(&c)
			f.comments = append(f.comments, c.GetBody())
			fmt.Fprint(w, "{}")
			return
		}
		var out []githubv39.IssueComment
		for i := range f.comments {
			out = append(out, githubv39.IssueComment{Body: &f.comments[i]})
		}
		_ = json.NewEncoder(w).Encode(out)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	gh := githubv39.NewClient(nil)
	gh.BaseURL, _ = url.Parse(srv.URL + "/")
	return gh
}

func TestApplyTriage(t *testing.T) {
	f := &fakeGitHub{}
	gh := f.client(t)
	doc := triageDoc(t, "recipe-triage-1")
	var out bytes.Buffer
	if err := Apply(context.Background(), gh, doc, false, &out); err != nil {
		t.Fatal(err)
	}
	if len(f.labels) != 1 || strings.Join(f.labels[0], ",") != "bug,area/x" {
		t.Errorf("labels = %v", f.labels)
	}
	if len(f.comments) != 1 || !strings.Contains(f.comments[0], "It breaks.") || !strings.Contains(f.comments[0], "Possible duplicates: #7") || !strings.Contains(f.comments[0], "task=recipe-triage-1") {
		t.Errorf("comments = %q", f.comments)
	}

	// Again: the labels are idempotent anyway, the comment is not posted twice.
	if err := Apply(context.Background(), gh, doc, false, &out); err != nil {
		t.Fatal(err)
	}
	if len(f.comments) != 1 {
		t.Errorf("applied twice, commented %d times", len(f.comments))
	}
	// Another task's triage of the same issue is a new comment.
	if err := Apply(context.Background(), gh, triageDoc(t, "recipe-triage-2"), false, &out); err != nil {
		t.Fatal(err)
	}
	if len(f.comments) != 2 {
		t.Errorf("a second task's triage: %d comments, want 2", len(f.comments))
	}
}

func TestApplyDryRunWritesNothing(t *testing.T) {
	f := &fakeGitHub{}
	var out bytes.Buffer
	if err := Apply(context.Background(), f.client(t), triageDoc(t, "recipe-triage-1"), true, &out); err != nil {
		t.Fatal(err)
	}
	if len(f.labels)+len(f.comments) != 0 {
		t.Errorf("dry run wrote labels %v comments %v", f.labels, f.comments)
	}
	if !strings.Contains(out.String(), "Would add labels [bug area/x]") || !strings.Contains(out.String(), "Would comment on") {
		t.Errorf("dry run said:\n%s", out.String())
	}
}

func TestApplyTriageNeedsAnIssue(t *testing.T) {
	doc := triageDoc(t, "x")
	doc.Target.URL = "https://github.com/o/r/pull/12"
	if err := Apply(context.Background(), (&fakeGitHub{}).client(t), doc, false, &bytes.Buffer{}); err == nil {
		t.Error("a Triage was applied to a PR")
	}
}

func planDoc(t *testing.T, task, raw string) *Document {
	t.Helper()
	doc, err := Wrap("Plan", raw, Target{URL: "https://github.com/o/r/issues/12"}, Source{Task: task, Recipe: "plan"})
	if err != nil {
		t.Fatalf("Wrap: %v", err)
	}
	return doc
}

func TestWrapPlan(t *testing.T) {
	for _, raw := range []string{
		"## Summary\nDo it.\n\n## Steps\n1. a",
		"```markdown\n## Summary\nDo it.\n\n## Steps\n1. a\n```",
		"```\n## Summary\nDo it.\n\n## Steps\n1. a\n```\n",
	} {
		data, err := Marshal(planDoc(t, "recipe-plan-1", raw))
		if err != nil {
			t.Fatal(err)
		}
		docs, err := Parse(data)
		if err != nil {
			t.Fatalf("Parse: %v", err)
		}
		p, err := docs[0].PlanSpec()
		if err != nil {
			t.Fatal(err)
		}
		if p.Markdown != "## Summary\nDo it.\n\n## Steps\n1. a" {
			t.Errorf("Wrap(%q) markdown = %q", raw, p.Markdown)
		}
	}
	if _, err := Wrap("Plan", " \n```\n```", Target{}, Source{}); err == nil {
		t.Error("an empty plan was wrapped")
	}
}

func TestApplyPlan(t *testing.T) {
	f := &fakeGitHub{}
	gh := f.client(t)
	doc := planDoc(t, "recipe-plan-1", "## Summary\nDo it.")
	var out bytes.Buffer
	if err := Apply(context.Background(), gh, doc, true, &out); err != nil {
		t.Fatal(err)
	}
	if len(f.comments) != 0 || !strings.Contains(out.String(), "Would comment on") {
		t.Errorf("dry run: comments %q, said:\n%s", f.comments, out.String())
	}
	for range 2 {
		if err := Apply(context.Background(), gh, doc, false, &out); err != nil {
			t.Fatal(err)
		}
	}
	if len(f.comments) != 1 || !strings.Contains(f.comments[0], "**Implementation plan**\n\n## Summary\nDo it.") || !strings.Contains(f.comments[0], "kind=Plan task=recipe-plan-1") {
		t.Errorf("applied twice, comments = %q", f.comments)
	}
	if len(f.labels) != 0 {
		t.Errorf("a Plan added labels %v", f.labels)
	}
	doc.Target.URL = "https://github.com/o/r/pull/12"
	if err := Apply(context.Background(), gh, doc, false, &out); err == nil {
		t.Error("a Plan was applied to a PR")
	}
}
