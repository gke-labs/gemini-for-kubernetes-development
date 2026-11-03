package graph

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/graph/graphtest"
)

// groupedGraph is kcc-groupedGraph's shape: prepopulate, types (a group gate:
// reviewed and merged on its own), controller, mockgcp, alignment, one at a
// time, then finally.
const groupedGraph = "apiVersion: factory.gemini.google.com/v1alpha1\nkind: IssueGraph\nmetadata: {name: gf}\nspec:\n" +
	"  maxActive: 1\n  steps:\n" +
	"    - {id: prepopulate, title: P, body: p}\n" +
	"    - {id: types, after: [prepopulate], title: T, body: t, group-gate: true}\n" +
	"    - {id: controller, after: [types], title: C, body: c}\n" +
	"    - {id: mockgcp, after: [controller], title: M, body: m}\n" +
	"    - {id: alignment, after: [mockgcp], title: A, body: a}\n" +
	"  finally: {title: F, body: f}\n"

func groupedSpec(t *testing.T) *Spec {
	t.Helper()
	tp, err := ParseTemplate([]byte(groupedGraph))
	if err != nil {
		t.Fatal(err)
	}
	if err := tp.Validate(); err != nil {
		t.Fatal(err)
	}
	return &tp.Spec
}

// A step continues its one after's PR when both are fix steps and that
// step neither is a group gate, nor has a checkpoint, nor is another
// step's after too; finally never does.
func TestGroupPrevNext(t *testing.T) {
	s := groupedSpec(t)
	for id, want := range map[string]string{"prepopulate": "", "types": "prepopulate", "controller": "", "mockgcp": "controller", "alignment": "mockgcp", FinallyID: ""} {
		if got := s.GroupPrev(id); got != want {
			t.Errorf("GroupPrev(%s) = %q, want %q", id, got, want)
		}
	}
	for id, want := range map[string]string{"prepopulate": "types", "types": "", "controller": "mockgcp", "mockgcp": "alignment", "alignment": ""} {
		if got := s.GroupNext(id); got != want {
			t.Errorf("GroupNext(%s) = %q, want %q", id, got, want)
		}
	}

	// Branching, a checkpoint and a plan step each end a group.
	line, err := ParsePlanBlock("```yaml graph\nsteps:\n" +
		"  - {id: a, title: A, body: a}\n" +
		"  - {id: b, after: [a], title: B, body: b}\n" +
		"  - {id: c, after: [b], title: C, body: c}\n```\n")
	if err != nil {
		t.Fatal(err)
	}
	if p := line.GroupPrev("c"); p != "b" {
		t.Errorf("c after b alone: continues %q, want b", p)
	}
	line.Steps[1].Checkpoint = Checkpoint{CheckpointStop}
	if p := line.GroupPrev("c"); p != "" {
		t.Errorf("c after b, which has a checkpoint: continues %q", p)
	}
	line.Steps[1].Checkpoint = nil
	line.Steps[2].Then = ThenPlan
	if p := line.GroupPrev("c"); p != "" {
		t.Errorf("a plan step continues %q", p)
	}
	line.Steps[2].Then = ""
	line.Steps = append(line.Steps, Step{ID: "d", After: []string{"b"}, Title: "D", Body: "d"})
	if p := line.GroupPrev("c"); p != "" {
		t.Errorf("c and d both after b: c continues %q, want no one", p)
	}
}

func groupedObserved(s *Spec, children map[string][]Child) Observed {
	o := observed(s, children, State{})
	o.Source, o.Grouped = "gf", true
	return o
}

func inPR(c Child, pr int, green bool) Child {
	c.GroupPR = &GroupPR{Number: pr, Author: "lovelace-coder-bot", Ready: green}
	return c
}

func createdInfo(t *testing.T, r Result, step string) (ChildInfo, string) {
	t.Helper()
	for _, a := range r.Actions {
		if a.Kind == ActCreateChild && a.Step == step {
			return a.Child, a.Body
		}
	}
	t.Fatalf("no create-child:%s in %v", step, kinds(r))
	return ChildInfo{}, ""
}

