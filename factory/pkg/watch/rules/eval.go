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
// decisions, which is what the replay and the summary check compare.
type Item struct {
	// Kind is the rule kind the item is for: OnIssue or OnPR.
	Kind   string
	Number int

	Labels    []string
	Assignees []string
	// Author opened the item; AuthorAssociation is GitHub's word for the
	// author's standing in the repository (OWNER, MEMBER, COLLABORATOR,
	// CONTRIBUTOR, NONE...): AuthorKind reads both.
	Author            string
	AuthorAssociation string `json:",omitempty"`
	UpdatedAt         time.Time
	// Closed is the item closed (a PR merged or closed): a gate.
	Closed bool

	// Issues.
	// LinkedPR is "open" when an open PR closes the issue (the shared
	// scan's: cheap); ConnectedPR, when one is connected to it on its
	// timeline (a read of its own, ReadFacts).
	LinkedPR    string
	ConnectedPR string
	// Workflow names the workflow definition the issue's body names.
	Workflow string
	// WorkflowMissing is that the definition it names cannot be loaded:
	// missing, or not a workflow it can run.
	WorkflowMissing bool
	// WorkflowCooldown is that definition's cooldown, 0 for its default.
	WorkflowCooldown time.Duration
	// FanoutParent is whether the issue carries the fanout label;
	// GraphParent, the graph label.
	FanoutParent bool
	GraphParent  bool `json:",omitempty"`
	// Plan is the shape of the issue's plan comment (When.Plan): pr or
	// graph, "" for none (a read of its own, FactPlan).
	Plan string `json:",omitempty"`
	// QuestionsOpen is that the issue's description still asks
	// (When.Questions).
	QuestionsOpen bool `json:",omitempty"`

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

	// The overseer's history of the item.
	// TaskActive is a task of the item queued or running: a group decides
	// nothing while one is.
	TaskActive bool
	// Queued are the rules whose task for the item is queued or running:
	// none of them decides again until it ends.
	Queued []string
	// Runs are the runs of the item's tasks that ended, by rule.
	Runs []PastRun
	// Unknown are the facts the scanner has not read (FactConnectedPR,
	// FactWorkflowCooldown, FactWorkflowDefinition, FactPlan on an issue; FactHistory,
	// FactFeedback, FactChecks on a PR): deciding, a predicate
	// on one fails and a cooldown it sets holds - nothing is decided on a
	// fact not read; asking which a decision needs (Set.Needs), they could
	// go either way.
	Unknown []string `json:",omitempty"`
	// assume is Set.Needs' asking: an unknown fact could go either way.
	assume bool
	// HeadChangedAt, HumanSpokeAt and PausedAt are when the PR's head last
	// changed, a trusted human last commented and the watcher last put it
	// on hold (its comment attaching the stop label): what a count since
	// head, human or pause starts from. A count since a pause is of the
	// runs after the hold was lifted, since none runs while it is on.
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
	// Members are the logins the overseer trusts (its allowlisted users
	// and bots, its reviewers): authors of kind member, whatever GitHub
	// says of them.
	Members []string `json:",omitempty"`
	// PRLabels are the labels the configuration adds to a PR a fix opens,
	// besides the trigger and the issue's ({{pr-labels}}).
	PRLabels []string
	// PRInactivityTimeout is {{pr-inactivity-timeout}}; 0 is never.
	PRInactivityTimeout time.Duration
	// MinNumber is the overseer's minNumber: items numbered below it are
	// gated, the repository's history ignored. 0 gates none.
	MinNumber int
	Now       time.Time
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
	// Phase orders the run's task in the queue (the rule's policy.phase).
	Phase int
	// Writes are the GitHub writes, placeholders filled in and only what
	// changes something.
	Writes Writes
	// Hooks are a recipe run's lifecycle hooks (Run.OnStart, OnSuccess,
	// OnFailure, OnEnd), filled in for the item as Writes are.
	Hooks HookWrites
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
	return s.Explain(it, env).Decisions()
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
	}
	if !w.admitsAuthor(it, env) {
		return false
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
	if w.ConnectedPR != "" {
		switch {
		case it.unknown(FactConnectedPR):
			if !it.assume {
				return false
			}
		case w.ConnectedPR != orNone(it.ConnectedPR):
			return false
		}
	}
	if w.Questions != "" && (w.Questions == "open") != it.QuestionsOpen {
		return false
	}
	if w.Plan != "" && !it.could(FactPlan) && (it.notRead(FactPlan) || w.Plan != orNone(it.Plan)) {
		return false
	}
	switch w.Workflow {
	case "none":
		if it.Workflow != "" {
			return false
		}
	case "any", "missing":
		if it.Workflow == "" {
			return false
		}
		if it.unknown(FactWorkflowDefinition) {
			if !it.assume {
				return false
			}
		} else if it.WorkflowMissing != (w.Workflow == "missing") {
			return false
		}
	}
	if w.OpenedBy == "watcher" && (env.Watcher == "" || !strings.EqualFold(it.Author, env.Watcher)) {
		return false
	}
	if w.Fanout != nil && it.FanoutParent != *w.Fanout {
		return false
	}
	if w.Graph != nil && it.GraphParent != *w.Graph {
		return false
	}

	if w.Checks != "" && !it.could(FactChecks) && (it.notRead(FactChecks) || w.Checks != it.Checks) {
		return false
	}
	if w.Mergeable != nil && (it.Mergeable == nil || *it.Mergeable != *w.Mergeable) {
		return false
	}
	if w.Feedback != "" && it.notRead(FactFeedback) {
		return false
	}
	switch {
	case it.could(FactFeedback):
	case w.Feedback == "new-trusted":
		if !it.NewTrustedFeedback {
			return false
		}
	case w.Feedback == "none":
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
		fact string
	}{
		{w.ReviewedAtHead, it.ReviewedAtHead, FactHistory}, {w.ChangesRequested, it.ChangesRequested, FactHistory},
		{w.Approved, it.Approved, FactChecks}, {w.Draft, it.Draft, ""},
	} {
		if b.want == nil || it.could(b.fact) {
			continue
		}
		if it.notRead(b.fact) || *b.want != b.got {
			return false
		}
	}
	if w.InactiveFor.IsSet() {
		d := w.InactiveFor.Duration
		if w.InactiveFor.Ref == "{{pr-inactivity-timeout}}" {
			d = env.PRInactivityTimeout
		}
		switch {
		case d <= 0 || it.notRead(FactHistory):
			return false
		case it.could(FactHistory):
		case it.LastTrustedActivity.IsZero() || env.Now.Sub(it.LastTrustedActivity) < d:
			return false
		}
	}

	if len(w.AnyOf) > 0 {
		if !slices.ContainsFunc(w.AnyOf, func(alt When) bool { return alt.holds(r, it, env, runs) }) {
			return false
		}
	}
	return true
}

