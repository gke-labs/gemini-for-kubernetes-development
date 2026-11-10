package graph

import (
	"fmt"
	"strings"
	"testing"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/graph/graphtest"
)

// requiresGraph is groupedGraph with mockgcp waiting on an assessment of
// the controller's PR: one of two labels a rule adds.
const requiresGraph = "apiVersion: factory.gemini.google.com/v1alpha1\nkind: IssueGraph\nmetadata: {name: gf}\nspec:\n" +
	"  maxActive: 1\n  steps:\n" +
	"    - {id: prepopulate, title: P, body: p}\n" +
	"    - {id: types, after: [prepopulate], title: T, body: t, group-gate: true}\n" +
	"    - {id: controller, after: [types], title: C, body: c}\n" +
	"    - id: mockgcp\n      after: [controller]\n      title: M\n      body: m\n" +
	"      requires: {labels: {any: [overseer/assessment/good, overseer/assessment/fair]}}\n" +
	"    - {id: alignment, after: [mockgcp], title: A, body: a}\n" +
	"  finally: {title: F, body: f}\n"

func requiresSpec(t *testing.T) *Spec {
	t.Helper()
	tp, err := ParseTemplate([]byte(requiresGraph))
	if err != nil {
		t.Fatal(err)
	}
	if err := tp.Validate(); err != nil {
		t.Fatal(err)
	}
	return &tp.Spec
}

func withPRLabels(c Child, labels ...string) Child {
	if c.GroupPR != nil {
		pr := *c.GroupPR
		pr.Labels = labels
		c.GroupPR = &pr
	} else {
		c.MergedPRLabels = labels
	}
	return c
}

func mergedIn(c Child, pr int) Child {
	c.MergedPR = pr
	return c
}

func TestLabelMatch(t *testing.T) {
	for _, tc := range []struct {
		m      LabelMatch
		labels []string
		want   bool
	}{
		{LabelMatch{Any: []string{"a", "b"}}, []string{"x", "b"}, true},
		{LabelMatch{Any: []string{"a", "b"}}, []string{"x"}, false},
		{LabelMatch{Any: []string{"a"}}, nil, false},
		{LabelMatch{All: []string{"a", "b"}}, []string{"b", "a"}, true},
		{LabelMatch{All: []string{"a", "b"}}, []string{"a"}, false},
		{LabelMatch{All: []string{"a"}, Any: []string{"b", "c"}}, []string{"a", "c"}, true},
		{LabelMatch{All: []string{"a"}, Any: []string{"b", "c"}}, []string{"a"}, false},
		{LabelMatch{Any: []string{"Overseer/Assessment/Good"}}, []string{"overseer/assessment/good"}, true},
		{LabelMatch{}, nil, true},
	} {
		if got := tc.m.Match(tc.labels); got != tc.want {
			t.Errorf("%+v.Match(%v) = %v, want %v", tc.m, tc.labels, got, tc.want)
		}
	}
	if got, want := (LabelMatch{All: []string{"a", "b"}, Any: []string{"c", "d"}}).String(), "`a` and `b`, and one of `c`, `d`"; got != want {
		t.Errorf("String() = %q, want %q", got, want)
	}
	if got, want := (LabelMatch{Any: []string{"c"}}).String(), "`c`"; got != want {
		t.Errorf("String() = %q, want %q", got, want)
	}
}

