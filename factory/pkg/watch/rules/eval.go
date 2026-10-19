package rules

import (
	"fmt"
	"slices"
	"strings"
	"time"
)

// Item is what the engine knows of one issue, PR, due schedule or event
// when it decides: its GitHub facts and the overseer's history of it, all
// gathered by the caller (the shared scan's snapshot, the queue's
// journal). Deciding makes no calls: the same Item always gives the same
// decisions, which is what the replay tests and shadow mode compare.
type Item struct {
	// Kind is the rule kind the item is for: OnIssue, OnPR, OnSchedule or
	// OnEvent.
	Kind   string
	Number int
	// Name names a schedule's agent definition, or an event's.
	Name string

	Labels    []string
	Assignees []string
	// Author opened the item; AuthorTrusted is whether they are trusted
	// (a repository member, or a trusted login).
	Author        string
	AuthorTrusted bool
	UpdatedAt     time.Time

	// Issues.
	// LinkedPR is "open" when an open PR closes or is linked to the issue.
	LinkedPR string
	// Workflow names the workflow definition the issue's body names.
	Workflow string
	// WorkflowCooldown is that definition's cooldown, 0 for its default.
	WorkflowCooldown time.Duration
	FanoutParent     bool
	FanoutChild      bool

	// PRs.
	Head      string
	Draft     bool
	Mergeable *bool // nil: GitHub has not computed it
	// Checks is failing, pending or green.
	Checks string
	// NewTrustedFeedback is a trusted human's feedback the overseer has not
	// answered yet.
	NewTrustedFeedback bool
	// ReviewedAtHead is an automated review of the head;
	// ChangesRequested, that it asked for changes.
	ReviewedAtHead   bool
	ChangesRequested bool
	Approved         bool
	MergeQueue       bool
	// LastTrustedActivity is the last time a trusted human acted on it.
	LastTrustedActivity time.Time
	Closes              []Closing

	// Schedules: the definition's schedule is due. Events: which one.
	Due   bool
	Event string

	// The overseer's history of the item.
	// TaskActive is a task of the item queued or running: a group decides
	// nothing while one is.
	TaskActive bool
	// Queued are the rules whose task for the item is queued or running:
	// none of them decides again until it ends.
	Queued []string
	// Runs are the runs of the item's tasks that ended, by rule.
	Runs []PastRun
	// HeadChangedAt, HumanSpokeAt and PausedAt are when the PR's head last
	// changed, a trusted human last commented and the stop label was last
	// taken off: what a count since head, human or pause starts from.
	HeadChangedAt time.Time
	HumanSpokeAt  time.Time
	PausedAt      time.Time
}

// Closing is an issue a PR closes.
type Closing struct {
	Number    int
	Labels    []string
	Assignees []string
}

// PastRun is one ended run of a rule on an item.
type PastRun struct {
	Rule      string
	StartedAt time.Time
	EndedAt   time.Time
	Failed    bool
	// Head is the PR's head the run worked on.
	Head string
}

// Env is the overseer's side of a decision: its settings, its accounts
// and the time.
type Env struct {
	// Trigger is the trigger label; Prefixes the namespaces its labels are
	// spelled in, overseer/ first (conventions.Prefixes). A label written
	// takes the last.
	Trigger  string
	Prefixes []string
	// Watcher is the watcher's own login; Pool the bots it runs as.
	Watcher string
	Pool    []string
	// Owner is the login assignee: owner means.
	Owner string
	// PRLabels are the labels the configuration adds to a PR a fix opens,
	// besides the trigger and the issue's ({{pr-labels}}).
	PRLabels []string
	// PRInactivityTimeout is {{pr-inactivity-timeout}}; 0 is never.
	PRInactivityTimeout time.Duration
	Now                 time.Time
}

// Decision is what a rule decided for an item.
type Decision struct {
	Rule   string
	Kind   string
	Number int
	// Outcome is OutcomeRun (queue the recipe, or make the writes) or
	// OutcomeGiveUp (the rule's give-up: the stop label and its comment).
	Outcome string
	// Recipe, Apply and Inputs are a recipe run's, placeholders filled in.
	Recipe string
	Apply  string
	Inputs map[string]string
	// Writes are the GitHub writes, placeholders filled in and only what
	// changes something.
	Writes Writes
	// SkipCooldown is the rule's policy: its task is queued inside the
	// cooldown of the rule whose task it is.
	SkipCooldown bool
}

