package rules

import (
	"slices"
	"strings"
	"testing"
)

// A recipe rule's hooks parse, are filled in for the item on its decision
// (a label taken off only when the item has it), and are refused on a rule
// that runs no recipe or with a placeholder they cannot fill.
func TestHooks(t *testing.T) {
	r, err := Parse([]byte(`name: adopt
on: pr
order: 8
when: {author: [owner, member, collaborator, external], labels: {any: ["{{trigger}}/adopt"]}}
run:
  recipe: adopt
  apply: open-pr
  on-start: {assign: "{{bot}}", label: ["*/working"]}
  on-success: {unlabel: ["{{trigger}}/adopt"]}
  on-failure: {stop: {comment: adopting failed}}
  on-end: {unlabel: ["*/working"], comment: done}
`))
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Validate(); err != nil {
		t.Fatal(err)
	}
	base, _ := Builtin()
	set, err := Overlay(base, []*Rule{r})
	if err != nil {
		t.Fatal(err)
	}
	mergeable := true
	it := Item{Kind: OnPR, Number: 7, Author: "alice", Labels: []string{"overseer/adopt", "overseer/working"}, Head: "abc", Mergeable: &mergeable}
	var d *Decision
	for _, x := range set.Decide(it, testEnv) {
		if x.Rule == "adopt" {
			d = &x
		}
	}
	if d == nil {
		t.Fatal("adopt not decided")
	}
	h := d.Hooks
	// on-start: {{bot}} kept for the watcher; */working already there, so
	// nothing to add.
	if !slices.Equal(h.OnStart.Assign, []string{Bot}) || len(h.OnStart.Label) != 0 {
		t.Errorf("on-start %+v", h.OnStart)
	}
	if !slices.Equal(h.OnSuccess.Unlabel, []string{"overseer/adopt"}) || len(h.OnSuccess.Label) > 0 {
		t.Errorf("on-success %+v", h.OnSuccess)
	}
	if !h.OnFailure.Stop || h.OnFailure.Comment != "adopting failed" {
		t.Errorf("on-failure %+v", h.OnFailure)
	}
	if !slices.Equal(h.OnEnd.Unlabel, []string{"overseer/working"}) || h.OnEnd.Comment != "done" {
		t.Errorf("on-end %+v", h.OnEnd)
	}
	// Re-filled at start on facts where */working is not there yet: added
	// on start, nothing to take off at the end.
	it.Labels = []string{"overseer/adopt"}
	if again := Hooks(set.Named("adopt"), it, testEnv); !slices.Equal(again.OnStart.Label, []string{"overseer/working"}) || len(again.OnEnd.Unlabel) != 0 {
		t.Errorf("refilled %+v", again)
	}

	for name, tc := range map[string]struct{ src, want string }{
		"no recipe":      {"run: {label: [a], on-success: {unlabel: [b]}}", "recipe's task's"},
		"placeholder":    {"run: {recipe: review, on-success: {unlabel: [\"{{nope}}\"]}}", "{{nope}}"},
		"stop on end":    {"run: {recipe: review, on-end: {stop: {comment: x}}}", "only on-failure may stop"},
		"bot not at end": {"run: {recipe: review, on-end: {unassign: \"{{bot}}\"}}", "{{bot}}"},
	} {
		r, err := Parse([]byte("name: x\non: pr\norder: 9\nwhen: {author: pool}\n" + tc.src + "\n"))
		if err == nil {
			err = r.Validate()
		}
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: %v, want %q", name, err, tc.want)
		}
	}
}
