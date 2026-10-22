package rules

import "testing"

// The gates, in order, each on the kinds it gates: the stop label and
// closed drop queued tasks, the merge queue only holds a PR back.
func TestGateOf(t *testing.T) {
	env := testEnv
	env.MinNumber = 100
	for _, tc := range []struct {
		name  string
		it    Item
		gate  string
		drops bool
	}{
		{"open, above the floor", Item{Kind: OnPR, Number: 200}, "", false},
		{"stop label", Item{Kind: OnIssue, Number: 200, Labels: []string{"overseer/stop"}}, "stop", true},
		{"stop before closed", Item{Kind: OnPR, Number: 200, Labels: []string{"overseer/stop"}, Closed: true}, "stop", true},
		{"closed", Item{Kind: OnIssue, Number: 200, Closed: true}, "closed", true},
		{"below the floor", Item{Kind: OnPR, Number: 50}, "below-min-number", true},
		{"merge queue", Item{Kind: OnPR, Number: 200, MergeQueue: true}, "merge-queue", false},
		{"an issue is never in the merge queue", Item{Kind: OnIssue, Number: 200, MergeQueue: true}, "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g, ok := GateOf(tc.it, env)
			if ok != (tc.gate != "") || g.Name != tc.gate || g.Drops != tc.drops {
				t.Errorf("GateOf = %q (drops %v, %v), want %q (drops %v)", g.Name, g.Drops, ok, tc.gate, tc.drops)
			}
		})
	}
}
