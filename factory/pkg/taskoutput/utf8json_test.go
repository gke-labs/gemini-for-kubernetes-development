package taskoutput

import (
	"strings"
	"testing"
)

// A Change written as JSON by Python's json.dump (the adopt and agent
// recipes): a character outside the Basic Multilingual Plane is a
// surrogate pair there, which YAML refuses; Wrap reads it all the same.
// Seen adopting PR #1775, whose description has 👀 and 👍.
func TestWrapJSONWithSurrogatePairs(t *testing.T) {
	// As json.dump writes them: the surrogate pairs, escaped.
	raw := `{"change": {"title": "adopt: watch: comment 1", "body": "Acked with \ud83d\udc40, done \ud83d\udc4d", "adopts": "https://github.com/o/r/pull/1775", "closeAdopted": false, "draft": false}}`
	doc, err := Wrap("Change", raw, Target{URL: "https://github.com/o/r/pull/1775"}, Source{})
	if err != nil {
		t.Fatalf("Wrap: %v", err)
	}
	var spec Change
	if err := doc.Spec.Decode(&spec); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(spec.Body, "👀") || !strings.Contains(spec.Body, "👍") || spec.Title != "adopt: watch: comment 1" {
		t.Errorf("change = %+v", spec)
	}
	// Plain YAML, and JSON without such characters, are read as before.
	for _, ok := range []string{"change:\n  title: t\n  body: b\n", `{"change": {"title": "t", "body": "é é"}}`} {
		if _, err := Wrap("Change", ok, Target{URL: "https://github.com/o/r/pull/1"}, Source{}); err != nil {
			t.Errorf("Wrap(%q): %v", ok, err)
		}
	}
}
