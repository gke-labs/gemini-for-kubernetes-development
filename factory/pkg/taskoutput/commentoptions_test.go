package taskoutput

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/commands/watch/conventions"
)

func assessDoc(t *testing.T, task string, c *CommentOptions) *Document {
	t.Helper()
	raw := "triage:\n  labels: [assess/good]\n  assessment: |\n    **Assessment: good** - clean.\n"
	doc, err := Wrap("Triage", raw, Target{URL: "https://github.com/o/r/issues/12"}, Source{Task: task, Recipe: "assess"})
	if err != nil {
		t.Fatalf("Wrap: %v", err)
	}
	doc.Comment = c
	return doc
}

// Unset, a triage's comment is as before: the kind's heading and the
// suggested labels line.
func TestTriageCommentDefault(t *testing.T) {
	f := &fakeComments{}
	var out bytes.Buffer
	if err := ApplyAction(context.Background(), f.client(t), assessDoc(t, "recipe-assess-1", nil), "comment", false, &out); err != nil {
		t.Fatal(err)
	}
	b := f.comments[0].GetBody()
	if !strings.HasPrefix(b, "**Triage assessment**\n\n**Assessment: good**") || !strings.Contains(b, "Suggested labels: `assess/good`") {
		t.Errorf("body = %q", b)
	}
}

// A recipe's prefix line comes first - an /overseer-ignore the overseer
// honours - its heading replaces the kind's ("" drops it), and
// suggested-labels: false leaves the labels line out.
func TestTriageCommentOptions(t *testing.T) {
	none, no := "", false
	f := &fakeComments{}
	var out bytes.Buffer
	c := &CommentOptions{Prefix: "/overseer-ignore", Heading: &none, SuggestedLabels: &no}
	if err := ApplyAction(context.Background(), f.client(t), assessDoc(t, "recipe-assess-1", c), "comment", false, &out); err != nil {
		t.Fatal(err)
	}
	b := f.comments[0].GetBody()
	if !strings.HasPrefix(b, "/overseer-ignore\n\n**Assessment: good** - clean.") {
		t.Errorf("body = %q", b)
	}
	if strings.Contains(b, "Triage assessment") || strings.Contains(b, "Suggested labels") {
		t.Errorf("heading or labels line left in: %q", b)
	}
	if !conventions.HasIgnorePrefix(b, "overseer") {
		t.Errorf("the overseer would act on %q", b)
	}

	heading := "**PR assessment**"
	f = &fakeComments{}
	if err := ApplyAction(context.Background(), f.client(t), assessDoc(t, "recipe-assess-2", &CommentOptions{Heading: &heading}), "comment", false, &out); err != nil {
		t.Fatal(err)
	}
	if b := f.comments[0].GetBody(); !strings.HasPrefix(b, "**PR assessment**\n\n**Assessment: good**") {
		t.Errorf("body = %q", b)
	}
}

// A keyed comment keeps its prefix above the versioned heading line.
func TestKeyedCommentPrefix(t *testing.T) {
	f := &fakeComments{}
	var out bytes.Buffer
	doc := keyedPlan(t, "recipe-plan-1", "## Summary\nFirst.")
	doc.Comment.Prefix = "/overseer-ignore"
	if err := ApplyAction(context.Background(), f.client(t), doc, "comment", false, &out); err != nil {
		t.Fatal(err)
	}
	if b := f.comments[0].GetBody(); !strings.HasPrefix(b, "/overseer-ignore\n\n**Implementation plan** · v1 · updated ") {
		t.Errorf("body = %q", b)
	}
}

func TestValidateComment(t *testing.T) {
	two := "a\nb"
	for _, c := range []*CommentOptions{{Key: "Bad"}, {Prefix: "a\nb"}, {Heading: &two}} {
		if ValidateComment(c) == nil {
			t.Errorf("%+v: no error", c)
		}
	}
	if err := ValidateComment(&CommentOptions{Key: "plan", Prefix: "/overseer-ignore"}); err != nil {
		t.Error(err)
	}
}