func TestValidateRequires(t *testing.T) {
	head := "apiVersion: factory.gemini.google.com/v1alpha1\nkind: IssueGraph\nmetadata: {name: g}\nspec:\n  steps:\n" +
		"    - {id: a, title: A, body: a}\n"
	for _, tc := range []struct {
		name, steps, err string
	}{
		{"ok", "    - {id: b, after: [a], title: B, body: b, requires: {labels: {any: [x, y], all: [z]}}}\n", ""},
		{"no after", "    - {id: b, title: B, body: b, requires: {labels: {any: [x]}}}\n", "requires labels with no after"},
		{"empty label", "    - {id: b, after: [a], title: B, body: b, requires: {labels: {all: ['']}}}\n", "an empty label"},
		{"twice", "    - {id: b, after: [a], title: B, body: b, requires: {labels: {all: [x], any: [X, y]}}}\n", "given twice"},
		{"on finally", "  finally: {title: F, body: f, requires: {labels: {any: [x]}}}\n", "finally: has requires"},
		{"unknown field", "    - {id: b, after: [a], title: B, body: b, requires: {label: [x]}}\n", "label"},
	} {
		tp, err := ParseTemplate([]byte(head + tc.steps))
		if err == nil {
			err = tp.Validate()
		}
		switch {
		case tc.err == "" && err != nil:
			t.Errorf("%s: %v", tc.name, err)
		case tc.err != "" && (err == nil || !strings.Contains(err.Error(), tc.err)):
			t.Errorf("%s: error %v, want one saying %q", tc.name, err, tc.err)
		}
	}
}

// Grouped: the controller's work in the group's PR, ready, is not enough
// for mockgcp; the PR must also carry one of the labels its requires names.
func TestReconcileRequiresGrouped(t *testing.T) {
	s := requiresSpec(t)
	merged := map[string][]Child{"prepopulate": {closed(101)}, "types": {closed(102)}}
	with := func(controller Child, more ...Child) map[string][]Child {
		m := map[string][]Child{"controller": {controller}}
		for k, v := range merged {
			m[k] = v
		}
		if len(more) > 0 {
			m["mockgcp"] = more
		}
		return m
	}
	for _, tc := range []struct {
		name     string
		children map[string][]Child
		actions  []string
		steps    string
		waiting  string
	}{
		{"controller ready, no assessment yet: mockgcp waits", with(inPR(open(103), 600, true)),
			nil,
			"prepopulate=Succeeded types=Succeeded controller=InGroup mockgcp=Pending alignment=Pending finally=Pending",
			"#600 needs one of `overseer/assessment/good`, `overseer/assessment/fair`"},
		{"assessed poor: still waits", with(withPRLabels(inPR(open(103), 600, true), "overseer/assessment/poor")),
			nil,
			"prepopulate=Succeeded types=Succeeded controller=InGroup mockgcp=Pending alignment=Pending finally=Pending",
			"#600 needs"},
		{"assessed fair: mockgcp continues the PR", with(withPRLabels(inPR(open(103), 600, true), "overseer", "overseer/assessment/fair")),
			[]string{"create-child:mockgcp"},
			"prepopulate=Succeeded types=Succeeded controller=InGroup mockgcp=Active alignment=Pending finally=Pending",
			""},
		{"labelled but not ready: waits for the step, not the label", with(withPRLabels(inPR(open(103), 600, false), "overseer/assessment/good")),
			nil,
			"prepopulate=Succeeded types=Succeeded controller=Active mockgcp=Pending alignment=Pending finally=Pending",
			""},
		{"mockgcp started: a label taken off later does not take it back", with(inPR(open(103), 600, true), open(104)),
			nil,
			"prepopulate=Succeeded types=Succeeded controller=InGroup mockgcp=Active alignment=Pending finally=Pending",
			""},
	} {
		r := Reconcile(groupedObserved(s, tc.children))
		if got := kinds(r); strings.Join(got, ",") != strings.Join(tc.actions, ",") {
			t.Errorf("%s: actions %v, want %v", tc.name, got, tc.actions)
		}
		if got := states(r); got != tc.steps {
			t.Errorf("%s: steps\n  %s\nwant\n  %s", tc.name, got, tc.steps)
		}
		var waiting string
		for _, st := range r.Steps {
			if st.ID == "mockgcp" {
				waiting = st.Waiting
			}
		}
		if (tc.waiting == "") != (waiting == "") || !strings.Contains(waiting, tc.waiting) {
			t.Errorf("%s: mockgcp waiting %q, want %q", tc.name, waiting, tc.waiting)
		}
	}
}