// Grouped: the first step of a group opens a draft PR; the step after it
// starts once that step's work is in the PR with green checks (not when
// it merges), continuing the PR as its author, the last marking it ready;
// a group gate's PR merges before anything after it starts.
func TestReconcileGrouped(t *testing.T) {
	s := groupedSpec(t)
	for _, tc := range []struct {
		name     string
		children map[string][]Child
		actions  []string
		steps    string
	}{
		{"start: prepopulate opens the group's PR", nil,
			[]string{"create-child:prepopulate"},
			"prepopulate=Active types=Pending controller=Pending mockgcp=Pending alignment=Pending finally=Pending"},
		{"prepopulate running, nothing pushed", map[string][]Child{"prepopulate": {open(101)}},
			nil,
			"prepopulate=Active types=Pending controller=Pending mockgcp=Pending alignment=Pending finally=Pending"},
		{"prepopulate pushed, checks not green yet: wait", map[string][]Child{"prepopulate": {inPR(open(101), 500, false)}},
			nil,
			"prepopulate=Active types=Pending controller=Pending mockgcp=Pending alignment=Pending finally=Pending"},
		{"prepopulate pushed and green: types continues its PR (maxActive 1 counts only Active)", map[string][]Child{"prepopulate": {inPR(open(101), 500, true)}},
			[]string{"create-child:types"},
			"prepopulate=InGroup types=Active controller=Pending mockgcp=Pending alignment=Pending finally=Pending"},
		{"types started: a later push not green yet does not take prepopulate back", map[string][]Child{"prepopulate": {inPR(open(101), 500, false)}, "types": {open(102)}},
			nil,
			"prepopulate=InGroup types=Active controller=Pending mockgcp=Pending alignment=Pending finally=Pending"},
		{"types is a group gate: pushed and green, controller still waits for the merge", map[string][]Child{"prepopulate": {inPR(open(101), 500, true)}, "types": {inPR(open(102), 500, true)}},
			nil,
			"prepopulate=InGroup types=Active controller=Pending mockgcp=Pending alignment=Pending finally=Pending"},
		{"merged: controller opens a new group", map[string][]Child{"prepopulate": {closed(101)}, "types": {closed(102)}},
			[]string{"create-child:controller"},
			"prepopulate=Succeeded types=Succeeded controller=Active mockgcp=Pending alignment=Pending finally=Pending"},
		{"mockgcp in the PR: alignment is the group's last", map[string][]Child{"prepopulate": {closed(101)}, "types": {closed(102)}, "controller": {inPR(open(103), 600, true)}, "mockgcp": {inPR(open(104), 600, true)}},
			[]string{"create-child:alignment"},
			"prepopulate=Succeeded types=Succeeded controller=InGroup mockgcp=InGroup alignment=Active finally=Pending"},
		{"alignment pushed: finally waits for the merge", map[string][]Child{"prepopulate": {closed(101)}, "types": {closed(102)}, "controller": {inPR(open(103), 600, true)}, "mockgcp": {inPR(open(104), 600, true)}, "alignment": {inPR(open(105), 600, true)}},
			nil,
			"prepopulate=Succeeded types=Succeeded controller=InGroup mockgcp=InGroup alignment=Active finally=Pending"},
	} {
		r := Reconcile(groupedObserved(s, tc.children))
		if got := kinds(r); strings.Join(got, ",") != strings.Join(tc.actions, ",") {
			t.Errorf("%s: actions %v, want %v", tc.name, got, tc.actions)
		}
		if got := states(r); got != tc.steps {
			t.Errorf("%s: steps\n  %s\nwant\n  %s", tc.name, got, tc.steps)
		}
	}

	r := Reconcile(groupedObserved(s, nil))
	if info, body := createdInfo(t, r, "prepopulate"); info.Group != GroupFirst || info.PR != 0 || info.Last || !strings.Contains(body, "## Pull request") || !strings.Contains(body, "draft pull request") {
		t.Errorf("prepopulate: %+v\n%s", info, body)
	}
	r = Reconcile(groupedObserved(s, map[string][]Child{"prepopulate": {inPR(open(101), 500, true)}}))
	if info, body := createdInfo(t, r, "types"); info.Group != GroupNext || info.PR != 500 || info.As != "lovelace-coder-bot" || !info.Last || info.In != 101 || !strings.Contains(body, "continues #500") || !strings.Contains(body, "marked ready") {
		t.Errorf("types: %+v\n%s", info, body)
	}
	r = Reconcile(groupedObserved(s, map[string][]Child{"prepopulate": {closed(101)}, "types": {closed(102)}, "controller": {inPR(open(103), 600, true)}}))
	if info, _ := createdInfo(t, r, "mockgcp"); info.Group != GroupNext || info.PR != 600 || info.Last || info.In != 103 {
		t.Errorf("mockgcp, in the middle of its group: %+v", info)
	}
	r = Reconcile(groupedObserved(s, map[string][]Child{"prepopulate": {closed(101)}, "types": {closed(102)}, "controller": {inPR(open(103), 600, true)}, "mockgcp": {inPR(open(104), 600, true)}}))
	if info, _ := createdInfo(t, r, "alignment"); info.PR != 600 || !info.Last || info.In != 103 {
		t.Errorf("alignment runs in the group's first step's sandbox (controller, #103): %+v", info)
	}
	r = Reconcile(groupedObserved(s, map[string][]Child{"prepopulate": {closed(101)}, "types": {closed(102)}, "controller": {closed(103)}, "mockgcp": {closed(104)}, "alignment": {closed(105)}}))
	if info, body := createdInfo(t, r, FinallyID); info.Group != "" || strings.Contains(body, "## Pull request") {
		t.Errorf("finally opens its own PR: %+v\n%s", info, body)
	}
}

