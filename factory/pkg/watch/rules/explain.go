package rules

import (
	"fmt"
	"reflect"
	"slices"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Step is one rule's part in what the set decides for an item: it decided
// (Decision), was not looked at (Skipped), its condition did not hold
// (Failed), or its policy held it back (Held).
type Step struct {
	Rule      string `json:"rule"`
	Exclusive bool   `json:"exclusive,omitempty"`
	// Decision is what the rule decided, when it did.
	Decision *Decision `json:"decision,omitempty"`
	// Skipped says why the rule was not looked at: it is off, a task of
	// the item is active, an earlier rule took the item, its own task is
	// queued, or the walk ended before it.
	Skipped string `json:"skipped,omitempty"`
	// Failed are the predicates of its when that do not hold, each with
	// what the item has.
	Failed []string `json:"failed,omitempty"`
	// Held says why its policy held it back, its when holding.
	Held string `json:"held,omitempty"`
	// Ends is set on a rule whose writes stop the item: the walk ends.
	Ends bool `json:"ends,omitempty"`
}

// Explanation is what the set decides for an item, rule by rule.
type Explanation struct {
	// Gate is why the engine's gate (Gates) that kept every rule off the
	// item did, when one did: "has the stop label", "is closed".
	Gate  string `json:"gate,omitempty"`
	Steps []Step `json:"steps"`
}

// Decisions are the decisions of the explanation's steps, in order.
func (e Explanation) Decisions() []Decision {
	var out []Decision
	for _, st := range e.Steps {
		if st.Decision != nil {
			out = append(out, *st.Decision)
		}
	}
	return out
}

// Explain is Decide, rule by rule: every rule on the item's kind, in the
// order the engine walks them, with what it decided or why it did not.
// Decide is Explain's decisions, so the two cannot disagree.
func (s Set) Explain(it Item, env Env) Explanation {
	var ex Explanation
	if g, ok := GateOf(it, env); ok {
		ex.Gate = g.Why
	}
	labels := slices.Clone(it.Labels)
	taken, ended := "", false
	for _, r := range s.Ordered() {
		if r.On != it.Kind {
			continue
		}
		st := Step{Rule: r.Name, Exclusive: r.IsExclusive()}
		switch {
		case ex.Gate != "":
			st.Skipped = "the item " + ex.Gate
		case !r.IsEnabled():
			st.Skipped = "off"
		case ended:
			st.Skipped = "the walk ended: the stop label"
		case st.Exclusive && taken != "":
			st.Skipped = taken + " took the item"
		case st.Exclusive && it.TaskActive:
			st.Skipped = "a task of the item is queued or running"
		default:
			cur := it
			cur.Labels = labels
			st = explainRule(r, cur, env, st)
		}
		if d := st.Decision; d != nil {
			if st.Exclusive {
				taken = r.Name
			} else {
				labels = applyLabels(labels, d.Writes)
				if d.Writes.Stop || hasLabel(labels, env.Prefixes, "*/stop") {
					st.Ends, ended = true, true
				}
			}
		}
		ex.Steps = append(ex.Steps, st)
	}
	return ex
}

// explainRule is one rule's step for the item, the walk having reached it.
func explainRule(r *Rule, it Item, env Env, st Step) Step {
	if slices.Contains(it.Queued, r.Name) {
		st.Skipped = "its task is queued or running"
		return st
	}
	runs := rulesRuns(r.Name, it.Runs)
	if st.Failed = r.When.failing(r, it, env, runs); len(st.Failed) > 0 {
		return st
	}
	// The give-up is decided on the condition: a rule that would run, but
	// has run its attempts out. Counted since the head, a human or a pause,
	// it waits for the PR's history: asking, the rule could run.
	g := r.Policy.GiveUp
	if g != nil && len(g.Since) > 0 && it.unknown(FactHistory) {
		if !it.assume {
			st.Held = "give-up: the history is not read"
			return st
		}
		g = nil
	}
	if g != nil && countSince(giveUpRuns(g, runs), since(g.Since, it)) >= g.Attempts {
		d := Decision{Rule: r.Name, Kind: it.Kind, Number: it.Number, Outcome: OutcomeGiveUp}
		d.Writes = Writes{Stop: true, Comment: g.Comment}
		st.Decision = &d
		return st
	}
	if st.Held = policyHolds(r, it, env, runs); st.Held != "" {
		return st
	}
	d := Decision{Rule: r.Name, Kind: it.Kind, Number: it.Number, Outcome: OutcomeRun}
	if r.Run.Recipe != "" {
		d.Recipe, d.Apply, d.Phase = r.Run.Recipe, r.Run.Apply, r.Policy.Phase
		if len(r.Run.Inputs) > 0 {
			d.Inputs = map[string]string{}
			for k, v := range r.Run.Inputs {
				d.Inputs[k] = fillInput(v, it, env)
			}
		}
		st.Decision = &d
		return st
	}
	d.Writes = resolveWrites(r.Run, it, env)
	if d.Writes.empty() {
		st.Held = "its writes would change nothing"
		return st
	}
	st.Decision = &d
	return st
}

// failing are the predicates of w that do not hold for the item, each as
// written and with what the item has: each predicate is held on its own,
// by holds, so a predicate reads the same here as when deciding.
func (w When) failing(r *Rule, it Item, env Env, runs []PastRun) []string {
	var out []string
	v := reflect.ValueOf(w)
	for i := range v.NumField() {
		f := v.Field(i)
		if f.IsZero() {
			continue
		}
		var one When
		reflect.ValueOf(&one).Elem().Field(i).Set(f)
		if one.holds(r, it, env, runs) {
			continue
		}
		name, _, _ := strings.Cut(v.Type().Field(i).Tag.Get("yaml"), ",")
		desc := one.String()
		if has := factOf(name, it, env, runs); has != "" {
			desc += " (has " + has + ")"
		}
		out = append(out, desc)
	}
	return out
}

// factOf is what the item has for a predicate, as the predicate reads it,
// or "" when there is nothing short to say.
func factOf(predicate string, it Item, env Env, runs []PastRun) string {
	switch predicate {
	case "labels":
		if len(it.Labels) == 0 {
			return "no labels"
		}
		return strings.Join(it.Labels, ", ")
	case "assignee":
		if len(it.Assignees) == 0 {
			return "nobody assigned"
		}
		return "assigned " + strings.Join(it.Assignees, ", ")
	case "author", "opened-by":
		return "author " + orNone(it.Author)
	case "last-run":
		last, ok := lastRun(runs)
		switch {
		case !ok:
			return "no run"
		case last.Failed:
			return "the last run failed, on " + short(last.Head)
		}
		return "the last run succeeded"
	case "linked-pr":
		return "linked-pr " + orNone(it.LinkedPR)
	case "connected-pr":
		if it.unknown(FactConnectedPR) {
			return "connected-pr not read"
		}
		return "connected-pr " + orNone(it.ConnectedPR)
	case "workflow":
		if it.WorkflowMissing {
			return "workflow " + it.Workflow + ", which cannot be loaded"
		}
		return "workflow " + orNone(it.Workflow)
	case "fanout":
		return fmt.Sprintf("fanout %t", it.FanoutParent)
	case "checks":
		if it.Checks == "" || it.unknown(FactChecks) {
			return "checks not read"
		}
		return "checks " + it.Checks
	case "mergeable":
		if it.Mergeable == nil {
			return "mergeable not computed yet"
		}
		return fmt.Sprintf("mergeable %t", *it.Mergeable)
	case "feedback":
		if it.unknown(FactFeedback) {
			return "feedback not read"
		}
		if it.NewTrustedFeedback {
			return "new trusted feedback"
		}
		return "no new feedback"
	case "closes":
		var labels []string
		for _, c := range it.Closes {
			labels = append(labels, c.Labels...)
		}
		if len(labels) == 0 {
			return "closing issues without labels"
		}
		return "closing issues labelled " + strings.Join(labels, ", ")
	case "reviewed-at-head", "changes-requested", "inactive-for":
		if it.unknown(FactHistory) {
			return "history not read"
		}
	}
	switch predicate {
	case "reviewed-at-head":
		return fmt.Sprintf("reviewed-at-head %t", it.ReviewedAtHead)
	case "changes-requested":
		return fmt.Sprintf("changes-requested %t", it.ChangesRequested)
	case "approved":
		if it.unknown(FactChecks) {
			return "checks not read"
		}
		return fmt.Sprintf("approved %t", it.Approved)
	case "draft":
		return fmt.Sprintf("draft %t", it.Draft)
	case "inactive-for":
		if env.PRInactivityTimeout <= 0 {
			return "no inactivity timeout set"
		}
		if it.LastTrustedActivity.IsZero() {
			return "no trusted activity read"
		}
		return "inactive " + human(env.Now.Sub(it.LastTrustedActivity))
	}
	return ""
}

// Held is the rule's policy alone, on the item's cheap facts (its runs,
// the queue, when it last changed), before the facts its when reads are
// gathered: why the policy holds the rule back now, and until when (zero
// for a dedupe, which holds until the item changes); "" when the rule must
// be decided on every fact, because its policy lets it run or a give-up
// may be due (a give-up is decided on the rule's when, before its policy).
// A scanner asks it first, to spare the reads of an item the rule cannot
// run on.
func (s Set) Held(rule string, it Item, env Env) (string, time.Time) {
	r := s.Named(rule)
	if r == nil {
		return "", time.Time{}
	}
	runs := rulesRuns(rule, it.Runs)
	if g := r.Policy.GiveUp; g != nil && countSince(giveUpRuns(g, runs), since(g.Since, it)) >= g.Attempts {
		return "", time.Time{}
	}
	return policyHold(r, it, env, runs)
}

// policyHolds says why the rule's dedupe or cooldown holds it back now,
// "" when they let it run.
func policyHolds(r *Rule, it Item, env Env, runs []PastRun) string {
	why, _ := policyHold(r, it, env, runs)
	return why
}

// policyHold is policyHolds, and until when a cooldown holds it (zero for
// a dedupe, which holds until the item changes).
func policyHold(r *Rule, it Item, env Env, runs []PastRun) (string, time.Time) {
	last, ok := lastRun(runs)
	if !ok {
		return "", time.Time{}
	}
	if r.Policy.Dedupe == "head" && it.Head != "" && last.Head == it.Head && !last.Failed {
		return "dedupe: head: it already ran on " + short(it.Head), time.Time{}
	}
	// Once per update: not again until the item changed after the last run
	// that succeeded; a failed run says nothing about the item, and retries
	// once its cooldown is over (the issue scanner's processed times leave
	// failed tasks out).
	if r.Policy.Dedupe == "update" {
		if good, ok := lastRun(slices.DeleteFunc(slices.Clone(runs), func(r PastRun) bool { return r.Failed })); ok && !it.UpdatedAt.After(good.EndedAt) {
			return "dedupe: update: not updated since its last good run ended " + good.EndedAt.UTC().Format(time.RFC3339), time.Time{}
		}
	}
	c := r.Policy.Cooldown
	if c == nil {
		return "", time.Time{}
	}
	wait, after := c.Success, "a success"
	if last.Failed {
		wait, after = c.Failure, "a failure"
	}
	d := wait.Duration
	if wait.Ref == "{{workflow-cooldown}}" {
		if it.unknown(FactWorkflowCooldown) {
			if it.assume {
				// Asking: it could have run its cooldown out.
				return "", time.Time{}
			}
			return "cooldown: the workflow's not read", time.Time{}
		}
		d = it.WorkflowCooldown
		if d == 0 {
			d = defaultWorkflowCooldown
		}
	}
	if d == 0 || env.Now.Sub(last.EndedAt) >= d {
		return "", time.Time{}
	}
	if slices.Contains(c.ResetOn, "head") && it.Head != "" && last.Head != it.Head {
		return "", time.Time{}
	}
	if c.SkipWhen != nil && c.SkipWhen.holds(r, it, env, runs) {
		return "", time.Time{}
	}
	return fmt.Sprintf("cooldown: %s left of %s after %s", human(d-env.Now.Sub(last.EndedAt)), human(d), after), last.EndedAt.Add(d)
}

// String is the condition as a rule file writes it, on one line, any-of
// last.
func (w When) String() string {
	alts := w.AnyOf
	w.AnyOf = nil
	s := flow(w)
	if s == "" && len(alts) == 0 {
		return ""
	}
	if len(alts) > 0 {
		anyOf := flow(When{AnyOf: alts})
		if s == "" {
			return anyOf
		}
		s += ", " + anyOf
	}
	return s
}

// flow is v as YAML on one line, without the outer braces.
func flow(v any) string {
	var n yaml.Node
	if err := n.Encode(v); err != nil {
		return fmt.Sprint(v)
	}
	var setFlow func(*yaml.Node)
	setFlow = func(n *yaml.Node) {
		if n.Kind == yaml.MappingNode || n.Kind == yaml.SequenceNode {
			n.Style = yaml.FlowStyle
		}
		for _, c := range n.Content {
			setFlow(c)
		}
	}
	setFlow(&n)
	b, err := yaml.Marshal(&n)
	if err != nil {
		return fmt.Sprint(v)
	}
	s := strings.TrimSpace(string(b))
	return strings.TrimSuffix(strings.TrimPrefix(s, "{"), "}")
}

// human is a duration to the minute, without zero units: 36m, 2h, 1h12m.
func human(d time.Duration) string {
	d = d.Round(time.Minute)
	if d < time.Minute {
		return "under a minute"
	}
	s := d.String()
	s = strings.TrimSuffix(s, "0s")
	return strings.TrimSuffix(s, "h0m") + map[bool]string{true: "h", false: ""}[strings.HasSuffix(s, "h0m")]
}

func short(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}

// Needs are the facts the item has not had read (Item.Unknown) that a
// decision on it turns on: those of each rule that could decide on what is
// known - its conditions hold or could, its policy does not hold it - in
// the item's Unknown order (cheapest first). A scanner reads the first,
// asks again, and decides once none is needed: a rule that cannot decide
// whatever an unknown fact says costs no read of it, and a fact no rule
// asks about is never read.
func (s Set) Needs(it Item, env Env) []string {
	if len(it.Unknown) == 0 {
		return nil
	}
	want := map[string]bool{}
	it.assume = true
	for _, st := range s.Explain(it, env).Steps {
		if st.Decision == nil {
			continue
		}
		if r := s.Named(st.Rule); r != nil {
			for _, f := range r.facts(rulesRuns(r.Name, it.Runs)) {
				want[f] = true
			}
		}
	}
	var out []string
	for _, f := range it.Unknown {
		if want[f] {
			out = append(out, f)
		}
	}
	return out
}

// Settled are the decisions of the walk up to its first rule whose step a
// fact not read yet (Item.Unknown) could change: deciding (a predicate on
// it fails) and asking (it could go either way) give that rule the same
// step, and every rule before it. A scanner reading an item's facts a
// stage at a time carries out what is settled after each stage: a rule
// after one still open waits, whatever its own facts, so a rule's place
// (order) is kept - an earlier rule's stop ends the walk before it.
func (s Set) Settled(it Item, env Env) []Decision {
	decided := s.Explain(it, env)
	if len(it.Unknown) == 0 {
		return decided.Decisions()
	}
	asking := it
	asking.assume = true
	open := s.Explain(asking, env)
	var out []Decision
	for i, st := range decided.Steps {
		if i >= len(open.Steps) || !sameStep(st, open.Steps[i]) {
			break
		}
		if st.Decision != nil {
			out = append(out, *st.Decision)
		}
	}
	return out
}

// sameStep is whether two steps of a rule come to the same: decided the
// same outcome or nothing, skipped the same, and end the walk the same.
func sameStep(a, b Step) bool {
	if (a.Decision == nil) != (b.Decision == nil) || a.Skipped != b.Skipped || a.Ends != b.Ends {
		return false
	}
	return a.Decision == nil || a.Decision.Outcome == b.Decision.Outcome
}

// facts are the facts read on demand the rule's decision turns on, given
// its runs: a predicate on one, in its condition or any alternative, or the
// workflow definition's cooldown when its last run succeeded (a failure's
// cooldown is the rule's own).
func (r *Rule) facts(runs []PastRun) []string {
	var out []string
	add := func(f string) {
		if !slices.Contains(out, f) {
			out = append(out, f)
		}
	}
	for _, w := range append([]When{r.When}, r.When.AnyOf...) {
		if w.ConnectedPR != "" {
			add(FactConnectedPR)
		}
		if w.Workflow == "any" || w.Workflow == "missing" {
			add(FactWorkflowDefinition)
		}
		if w.ReviewedAtHead != nil || w.ChangesRequested != nil || w.InactiveFor.IsSet() {
			add(FactHistory)
		}
		if w.Feedback != "" {
			add(FactFeedback)
		}
		if w.Checks != "" || w.Approved != nil {
			add(FactChecks)
		}
	}
	if g := r.Policy.GiveUp; g != nil && len(g.Since) > 0 {
		add(FactHistory)
	}
	if c := r.Policy.Cooldown; c != nil {
		if last, ok := lastRun(runs); ok {
			wait := c.Success
			if last.Failed {
				wait = c.Failure
			}
			if wait.Ref == "{{workflow-cooldown}}" {
				add(FactWorkflowCooldown)
			}
		}
	}
	return out
}