// The facts a scanner reads only when a rule needs them (Item.Unknown,
// Set.Needs), each a read of its own.
const (
	// FactConnectedPR is Item.ConnectedPR: the issue's timeline.
	FactConnectedPR = "connected-pr"
	// FactWorkflowCooldown is Item.WorkflowCooldown: the cooldown the
	// workflow definition the issue names declares, fetched.
	FactWorkflowCooldown = "workflow-cooldown"
	// FactWorkflowDefinition is Item.WorkflowMissing: whether that
	// definition can be loaded, fetched.
	FactWorkflowDefinition = "workflow-definition"
	// FactPlan is Item.Plan: the issue's comments, its plan comment's
	// shape.
	FactPlan = "plan"
	// FactHistory is what a PR's commits, comments and reviews say:
	// Item.ReviewedAtHead, ChangesRequested, LastTrustedActivity and the
	// times a give-up counts from (HeadChangedAt, HumanSpokeAt, PausedAt).
	FactHistory = "history"
	// FactFeedback is Item.NewTrustedFeedback: the PR's feedback weighed.
	FactFeedback = "feedback"
	// FactChecks is Item.Checks and Approved: the PR head's checks.
	FactChecks = "checks"
)

// unknown reports whether the item's fact has not been read.
func (it Item) unknown(fact string) bool { return slices.Contains(it.Unknown, fact) }

// notRead is whether a predicate on fact fails for not being read: the
// fact is unknown and the engine is deciding, not asking (Set.Needs,
// Set.Settled), when it could go either way. "" is a fact always read.
func (it Item) notRead(fact string) bool { return fact != "" && it.unknown(fact) && !it.assume }

// could is whether a predicate on fact holds for not being read: the
// engine asking, the fact unknown, it could go either way.
func (it Item) could(fact string) bool { return fact != "" && it.unknown(fact) && it.assume }

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

// giveUpRuns are the runs a give-up counts: every one, or the failed ones.
func giveUpRuns(g *GiveUp, runs []PastRun) []PastRun {
	if !g.Failed {
		return runs
	}
	var out []PastRun
	for _, r := range runs {
		if r.Failed {
			out = append(out, r)
		}
	}
	return out
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
			// Never ready-for-human; and once the PR is handed to the
			// humans, not */review either, which would set bot reviews
			// going again on a PR a person now owns.
			ready := hasLabel(it.Labels, env.Prefixes, "*/ready-for-human")
			for _, ci := range it.Closes {
				for _, cl := range ci.Labels {
					if labelIs(cl, env.Prefixes, "*/ready-for-human") || (ready && labelIs(cl, env.Prefixes, "*/review")) {
						continue
					}
					names = append(names, cl)
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
		if (a == Bot || anyIn(it.Assignees, []string{a})) && !anyIn(w.Unassign, []string{a}) {
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

// Bot is the bot a task runs as, in an on-start hook's assign or
// unassign: known when the task starts, filled in by the watcher then.
const Bot = "{{bot}}"

// HookWrites are a recipe run's lifecycle hooks filled in for an item.
type HookWrites struct {
	OnStart, OnSuccess, OnFailure, OnEnd Writes
}

// Hooks are rule's hooks filled in for the item: what a decision on it
// carries, and what the watcher fills in again when the task starts, on
// the facts read then. {{bot}} stays for the watcher to fill in.
func Hooks(r *Rule, it Item, env Env) HookWrites {
	var h HookWrites
	if r == nil {
		return h
	}
	fill := func(hook *Hook) Writes {
		if hook == nil {
			return Writes{}
		}
		return resolveWrites(hook.run(), it, env)
	}
	h.OnStart, h.OnSuccess, h.OnFailure, h.OnEnd = fill(r.Run.OnStart), fill(r.Run.OnSuccess), fill(r.Run.OnFailure), fill(r.Run.OnEnd)
	return h
}

// assignees fills in an assign or unassign.
func assignees(v string, it Item, env Env) []string {
	switch v {
	case "":
		return nil
	case "{{watcher}}":
		return []string{env.Watcher}
	case Bot:
		// Filled in by the watcher when the task starts.
		return []string{Bot}
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

// String is the decision in a line: what the replay and the logs show.
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
