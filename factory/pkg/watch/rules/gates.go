package rules

import "slices"

// Gate keeps every rule off an item, whatever the rules say: the engine's,
// not a rule's, so that no rule file - the factory's or a repository's -
// can turn it off, and it holds for every rule, those not written yet
// too. The scan and the dispatcher's decision at start both pass an item
// through the gates before any rule.
type Gate struct {
	Name string
	// Why is what the item is, as explain prints it: "the item <why>".
	Why string
	// Drops is that the item's queued tasks are dropped, not only held
	// back: the item will not want them when the gate lifts, if it ever
	// does, without being decided anew.
	Drops bool
	// Kinds are the kinds of item it gates.
	Kinds []string
	holds func(Item, Env) bool
}

// Gates are the engine's gates, in the order they are checked.
var Gates = []Gate{
	{Name: "stop", Why: "has the stop label", Drops: true, Kinds: []string{OnIssue, OnPR},
		holds: func(it Item, env Env) bool { return hasLabel(it.Labels, env.Prefixes, "*/stop") }},
	{Name: "closed", Why: "is closed", Drops: true, Kinds: []string{OnIssue, OnPR},
		holds: func(it Item, _ Env) bool { return it.Closed }},
	{Name: "below-min-number", Why: "is numbered below the overseer's minNumber", Drops: true, Kinds: []string{OnIssue, OnPR},
		holds: func(it Item, env Env) bool { return env.MinNumber > 0 && it.Number < env.MinNumber }},
	{Name: "merge-queue", Why: "is in the merge queue", Kinds: []string{OnPR},
		holds: func(it Item, _ Env) bool { return it.MergeQueue }},
}

// GateOf is the first gate that keeps every rule off the item, if one
// does.
func GateOf(it Item, env Env) (Gate, bool) {
	for _, g := range Gates {
		if slices.Contains(g.Kinds, it.Kind) && g.holds(it, env) {
			return g, true
		}
	}
	return Gate{}, false
}
