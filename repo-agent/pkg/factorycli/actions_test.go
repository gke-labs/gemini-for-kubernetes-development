package factorycli

import (
	"reflect"
	"strings"
	"testing"
)

// The board keeps the whole document: the draft is its spec, and it says
// its source and the actions it offers.
func TestPlanTaskOutputKeepsTheDocument(t *testing.T) {
	doc := planTaskOutput + `actions:
  - verb: comment
    label: Post plan
  - verb: run
    run: fix
`
	got := HarvestedOutput("Plan", "Running recipe plan...\n"+taskOutputBanner+"\n"+doc+bannerCloser+"\n")
	if !strings.HasPrefix(got, "apiVersion: ") || Draft("Plan", got) == "" {
		t.Fatalf("HarvestedOutput = %q, want the whole document", got)
	}
	if task := TaskOutputTask("Plan", got); task != "recipe-plan-20261003-120000-0001" {
		t.Errorf("task = %q", task)
	}
	want := []Action{{Verb: "comment", Label: "Post plan"}, {Verb: "run", Run: "fix"}}
	if acts := OfferedActions("Plan", got); !reflect.DeepEqual(acts, want) {
		t.Errorf("offered = %+v, want %+v", acts, want)
	}
	if HarvestedOutput("Plan", taskOutputBanner+"\njust markdown\n"+bannerCloser) != "" {
		t.Error("a plan that is no document has none to keep")
	}
}

func TestOfferedActions(t *testing.T) {
	// No document (a draft from before documents were kept), or one that
	// declares none: the kind's defaults.
	for _, doc := range []string{"", "kind: Plan\n", "not: [yaml"} {
		if got := OfferedActions("Plan", doc); !reflect.DeepEqual(got, defaultActions["Plan"]) {
			t.Errorf("OfferedActions(%q) = %+v, want the defaults", doc, got)
		}
	}
	// Any verb the document offers is offered, whatever the kind: the
	// board does not filter writes by kind; who may click them is VerbNeeds.
	doc := `kind: Triage
actions:
  - verb: run
    run: fix
  - verb: label
  - verb: frobnicate
  - verb: reject
  - verb: ""
`
	want := []Action{{Verb: "run", Run: "fix"}, {Verb: "label"}, {Verb: "frobnicate"}, {Verb: "reject"}}
	if got := OfferedActions("Triage", doc); !reflect.DeepEqual(got, want) {
		t.Errorf("OfferedActions = %+v, want %+v", got, want)
	}
	// Another kind's document is no declaration for this one.
	if got := OfferedActions("Plan", doc); !reflect.DeepEqual(got, defaultActions["Plan"]) {
		t.Errorf("OfferedActions(Plan, triage doc) = %+v", got)
	}
	if !Offers("Triage", doc, "label") || Offers("Triage", doc, "comment") || Offers("Plan", doc, "frobnicate") {
		t.Error("Offers disagrees with OfferedActions")
	}
	// Only fix is a follow-up the board runs.
	if got := OfferedActions("Plan", "kind: Plan\nactions:\n  - verb: run\n    run: deploy\n"); len(got) != 0 {
		t.Errorf("run deploy offered: %+v", got)
	}
}

// A plan's revises are offered by id; one without an id is no action.
func TestOfferedRevises(t *testing.T) {
	doc := `kind: Plan
source:
  task: recipe-plan-2
  session: recipe-plan-1
actions:
  - verb: comment
  - verb: revise
    revise: plan
    label: Update plan
  - verb: revise
`
	want := []Action{{Verb: "comment"}, {Verb: "revise", Revise: "plan", Label: "Update plan"}}
	if got := OfferedActions("Plan", doc); !reflect.DeepEqual(got, want) {
		t.Errorf("OfferedActions = %+v, want %+v", got, want)
	}
	if got := TaskOutputSession("Plan", doc); got != "recipe-plan-1" {
		t.Errorf("session = %q, want the one the revise revised in", got)
	}
	if got := TaskOutputSession("Plan", "kind: Plan\nsource:\n  task: recipe-plan-1\n"); got != "recipe-plan-1" {
		t.Errorf("session of a start = %q, want its task", got)
	}
	// Any kind's.
	want = []Action{{Verb: "revise", Revise: "comment"}}
	if got := OfferedActions("Triage", "kind: Triage\nactions:\n  - verb: revise\n    revise: comment\n"); !reflect.DeepEqual(got, want) {
		t.Errorf("triage revise offered: %+v, want %+v", got, want)
	}
}

// Writes the board does not know need push; follow-ups and draft verbs
// are no writes.
func TestVerbs(t *testing.T) {
	for verb, want := range map[string]string{
		"label": NeedsTriage, "comment": NeedsNothing, "open-pr": NeedsNothing,
		"post-review": NeedsNothing, "edit": NeedsNothing, "frobnicate": NeedsPush,
	} {
		if got := VerbNeeds(verb); got != want {
			t.Errorf("VerbNeeds(%s) = %q, want %q", verb, got, want)
		}
	}
	for verb, want := range map[string]bool{
		"comment": true, "label": true, "push-notes": true, "open-pr": true, "frobnicate": true,
		"edit": false, "reject": false, "run": false, "revise": false,
	} {
		if got := IsApplyVerb(verb); got != want {
			t.Errorf("IsApplyVerb(%s) = %v, want %v", verb, got, want)
		}
	}
}
