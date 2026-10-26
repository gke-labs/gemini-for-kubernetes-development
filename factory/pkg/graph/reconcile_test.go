package graph

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
)

// abc is a plan's steps: a, then b and c side by side (maxActive 2), then
// d after both, then finally.
func abc(t *testing.T, extra string) *Spec {
	t.Helper()
	s, err := ParsePlanBlock("```yaml graph\nmaxActive: 2\nsteps:\n" +
		"  - {id: a, title: A, body: 'step a'}\n" +
		"  - {id: b, after: [a], title: B, body: 'after #{{ (step \"a\").Issue }}'}\n" +
		"  - {id: c, after: [a], title: C, body: c, then: plan}\n" +
		"  - {id: d, after: [b, c], title: D, body: d" + extra + "}\n" +
		"finally: {title: F, body: f}\n```\n")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.ValidatePlan(); err != nil {
		t.Fatal(err)
	}
	return s
}

func observed(s *Spec, children map[string][]Child, st State) Observed {
	return Observed{
		Parent:   Parent{Number: 100, Open: true},
		Source:   "plan",
		Spec:     s,
		Children: children,
		State:    st,
		Trigger:  "overseer",
	}
}

func open(n int) Child {
	return Child{Number: n, Open: true, Linked: true, Labels: []string{"overseer"}}
}
func closed(n int) Child { return Child{Number: n, Linked: true} }

// kinds is a result's actions, as kind:step, for comparing.
func kinds(r Result) []string {
	var out []string
	for _, a := range r.Actions {
		out = append(out, a.Kind+":"+a.Step)
	}
	return out
}

func states(r Result) string {
	var parts []string
	for _, s := range r.Steps {
		parts = append(parts, s.ID+"="+s.State)
	}
	return strings.Join(parts, " ")
}

func TestReconcileProgress(t *testing.T) {
	s := abc(t, "")
	for _, tc := range []struct {
		name     string
		children map[string][]Child
		state    State
		phase    string
		actions  []string
		steps    string
	}{
		{"start: the first step only", nil, State{}, PhaseRunning,
			[]string{"create-child:a"},
			"a=Active b=Pending c=Pending d=Pending finally=Pending"},
		{"a running: nothing more", map[string][]Child{"a": {open(101)}}, State{}, PhaseRunning,
			nil,
			"a=Active b=Pending c=Pending d=Pending finally=Pending"},
		{"a done: b and c together, within maxActive 2", map[string][]Child{"a": {closed(101)}}, State{}, PhaseRunning,
			[]string{"create-child:b", "create-child:c"},
			"a=Succeeded b=Active c=Active d=Pending finally=Pending"},
		{"b done, c running: d waits for both", map[string][]Child{"a": {closed(101)}, "b": {closed(102)}, "c": {open(103)}}, State{}, PhaseRunning,
			nil,
			"a=Succeeded b=Succeeded c=Active d=Pending finally=Pending"},
		{"c skipped counts as done: d starts", map[string][]Child{"a": {closed(101)}, "b": {closed(102)}, "c": {{Number: 103, NotPlanned: true, Linked: true}}}, State{}, PhaseRunning,
			[]string{"create-child:d"},
			"a=Succeeded b=Succeeded c=Skipped d=Active finally=Pending"},
		{"every step done: finally", map[string][]Child{"a": {closed(101)}, "b": {closed(102)}, "c": {closed(103)}, "d": {closed(104)}}, State{}, PhaseRunning,
			[]string{"create-child:finally"},
			"a=Succeeded b=Succeeded c=Succeeded d=Succeeded finally=Active"},
		{"finally done: the parent closes", map[string][]Child{"a": {closed(101)}, "b": {closed(102)}, "c": {closed(103)}, "d": {closed(104)}, FinallyID: {closed(105)}}, State{}, PhaseSucceeded,
			[]string{"close-parent:"},
			"a=Succeeded b=Succeeded c=Succeeded d=Succeeded finally=Succeeded"},
		{"a reopened: active again, b and c wait", map[string][]Child{"a": {open(101)}}, State{}, PhaseRunning,
			nil,
			"a=Active b=Pending c=Pending d=Pending finally=Pending"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := Reconcile(observed(s, tc.children, tc.state))
			if r.Phase != tc.phase || !slices.Equal(kinds(r), tc.actions) || states(r) != tc.steps {
				t.Errorf("phase %s (%s), actions %v, steps %s\nwant %s, %v, %s", r.Phase, r.Reason, kinds(r), states(r), tc.phase, tc.actions, tc.steps)
			}
		})
	}
}

