package graph

import (
	"fmt"
	"slices"
	"strings"
)

// Phases of a graph (Result.Phase).
const (
	PhaseRunning   = "Running"
	PhaseBlocked   = "Blocked"
	PhasePaused    = "Paused"
	PhaseSucceeded = "Succeeded"
	PhaseCancelled = "Cancelled"
	PhaseInvalid   = "Invalid"
)

// States of a step (StepStatus.State).
const (
	StepPending   = "Pending"
	StepReady     = "Ready"
	StepActive    = "Active"
	StepSucceeded = "Succeeded"
	StepSkipped   = "Skipped"
)

// What closed the parent (Parent.ClosedBy).
const (
	ClosedByPerson = "person"
	ClosedByCommit = "commit" // a PR's "Fixes #N", or a commit
)

// Observed is what a pass reads of GitHub, for Reconcile to decide on.
type Observed struct {
	Parent Parent
	// Source names where the steps came from: a template's name, or
	// "plan". It goes into every child's marker.
	Source string
	// Spec is the steps; nil with SpecErr when they could not be read or
	// are not valid.
	Spec    *Spec
	SpecErr error
	// Params are the params the parent supplies (a template's); nil for a
	// plan.
	Params map[string]string
	// Children are the issues marked as this parent's, by step id (the
	// finally step's under FinallyID), as found on GitHub.
	Children map[string][]Child
	// State is the status comment's state block, the zero State before the
	// first pass.
	State State
	// Trigger is the deployment's trigger label: the label a child to be
	// fixed gets, and the prefix of the plan, needs-human and stop labels.
	Trigger string
	// MaxDepth is how deep graphs may nest: a parent at this depth or more
	// starts none. 0 is the default, 3.
	MaxDepth int
}

// Parent is the graph's parent issue, as observed.
type Parent struct {
	Number int
	Open   bool
	// ClosedBy is who closed a closed parent: ClosedByPerson or
	// ClosedByCommit.
	ClosedBy string
	Labels   []string
	// Assignees are the parent's: an ask step's child, the plan naming no
	// one, goes to its humans among them (askAssignees).
	Assignees []string
	// Depth is the parent's own depth: 0 for an issue that is no graph's
	// child, a child's marker depth otherwise.
	Depth int
}

// Child is one issue marked as a step's child, as observed.
type Child struct {
	Number int
	Open   bool
	// NotPlanned is a closed child closed as not planned or as a
	// duplicate: its step is skipped.
	NotPlanned bool
	// Linked is whether it is a sub-issue of the parent.
	Linked bool
	Labels []string
	// MergedBotPR is a bot-authored PR referencing the open child that has
	// merged, 0 for none: the child is done all the same.
	MergedBotPR int
}

// Result is what Reconcile decides: the phase, every step's state, the
// actions to apply in order, and the state to keep.
type Result struct {
	Phase string
	// Reason says why the phase is Invalid, Blocked or Paused.
	Reason  string
	Steps   []StepStatus
	Actions []Action
	State   State
	// Orphans are children of steps no longer in the definition.
	Orphans []int
}

// StepStatus is one step's state, and its child (0 for none yet).
type StepStatus struct {
	ID    string
	State string
	Child int
}

// Kinds of action (Action.Kind).
const (
	ActCreateChild         = "create-child"
	ActLinkChild           = "link-child"
	ActCloseDuplicate      = "close-duplicate"
	ActAddChildLabels      = "add-child-labels"
	ActCloseChildCompleted = "close-child-completed"
	ActAddParentLabels     = "add-parent-labels"
	ActReopenParent        = "reopen-parent"
	ActCloseParent         = "close-parent"
)

