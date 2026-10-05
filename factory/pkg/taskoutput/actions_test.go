package taskoutput

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestValidateActions(t *testing.T) {
	good := map[string][]Action{
		"Triage": DefaultActions("Triage"),
		"Plan":   DefaultActions("Plan"),
	}
	for kind, acts := range good {
		if err := ValidateActions(kind, acts); err != nil {
			t.Errorf("%s defaults: %v", kind, err)
		}
	}
	for name, tc := range map[string]struct {
		kind string
		acts []Action
	}{
		"unknown verb":         {"Plan", []Action{{Verb: "merge"}}},
		"verb not for kind":    {"Plan", []Action{{Verb: "label"}}},
		"twice":                {"Plan", []Action{{Verb: "comment"}, {Verb: "comment"}}},
		"edit without field":   {"Plan", []Action{{Verb: "edit", Format: "markdown"}}},
		"edit outside spec":    {"Plan", []Action{{Verb: "edit", Field: "target.url", Format: "yaml"}}},
		"edit format":          {"Plan", []Action{{Verb: "edit", Field: "spec", Format: "html"}}},
		"unknown follow-up":    {"Plan", []Action{{Verb: "run", Run: "deploy"}}},
		"follow-up not a plan": {"Triage", []Action{{Verb: "run", Run: "fix"}}},
		"comment with a run":   {"Plan", []Action{{Verb: "comment", Run: "fix"}}},
		"revise without one":   {"Plan", []Action{{Verb: "revise"}}},
		"revise twice":         {"Plan", []Action{{Verb: "revise", Revise: "plan"}, {Verb: "revise", Revise: "plan"}}},
		"revise with a run":    {"Plan", []Action{{Verb: "revise", Revise: "plan", Run: "fix"}}},
		"comment with revise":  {"Plan", []Action{{Verb: "comment", Revise: "plan"}}},
	} {
		if err := ValidateActions(tc.kind, tc.acts); err == nil {
			t.Errorf("%s: accepted %+v", name, tc.acts)
		}
	}
}

// A document without actions offers its kind's defaults; one with actions
// offers those, less any verb this factory doesn't know or the kind
// doesn't take.
func TestOffered(t *testing.T) {
	doc := triageDoc(t, "x")
	if got := verbsOf(doc.Offered()); got != "edit label comment reject" {
		t.Errorf("defaults = %s", got)
	}
	doc.Actions = []Action{{Verb: "comment"}, {Verb: "teleport"}, {Verb: "run", Run: "fix"}}
	if got := verbsOf(doc.Offered()); got != "comment" {
		t.Errorf("offered = %s", got)
	}
	if _, err := doc.Offer("label", ""); err == nil || !strings.Contains(err.Error(), "it offers: comment") {
		t.Errorf("Offer(label) = %v", err)
	}
	plan := planDoc(t, "x", "## Summary\nDo it.")
	if a, err := plan.Offer("run", ""); err != nil || a.Run != "fix" {
		t.Errorf("Offer(run) = %+v, %v", a, err)
	}
	if _, err := plan.Offer("run", "deploy"); err == nil {
		t.Error("Offer(run deploy) on a plan offering run fix")
	}
}

// Each revise is an action of its own, offered by its id.
func TestRevisesAreOfferedByID(t *testing.T) {
	acts := []Action{{Verb: "comment"}, {Verb: "revise", Revise: "plan", Label: "Use as plan"}, {Verb: "revise", Revise: "shorter"}}
	if err := ValidateActions("Plan", acts); err != nil {
		t.Fatal(err)
	}
	plan := planDoc(t, "x", "## Summary\nDo it.")
	plan.Actions = acts
	if a, err := plan.Offer("revise", "shorter"); err != nil || a.Revise != "shorter" {
		t.Errorf("Offer(revise shorter) = %+v, %v", a, err)
	}
	if _, err := plan.Offer("revise", "longer"); err == nil || !strings.Contains(err.Error(), "revise plan, revise shorter") {
		t.Errorf("Offer(revise longer) = %v", err)
	}
	if class, _ := VerbClass("revise"); class != ClassFollowUp {
		t.Errorf("revise is %s", class)
	}
}

func verbsOf(acts []Action) string {
	var v []string
	for _, a := range acts {
		v = append(v, a.Verb)
	}
	return strings.Join(v, " ")
}

// Apply does only the writes the document offers; ApplyAction one of them.
func TestApplyFollowsActions(t *testing.T) {
	f := &fakeGitHub{}
	gh := f.client(t)
	doc := triageDoc(t, "recipe-triage-1")
	doc.Actions = []Action{{Verb: "comment"}, {Verb: "reject"}}
	if err := Apply(context.Background(), gh, doc, false, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if len(f.labels) != 0 || len(f.comments) != 1 {
		t.Errorf("labels %v comments %d, want only the comment", f.labels, len(f.comments))
	}

	f2 := &fakeGitHub{}
	doc2 := triageDoc(t, "recipe-triage-2")
	if err := ApplyAction(context.Background(), f2.client(t), doc2, "label", false, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if len(f2.labels) != 1 || len(f2.comments) != 0 {
		t.Errorf("label alone: labels %v comments %d", f2.labels, len(f2.comments))
	}
	if err := ApplyAction(context.Background(), gh, doc, "label", false, &bytes.Buffer{}); err == nil {
		t.Error("applied label, which the document does not offer")
	}
	if err := ApplyAction(context.Background(), gh, doc, "reject", false, &bytes.Buffer{}); err == nil {
		t.Error("apply executed a draft action")
	}
}

// Actions survive Marshal and Parse.
func TestActionsRoundTrip(t *testing.T) {
	doc := planDoc(t, "x", "## Summary\nDo it.")
	doc.Actions = DefaultActions("Plan")
	data, err := Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	docs, err := Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	if got := docs[0].Actions; len(got) != 4 || got[0].Field != "spec.markdown" || got[2].Run != "fix" || got[2].Label != "Fix with this plan" {
		t.Errorf("actions = %+v\n%s", got, data)
	}
}
