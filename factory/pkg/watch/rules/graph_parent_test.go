package rules

import (
	"slices"
	"testing"
)

// fix and workflow leave an issue graph's parent alone (graph: false; the
// scanner reads the fact from the <trigger>/graph label): its children are
// the graph's work.
func TestGraphParentIsNotFixed(t *testing.T) {
	set, err := Builtin()
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		it   Item
		rule string
	}{
		{"fix", Item{Kind: OnIssue, Number: 7, Labels: []string{"overseer"}}, "fix"},
		{"workflow", Item{Kind: OnIssue, Number: 8, Labels: []string{"overseer"}, Workflow: "kcc-x"}, "workflow"},
	} {
		if got := names(set.Decide(tc.it, testEnv)); !slices.Contains(got, tc.rule) {
			t.Fatalf("%s: decided %v on an issue that is no graph parent", tc.name, got)
		}
		tc.it.GraphParent = true
		if got := names(set.Decide(tc.it, testEnv)); slices.Contains(got, tc.rule) {
			t.Errorf("%s decided on a graph parent: %v", tc.name, got)
		}
	}
}