// Action is one write a pass makes. Every action is safe to repeat.
type Action struct {
	Kind string
	Step string
	// Issue is the issue acted on: a child, its duplicate, or the parent.
	Issue int
	// Of is the canonical child a duplicate is closed as a duplicate of,
	// or the PR a child is closed as completed by.
	Of     int
	Labels []string
	// Assignees are the step's, given its child when it is created (GitHub
	// notifies them). AskPerson is a then: ask step's child: with no
	// assignees of the step's, it goes to the parent's humans, else to
	// whoever approved the graph (askAssignees).
	Assignees []string
	AskPerson bool
	// Title, Body and Child are a child to create: the rendered title,
	// body (without the marker) and what its marker says.
	Title string
	Body  string
	Child ChildInfo
}

// Reconcile decides one pass over a graph: from the steps, the params and
// what GitHub holds, the phase, every step's state and the actions to
// take. It reads no clock and does no I/O: the same input always gives the
// same result.
func Reconcile(o Observed) Result {
	r := Result{State: o.State.clone(), Phase: PhaseRunning}
	stop := o.Trigger + "/stop"
	maxDepth := o.MaxDepth
	if maxDepth <= 0 {
		maxDepth = 3
	}

	// 1. A closed parent.
	if !o.Parent.Open {
		switch {
		case o.State.Completed:
			r.Phase = PhaseSucceeded
			return r
		case o.Parent.ClosedBy == ClosedByCommit:
			r.Actions = append(r.Actions, Action{Kind: ActReopenParent, Issue: o.Parent.Number})
		default:
			r.Phase, r.Reason = PhaseCancelled, "closed by a person"
			return r
		}
	}
	if o.State.Completed {
		// Completion is final: a parent reopened afterwards is left alone.
		r.Phase = PhaseSucceeded
		return r
	}

	// 3. Steps that cannot be used.
	switch {
	case o.SpecErr != nil:
		r.Phase, r.Reason = PhaseInvalid, o.SpecErr.Error()
		return r
	case o.Spec == nil:
		r.Phase, r.Reason = PhaseInvalid, "no steps"
		return r
	case o.Parent.Depth >= maxDepth:
		r.Phase, r.Reason = PhaseInvalid, fmt.Sprintf("the parent is at depth %d; graphs nest at most %d deep", o.Parent.Depth, maxDepth)
		return r
	}
	s := o.Spec

	// The canonical child of each step, and the duplicates; orphans.
	canonical := map[string]Child{}
	var duplicates []Action
	ids := stepIDs(s)
	for id, children := range o.Children {
		if !slices.Contains(ids, id) {
			for _, c := range children {
				r.Orphans = append(r.Orphans, c.Number)
			}
			continue
		}
		canon, dups := pickCanonical(children)
		canonical[id] = canon
		for _, d := range dups {
			duplicates = append(duplicates, Action{Kind: ActCloseDuplicate, Step: id, Issue: d.Number, Of: canon.Number})
		}
	}
	slices.Sort(r.Orphans)
	slices.SortFunc(duplicates, func(a, b Action) int { return a.Issue - b.Issue })

	r.Steps = statuses(s, ids, canonical)

	// 2. The stop label: only the status.
	if hasLabel(o.Parent.Labels, stop) {
		r.Phase, r.Reason = PhasePaused, "the parent has "+stop
		return r
	}

	// 4. Params changed after start.
	hash := HashParams(o.Params)
	if o.State.Params != "" && o.State.Params != hash {
		r.Phase, r.Reason = PhaseBlocked, "the parent's params changed after the graph started; restore them, or file a new parent"
		return r
	}

	// 5. Repair: duplicates, and children not yet linked.
	r.Actions = append(r.Actions, duplicates...)
	for _, id := range ids {
		if c, ok := canonical[id]; ok && !c.Linked {
			r.Actions = append(r.Actions, Action{Kind: ActLinkChild, Step: id, Issue: c.Number})
		}
	}

	// 6. Active children: labels a child to be fixed must keep, and
	// children done by a merged bot PR.
	for _, id := range ids {
		c, ok := canonical[id]
		if !ok || !c.Open {
			continue
		}
		st := s.Step(id)
		if st.ThenOrDefault() == ThenFix {
			// A plan or needs-human label is taken off when it is acted on: only a
			// child to be fixed keeps its labels.
			if missing := missingLabels(c.Labels, childLabels(s, *st, o.Trigger)); len(missing) > 0 {
				r.Actions = append(r.Actions, Action{Kind: ActAddChildLabels, Step: id, Issue: c.Number, Labels: missing})
			}
		}
		if c.MergedBotPR > 0 {
			r.Actions = append(r.Actions, Action{Kind: ActCloseChildCompleted, Step: id, Issue: c.Number, Of: c.MergedBotPR})
			c.Open = false
			canonical[id] = c
		}
	}
	r.Steps = statuses(s, ids, canonical)
	state := map[string]string{}
	for _, ss := range r.Steps {
		state[ss.ID] = ss.State
	}

	// 7. Checkpoints: once each, when their step is done. A stop ends the
	// pass.
	for _, st := range s.Steps {
		if len(st.Checkpoint) == 0 || !done(state[st.ID]) || r.State.fired(st.ID) {
			continue
		}
		var labels []string
		if st.Checkpoint.Has(CheckpointReplan) {
			labels = append(labels, o.Trigger+"/plan")
		}
		if st.Checkpoint.Has(CheckpointStop) {
			labels = append(labels, stop)
		}
		r.Actions = append(r.Actions, Action{Kind: ActAddParentLabels, Step: st.ID, Issue: o.Parent.Number, Labels: labels})
		r.State.Checkpoints = append(r.State.Checkpoints, st.ID)
		if st.Checkpoint.Has(CheckpointStop) {
			r.Phase, r.Reason = PhasePaused, "checkpoint "+st.ID
			return r
		}
	}

	// 8. Create: ready steps, in definition order, within maxActive.
	active := 0
	for _, ss := range r.Steps {
		if ss.State == StepActive {
			active++
		}
	}
	children := map[string]StepRef{}
	for id, c := range canonical {
		children[id] = StepRef{Issue: c.Number}
	}
	for i, ss := range r.Steps {
		if ss.State != StepReady || active >= s.ActiveLimit() {
			continue
		}
		st := s.Step(ss.ID)
		title, terr := s.Render(ss.ID, st.Title, o.Params, children)
		body, berr := s.Render(ss.ID, st.Body, o.Params, children)
		if terr != nil || berr != nil {
			r.Phase, r.Reason = PhaseInvalid, fmt.Sprintf("step %s does not render: %v", ss.ID, firstErr(terr, berr))
			return r
		}
		if st.Review != "" {
			review, err := s.Render(ss.ID, st.Review, o.Params, children)
			if err != nil {
				r.Phase, r.Reason = PhaseInvalid, fmt.Sprintf("step %s's review does not render: %v", ss.ID, err)
				return r
			}
			body = strings.TrimRight(body, "\n") + "\n\n## Review Instructions\n\n" + review
		}
		if st.ThenOrDefault() == ThenAsk {
			body = strings.TrimRight(body, "\n") + "\n\n" + needsHumanSection(o.Parent.Number, o.Trigger)
		}
		r.Actions = append(r.Actions, Action{
			Kind: ActCreateChild, Step: ss.ID, Title: strings.TrimSpace(title), Body: body,
			Labels:    childLabels(s, *st, o.Trigger),
			Assignees: slices.Clone(st.Assignees),
			AskPerson: st.ThenOrDefault() == ThenAsk,
			Child:     ChildInfo{Parent: o.Parent.Number, Source: o.Source, Step: ss.ID, Depth: o.Parent.Depth + 1, Params: hash},
		})
		r.Steps[i].State = StepActive
		active++
		if r.State.Params == "" {
			r.State.Params = hash
		}
	}

	// 9. Complete: every step, and the finally step if there is one, done.
	for _, ss := range r.Steps {
		if !done(ss.State) {
			return r
		}
	}
	r.Actions = append(r.Actions, Action{Kind: ActCloseParent, Issue: o.Parent.Number})
	r.State.Completed = true
	r.Phase = PhaseSucceeded
	return r
}

