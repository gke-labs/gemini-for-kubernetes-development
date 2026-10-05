package factorycli

import (
	"reflect"
	"strings"
	"testing"
)

// The board keeps the document without its spec — the draft is the spec —
// for its source and the actions it offers.
func TestPlanTaskOutputKeepsAllButTheSpec(t *testing.T) {
	doc := planTaskOutput + `actions:
  - verb: comment
    label: Post plan
  - verb: run
    run: fix
`
	got := PlanTaskOutput("Running recipe plan...\n" + planBanner + "\n" + doc + bannerCloser + "\n")
	if got == "" || strings.Contains(got, "spec:") || strings.Contains(got, "Fix the crash") {
		t.Fatalf("PlanTaskOutput = %q, want the document without its spec", got)
	}
	if task := TaskOutputTask("Plan", got); task != "recipe-plan-20261003-120000-0001" {
		t.Errorf("task = %q", task)
	}
	want := []Action{{Verb: "comment", Label: "Post plan"}, {Verb: "run", Run: "fix"}}
	if acts := OfferedActions("Plan", got); !reflect.DeepEqual(acts, want) {
		t.Errorf("offered = %+v, want %+v", acts, want)
	}
	if PlanTaskOutput(planBanner+"\njust markdown\n"+bannerCloser) != "" {
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
	// Verbs the board does not execute, or the wrong kind's, are left out.
	doc := `kind: Triage
actions:
  - verb: run
    run: fix
  - verb: label
  - verb: frobnicate
  - verb: reject
`
	want := []Action{{Verb: "label"}, {Verb: "reject"}}
	if got := OfferedActions("Triage", doc); !reflect.DeepEqual(got, want) {
		t.Errorf("OfferedActions = %+v, want %+v", got, want)
	}
	// Another kind's document is no declaration for this one.
	if got := OfferedActions("Plan", doc); !reflect.DeepEqual(got, defaultActions["Plan"]) {
		t.Errorf("OfferedActions(Plan, triage doc) = %+v", got)
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
    label: Use as plan
  - verb: revise
`
	want := []Action{{Verb: "comment"}, {Verb: "revise", Revise: "plan", Label: "Use as plan"}}
	if got := OfferedActions("Plan", doc); !reflect.DeepEqual(got, want) {
		t.Errorf("OfferedActions = %+v, want %+v", got, want)
	}
	if got := TaskOutputSession("Plan", doc); got != "recipe-plan-1" {
		t.Errorf("session = %q, want the one the revise revised in", got)
	}
	if got := TaskOutputSession("Plan", "kind: Plan\nsource:\n  task: recipe-plan-1\n"); got != "recipe-plan-1" {
		t.Errorf("session of a start = %q, want its task", got)
	}
	// Not yet a triage's.
	if got := OfferedActions("Triage", "kind: Triage\nactions:\n  - verb: revise\n    revise: comment\n"); len(got) != 0 {
		t.Errorf("triage revise offered: %+v", got)
	}
}
