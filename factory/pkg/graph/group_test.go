package graph

import (
	"context"
	"errors"
	"fmt"
	"slices"
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

// The parent's block can say which steps gate (gates:), in place of the
// graph's own group-gate: each named step a gate, every other not; an
// empty list gates none. Not given, the graph's decide. Gates are a
// grouped graph's, and must name its steps.
func TestStartGates(t *testing.T) {
	s, err := ParseStart("```factory-graph\ngraph: gf\ngrouped: true\ngates: [controller]\n```\n")
	if err != nil || s == nil || strings.Join(s.Gates, ",") != "controller" {
		t.Fatalf("ParseStart: %+v, %v", s, err)
	}
	if b := s.StartBlock(); !strings.Contains(b, "gates: [controller]\n") {
		t.Errorf("StartBlock:\n%s", b)
	}
	if _, err := ParseStart("```factory-graph\ngraph: gf\ngates: [controller]\n```\n"); err == nil || !strings.Contains(err.Error(), "grouped: true") {
		t.Errorf("gates without grouped: %v", err)
	}
	if s, err := ParseStart("```factory-graph\ngraph: gf\ngrouped: true\ngates: []\n```\n"); err != nil || s.Gates == nil || len(s.Gates) != 0 {
		t.Errorf("an empty list is given, not absent: %+v, %v", s, err)
	}

	spec := groupedSpec(t)
	groups := func(sp *Spec) string {
		var out []string
		for _, st := range sp.Steps {
			out = append(out, st.ID+"<"+sp.GroupPrev(st.ID))
		}
		return strings.Join(out, " ")
	}
	for _, tc := range []struct {
		name  string
		gates []string
		want  string
	}{
		{"not given: the graph's (types gates)", nil,
			"prepopulate< types<prepopulate controller< mockgcp<controller alignment<mockgcp"},
		{"controller instead: prepopulate, types and controller one PR", []string{"controller"},
			"prepopulate< types<prepopulate controller<types mockgcp< alignment<mockgcp"},
		{"none: the whole line one PR", []string{},
			"prepopulate< types<prepopulate controller<types mockgcp<controller alignment<mockgcp"},
		{"finally named: gates nothing more", []string{"types", FinallyID},
			"prepopulate< types<prepopulate controller< mockgcp<controller alignment<mockgcp"},
	} {
		got, err := Start{Grouped: true, Gates: tc.gates}.WithGates(spec)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if g := groups(got); g != tc.want {
			t.Errorf("%s:\n  %s\nwant\n  %s", tc.name, g, tc.want)
		}
	}
	if groups(spec) != "prepopulate< types<prepopulate controller< mockgcp<controller alignment<mockgcp" {
		t.Errorf("WithGates changed the graph's own spec: %s", groups(spec))
	}
	if _, err := (Start{Grouped: true, Gates: []string{"nope"}}).WithGates(spec); err == nil || !strings.Contains(err.Error(), "nope is not a step") {
		t.Errorf("an unknown gate: %v", err)
	}
}

// A parent whose gates name no step of its graph is Invalid, saying why,
// and nothing is created.
func TestSyncUnknownGateIsInvalid(t *testing.T) {
	f := graphtest.New()
	f.Files[".agents/graphs/gf.yaml"] = groupedGraph
	f.Issues[100] = &graphtest.Issue{Title: "Onboard X", Body: "```factory-graph\ngraph: gf\ngrouped: true\ngates: [typo]\n```\n", Author: "alice", Assoc: "MEMBER", Open: true}
	opts := Options{Issue: 100, Trigger: "overseer", BotLogin: "watcher", Pool: []string{"watcher"}}
	p := sync(t, f, opts)
	if p.Result.Phase != PhaseInvalid || !strings.Contains(p.Result.Reason, "typo is not a step") || childOf(f, 100, "prepopulate") != 0 {
		t.Errorf("phase %s (%s), prepopulate #%d", p.Result.Phase, p.Result.Reason, childOf(f, 100, "prepopulate"))
	}
}

// The status draws the graph: a mermaid node per step, coloured by its
// state, with its issue (linked relative to the parent's page), the after
// edges, finally after the steps no other follows; a grouped graph boxes
// each group's steps - its PR, merged or not. The table stays, folded.
func TestRenderStatusDiagram(t *testing.T) {
	s := groupedSpec(t)
	children := func(grouped bool) map[string][]Child {
		c := map[string][]Child{"prepopulate": {closed(101)}, "types": {closed(102)}, "controller": {open(103)}, "mockgcp": {open(104)}}
		if grouped {
			c["controller"] = []Child{inPR(open(103), 600, true)}
			c["mockgcp"] = []Child{inPR(open(104), 600, false)}
		}
		return c
	}
	plainObs := observed(s, children(false), State{})
	plainObs.Parent.URL = "https://github.com/o/r/issues/100"
	plain := RenderStatus("gf", 0, s, Reconcile(plainObs))
	for _, want := range []string{
		"```mermaid\nflowchart LR\n",
		`n0["✓ prepopulate<br/><small>#101</small>"]:::done`,
		`n3["● mockgcp<br/><small>#104 · active</small>"]:::active`,
		`n4["alignment"]:::pending`,
		`n5(["finally"]):::pending`,
		"  n0 --> n1\n", "  n3 --> n4\n", "  n4 --> n5\n",
		// Absolute and in a new tab: GitHub draws the diagram in a frame
		// of its own, where a relative "104" went to
		// viewscreen.githubusercontent.com/markdown/104.
		`  click n3 "https://github.com/o/r/issues/104" _blank`,
		"<details><summary>Steps</summary>\n\n| Step | After | Then | Issue | State |",
		"| mockgcp | controller | fix | #104 | 🔄 Active |",
		"</details>",
	} {
		if !strings.Contains(plain, want) {
			t.Errorf("not grouped: status lacks %q:\n%s", want, plain)
		}
	}
	if strings.Contains(plain, "subgraph") || strings.Contains(plain, "gate") {
		t.Errorf("not grouped: no PR boxes, no gates:\n%s", plain)
	}
	if strings.Contains(plain, `click n0 "1`) || strings.Contains(plain, `"101"`) {
		t.Errorf("a relative link is left:\n%s", plain)
	}
	// Without the parent's page there is nowhere to link to: no links.
	if noURL := RenderStatus("gf", 0, s, Reconcile(observed(s, children(false), State{}))); strings.Contains(noURL, "click ") {
		t.Errorf("no parent URL: links anyway:\n%s", noURL)
	}
	for in, want := range map[string]string{
		"https://github.com/o/r/issues/100": "https://github.com/o/r/issues/",
		"https://github.com/o/r/pull/100":   "",
		"":                                  "",
	} {
		if got := issuesURL(in); got != want {
			t.Errorf("issuesURL(%q) = %q, want %q", in, got, want)
		}
	}

	grouped := RenderStatus("gf", 0, s, Reconcile(groupedObserved(s, children(true))))
	for _, want := range []string{
		"  subgraph g0[\"one PR · merged\"]\n    direction LR\n    n0[",
		`n1["✓ types<br/><small>#102 · gate</small>"]:::done`,
		"  class g0 prmerged\n",
		"  subgraph g1[\"PR #600\"]\n",
		`n2["📦 controller<br/><small>#103 · in the PR</small>"]:::ingroup`,
		"  class g1 pr\n",
		"| controller | types | fix | #103 | 📦 In the group's PR |",
	} {
		if !strings.Contains(grouped, want) {
			t.Errorf("grouped: status lacks %q:\n%s", want, grouped)
		}
	}
	if strings.Count(grouped, "subgraph") != 2 {
		t.Errorf("grouped: want 2 PR boxes (finally is its own, unboxed):\n%s", grouped)
	}
	// Every mermaid line is one the renderer means: no empty labels (mermaid
	// refuses them), nodes declared once.
	if strings.Contains(grouped, `[""]`) || strings.Contains(grouped, `((""))`) || strings.Count(grouped, "n2[") != 1 {
		t.Errorf("grouped: malformed nodes:\n%s", grouped)
	}
}

// A label the graph names that the repository lacks - and the watcher may
// not create (#14659's mockgcp-alignment, step/mockgcp-alignment) - does
// not stop the graph: the step's child is made without it, an open child
// is labelled with the rest, and the status says which labels to create.
func TestSyncWithoutALabelTheRepositoryLacks(t *testing.T) {
	f := graphtest.New()
	f.Files[".agents/graphs/gf.yaml"] = strings.Replace(groupedGraph, "{id: prepopulate, title: P, body: p}", "{id: prepopulate, title: P, body: p, labels: [step/prepopulate]}", 1)
	f.MissingLabels = []string{"step/prepopulate"}
	f.Issues[100] = &graphtest.Issue{Title: "Onboard X", Body: "```factory-graph\ngraph: gf\n```\n", Author: "alice", Assoc: "MEMBER", Open: true}
	opts := Options{Issue: 100, Trigger: "overseer", BotLogin: "watcher", Pool: []string{"watcher"}}
	p := sync(t, f, opts)
	pre := childOf(f, 100, "prepopulate")
	if pre == 0 {
		t.Fatalf("no prepopulate child: %+v", p.Result)
	}
	if got := f.Issues[pre].Labels; slices.Contains(got, "step/prepopulate") || !slices.Contains(got, "overseer") {
		t.Errorf("child labels %v: want the trigger, without the missing label", got)
	}
	st := statusComments(f, 100)
	if len(st) == 0 || !strings.Contains(st[len(st)-1], "do not exist in the repository") || !strings.Contains(st[len(st)-1], "`step/prepopulate`") {
		t.Errorf("status does not name the missing label:\n%s", strings.Join(st, "\n---\n"))
	}

	// Each pass would label the open child again: it goes without the
	// missing label, without failing, the status still naming it.
	p = sync(t, f, opts)
	if !slices.Contains(p.Result.MissingLabels, "step/prepopulate") {
		t.Errorf("a later pass forgot the missing label: %v", p.Result.MissingLabels)
	}

	// Created: the next pass labels the child, and the warning goes.
	f.MissingLabels = nil
	p = sync(t, f, opts)
	if !slices.Contains(f.Issues[pre].Labels, "step/prepopulate") || len(p.Result.MissingLabels) != 0 {
		t.Errorf("after creating the label: child labels %v, missing %v", f.Issues[pre].Labels, p.Result.MissingLabels)
	}
	if st := statusComments(f, 100); strings.Contains(st[len(st)-1], "do not exist in the repository") {
		t.Errorf("the warning stayed:\n%s", st[len(st)-1])
	}
}

// In a grouped graph, a step whose work is in its group's PR - a later
// step carrying it on - stops asking for review: its <trigger>/review label
// is taken off, and its labels are no longer kept, or the shared PR would
// be reviewed again for steps already reviewed. The active step and the
// group's last keep theirs; an ungrouped graph's children keep theirs.
func TestInGroupStepsDropTheirReviewLabel(t *testing.T) {
	tp, err := ParseTemplate([]byte(strings.NewReplacer(
		"{id: controller, after: [types], title: C, body: c}", "{id: controller, after: [types], title: C, body: c, review: rc}",
		"{id: mockgcp, after: [controller], title: M, body: m}", "{id: mockgcp, after: [controller], title: M, body: m, review: rm}",
		"{id: alignment, after: [mockgcp], title: A, body: a}", "{id: alignment, after: [mockgcp], title: A, body: a, review: ra}",
	).Replace(groupedGraph)))
	if err != nil {
		t.Fatal(err)
	}
	s := &tp.Spec
	labelled := func(c Child, labels ...string) Child { c.Labels = labels; return c }
	children := map[string][]Child{
		"prepopulate": {closed(101)}, "types": {closed(102)},
		// In the PR, carried on by mockgcp: drop review (and do not top up
		// the missing greenfield-ish labels).
		"controller": {labelled(inPR(open(103), 600, true), "overseer", "overseer/review")},
		// The active step: keeps (and gets) its labels.
		"mockgcp": {labelled(inPR(open(104), 600, false), "overseer")},
	}
	r := Reconcile(groupedObserved(s, children))
	var removed, added []string
	for _, a := range r.Actions {
		switch a.Kind {
		case ActRemoveChildLabels:
			removed = append(removed, fmt.Sprintf("%s:%v", a.Step, a.Labels))
		case ActAddChildLabels:
			added = append(added, fmt.Sprintf("%s:%v", a.Step, a.Labels))
		}
	}
	if strings.Join(removed, " ") != "controller:[overseer/review]" {
		t.Errorf("removed %v, want controller's review label only", removed)
	}
	if strings.Join(added, " ") != "mockgcp:[overseer/review]" {
		t.Errorf("added %v, want the active step's review label only (none topped up on controller)", added)
	}

	// Ungrouped: nobody's review label is taken off.
	o := groupedObserved(s, children)
	o.Grouped = false
	for _, a := range Reconcile(o).Actions {
		if a.Kind == ActRemoveChildLabels {
			t.Errorf("ungrouped graph removed labels: %+v", a)
		}
	}

	// End to end on the fake: the label goes, and stays gone on the next pass.
	f := graphtest.New()
	f.Files[".agents/graphs/gf.yaml"] = strings.Replace(groupedGraph, "{id: prepopulate, title: P, body: p}", "{id: prepopulate, title: P, body: p, review: rp}", 1)
	f.Issues[100] = &graphtest.Issue{Title: "Onboard X", Body: "```factory-graph\ngraph: gf\ngrouped: true\n```\n", Author: "alice", Assoc: "MEMBER", Open: true}
	opts := Options{Issue: 100, Trigger: "overseer", BotLogin: "watcher", Pool: []string{"lovelace-coder-bot", "watcher"}}
	sync(t, f, opts)
	pre := childOf(f, 100, "prepopulate")
	if !slices.Contains(f.Issues[pre].Labels, "overseer/review") {
		t.Fatalf("prepopulate lacks its review label at first: %v", f.Issues[pre].Labels)
	}
	f.Issues[500] = &graphtest.Issue{Title: "P", Body: "Fixes #201", Author: "lovelace-coder-bot", Open: true, IsPR: true, Head: "abc", Checks: []string{"success"}}
	f.Issues[pre].Refs = []int{500}
	sync(t, f, opts) // types starts on the PR: prepopulate is in the group's PR
	sync(t, f, opts)
	if slices.Contains(f.Issues[pre].Labels, "overseer/review") {
		t.Errorf("prepopulate, in the group's PR, still asks for review: %v", f.Issues[pre].Labels)
	}
}

// A step may be marked as possibly needing no PR (no-change: ok): its
// issues say so in their marker; only a fix step may be, and only "ok".
func TestNoChangeStep(t *testing.T) {
	tp, err := ParseTemplate([]byte(strings.Replace(groupedGraph, "{id: prepopulate, title: P, body: p}", "{id: prepopulate, title: P, body: p, no-change: ok}", 1)))
	if err != nil || tp.Validate() != nil {
		t.Fatalf("parse/validate: %v %v", err, tp.Validate())
	}
	r := Reconcile(groupedObserved(&tp.Spec, nil))
	if info, _ := createdInfo(t, r, "prepopulate"); !info.NoChange || !strings.Contains(info.Marker(), " nochange=ok") {
		t.Errorf("prepopulate's marker %q", info.Marker())
	}
	if got, ok := ParseChildMarker("x\n" + (ChildInfo{Parent: 9, Step: "a", NoChange: true, Group: GroupFirst}).Marker()); !ok || !got.NoChange || got.Group != GroupFirst {
		t.Errorf("round trip: %+v", got)
	}
	for bad, want := range map[string]string{
		"{id: prepopulate, title: P, body: p, no-change: maybe}":          `no-change "maybe" is not ok`,
		"{id: prepopulate, title: P, body: p, no-change: ok, then: plan}": "no-change is a fix step's",
	} {
		tp, err := ParseTemplate([]byte(strings.Replace(groupedGraph, "{id: prepopulate, title: P, body: p}", bad, 1)))
		if err != nil {
			t.Fatal(err)
		}
		if err := tp.Validate(); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v, want %q", bad, err, want)
		}
	}
}

