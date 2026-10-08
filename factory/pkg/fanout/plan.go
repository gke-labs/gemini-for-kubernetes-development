package fanout

import (
	"fmt"
	"slices"
	"sort"
	"strings"
)

// Child is a child issue as GitHub has it.
type Child struct {
	Number int
	// Key is the item the child is for; empty for the final child.
	Key   string
	Final bool
	Title string
	Body  string
	Open  bool
	// NotPlanned is a child closed as not planned: a person skipped it.
	NotPlanned bool
	// Labelled is a child carrying the trigger label.
	Labelled bool
	// PRs are the pull requests that reference the child.
	PRs []PR
}

// PR is a pull request referencing a child.
type PR struct {
	Number int
	Open   bool
	Merged bool
}

// Input is everything a pass decides from.
type Input struct {
	Parent      int
	ParentTitle string
	Spec        Spec
	Children    []Child
	// State is the progress comment's, nil when there is none (or it was
	// lost): it is then recomputed from the children.
	State *State
	// Stopped is a parent carrying the stop label.
	Stopped      bool
	TriggerLabel string
	StopLabel    string
}

// Plan is what a pass does, in the order the caller should do it: rewrite,
// create, label, stop, then write State to the progress comment, then close
// the parent.
type Plan struct {
	State   State
	Rewrite []Rewrite
	Create  []NewChild
	Label   []Labelling
	// Stop, when set, is the comment to post with the stop label.
	Stop        string
	CloseParent bool
}

// Rewrite brings a child not yet labelled up to date with the spec.
type Rewrite struct {
	Number int
	Key    string
	Title  string
	Body   string
}

// NewChild is a child to create, labelled at once when Labels is set.
type NewChild struct {
	Key    string
	Final  bool
	Title  string
	Body   string
	Labels []string
}

// Labelling labels an existing child.
type Labelling struct {
	Number int
	Key    string
	Labels []string
}

// view is the input indexed: each item's child, and the final child.
type view struct {
	in     Input
	byKey  map[string]Child
	final  *Child
	labels []string
}

func newView(in Input) view {
	v := view{in: in, byKey: map[string]Child{}}
	children := slices.Clone(in.Children)
	sort.Slice(children, func(i, j int) bool { return children[i].Number < children[j].Number })
	for _, c := range children {
		switch {
		case c.Final:
			if v.final == nil {
				c := c
				v.final = &c
			}
		case c.Key != "":
			if _, dup := v.byKey[c.Key]; !dup {
				v.byKey[c.Key] = c
			}
		}
	}
	v.labels = append([]string{in.TriggerLabel}, in.Spec.Settings.Labels...)
	v.labels = slices.Compact(v.labels)
	return v
}

// done is the number of items whose child is closed, and whether every item
// is done.
func (v view) done() (int, bool) {
	n := 0
	for _, it := range v.in.Spec.Items {
		if c, ok := v.byKey[it.Key]; ok && !c.Open {
			n++
		}
	}
	return n, n == len(v.in.Spec.Items)
}

// Decide works out a pass. It is pure: the same input gives the same plan.
func Decide(in Input) Plan {
	v := newView(in)
	st := v.account()
	p := Plan{State: st}

	if in.Stopped {
		return p
	}

	done, allDone := v.done()
	if reached := v.checkpoints(&p.State, done); len(reached) > 0 {
		p.Stop = v.checkpointComment(done)
		return p
	}

	v.createAndLabel(&p)

	if allDone {
		v.finish(&p)
	}
	return p
}

// account moves the window for what closed since the last pass: +1 for a
// child closed as completed, halved for a PR closed unmerged. A lost state
// is recomputed instead.
func (v view) account() State {
	set := v.in.Spec.Settings
	if v.in.State == nil {
		return v.recompute()
	}
	st := v.in.State.clone()
	children := v.itemChildren()
	for _, c := range children {
		if !c.Open && !st.counted(c.Number) {
			st.count(c.Number)
			if !c.NotPlanned {
				st.Window++
			}
		}
	}
	for _, c := range children {
		for _, pr := range c.PRs {
			if !pr.Open && !pr.Merged && !st.counted(pr.Number) {
				st.count(pr.Number)
				st.Window = max(1, st.Window/2)
			}
		}
	}
	st.Window = min(max(st.Window, 1), set.Window.Max)
	v.remember(&st)
	return st
}

// recompute rebuilds a lost state: the window is start plus the children
// completed, and the checkpoints at or below the items done are passed.
func (v view) recompute() State {
	set := v.in.Spec.Settings
	st := State{Window: set.Window.Start}
	for _, c := range v.itemChildren() {
		if !c.Open {
			st.count(c.Number)
			if !c.NotPlanned {
				st.Window++
			}
		}
		for _, pr := range c.PRs {
			if !pr.Open && !pr.Merged {
				st.count(pr.Number)
			}
		}
		if c.Labelled {
			st.started(c.Number)
		}
	}
	st.Window = min(st.Window, set.Window.Max)
	done, _ := v.done()
	for _, cp := range set.Checkpoints {
		if cp <= done && !slices.Contains(st.Checkpoints, cp) {
			st.Checkpoints = append(st.Checkpoints, cp)
		}
	}
	v.remember(&st)
	return st
}