// stepIDs are the steps' ids in definition order, the finally step last.
func stepIDs(s *Spec) []string {
	var ids []string
	for _, st := range s.Steps {
		ids = append(ids, st.ID)
	}
	if s.Finally != nil {
		ids = append(ids, FinallyID)
	}
	return ids
}

// pickCanonical is a step's child - the oldest open one, else the newest
// closed - and the other open ones, duplicates of it.
func pickCanonical(children []Child) (Child, []Child) {
	sorted := slices.Clone(children)
	slices.SortFunc(sorted, func(a, b Child) int { return a.Number - b.Number })
	var open []Child
	for _, c := range sorted {
		if c.Open {
			open = append(open, c)
		}
	}
	if len(open) > 0 {
		return open[0], open[1:]
	}
	return sorted[len(sorted)-1], nil
}

// statuses is every step's state, from its canonical child and its
// dependencies.
func statuses(s *Spec, ids []string, canonical map[string]Child) []StepStatus {
	out := make([]StepStatus, 0, len(ids))
	state := map[string]string{}
	stateOf := func(id string) string {
		c, ok := canonical[id]
		switch {
		case ok && c.Open:
			return StepActive
		case ok && c.NotPlanned:
			return StepSkipped
		case ok:
			return StepSucceeded
		}
		return ""
	}
	for _, st := range s.Steps {
		if v := stateOf(st.ID); v != "" {
			state[st.ID] = v
		}
	}
	for _, id := range ids {
		v := stateOf(id)
		if v == "" {
			v = StepReady
			deps := s.Step(id).After
			if id == FinallyID {
				deps = ids[:len(ids)-1]
			}
			for _, d := range deps {
				if !done(state[d]) {
					v = StepPending
					break
				}
			}
		}
		out = append(out, StepStatus{ID: id, State: v, Child: canonical[id].Number})
	}
	return out
}

