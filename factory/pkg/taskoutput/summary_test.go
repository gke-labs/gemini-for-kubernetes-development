package taskoutput

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

// A Summary is markdown, posted once as a comment on its issue, and
// offers what a draft does by default when its document declares nothing.
func TestSummary(t *testing.T) {
	doc, err := Wrap("Summary", "```markdown\nIt asks for X.\n```", Target{URL: "https://github.com/o/r/issues/12"}, Source{Task: "recipe-summarize-1", Recipe: "summarize"})
	if err != nil {
		t.Fatal(err)
	}
	if s, err := doc.SummarySpec(); err != nil || s.Markdown != "It asks for X." {
		t.Fatalf("SummarySpec = %+v, %v", s, err)
	}
	if _, err := Wrap("Summary", " ", Target{}, Source{}); err == nil {
		t.Error("an empty summary was wrapped")
	}
	var verbs []string
	for _, a := range doc.Offered() {
		verbs = append(verbs, a.Verb)
	}
	if strings.Join(verbs, ",") != "edit,comment,reject" {
		t.Errorf("actions = %v", verbs)
	}

	f := &fakeGitHub{}
	gh := f.client(t)
	var out bytes.Buffer
	for range 2 {
		if err := Apply(context.Background(), gh, doc, false, &out); err != nil {
			t.Fatal(err)
		}
	}
	if len(f.comments) != 1 || !strings.Contains(f.comments[0], "**Summary**\n\nIt asks for X.") {
		t.Errorf("applied twice, comments = %q", f.comments)
	}
}