// remember records the children found in the state.
func (v view) remember(st *State) {
	if st.Children == nil {
		st.Children = map[string]int{}
	}
	for k, c := range v.byKey {
		st.Children[k] = c.Number
	}
	if v.final != nil {
		st.Final = v.final.Number
	}
}

// itemChildren are the children for items, the spec's or not, in number
// order: the window moves for every one of them.
func (v view) itemChildren() []Child {
	var out []Child
	for _, c := range v.byKey {
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Number < out[j].Number })
	return out
}

// checkpoints marks the checkpoints reached and not yet passed as passed,
// and returns them.
func (v view) checkpoints(st *State, done int) []int {
	var reached []int
	for _, cp := range v.in.Spec.Settings.Checkpoints {
		if cp <= done && !slices.Contains(st.Checkpoints, cp) {
			reached = append(reached, cp)
			st.Checkpoints = append(st.Checkpoints, cp)
		}
	}
	return reached
}

func (v view) checkpointComment(done int) string {
	var finished []string
	for _, it := range v.in.Spec.Items {
		c, ok := v.byKey[it.Key]
		if !ok || c.Open {
			continue
		}
		s := fmt.Sprintf("#%d", c.Number)
		if pr := mainPR(c); pr != nil {
			s += fmt.Sprintf(" → PR #%d", pr.Number)
		}
		if c.NotPlanned {
			s += " (skipped)"
		}
		finished = append(finished, s)
	}
	return fmt.Sprintf("Fan-out checkpoint: %d done: %s.\n\nEdit the spec if needed, then remove `%s` to continue. Children not yet started are rewritten from the edited spec.",
		done, strings.Join(finished, ", "), v.in.StopLabel)
}

// createAndLabel labels the next items in the window, creating or rewriting
// their children as needed, and with create: all, creates and rewrites the
// rest unlabelled.
func (v view) createAndLabel(p *Plan) {
	spec := v.in.Spec
	active := 0
	for _, c := range v.byKey {
		if c.Open && c.Labelled {
			active++
		}
	}
	slots := p.State.Window - active
	for _, it := range spec.Items {
		title := spec.ChildTitle(v.in.Parent, v.in.ParentTitle, it)
		body := spec.ChildBody(v.in.Parent, v.in.ParentTitle, it)
		c, exists := v.byKey[it.Key]
		if !exists {
			switch {
			case slots > 0:
				p.Create = append(p.Create, NewChild{Key: it.Key, Title: title, Body: body, Labels: v.labels})
				slots--
			case spec.Settings.Create == CreateAll:
				p.Create = append(p.Create, NewChild{Key: it.Key, Title: title, Body: body})
			}
			continue
		}
		if !c.Open || c.Labelled || slices.Contains(p.State.Started, c.Number) {
			continue
		}
		if c.Title != title || !sameText(c.Body, body) {
			p.Rewrite = append(p.Rewrite, Rewrite{Number: c.Number, Key: it.Key, Title: title, Body: body})
		}
		if slots > 0 {
			p.Label = append(p.Label, Labelling{Number: c.Number, Key: it.Key, Labels: v.labels})
			p.State.started(c.Number)
			slots--
		}
	}
}

// finish runs the final step once every item is done: the final child, and
// when it is closed (or there is none), the parent.
func (v view) finish(p *Plan) {
	spec := v.in.Spec
	if strings.TrimSpace(spec.Finally) == "" {
		p.CloseParent = true
		return
	}
	switch c := v.final; {
	case c == nil:
		p.Create = append(p.Create, NewChild{Final: true, Title: spec.FinalTitle(v.in.ParentTitle), Body: spec.FinalBody(v.in.Parent), Labels: v.labels})
	case !c.Open:
		p.CloseParent = true
	case !c.Labelled && !slices.Contains(p.State.Started, c.Number):
		p.Label = append(p.Label, Labelling{Number: c.Number, Labels: v.labels})
		p.State.started(c.Number)
	}
}

// mainPR is the PR to show for a child: a merged one, else an open one, else
// the newest.
func mainPR(c Child) *PR {
	var best *PR
	for i := range c.PRs {
		pr := &c.PRs[i]
		switch {
		case best == nil:
			best = pr
		case pr.Merged && !best.Merged:
			best = pr
		case pr.Open && !best.Merged && !best.Open:
			best = pr
		case pr.Merged == best.Merged && pr.Open == best.Open && pr.Number > best.Number:
			best = pr
		}
	}
	return best
}