// What a created child carries: its title and body rendered (a step it
// comes after referenced by number), the labels its then gives, and the
// marker the executor appends.
func TestReconcileCreate(t *testing.T) {
	s := abc(t, "")
	r := Reconcile(observed(s, map[string][]Child{"a": {closed(101)}}, State{}))
	var b, c Action
	for _, a := range r.Actions {
		switch a.Step {
		case "b":
			b = a
		case "c":
			c = a
		}
	}
	if b.Title != "B" || b.Body != "after #101" || !slices.Equal(b.Labels, []string{"overseer"}) {
		t.Errorf("b = %+v", b)
	}
	if !slices.Equal(c.Labels, []string{"overseer/plan"}) {
		t.Errorf("c (then: plan) labels = %v", c.Labels)
	}
	if b.Child != (ChildInfo{Parent: 100, Source: "plan", Step: "b", Depth: 1, Params: HashParams(nil)}) {
		t.Errorf("b's marker = %+v", b.Child)
	}
	if r.State.Params != HashParams(nil) {
		t.Errorf("state params = %q", r.State.Params)
	}
	m, ok := ParseChildMarker("body\n\n" + b.Child.Marker())
	if !ok || m != b.Child {
		t.Errorf("marker round trip: %+v, %v", m, ok)
	}
}

func TestReconcileRoutesAndLabels(t *testing.T) {
	s, err := ParsePlanBlock("```yaml graph\nmaxActive: 3\ndefaults: {labels: [area/x]}\nsteps:\n" +
		"  - {id: f, title: F, body: f, labels: [kind/y], review: Look closely.}\n" +
		"  - {id: p, title: P, body: p, then: plan}\n" +
		"  - {id: q, title: Q, body: q, then: ask, assignees: [alice]}\n```\n")
	if err != nil {
		t.Fatal(err)
	}
	r := Reconcile(observed(s, nil, State{}))
	got := map[string]Action{}
	for _, a := range r.Actions {
		got[a.Step] = a
	}
	if l := got["f"].Labels; !slices.Equal(l, []string{"overseer", "area/x", "kind/y", "overseer/review"}) {
		t.Errorf("fix labels %v", l)
	}
	if !strings.Contains(got["f"].Body, "## Review Instructions\n\nLook closely.") {
		t.Errorf("review not appended: %q", got["f"].Body)
	}
	if l := got["p"].Labels; !slices.Equal(l, []string{"overseer/plan"}) {
		t.Errorf("plan labels %v", l)
	}
	if l := got["q"].Labels; !slices.Equal(l, []string{"overseer/needs-human", "overseer/stop"}) {
		t.Errorf("ask labels %v", l)
	}
	// An ask step's child says it waits for a person, and what they can do;
	// it is assigned to the step's assignees. No other step's says so.
	if b := got["q"].Body; !strings.HasPrefix(b, "q\n\n## Needs a person\n\nThis step of #100's plan") ||
		!strings.Contains(b, "remove `overseer/stop` and label it `overseer` to have it fixed") || !strings.Contains(b, "remove `overseer/stop` and label it `overseer/plan` to have it planned first") {
		t.Errorf("ask body %q", b)
	}
	if !slices.Equal(got["q"].Assignees, []string{"alice"}) || len(got["f"].Assignees) != 0 {
		t.Errorf("assignees q %v, f %v", got["q"].Assignees, got["f"].Assignees)
	}
	if strings.Contains(got["f"].Body, "Needs a person") || strings.Contains(got["p"].Body, "Needs a person") {
		t.Errorf("a fix or plan step's child asks for a person: %q / %q", got["f"].Body, got["p"].Body)
	}
	// An open child to be fixed that lost a label gets it back; one to be
	// planned does not (its plan label is a command the plan rule takes off).
	r = Reconcile(observed(s, map[string][]Child{
		"f": {{Number: 1, Open: true, Linked: true, Labels: []string{"overseer"}}},
		"p": {{Number: 2, Open: true, Linked: true}},
		"q": {{Number: 3, Open: true, Linked: true, Labels: []string{"overseer/needs-human"}}},
	}, State{}))
	if !slices.Equal(kinds(r), []string{"add-child-labels:f"}) || !slices.Equal(r.Actions[0].Labels, []string{"area/x", "kind/y", "overseer/review"}) {
		t.Errorf("re-applied %v %v", kinds(r), r.Actions)
	}
}

