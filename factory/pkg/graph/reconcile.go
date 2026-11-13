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
	StepPending = "Pending"
	StepReady   = "Ready"
	StepActive  = "Active"
	// StepInGroup is a step of a grouped graph whose work is in its
	// group's PR (pushed, and ready: GroupPR.Ready), the step after it continuing
	// that PR: done but for the PR's merge. It counts toward neither
	// maxActive nor completion.
	StepInGroup   = "InGroup"
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
	// Grouped is the start block's grouped: consecutive steps share one
	// PR (Spec.GroupPrev).
	Grouped bool
}

// Parent is the graph's parent issue, as observed.
type Parent struct {
	Number int
	// URL is the parent's page (its html_url): the status's diagram links
	// each step's issue beside it.
	URL  string
	Open bool
	// ClosedBy is who closed a closed parent: ClosedByPerson or
	// ClosedByCommit.
	ClosedBy string
	Labels   []string
	// Humans are the people among the parent's assignees - neither the
	// pool's bots, the watcher, nor an app - as Observe reads them: every
	// child is assigned to them, at creation and when one is added later.
	Humans []string
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
	Linked    bool
	Labels    []string
	Assignees []string
	// MergedBotPR is a bot-authored PR referencing the open child that has
	// merged, 0 for none: the child is done all the same.
	MergedBotPR int
	// GroupPR is, in a grouped graph, the open bot PR that fixes the open
	// child of a step another continues (Spec.GroupNext); nil for none, or
	// when not read.
	GroupPR *GroupPR
	// MergedPR is, for a closed child, the newest bot PR referencing it
	// that merged; read only when a step after it requires labels
	// (Step.Requires), 0 otherwise. MergedPRLabels are its labels - or, for
	// an open child, MergedBotPR's. See WorkPR.
	MergedPR       int
	MergedPRLabels []string
	// OpenPRs are, for an open child, the open bot PRs that fix it (their
	// body says so: "Fixes #n"), with their labels: where the parent's
	// labels the template names (Spec.PRLabelsFromParent) go.
	OpenPRs []PRRef
}

// PRRef is a PR as observed: its number and labels.
type PRRef struct {
	Number int
	Labels []string
}

// WorkPR is the PR carrying the child's step's work, and its labels, for a
// later step's requires: its group's open PR, else the bot PR that merged
// for it; 0 for none known (no PR, or not read).
func (c Child) WorkPR() (int, []string) {
	switch {
	case c.GroupPR != nil:
		return c.GroupPR.Number, c.GroupPR.Labels
	case c.MergedPR > 0:
		return c.MergedPR, c.MergedPRLabels
	case c.MergedBotPR > 0:
		return c.MergedBotPR, c.MergedPRLabels
	}
	return 0, nil
}

// GroupPR is an open bot PR a step's work was pushed to, as observed.
type GroupPR struct {
	Number int
	Author string
	// Labels are the PR's labels, for a later step's requires.
	Labels []string
	// Ready is that the step after may start on it: the PR is as the
	// watch's ready rule would hand it to people, were it not a draft -
	// green, mergeable, no trusted feedback waiting, and reviewed at its
	// head without changes asked when a review is wanted (Options.StepReady);
	// without the watch, that its head's checks all passed.
	Ready bool
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
	// Grouped is the graph's grouped: the status draws each group's PR.
	Grouped bool
	// MissingLabels are labels the graph names that the repository lacks
	// (and the watcher may not create): the pass's writes went without
	// them, and the status names them, for someone to create.
	MissingLabels []string
	// IssuesURL is where the repository's issues are
	// (https://github.com/<owner>/<repo>/issues/), from the parent's page:
	// the diagram links each step's issue there. Empty: no links.
	IssuesURL string
}

// StepStatus is one step's state, and its child (0 for none yet).
type StepStatus struct {
	ID    string
	State string
	Child int
	// PR is, in a grouped graph, the group's PR the step's work is in, as
	// observed; 0 when not known.
	PR int
	// Waiting says what a Pending step whose after steps are done waits
	// for: the labels its requires names, on the PR missing them.
	Waiting string
}