// Not grouped: the controller's PR merged, closing its issue; mockgcp waits
// on that merged PR's labels. A step closed with no PR has none to check.
func TestReconcileRequiresMerged(t *testing.T) {
	s := requiresSpec(t)
	base := map[string][]Child{"prepopulate": {closed(101)}, "types": {closed(102)}}
	for _, tc := range []struct {
		name       string
		controller Child
		actions    []string
	}{
		{"merged, not assessed", mergedIn(closed(103), 600), nil},
		{"merged, assessed good", withPRLabels(mergedIn(closed(103), 600), "overseer/assessment/good"), []string{"create-child:mockgcp"}},
		{"closed with no PR known", closed(103), []string{"create-child:mockgcp"}},
	} {
		children := map[string][]Child{"controller": {tc.controller}}
		for k, v := range base {
			children[k] = v
		}
		o := groupedObserved(s, children)
		o.Grouped = false
		if got := kinds(Reconcile(o)); strings.Join(got, ",") != strings.Join(tc.actions, ",") {
			t.Errorf("%s: actions %v, want %v", tc.name, got, tc.actions)
		}
	}
}

func TestRenderStatusWaiting(t *testing.T) {
	s := requiresSpec(t)
	r := Reconcile(groupedObserved(s, map[string][]Child{"prepopulate": {closed(101)}, "types": {closed(102)}, "controller": {inPR(open(103), 600, true)}}))
	if body := RenderStatus("gf", 0, s, r); !strings.Contains(body, "waiting: #600 needs one of `overseer/assessment/good`") {
		t.Errorf("status does not say what mockgcp waits for:\n%s", body)
	}
}

// Sync reads the labels: of the group's open PR, and - for a closed child a
// later step requires labels of - of the bot PR that merged for it.
func TestSyncRequiresReadsPRLabels(t *testing.T) {
	for _, tc := range []struct {
		name    string
		grouped bool
		labels  []string
		started bool
	}{
		{"grouped, no label", true, nil, false},
		{"grouped, assessed good", true, []string{"overseer/assessment/good"}, true},
		{"merged, no label", false, nil, false},
		{"merged, assessed fair", false, []string{"overseer/assessment/fair"}, true},
	} {
		f := graphtest.New()
		f.Files[".agents/graphs/gf.yaml"] = requiresGraph
		start := "```factory-graph\ngraph: gf\n```\n"
		if tc.grouped {
			start = "```factory-graph\ngraph: gf\ngrouped: true\n```\n"
		}
		f.Issues[100] = &graphtest.Issue{Title: "Onboard X", Body: start, Author: "alice", Assoc: "MEMBER", Open: true}
		opts := Options{Issue: 100, Trigger: "overseer", BotLogin: "watcher", Pool: []string{"lovelace-coder-bot", "watcher"}}
		sync(t, f, opts)
		// Run the graph up to the controller: close the earlier steps' issues.
		for _, step := range []string{"prepopulate", "types"} {
			n := childOf(f, 100, step)
			if n == 0 {
				t.Fatalf("%s: no %s child", tc.name, step)
			}
			f.Issues[n].Open, f.Issues[n].Reason = false, "completed"
			sync(t, f, opts)
		}
		ctl := childOf(f, 100, "controller")
		if ctl == 0 {
			t.Fatalf("%s: no controller child", tc.name)
		}
		pr := &graphtest.Issue{Title: "Controller X", Body: fmt.Sprintf("Fixes #%d", ctl), Author: "lovelace-coder-bot", IsPR: true, Head: "abc", Checks: []string{"success"}, Labels: tc.labels}
		if tc.grouped {
			pr.Open = true
		} else {
			pr.Merged = true
			f.Issues[ctl].Open, f.Issues[ctl].Reason = false, "completed"
		}
		f.Issues[700] = pr
		f.Issues[ctl].Refs = []int{700}
		sync(t, f, opts)
		if started := childOf(f, 100, "mockgcp") != 0; started != tc.started {
			t.Errorf("%s: mockgcp started %v, want %v\n%s", tc.name, started, tc.started, strings.Join(statusComments(f, 100), "\n---\n"))
		}
	}
}
