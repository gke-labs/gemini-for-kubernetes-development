package rules

import (
	"strings"
	"testing"
)

// A rule applies its recipe's result with one action or a list, in order;
// each must be an action that writes a result, and a list is a YAML list.
func TestRuleApplyActions(t *testing.T) {
	for y, want := range map[string]string{
		"name: t\non: issue\norder: 90\nrun: {recipe: triage, apply: comment}\n":          "comment",
		"name: t\non: issue\norder: 90\nrun: {recipe: triage, apply: [comment, label]}\n": "comment,label",
	} {
		r, err := Parse([]byte(y))
		if err == nil {
			err = r.Validate()
		}
		if err != nil || string(r.Run.Apply) != want {
			t.Errorf("%q: %v, apply %q; want %q", y, err, r.Run.Apply, want)
		}
	}
	if got := ApplyActions("comment,label").List(); len(got) != 2 || got[1] != "label" {
		t.Errorf("List = %v", got)
	}
	for y, want := range map[string]string{
		"name: t\non: issue\norder: 90\nrun: {recipe: triage, apply: \"comment,label\"}\n":  "YAML list",
		"name: t\non: issue\norder: 90\nrun: {recipe: triage, apply: [comment, comment]}\n": "twice",
		"name: t\non: issue\norder: 90\nrun: {recipe: triage, apply: [edit]}\n":             "not an action that writes",
		"name: t\non: issue\norder: 90\nrun: {apply: comment, label: [x]}\n":                "needs a run.recipe",
	} {
		r, err := Parse([]byte(y))
		if err == nil {
			err = r.Validate()
		}
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: %v, want %q", y, err, want)
		}
	}
}