// Kinds of action (Action.Kind).
const (
	ActCreateChild         = "create-child"
	ActLinkChild           = "link-child"
	ActCloseDuplicate      = "close-duplicate"
	ActAddChildLabels      = "add-child-labels"
	ActRemoveChildLabels   = "remove-child-labels"
	ActAddChildAssignees   = "add-child-assignees"
	ActCloseChildCompleted = "close-child-completed"
	ActAddParentLabels     = "add-parent-labels"
	ActReopenParent        = "reopen-parent"
	ActCloseParent         = "close-parent"
	// ActMarkGroupPRReady marks a group's PR ready for review when the
	// group's last step closed needing no change: no push of its marks it.
	ActMarkGroupPRReady = "mark-group-pr-ready"
	// ActAddPRLabels and ActRemovePRLabels keep the parent's labels the
	// template names (Spec.PRLabelsFromParent) on the open PRs carrying the
	// children's work.
	ActAddPRLabels    = "add-pr-labels"
	ActRemovePRLabels = "remove-pr-labels"
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
	// Assignees are, for a child to create, the step's and the parent's
	// humans (childAssignees), given it when it is created (GitHub
	// notifies them); for an open child, the parent's humans added since
	// they were last handed down that it lacks. AskPerson is a then: ask
	// step's child: with no one from either, it goes to whoever approved
	// the graph (approverAssignees).
	Assignees []string
	AskPerson bool
	// Title, Body and Child are a child to create: the rendered title,
	// body (without the marker) and what its marker says.
	Title string
	Body  string
	Child ChildInfo
	// As is, for a group's PR to mark ready, its author: the watcher may
	// not change a PR on someone's fork, its author may
	// (Options.MarkReadyAs).
	As string
}