func done(state string) bool { return state == StepSucceeded || state == StepSkipped }

// NeedsHumanLabel, under the trigger, marks the child of a then: ask step:
// it waits for a person, and no bot works on it until one decides.
const NeedsHumanLabel = "needs-human"

// needsHumanSection is what a then: ask step's child says under the step's
// own text: that it waits for a person, and what that person can do.
func needsHumanSection(parent int, trigger string) string {
	return fmt.Sprintf(`## Needs a person

This step of #%d's plan waits for a decision: no bot works on it until a person makes it. What it needs is described above. Then either:

- label it `+"`%s`"+` to have it fixed (one PR),
- label it `+"`%s/plan`"+` to have it planned first,
- do it yourself and close it as completed, or
- close it as not planned to skip the step.

The steps after it start once it is closed.`, parent, trigger, trigger)
}

// childLabels are the labels a step's child is created with, by what it
// does first: to be fixed, the trigger label, the defaults, the step's own
// and the review label when it has review instructions; to be planned,
// <trigger>/plan; to wait for a person, <trigger>/needs-human.
func childLabels(s *Spec, st Step, trigger string) []string {
	switch st.ThenOrDefault() {
	case ThenPlan:
		return []string{trigger + "/plan"}
	case ThenAsk:
		return []string{trigger + "/" + NeedsHumanLabel}
	}
	labels := []string{trigger}
	labels = append(labels, s.Defaults.Labels...)
	labels = append(labels, st.Labels...)
	if st.Review != "" {
		labels = append(labels, trigger+"/review")
	}
	var out []string
	for _, l := range labels {
		if !hasLabel(out, l) {
			out = append(out, l)
		}
	}
	return out
}

func missingLabels(have, want []string) []string {
	var out []string
	for _, l := range want {
		if !hasLabel(have, l) {
			out = append(out, l)
		}
	}
	return out
}

func hasLabel(labels []string, label string) bool {
	return slices.ContainsFunc(labels, func(l string) bool { return strings.EqualFold(l, label) })
}

func firstErr(errs ...error) error {
	for _, err := range errs {
		if err != nil {
			return err
		}
	}
	return nil
}