// Not grouped (the default): a step waits for the one before it to merge,
// whatever PR that one has, and no child carries a group.
func TestReconcileNotGrouped(t *testing.T) {
	s := groupedSpec(t)
	o := groupedObserved(s, map[string][]Child{"prepopulate": {inPR(open(101), 500, true)}})
	o.Grouped = false
	if r := Reconcile(o); len(r.Actions) != 0 || !strings.HasPrefix(states(r), "prepopulate=Active types=Pending") {
		t.Errorf("not grouped: %v %s", kinds(r), states(r))
	}
	o = groupedObserved(s, nil)
	o.Grouped = false
	if info, body := createdInfo(t, Reconcile(o), "prepopulate"); info.Group != "" || strings.Contains(body, "## Pull request") {
		t.Errorf("not grouped: %+v\n%s", info, body)
	}
}

func TestGroupMarkerRoundTrip(t *testing.T) {
	for _, c := range []ChildInfo{
		{Parent: 9, Source: "gf", Step: "types", Depth: 1, Params: "abc", Group: GroupNext, PR: 500, Last: true, As: "lovelace-coder-bot", In: 101},
		{Parent: 9, Source: "gf", Step: "prepopulate", Depth: 1, Params: "abc", Group: GroupFirst},
		{Parent: 9, Source: "gf", Step: "x", Depth: 1, Params: "abc"},
	} {
		got, ok := ParseChildMarker("body\n\n" + c.Marker())
		if !ok || got != c {
			t.Errorf("%s: round trip %+v, want %+v", c.Marker(), got, c)
		}
	}
	if m := (ChildInfo{Parent: 9, Source: "gf", Step: "x", Depth: 1, Params: "abc"}).Marker(); strings.Contains(m, "group") {
		t.Errorf("a step of its own PR's marker is unchanged: %s", m)
	}
}

func TestStartGrouped(t *testing.T) {
	s, err := ParseStart("```factory-graph\ngraph: gf\ngrouped: true\nparams:\n  kind: X\n```\n")
	if err != nil || s == nil || !s.Grouped {
		t.Fatalf("ParseStart: %+v, %v", s, err)
	}
	if b := s.StartBlock(); !strings.Contains(b, "grouped: true\n") {
		t.Errorf("StartBlock:\n%s", b)
	}
	if s, _ := ParseStart("```factory-graph\ngraph: gf\n```\n"); s.Grouped || strings.Contains(s.StartBlock(), "grouped") {
		t.Errorf("grouped by default: %+v", s)
	}
}