// What a decision does.
const (
	OutcomeRun    = "run"
	OutcomeGiveUp = "give-up"
)

// Writes are a decision's GitHub writes.
type Writes struct {
	Label    []string `json:"label,omitempty"`
	Unlabel  []string `json:"unlabel,omitempty"`
	Assign   []string `json:"assign,omitempty"`
	Unassign []string `json:"unassign,omitempty"`
	Comment  string   `json:"comment,omitempty"`
	// Stop adds the stop label, with Comment.
	Stop bool `json:"stop,omitempty"`
	// Drop drops the item's queued tasks.
	Drop bool `json:"drop,omitempty"`
}

func (w Writes) empty() bool {
	return len(w.Label) == 0 && len(w.Unlabel) == 0 && len(w.Assign) == 0 && len(w.Unassign) == 0 &&
		w.Comment == "" && !w.Stop && !w.Drop
}

// Decide is what the set decides for one item:
//
//   - a stopped item (the stop label) gets nothing: the stop label is a
//     gate on every rule;
//   - only the enabled rules of the item's kind are looked at (the item
//     is one the watch tracks: the scanners list no others), in order: one
//     list per kind;
//   - a rule that takes the item (IsExclusive) is decided only if none
//     before it was, and none is while a task of the item is active;
//   - every other rule decides on its own, and its label writes are what
//     the rules after it see; one that stops the item (inactive, or the
//     stop label inherited from a closing issue) ends the decision.
//
// A rule holds when its predicates do and its policy lets it run: not in
// its cooldown, not already run on this head when deduped by head. A rule
// whose give-up count is reached decides OutcomeGiveUp instead. A rule of
// GitHub writes decides only when its writes change something.
func (s Set) Decide(it Item, env Env) []Decision {
	if it.Kind == OnIssue || it.Kind == OnPR {
		if hasLabel(it.Labels, env.Prefixes, "*/stop") {
			return nil
		}
	}
	// A PR in the merge queue is out of the overseer's hands: a push now
	// would only knock it back out.
	if it.Kind == OnPR && it.MergeQueue {
		return nil
	}
	var mine Set
	for _, r := range s {
		if r.On == it.Kind && r.IsEnabled() {
			mine = append(mine, r)
		}
	}
	var out []Decision
	labels := slices.Clone(it.Labels)
	taken := false
	for _, r := range mine.Ordered() {
		exclusive := r.IsExclusive()
		if exclusive && (taken || it.TaskActive) {
			continue
		}
		cur := it
		cur.Labels = labels
		d, ok := decide(r, cur, env)
		if !ok {
			continue
		}
		out = append(out, d)
		if exclusive {
			taken = true
			continue
		}
		labels = applyLabels(labels, d.Writes)
		if d.Writes.Stop || hasLabel(labels, env.Prefixes, "*/stop") {
			break
		}
	}
	return out
}

// decide is one rule's decision for the item, if it makes one.
func decide(r *Rule, it Item, env Env) (Decision, bool) {
	if slices.Contains(it.Queued, r.Name) {
		return Decision{}, false
	}
	runs := rulesRuns(r.Name, it.Runs)
	// The give-up is decided on the rest of the condition: a rule that
	// would run, but has run its attempts out.
	rest := r.When
	rest.Attempts = nil
	if !rest.holds(r, it, env, runs) {
		return Decision{}, false
	}
	if g := r.Policy.GiveUp; g != nil && countSince(runs, since(g.Since, it)) >= g.Attempts {
		d := Decision{Rule: r.Name, Kind: it.Kind, Number: it.Number, Outcome: OutcomeGiveUp}
		d.Writes = Writes{Stop: true, Comment: g.Comment}
		return d, true
	}
	if a := r.When.Attempts; a != nil {
		counted := runs
		if a.Rule != "" {
			counted = rulesRuns(a.Rule, it.Runs)
		}
		if countSince(counted, since(a.Since, it)) >= a.Max {
			return Decision{}, false
		}
	}
	if !policyAllows(r, it, env, runs) {
		return Decision{}, false
	}
	d := Decision{Rule: r.Name, Kind: it.Kind, Number: it.Number, Outcome: OutcomeRun, SkipCooldown: r.Policy.SkipCooldown}
	if r.Run.Recipe != "" {
		d.Recipe, d.Apply = r.Run.Recipe, r.Run.Apply
		if len(r.Run.Inputs) > 0 {
			d.Inputs = map[string]string{}
			for k, v := range r.Run.Inputs {
				d.Inputs[k] = fillInput(v, it, env)
			}
		}
		return d, true
	}
	d.Writes = resolveWrites(r.Run, it, env)
	if d.Writes.empty() {
		return Decision{}, false
	}
	return d, true
}