func TestReconcileRepair(t *testing.T) {
	s := abc(t, "")
	r := Reconcile(observed(s, map[string][]Child{
		"a":    {open(105), open(101), closed(99)},
		"b":    {{Number: 110, Open: true, Labels: []string{"overseer"}}}, // not linked
		"gone": {closed(120)},
	}, State{}))
	want := []string{"close-duplicate:a", "link-child:b"}
	if !slices.Equal(kinds(r), want) {
		t.Fatalf("actions %v, want %v", kinds(r), want)
	}
	if d := r.Actions[0]; d.Issue != 105 || d.Of != 101 {
		t.Errorf("duplicate %+v: want 105 closed as a duplicate of 101 (the oldest open)", d)
	}
	if !slices.Equal(r.Orphans, []int{120}) {
		t.Errorf("orphans %v", r.Orphans)
	}
}

// A merged bot PR referencing an open child completes it, and the steps
// after it start in the same pass.
func TestReconcileMergedBotPR(t *testing.T) {
	s := abc(t, "")
	a := open(101)
	a.MergedBotPR = 500
	r := Reconcile(observed(s, map[string][]Child{"a": {a}}, State{}))
	want := []string{"close-child-completed:a", "create-child:b", "create-child:c"}
	if !slices.Equal(kinds(r), want) || r.Actions[0].Of != 500 {
		t.Errorf("actions %v (%+v), want %v", kinds(r), r.Actions[0], want)
	}
}

func TestReconcileCheckpoints(t *testing.T) {
	doneA := map[string][]Child{"a": {closed(101)}, "b": {closed(102)}, "c": {closed(103)}, "d": {closed(104)}}
	for _, tc := range []struct {
		name, checkpoint string
		state            State
		phase            string
		actions          []string
		labels           []string
	}{
		{"stop: the stop label, and the pass ends", ", checkpoint: stop", State{}, PhasePaused,
			[]string{"add-parent-labels:d"}, []string{"overseer/stop"}},
		{"replan: the plan label, and the pass goes on", ", checkpoint: replan", State{}, PhaseRunning,
			[]string{"add-parent-labels:d", "create-child:finally"}, []string{"overseer/plan"}},
		{"both", ", checkpoint: [replan, stop]", State{}, PhasePaused,
			[]string{"add-parent-labels:d"}, []string{"overseer/plan", "overseer/stop"}},
		{"fired already: not again (the stop label was taken off)", ", checkpoint: stop", State{Checkpoints: []string{"d"}}, PhaseRunning,
			[]string{"create-child:finally"}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := Reconcile(observed(abc(t, tc.checkpoint), doneA, tc.state))
			if r.Phase != tc.phase || !slices.Equal(kinds(r), tc.actions) {
				t.Fatalf("phase %s, actions %v; want %s, %v", r.Phase, kinds(r), tc.phase, tc.actions)
			}
			if tc.labels != nil && !slices.Equal(r.Actions[0].Labels, tc.labels) {
				t.Errorf("labels %v, want %v", r.Actions[0].Labels, tc.labels)
			}
			if !r.State.fired("d") {
				t.Error("the checkpoint is not recorded")
			}
		})
	}
}