// A grouped step that closed needing no change is passed over: the next
// step continues the nearest earlier step's PR (KCC #14934: prepopulate
// already done, so types opens the PR); and a group whose last step closed
// so, an earlier step's work in an open PR, gets that PR marked ready.
func TestGroupWithANoChangeStep(t *testing.T) {
	s := groupedSpec(t)
	// #14934: prepopulate (the group's first) closed with no change: types,
	// the gate, opens a PR of its own.
	r := Reconcile(groupedObserved(s, map[string][]Child{"prepopulate": {closed(101)}}))
	if info, _ := createdInfo(t, r, "types"); info.Group != "" || info.PR != 0 {
		t.Errorf("types after a no-change prepopulate: %+v, want a PR of its own", info)
	}
	// A whole group with no PR (prepopulate and types both closed needing
	// no change): nothing to mark ready, and the next group starts -
	// controller opens its draft.
	r = Reconcile(groupedObserved(s, map[string][]Child{"prepopulate": {closed(101)}, "types": {closed(102)}}))
	if info, _ := createdInfo(t, r, "controller"); info.Group != GroupFirst {
		t.Errorf("controller after a group with no PR: %+v, want the next group's first", info)
	}
	for _, a := range r.Actions {
		if a.Kind == ActMarkGroupPRReady {
			t.Errorf("marked a PR ready for a group with none: %+v", a)
		}
	}
	// controller's work in PR 600; mockgcp closed with no change:
	// alignment continues PR 600, in controller's sandbox, and is last.
	r = Reconcile(groupedObserved(s, map[string][]Child{
		"prepopulate": {closed(101)}, "types": {closed(102)},
		"controller": {inPR(open(103), 600, true)}, "mockgcp": {closed(104)},
	}))
	if info, _ := createdInfo(t, r, "alignment"); info.Group != GroupNext || info.PR != 600 || info.In != 103 || !info.Last {
		t.Errorf("alignment past a no-change mockgcp: %+v, want PR 600 in #103, last", info)
	}
	// alignment, the group's last, closed with no change: PR 600 is marked
	// ready, as its push would have.
	r = Reconcile(groupedObserved(s, map[string][]Child{
		"prepopulate": {closed(101)}, "types": {closed(102)},
		"controller": {inPR(open(103), 600, true)}, "mockgcp": {inPR(open(104), 600, true)}, "alignment": {closed(105)},
	}))
	var readied []int
	for _, a := range r.Actions {
		if a.Kind == ActMarkGroupPRReady {
			readied = append(readied, a.Issue)
		}
	}
	if len(readied) != 1 || readied[0] != 600 {
		t.Errorf("marked ready %v, want [600]", readied)
	}
	// The group's PR merged (every step closed): nothing to mark.
	r = Reconcile(groupedObserved(s, map[string][]Child{
		"prepopulate": {closed(101)}, "types": {closed(102)},
		"controller": {closed(103)}, "mockgcp": {closed(104)}, "alignment": {closed(105)},
	}))
	for _, a := range r.Actions {
		if a.Kind == ActMarkGroupPRReady {
			t.Errorf("marked a merged group's PR ready: %+v", a)
		}
	}

	// End to end on the fake: the draft is marked ready.
	f := graphtest.New()
	f.Files[".agents/graphs/gf.yaml"] = groupedGraph
	f.Issues[100] = &graphtest.Issue{Title: "Onboard X", Body: "```factory-graph\ngraph: gf\ngrouped: true\n```\n", Author: "alice", Assoc: "MEMBER", Open: true}
	f.Issues[600] = &graphtest.Issue{Title: "group", Body: "Fixes #103\nFixes #104", Author: "lovelace-coder-bot", Open: true, IsPR: true, Draft: true, Head: "abc", Checks: []string{"success"}}
	mk := func(n int, step string, open bool, refs ...int) {
		f.Issues[n] = &graphtest.Issue{Title: step, Body: (ChildInfo{Parent: 100, Source: "gf", Step: step, Depth: 1, Params: HashParams(nil)}).Marker(), Author: "watcher", Open: open, Refs: refs}
		f.Issues[100].SubIssues = append(f.Issues[100].SubIssues, n)
	}
	mk(101, "prepopulate", false)
	mk(102, "types", false)
	mk(103, "controller", true, 600)
	mk(104, "mockgcp", true, 600)
	mk(105, "alignment", false)
	sync(t, f, Options{Issue: 100, Trigger: "overseer", BotLogin: "watcher", Pool: []string{"lovelace-coder-bot", "watcher"}})
	if len(f.Readied) != 1 || f.Readied[0] != 600 || f.Issues[600].Draft {
		t.Errorf("readied %v, PR 600 draft %v", f.Readied, f.Issues[600].Draft)
	}
}