// holds is whether every predicate set holds for the item.
func (w When) holds(r *Rule, it Item, env Env, runs []PastRun) bool {
	if l := w.Labels; l != nil && !labelsMatch(*l, it.Labels, env) {
		return false
	}
	switch w.Assignee {
	case "pool":
		if !anyIn(it.Assignees, env.Pool) {
			return false
		}
	case "not-pool":
		if anyIn(it.Assignees, env.Pool) {
			return false
		}
	case "none":
		if len(it.Assignees) > 0 {
			return false
		}
	case "owner":
		if env.Owner == "" || !anyIn(it.Assignees, []string{env.Owner}) {
			return false
		}
	}
	switch w.Author {
	case "pool":
		if !anyIn([]string{it.Author}, env.Pool) {
			return false
		}
	case "trusted":
		if !it.AuthorTrusted {
			return false
		}
	}
	if w.Stopped != nil && hasLabel(it.Labels, env.Prefixes, "*/stop") != *w.Stopped {
		return false
	}
	if w.UpdatedSinceRun != nil {
		// Since the last run that succeeded: a failed run says nothing
		// about the item, and retries once its cooldown is over (the issue
		// scanner's processed times leave failed tasks out).
		updated := true
		if last, ok := lastRun(slices.DeleteFunc(slices.Clone(runs), func(r PastRun) bool { return r.Failed })); ok {
			updated = it.UpdatedAt.After(last.EndedAt)
		}
		if updated != *w.UpdatedSinceRun {
			return false
		}
	}
	if w.LastRun == "failed" {
		last, ok := lastRun(runs)
		if !ok || !last.Failed || last.Head != it.Head {
			return false
		}
	}

	if w.LinkedPR != "" && w.LinkedPR != orNone(it.LinkedPR) {
		return false
	}
	switch w.Workflow {
	case "none":
		if it.Workflow != "" {
			return false
		}
	case "any":
		if it.Workflow == "" {
			return false
		}
	}
	if w.OpenedBy == "watcher" && (env.Watcher == "" || !strings.EqualFold(it.Author, env.Watcher)) {
		return false
	}
	if w.Fanout != "" {
		got := "none"
		switch {
		case it.FanoutParent:
			got = "parent"
		case it.FanoutChild:
			got = "child"
		}
		if got != w.Fanout {
			return false
		}
	}
	if w.FanoutHeld != nil {
		held := it.FanoutParent || (it.FanoutChild && !hasLabel(it.Labels, env.Prefixes, env.Trigger))
		if held != *w.FanoutHeld {
			return false
		}
	}

	if w.Checks != "" && w.Checks != it.Checks {
		return false
	}
	if w.Mergeable != nil && (it.Mergeable == nil || *it.Mergeable != *w.Mergeable) {
		return false
	}
	switch w.Feedback {
	case "new-trusted":
		if !it.NewTrustedFeedback {
			return false
		}
	case "none":
		if it.NewTrustedFeedback {
			return false
		}
	}
	if c := w.Closes; c != nil && c.Labels != nil {
		var union []string
		for _, ci := range it.Closes {
			union = append(union, ci.Labels...)
		}
		if !labelsMatch(*c.Labels, union, env) {
			return false
		}
	}
	for _, b := range []struct {
		want *bool
		got  bool
	}{
		{w.ReviewedAtHead, it.ReviewedAtHead}, {w.ChangesRequested, it.ChangesRequested}, {w.Approved, it.Approved},
		{w.Draft, it.Draft}, {w.MergeQueue, it.MergeQueue}, {w.Due, it.Due},
	} {
		if b.want != nil && *b.want != b.got {
			return false
		}
	}
	if w.InactiveFor.IsSet() {
		d := w.InactiveFor.Duration
		if w.InactiveFor.Ref == "{{pr-inactivity-timeout}}" {
			d = env.PRInactivityTimeout
		}
		if d <= 0 || it.LastTrustedActivity.IsZero() || env.Now.Sub(it.LastTrustedActivity) < d {
			return false
		}
	}
	if w.Event != "" && w.Event != it.Event {
		return false
	}

	if len(w.AnyOf) > 0 {
		if !slices.ContainsFunc(w.AnyOf, func(alt When) bool { return alt.holds(r, it, env, runs) }) {
			return false
		}
	}
	return true
}