func TestReconcilePhases(t *testing.T) {
	s := abc(t, "")
	for _, tc := range []struct {
		name    string
		mutate  func(*Observed)
		phase   string
		actions []string
	}{
		{"stop label: paused, nothing written", func(o *Observed) { o.Parent.Labels = []string{"overseer/stop"} }, PhasePaused, nil},
		{"closed by a person: cancelled", func(o *Observed) { o.Parent.Open, o.Parent.ClosedBy = false, ClosedByPerson }, PhaseCancelled, nil},
		{"closed by a commit with steps left: reopened, and it goes on", func(o *Observed) { o.Parent.Open, o.Parent.ClosedBy = false, ClosedByCommit },
			PhaseRunning, []string{"reopen-parent:", "create-child:a"}},
		{"completed: done, even reopened", func(o *Observed) { o.State.Completed = true }, PhaseSucceeded, nil},
		{"completed and closed: done", func(o *Observed) { o.State.Completed, o.Parent.Open = true, false }, PhaseSucceeded, nil},
		{"steps that do not parse: invalid", func(o *Observed) { o.Spec, o.SpecErr = nil, errors.New("bad block") }, PhaseInvalid, nil},
		{"at the depth limit: invalid", func(o *Observed) { o.Parent.Depth = 3 }, PhaseInvalid, nil},
		{"below a raised limit: runs", func(o *Observed) { o.Parent.Depth, o.MaxDepth = 3, 5 }, PhaseRunning, []string{"create-child:a"}},
		{"params changed after start: blocked", func(o *Observed) {
			o.Params = map[string]string{"kind": "Bar"}
			o.State.Params = HashParams(map[string]string{"kind": "Foo"})
		}, PhaseBlocked, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o := observed(s, nil, State{})
			tc.mutate(&o)
			r := Reconcile(o)
			if r.Phase != tc.phase || !slices.Equal(kinds(r), tc.actions) {
				t.Errorf("phase %s (%s), actions %v; want %s, %v", r.Phase, r.Reason, kinds(r), tc.phase, tc.actions)
			}
		})
	}
}

// A child's depth is its parent's + 1; a nested parent at depth 1 makes
// depth-2 children.
func TestReconcileDepth(t *testing.T) {
	o := observed(abc(t, ""), nil, State{})
	o.Parent.Depth = 1
	r := Reconcile(o)
	if len(r.Actions) != 1 || r.Actions[0].Child.Depth != 2 {
		t.Errorf("actions %+v", r.Actions)
	}
}

// The same input always gives the same result: map order does not leak.
func TestReconcileDeterministic(t *testing.T) {
	s := abc(t, "")
	children := map[string][]Child{"a": {closed(101)}, "b": {open(103), open(102)}, "x": {closed(9)}, "y": {closed(8)}}
	first := fmt.Sprint(Reconcile(observed(s, children, State{})))
	for range 20 {
		if got := fmt.Sprint(Reconcile(observed(s, children, State{}))); got != first {
			t.Fatalf("results differ:\n%s\n%s", first, got)
		}
	}
}

func TestStateBlock(t *testing.T) {
	st := State{Completed: true, Checkpoints: []string{"d", "b"}, Params: "abc", Children: map[string]int{"a": 101}}
	got, ok := ParseState("status\n" + st.Block())
	if !ok || !got.Completed || !slices.Equal(got.Checkpoints, []string{"b", "d"}) || got.Children["a"] != 101 || got.Params != "abc" {
		t.Errorf("round trip: %+v, %v", got, ok)
	}
	if _, ok := ParseState("no state here"); ok {
		t.Error("parsed a state from nothing")
	}
	if HashParams(map[string]string{"a": "1", "b": "2"}) != HashParams(map[string]string{"b": "2", "a": "1"}) {
		t.Error("the params hash depends on order")
	}
}