// Reconcile decides one pass over a graph: from the steps, the params and
// what GitHub holds, the phase, every step's state and the actions to
// take. It reads no clock and does no I/O: the same input always gives the
// same result.
func Reconcile(o Observed) Result {
	r := Result{State: o.State.clone(), Phase: PhaseRunning, Grouped: o.Grouped, IssuesURL: issuesURL(o.Parent.URL)}
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

	r.Steps = statuses(s, ids, canonical, o.Grouped)

	// 2. The stop label: only the status - and the PR labels the parent no
	// longer has taken off, so stopping a graph and taking its
	// overseer/auto-merge off does stop its PRs merging. Nothing is added.
	if containsFold(o.Parent.Labels, stop) {
		r.Phase, r.Reason = PhasePaused, "the parent has "+stop
		var prActions []Action
		prActions, r.State.PRLabels = prLabels(o, ids, canonical, false)
		r.Actions = append(r.Actions, prActions...)
		return r
	}

	// 4. Params changed after start: likewise.
	hash := HashParams(o.Params)
	if o.State.Params != "" && o.State.Params != hash {
		r.Phase, r.Reason = PhaseBlocked, "the parent's params changed after the graph started; restore them, or file a new parent"
		var prActions []Action
		prActions, r.State.PRLabels = prLabels(o, ids, canonical, false)
		r.Actions = append(r.Actions, prActions...)
		return r
	}

	// 5. Repair: duplicates, and children not yet linked.
	r.Actions = append(r.Actions, duplicates...)
	for _, id := range ids {
		if c, ok := canonical[id]; ok && !c.Linked {
			r.Actions = append(r.Actions, Action{Kind: ActLinkChild, Step: id, Issue: c.Number})
		}
	}

	// 6. Active children: labels a child to be fixed must keep, the
	// parent's humans added since they were last handed down, and
	// children done by a merged bot PR.
	added := missingLogins(o.State.Inherited, o.Parent.Humans)
	for _, id := range ids {
		c, ok := canonical[id]
		if !ok || !c.Open {
			continue
		}
		st := s.Step(id)
		switch {
		case st.ThenOrDefault() == ThenFix && o.Grouped && inGroup(s, id, c, canonical):
			// Its work is in its group's PR, which a later step carries on:
			// its review request is the later step's to make, or the PR
			// would be reviewed again for steps already reviewed - so it
			// is taken off, and its labels are no longer kept.
			if review := o.Trigger + "/review"; containsFold(c.Labels, review) {
				r.Actions = append(r.Actions, Action{Kind: ActRemoveChildLabels, Step: id, Issue: c.Number, Labels: []string{review}})
			}
		case st.ThenOrDefault() == ThenFix:
			// A plan or needs-human label is taken off when it is acted on: only a
			// child to be fixed keeps its labels.
			if missing := missingLabels(c.Labels, childLabels(s, *st, o.Trigger)); len(missing) > 0 {
				r.Actions = append(r.Actions, Action{Kind: ActAddChildLabels, Step: id, Issue: c.Number, Labels: missing})
			}
		}
		// A child a merged bot PR completes is closed below: assigning it
		// first would only notify people of a closed issue.
		if missing := missingLogins(c.Assignees, added); len(missing) > 0 && c.MergedBotPR == 0 {
			r.Actions = append(r.Actions, Action{Kind: ActAddChildAssignees, Step: id, Issue: c.Number, Assignees: missing})
		}
		if c.MergedBotPR > 0 {
			r.Actions = append(r.Actions, Action{Kind: ActCloseChildCompleted, Step: id, Issue: c.Number, Of: c.MergedBotPR})
			c.Open = false
			canonical[id] = c
		}
	}
	// A group whose last step closed without a push of its own - needing
	// no change, or closed by a person - an earlier step's work in an open
	// PR: no last push marks that PR ready, so the reconciler does, as the
	// PR's author (a PR ready already is left as it is).
	if o.Grouped {
		for _, st := range s.Steps {
			if s.GroupPrev(st.ID) == "" || s.GroupNext(st.ID) != "" {
				continue // not a group's last step
			}
			if c, ok := canonical[st.ID]; ok && !c.Open && c.GroupPR == nil {
				if carrier := groupCarrier(s, st.ID, canonical); carrier != "" {
					pr := canonical[carrier].GroupPR
					r.Actions = append(r.Actions, Action{Kind: ActMarkGroupPRReady, Step: st.ID, Issue: pr.Number, As: pr.Author})
				}
			}
		}
	}
	// The parent's labels the template names on the open PRs carrying the
	// children's work (a child a merged PR completes has none left open).
	var prActions []Action
	prActions, r.State.PRLabels = prLabels(o, ids, canonical, true)
	r.Actions = append(r.Actions, prActions...)
	// Who is handed down from now on: the parent's humans as they are, so
	// one taken off the parent and put back is handed down again.
	r.State.Inherited = slices.Clone(o.Parent.Humans)
	r.Steps = statuses(s, ids, canonical, o.Grouped)
	// A step whose open child waits for a person - a fix that was blocked,
	// or a then: ask step - says so in the status.
	for i, ss := range r.Steps {
		if c, ok := canonical[ss.ID]; ok && c.Open && ss.Waiting == "" && containsFold(c.Labels, o.Trigger+"/"+NeedsHumanLabel) {
			r.Steps[i].Waiting = fmt.Sprintf("a person (#%d is labelled %s/%s)", c.Number, o.Trigger, NeedsHumanLabel)
		}
	}
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
		info := ChildInfo{Parent: o.Parent.Number, Source: o.Source, Step: ss.ID, Depth: o.Parent.Depth + 1, Params: hash, NoChange: st.NoChange == NoChangeOK}
		if o.Grouped {
			info, body = groupChild(s, ss.ID, canonical, info, body)
		}
		r.Actions = append(r.Actions, Action{
			Kind: ActCreateChild, Step: ss.ID, Title: strings.TrimSpace(title), Body: body,
			Labels:    childLabels(s, *st, o.Trigger),
			Assignees: childAssignees(st.Assignees, o.Parent.Humans),
			// A child whose description asks (its own ## Questions) waits
			// for an answer too: it goes to a person all the same.
			AskPerson: st.ThenOrDefault() == ThenAsk || len(OpenQuestions(body)) > 0,
			Child:     info,
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

// groupChild is a grouped graph's child's place in its group's PR, and
// its body saying so: the first of a group opens the PR, as a draft; a
// step continuing one commits on its PR's branch, as its author, and the
// last marks it ready.
func groupChild(s *Spec, id string, canonical map[string]Child, info ChildInfo, body string) (ChildInfo, string) {
	next := s.GroupNext(id)
	// The PR to continue: the nearest earlier step's of the group that has
	// one, past steps that closed needing no change (no-change: ok).
	carrier := groupCarrier(s, id, canonical)
	var note string
	switch {
	case carrier != "":
		pr := canonical[carrier].GroupPR
		info.Group, info.PR, info.As, info.Last = GroupNext, pr.Number, pr.Author, next == ""
		first := carrier
		for p := s.GroupPrev(first); p != ""; p = s.GroupPrev(first) {
			first = p
		}
		info.In = canonical[first].Number
		note = fmt.Sprintf("This step continues #%d, step `%s`'s pull request (the graph is grouped): commit on its branch, and #%d fixes this issue too.", pr.Number, carrier, pr.Number)
		if info.Last {
			note += " It is the group's last step: the pull request is marked ready for review once it is pushed."
		}
	case next != "":
		info.Group = GroupFirst
		note = fmt.Sprintf("This step opens a draft pull request that the steps after it (from `%s`) continue (the graph is grouped); it is marked ready for review after the group's last step.", next)
	default:
		return info, body
	}
	return info, strings.TrimRight(body, "\n") + "\n\n## Pull request\n\n" + note
}

// groupCarrier is, in a grouped graph, the earlier step of id's group whose
// PR id continues: its group predecessor if that one's work is in an open
// PR, else - past predecessors whose issues closed needing no change - the
// nearest earlier one whose is. "" for none (the group's PR is id's to
// open, or it merged).
func groupCarrier(s *Spec, id string, canonical map[string]Child) string {
	for p := s.GroupPrev(id); p != ""; p = s.GroupPrev(p) {
		c, ok := canonical[p]
		switch {
		case !ok:
			return ""
		case c.GroupPR != nil:
			return p
		case c.Open:
			return "" // its work not in a PR yet
		}
		// Closed without the group's PR merging (that would close the
		// earlier steps too): it needed no change. Look further back.
	}
	return ""
}

// statuses is every step's state, from its canonical child and its
// dependencies. In a grouped graph a step whose work is in its group's PR
// - pushed and ready (GroupPR.Ready), or the step after it already started
// on it - is StepInGroup, and the step after it may start.
func statuses(s *Spec, ids []string, canonical map[string]Child, grouped bool) []StepStatus {
	out := make([]StepStatus, 0, len(ids))
	state := map[string]string{}
	stateOf := func(id string) string {
		c, ok := canonical[id]
		switch {
		case ok && c.Open && grouped && inGroup(s, id, c, canonical):
			return StepInGroup
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
				if !done(state[d]) && (state[d] != StepInGroup || s.GroupPrev(id) != d) {
					v = StepPending
					break
				}
			}
		}
		var waiting string
		if v == StepReady {
			if waiting = unmetRequires(s.Step(id), canonical); waiting != "" {
				v = StepPending
			}
		}
		ss := StepStatus{ID: id, State: v, Child: canonical[id].Number, Waiting: waiting}
		if pr := canonical[id].GroupPR; pr != nil {
			ss.PR = pr.Number
		}
		out = append(out, ss)
	}
	return out
}

func done(state string) bool { return state == StepSucceeded || state == StepSkipped }

// unmetRequires is what step st, its after steps done, still waits for:
// the labels its requires names, on the first PR carrying an after step's
// work that lacks them ("" when none does). An after step with no PR known
// - skipped, closed needing no change, or closed by a person - has nothing
// to check. Only a step not yet started is asked: once its child exists,
// a label taken off later does not take it back.
func unmetRequires(st *Step, canonical map[string]Child) string {
	if st == nil || st.Requires.IsZero() {
		return ""
	}
	for _, d := range st.After {
		pr, labels := canonical[d].WorkPR()
		if pr == 0 || st.Requires.Labels.Match(labels) {
			continue
		}
		return fmt.Sprintf("#%d needs %s", pr, st.Requires.Labels)
	}
	return ""
}

// issuesURL is where a parent's repository keeps its issues, from the
// parent's own page (https://github.com/o/r/issues/123 → …/issues/); ""
// for anything else.
func issuesURL(parent string) string {
	i := strings.LastIndex(parent, "/issues/")
	if i < 0 || !strings.HasPrefix(parent, "https://") {
		return ""
	}
	return parent[:i+len("/issues/")]
}

// inGroup is whether the open child c of step id has its work in its
// group's PR, for the step after it to continue: its PR is known, and
// ready (GroupPR.Ready), or the step after it has a child already (a later
// push to the PR that is not ready yet does not take the step back).
func inGroup(s *Spec, id string, c Child, canonical map[string]Child) bool {
	next := s.GroupNext(id)
	if next == "" || c.GroupPR == nil {
		return false
	}
	_, started := canonical[next]
	return c.GroupPR.Ready || started
}

// NeedsHumanLabel, under the trigger, marks the child of a then: ask step:
// it waits for a person, and no bot works on it until one decides - the
// child also carries the stop label, which every rule and the intake of the
// watcher's own issues respect (the graph files its children as the
// watcher).
const NeedsHumanLabel = "needs-human"

// needsHumanSection is what a then: ask step's child says under the step's
// own text: that it waits for a person, and what that person can do.
func needsHumanSection(parent int, trigger string) string {
	return fmt.Sprintf(`## Needs a person

This step of #%d's plan waits for a decision: no bot works on it until a person makes it - it carries `+"`%s/stop`"+` until then. What it needs is described above. Then either:

- remove `+"`%s/stop`"+` and label it `+"`%s`"+` to have it fixed (one PR),
- remove `+"`%s/stop`"+` and label it `+"`%s/plan`"+` to have it planned first,
- do it yourself and close it as completed, or
- close it as not planned to skip the step.

The steps after it start once it is closed.`, parent, trigger, trigger, trigger, trigger, trigger)
}

// prLabels decides the parent's labels the template names
// (Spec.PRLabelsFromParent) on the open PRs fixing the open children: each
// PR gets those the parent has that it lacks (only when add: a paused or
// blocked graph adds nothing), and loses those the graph put there
// (State.PRLabels) that the parent no longer has. It returns the actions and
// what the graph has put on each PR from now on - what it recorded and the
// parent still has, and what it adds now; a label a person put on first is
// not recorded, so never taken off. A PR no longer open (merged, closed) or
// no longer fixing an open child drops out of the record: nothing is taken
// off it.
func prLabels(o Observed, ids []string, canonical map[string]Child, add bool) ([]Action, map[int][]string) {
	var want []string
	for _, l := range o.Spec.PRLabelsFromParent {
		if containsFold(o.Parent.Labels, l) && !containsFold(want, l) {
			want = append(want, l)
		}
	}
	type openPR struct {
		step   string
		labels []string
	}
	prs := map[int]openPR{}
	var numbers []int
	for _, id := range ids {
		c, ok := canonical[id]
		if !ok || !c.Open {
			continue
		}
		for _, pr := range c.OpenPRs {
			if _, seen := prs[pr.Number]; !seen {
				prs[pr.Number] = openPR{step: id, labels: pr.Labels}
				numbers = append(numbers, pr.Number)
			}
		}
	}
	slices.Sort(numbers)
	var actions []Action
	record := map[int][]string{}
	for _, n := range numbers {
		pr := prs[n]
		var keep, drop []string
		for _, l := range o.State.PRLabels[n] {
			switch {
			case containsFold(want, l):
				keep = append(keep, l)
			case containsFold(pr.labels, l):
				drop = append(drop, l)
			}
		}
		if missing := missingLabels(pr.labels, want); add && len(missing) > 0 {
			actions = append(actions, Action{Kind: ActAddPRLabels, Step: pr.step, Issue: n, Labels: missing})
			for _, l := range missing {
				if !containsFold(keep, l) {
					keep = append(keep, l)
				}
			}
		}
		if len(drop) > 0 {
			actions = append(actions, Action{Kind: ActRemovePRLabels, Step: pr.step, Issue: n, Labels: drop})
		}
		if len(keep) > 0 {
			record[n] = keep
		}
	}
	if len(record) == 0 {
		record = nil
	}
	return actions, record
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
		return []string{trigger + "/" + NeedsHumanLabel, trigger + "/stop"}
	}
	labels := []string{trigger}
	labels = append(labels, s.Defaults.Labels...)
	labels = append(labels, st.Labels...)
	if st.Review != "" {
		labels = append(labels, trigger+"/review")
	}
	var out []string
	for _, l := range labels {
		if !containsFold(out, l) {
			out = append(out, l)
		}
	}
	return out
}

func missingLabels(have, want []string) []string {
	var out []string
	for _, l := range want {
		if !containsFold(have, l) {
			out = append(out, l)
		}
	}
	return out
}

// childAssignees is who a step's child is assigned to when it is created:
// the step's own assignees, then the parent's humans - as a pool's PR
// takes the human assignees of the issues it closes
// ({{closing-issue-assignees}}) - each once.
func childAssignees(step, humans []string) []string {
	var out []string
	for _, a := range append(slices.Clone(step), humans...) {
		if a != "" && !containsFold(out, a) {
			out = append(out, a)
		}
	}
	return out
}

// missingLogins are the logins of want not in have, each once.
func missingLogins(have, want []string) []string {
	var out []string
	for _, a := range want {
		if a != "" && !containsFold(have, a) && !containsFold(out, a) {
			out = append(out, a)
		}
	}
	return out
}

// containsFold is whether list holds s, ignoring case, as GitHub compares
// labels and logins.
func containsFold(list []string, s string) bool {
	return slices.ContainsFunc(list, func(l string) bool { return strings.EqualFold(l, s) })
}

func firstErr(errs ...error) error {
	for _, err := range errs {
		if err != nil {
			return err
		}
	}
	return nil
}