// policyAllows is whether the rule's dedupe and cooldown let it run now.
func policyAllows(r *Rule, it Item, env Env, runs []PastRun) bool {
	last, ok := lastRun(runs)
	if !ok {
		return true
	}
	if r.Policy.Dedupe == "head" && it.Head != "" && last.Head == it.Head && !last.Failed {
		return false
	}
	c := r.Policy.Cooldown
	if c == nil {
		return true
	}
	wait := c.Success
	if last.Failed {
		wait = c.Failure
	}
	d := wait.Duration
	if wait.Ref == "{{workflow-cooldown}}" {
		d = it.WorkflowCooldown
		if d == 0 {
			d = defaultWorkflowCooldown
		}
	}
	if d == 0 || env.Now.Sub(last.EndedAt) >= d {
		return true
	}
	if slices.Contains(c.ResetOn, "head") && it.Head != "" && last.Head != it.Head {
		return true
	}
	if c.SkipWhen != nil && c.SkipWhen.holds(r, it, env, runs) {
		return true
	}
	return false
}

// defaultWorkflowCooldown is a workflow definition's cooldown when it sets
// none.
const defaultWorkflowCooldown = 10 * time.Minute

// rulesRuns are the item's runs of one rule, in the order they ended.
func rulesRuns(rule string, runs []PastRun) []PastRun {
	var out []PastRun
	for _, r := range runs {
		if r.Rule == rule {
			out = append(out, r)
		}
	}
	slices.SortStableFunc(out, func(a, b PastRun) int { return a.EndedAt.Compare(b.EndedAt) })
	return out
}

func lastRun(runs []PastRun) (PastRun, bool) {
	if len(runs) == 0 {
		return PastRun{}, false
	}
	return runs[len(runs)-1], true
}

// since is when a count since the events starts: the latest of them.
func since(events Since, it Item) time.Time {
	var t time.Time
	for _, e := range events {
		var at time.Time
		switch e {
		case "head":
			at = it.HeadChangedAt
		case "human":
			at = it.HumanSpokeAt
		case "pause":
			at = it.PausedAt
		}
		if at.After(t) {
			t = at
		}
	}
	return t
}

// countSince counts the runs that started after t.
func countSince(runs []PastRun, t time.Time) int {
	n := 0
	for _, r := range runs {
		if r.StartedAt.After(t) {
			n++
		}
	}
	return n
}

// labelsMatch is whether labels has any of l.Any, all of l.All and none of
// l.None.
func labelsMatch(l Labels, labels []string, env Env) bool {
	if len(l.Any) > 0 && !slices.ContainsFunc(l.Any, func(p string) bool { return hasLabel(labels, env.Prefixes, fill(p, env)) }) {
		return false
	}
	for _, p := range l.All {
		if !hasLabel(labels, env.Prefixes, fill(p, env)) {
			return false
		}
	}
	for _, p := range l.None {
		if hasLabel(labels, env.Prefixes, fill(p, env)) {
			return false
		}
	}
	return true
}

// hasLabel is whether labels has name, case-insensitively; "*/suffix" is
// suffix in any of the prefixes (overseer/review, kcc/review).
func hasLabel(labels, prefixes []string, name string) bool {
	return slices.ContainsFunc(labels, func(l string) bool { return labelIs(l, prefixes, name) })
}

func labelIs(label string, prefixes []string, name string) bool {
	if suffix, ok := strings.CutPrefix(name, "*/"); ok {
		for _, p := range prefixes {
			if strings.EqualFold(label, p+"/"+suffix) {
				return true
			}
		}
		return false
	}
	return name != "" && strings.EqualFold(label, name)
}

// fill fills in {{trigger}}.
func fill(s string, env Env) string { return strings.ReplaceAll(s, "{{trigger}}", env.Trigger) }