// A pass over a grouped graph reads the open bot PR that fixes a step's
// child, and its head's checks: green (every check passed) lets the next
// step start on it as the PR's author; a check still running, or a PR
// that only mentions the child, does not.
func TestSyncGroupedReadsThePRAndItsChecks(t *testing.T) {
	for _, tc := range []struct {
		name    string
		prBody  string
		checks  []string
		started bool
	}{
		{"fixes it, checks green", "Prepopulate.\n\nFixes #201", []string{"success", "skipped"}, true},
		{"fixes it, a check running", "Fixes #201", []string{"success", ""}, false},
		{"fixes it, a check failed", "Fixes #201", []string{"success", "failure"}, false},
		{"fixes it, no check yet", "Fixes #201", nil, false},
		{"only mentions it", "See #201", []string{"success"}, false},
	} {
		f := graphtest.New()
		f.Files[".agents/graphs/gf.yaml"] = groupedGraph
		f.Issues[100] = &graphtest.Issue{Title: "Onboard X", Body: "```factory-graph\ngraph: gf\ngrouped: true\n```\n", Author: "alice", Assoc: "MEMBER", Open: true}
		opts := Options{Issue: 100, Trigger: "overseer", BotLogin: "watcher", Pool: []string{"lovelace-coder-bot", "watcher"}}
		sync(t, f, opts)
		pre := childOf(f, 100, "prepopulate")
		if pre != 201 {
			t.Fatalf("%s: prepopulate is #%d, want #201", tc.name, pre)
		}
		if m, _ := ParseChildMarker(f.Issues[pre].Body); m.Group != GroupFirst {
			t.Fatalf("%s: prepopulate's marker %+v", tc.name, m)
		}
		f.Issues[500] = &graphtest.Issue{Title: "Prepopulate X", Body: tc.prBody, Author: "lovelace-coder-bot", Open: true, IsPR: true, Head: "abc123", Checks: tc.checks}
		f.Issues[pre].Refs = []int{500}
		sync(t, f, opts)
		types := childOf(f, 100, "types")
		if started := types != 0; started != tc.started {
			t.Errorf("%s: types started %v, want %v", tc.name, started, tc.started)
			continue
		}
		if !tc.started {
			continue
		}
		if m, _ := ParseChildMarker(f.Issues[types].Body); m.Group != GroupNext || m.PR != 500 || m.As != "lovelace-coder-bot" || !m.Last {
			t.Errorf("%s: types' marker %+v", tc.name, m)
		}
		if st := statusComments(f, 100); len(st) == 0 || !strings.Contains(st[len(st)-1], "In the group's PR") {
			t.Errorf("%s: status:\n%s", tc.name, strings.Join(st, "\n---\n"))
		}
	}
}

// With the watch's judge (Options.StepReady), the next step waits for it -
// the reviewer and the care rules - whatever the checks say; when it
// cannot judge (ErrNoStepReady), the checks decide; any other error fails
// the pass.
func TestSyncGroupedAsksStepReady(t *testing.T) {
	setup := func(checks []string) (*graphtest.Fake, Options, int) {
		f := graphtest.New()
		f.Files[".agents/graphs/gf.yaml"] = groupedGraph
		f.Issues[100] = &graphtest.Issue{Title: "Onboard X", Body: "```factory-graph\ngraph: gf\ngrouped: true\n```\n", Author: "alice", Assoc: "MEMBER", Open: true}
		opts := Options{Issue: 100, Trigger: "overseer", BotLogin: "watcher", Pool: []string{"lovelace-coder-bot", "watcher"}}
		sync(t, f, opts)
		pre := childOf(f, 100, "prepopulate")
		f.Issues[500] = &graphtest.Issue{Title: "P", Body: "Fixes #201", Author: "lovelace-coder-bot", Open: true, IsPR: true, Head: "abc", Checks: checks}
		f.Issues[pre].Refs = []int{500}
		return f, opts, pre
	}
	for _, tc := range []struct {
		name    string
		checks  []string
		judge   func(context.Context, int) (bool, error)
		started bool
	}{
		{"green, the review not caught up: wait", []string{"success"}, func(context.Context, int) (bool, error) { return false, nil }, false},
		{"ready by the rules: go", []string{"success"}, func(_ context.Context, pr int) (bool, error) { return pr == 500, nil }, true},
		{"no judge here: green checks decide", []string{"success"}, func(context.Context, int) (bool, error) { return false, ErrNoStepReady }, true},
		{"no judge here, checks running: wait", []string{""}, func(context.Context, int) (bool, error) { return false, ErrNoStepReady }, false},
	} {
		f, opts, _ := setup(tc.checks)
		opts.StepReady = tc.judge
		sync(t, f, opts)
		if started := childOf(f, 100, "types") != 0; started != tc.started {
			t.Errorf("%s: types started %v, want %v", tc.name, started, tc.started)
		}
	}
	f, opts, _ := setup([]string{"success"})
	opts.StepReady = func(context.Context, int) (bool, error) { return false, errors.New("boom") }
	if _, err := Sync(context.Background(), f, opts); err == nil || !strings.Contains(err.Error(), "boom") {
		t.Errorf("a failed judgement: %v", err)
	}
}