// written is the label a write adds for name: "*/suffix" in the last of
// the prefixes, the deployment's own.
func written(name string, env Env) string {
	if suffix, ok := strings.CutPrefix(name, "*/"); ok && len(env.Prefixes) > 0 {
		return env.Prefixes[len(env.Prefixes)-1] + "/" + suffix
	}
	return fill(name, env)
}

// fillInput fills in a recipe input's placeholders.
func fillInput(v string, it Item, env Env) string {
	if strings.Contains(v, "{{pr-labels}}") {
		labels := []string{env.Trigger}
		labels = append(labels, env.PRLabels...)
		labels = append(labels, it.Labels...)
		v = strings.ReplaceAll(v, "{{pr-labels}}", strings.Join(uniqueFold(labels), ","))
	}
	return fill(v, env)
}

// resolveWrites fills in a rule's writes for the item and keeps only what
// changes something: a label it has not, an assignee it lacks.
func resolveWrites(run Run, it Item, env Env) Writes {
	var w Writes
	for _, l := range run.Label {
		var names []string
		if l == "{{closing-issue-labels}}" {
			for _, ci := range it.Closes {
				for _, cl := range ci.Labels {
					if !labelIs(cl, env.Prefixes, "*/ready-for-human") {
						names = append(names, cl)
					}
				}
			}
		} else {
			names = []string{written(l, env)}
		}
		for _, n := range names {
			if n != "" && !hasLabel(it.Labels, nil, n) && !hasLabel(w.Label, nil, n) {
				w.Label = append(w.Label, n)
			}
		}
	}
	for _, l := range run.Unlabel {
		for _, have := range it.Labels {
			if labelIs(have, env.Prefixes, fill(l, env)) && !hasLabel(w.Unlabel, nil, have) {
				w.Unlabel = append(w.Unlabel, have)
			}
		}
	}
	for _, a := range assignees(run.Assign, it, env) {
		if !anyIn(it.Assignees, []string{a}) && !anyIn(w.Assign, []string{a}) {
			w.Assign = append(w.Assign, a)
		}
	}
	for _, a := range assignees(run.Unassign, it, env) {
		if anyIn(it.Assignees, []string{a}) && !anyIn(w.Unassign, []string{a}) {
			w.Unassign = append(w.Unassign, a)
		}
	}
	if run.Comment != "" && run.Stop == nil {
		w.Comment = run.Comment
	}
	if run.Stop != nil && !hasLabel(it.Labels, env.Prefixes, "*/stop") {
		w.Stop, w.Comment = true, run.Stop.Comment
	}
	w.Drop = run.Drop
	return w
}

// assignees fills in an assign or unassign.
func assignees(v string, it Item, env Env) []string {
	switch v {
	case "":
		return nil
	case "{{watcher}}":
		return []string{env.Watcher}
	case "{{pool}}":
		var out []string
		for _, a := range it.Assignees {
			if anyIn([]string{a}, env.Pool) {
				out = append(out, a)
			}
		}
		return out
	case "{{closing-issue-assignees}}":
		var out []string
		for _, ci := range it.Closes {
			for _, a := range ci.Assignees {
				if !anyIn([]string{a}, env.Pool) && !strings.EqualFold(a, env.Watcher) {
					out = append(out, a)
				}
			}
		}
		return uniqueFold(out)
	}
	return []string{v}
}

// applyLabels is labels after w's label writes.
func applyLabels(labels []string, w Writes) []string {
	out := slices.Clone(labels)
	out = slices.DeleteFunc(out, func(l string) bool { return hasLabel(w.Unlabel, nil, l) })
	return append(out, w.Label...)
}

func anyIn(have, want []string) bool {
	for _, h := range have {
		for _, w := range want {
			if h != "" && strings.EqualFold(h, w) {
				return true
			}
		}
	}
	return false
}

func uniqueFold(in []string) []string {
	var out []string
	for _, s := range in {
		if s != "" && !anyIn(out, []string{s}) {
			out = append(out, s)
		}
	}
	return out
}

func orNone(s string) string {
	if s == "" {
		return "none"
	}
	return s
}

// String is the decision in a line: what the replay and shadow logs show.
func (d Decision) String() string {
	what := d.Recipe
	if d.Apply != "" {
		what += "→" + d.Apply
	}
	if d.Recipe == "" {
		what = fmt.Sprintf("%+v", d.Writes)
	}
	return fmt.Sprintf("%s %s#%d %s %s", d.Rule, d.Kind, d.Number, d.Outcome, what)
}
