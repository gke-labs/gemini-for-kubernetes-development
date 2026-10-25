package graph

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/graph/graphtest"
)

// childOf is the open or closed child of step, 0 for none.
func childOf(f *graphtest.Fake, parent int, step string) int {
	for n, is := range f.Issues {
		if m, ok := ParseChildMarker(is.Body); ok && m.Parent == parent && m.Step == step {
			return n
		}
	}
	return 0
}

func statusComments(f *graphtest.Fake, n int) []string {
	var out []string
	for _, c := range f.Issues[n].Comments {
		if IsStatusComment(c.GetBody()) {
			out = append(out, c.GetBody())
		}
	}
	return out
}

const planComment = "**Implementation plan** · v2 · updated x\n\n## Breakdown\n" +
	"```yaml graph\nmaxActive: 2\nsteps:\n" +
	"  - {id: a, title: A, body: 'do a'}\n" +
	"  - {id: b, after: [a], title: B, body: 'after #{{ (step \"a\").Issue }}'}\n" +
	"  - {id: c, after: [a], title: C, body: c, then: plan}\n" +
	"finally: {title: F, body: f}\n```\n\n<!-- factory:comment key=plan v=2 -->"

func planParent(t *testing.T) (*graphtest.Fake, Options) {
	t.Helper()
	f := graphtest.New()
	f.Issues[100] = &graphtest.Issue{Title: "Epic", Body: "Build it.", Author: "alice", Assoc: "MEMBER", Open: true, Labels: []string{"overseer/planned"}}
	f.Comment(100, "hopper-coder-bot", "NONE", planComment)
	f.Comment(100, "hopper-coder-bot", "NONE", Start{Source: SourcePlan}.StartComment())
	return f, Options{Issue: 100, Trigger: "overseer", BotLogin: "watcher", Pool: []string{"hopper-coder-bot", "watcher"}}
}

func sync(t *testing.T, f *graphtest.Fake, opts Options) *Pass {
	t.Helper()
	p, err := Sync(context.Background(), f, opts)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// A graph-labelled issue with no start block whose plan has a Breakdown
// is a graph started from its plan (the label is the approval: the watch's
// implement-graph rule, or a maintainer's); without the label, or with only
// an outsider's plan, it is no graph's parent.
func TestSyncImplicitPlanStart(t *testing.T) {
	for _, tc := range []struct {
		name, label, author, assoc string
		started                    bool
	}{
		{"graph label, the pool's plan", "overseer/graph", "hopper-coder-bot", "NONE", true},
		{"trigger's graph label, a member's plan", "kcc/graph", "alice", "MEMBER", true},
		{"planned only", "overseer/planned", "hopper-coder-bot", "NONE", false},
		{"graph label, an outsider's plan", "overseer/graph", "mallory", "NONE", false},
	} {
		f := graphtest.New()
		f.Issues[100] = &graphtest.Issue{Title: "Epic", Body: "Build it.", Author: "alice", Assoc: "MEMBER", Open: true, Labels: []string{tc.label}}
		f.Comment(100, tc.author, tc.assoc, planComment)
		opts := Options{Issue: 100, Trigger: "kcc", BotLogin: "watcher", Pool: []string{"hopper-coder-bot", "watcher"}}
		p := sync(t, f, opts)
		a := childOf(f, 100, "a")
		if started := !p.NoGraph && a != 0; started != tc.started {
			t.Errorf("%s: started %v (NoGraph %v, a=#%d), want %v", tc.name, started, p.NoGraph, a, tc.started)
		}
		if tc.started && (len(statusComments(f, 100)) != 1 || !strings.Contains(statusComments(f, 100)[0], "steps: plan v2")) {
			t.Errorf("%s: status:\n%s", tc.name, strings.Join(statusComments(f, 100), "\n---\n"))
		}
	}
}

// A then: ask step's child is created labelled needs-human, saying what
// a person can do, and assigned to the step's assignees.
func TestSyncAskStep(t *testing.T) {
	f := graphtest.New()
	f.Issues[100] = &graphtest.Issue{Title: "Epic", Body: "Build it.", Author: "alice", Assoc: "MEMBER", Open: true, Labels: []string{"overseer/graph"}}
	f.Comment(100, "hopper-coder-bot", "NONE", "**Implementation plan** · v1\n\n```yaml graph\nsteps:\n"+
		"  - {id: decide, title: Pick the storage, body: 'Spanner or Bigtable?', then: ask, assignees: [alice]}\n```\n\n<!-- factory:comment key=plan v=1 -->")
	sync(t, f, Options{Issue: 100, Trigger: "overseer", BotLogin: "watcher", Pool: []string{"hopper-coder-bot", "watcher"}})
	n := childOf(f, 100, "decide")
	if n == 0 {
		t.Fatal("no child for the ask step")
	}
	is := f.Issues[n]
	if !slices.Equal(is.Labels, []string{"overseer/needs-human"}) || !slices.Equal(is.Assignees, []string{"alice"}) ||
		!strings.Contains(is.Body, "Spanner or Bigtable?\n\n## Needs a person") || !slices.Contains(f.Issues[100].SubIssues, n) {
		t.Errorf("child = %+v", is)
	}
}

// A plan's graph from start to finish: children created as their steps
// become ready, linked as sub-issues and labelled by their then; one
// status comment, edited in place; the parent closed at the end.
func TestSyncLifecycle(t *testing.T) {
	f, opts := planParent(t)

	p := sync(t, f, opts)
	a := childOf(f, 100, "a")
	if p.Result.Phase != PhaseRunning || a == 0 || childOf(f, 100, "b") != 0 {
		t.Fatalf("pass 1: phase %s, a=#%d", p.Result.Phase, a)
	}
	if is := f.Issues[a]; !slices.Contains(is.Labels, "overseer") || !slices.Contains(f.Issues[100].SubIssues, a) || !strings.Contains(is.Body, "do a") {
		t.Errorf("a = %+v", is)
	}
	if m, _ := ParseChildMarker(f.Issues[a].Body); m.Depth != 1 || m.Source != SourcePlan {
		t.Errorf("a's marker = %+v", m)
	}
	if len(statusComments(f, 100)) != 1 || !strings.Contains(statusComments(f, 100)[0], fmt.Sprintf("| a | - | fix | #%d | 🔄 Active |", a)) {
		t.Errorf("status:\n%s", strings.Join(statusComments(f, 100), "\n---\n"))
	}

	// Nothing changed: nothing written, not even the status.
	before := f.Writes
	sync(t, f, opts)
	if f.Writes != before {
		t.Errorf("an idle pass wrote %d times", f.Writes-before)
	}

	f.CloseChild(a, "completed")
	sync(t, f, opts)
	b, c := childOf(f, 100, "b"), childOf(f, 100, "c")
	if b == 0 || c == 0 {
		t.Fatalf("after a: b=#%d c=#%d", b, c)
	}
	if !strings.Contains(f.Issues[b].Body, fmt.Sprintf("after #%d", a)) || !slices.Equal(f.Issues[c].Labels, []string{"overseer/plan"}) {
		t.Errorf("b body %q, c labels %v", f.Issues[b].Body, f.Issues[c].Labels)
	}

	f.CloseChild(b, "completed")
	f.CloseChild(c, "not_planned")
	sync(t, f, opts)
	fin := childOf(f, 100, FinallyID)
	if fin == 0 {
		t.Fatal("no finally child")
	}
	f.CloseChild(fin, "completed")
	p = sync(t, f, opts)
	if p.Result.Phase != PhaseSucceeded || f.Issues[100].Open || f.Issues[100].Reason != "completed" {
		t.Errorf("end: phase %s, parent open %v reason %q", p.Result.Phase, f.Issues[100].Open, f.Issues[100].Reason)
	}
	if n := len(statusComments(f, 100)); n != 1 {
		t.Errorf("%d status comments, want one edited in place", n)
	}
	if st, _ := ParseState(statusComments(f, 100)[0]); !st.Completed || st.Children["a"] != a {
		t.Errorf("state = %+v", st)
	}
	// Completed: a reopened parent is left alone.
	f.Issues[100].Open = true
	before = f.Writes
	if p = sync(t, f, opts); p.Result.Phase != PhaseSucceeded || f.Writes != before {
		t.Errorf("reopened after completion: %s, %d writes", p.Result.Phase, f.Writes-before)
	}
}

// A child created whose link and status were lost (a crash) is found by
// its marker among the bot's issues and linked, not created again.
func TestSyncRecoversAnUnlinkedChild(t *testing.T) {
	f, opts := planParent(t)
	f.Issues[150] = &graphtest.Issue{Title: "A", Body: "do a\n\n" + ChildInfo{Parent: 100, Source: SourcePlan, Step: "a", Depth: 1, Params: HashParams(nil)}.Marker(),
		Author: "watcher", Open: true, Labels: []string{"overseer"}}
	sync(t, f, opts)
	if !slices.Contains(f.Issues[100].SubIssues, 150) {
		t.Error("the lost child is not linked")
	}
	n := 0
	for _, is := range f.Issues {
		if m, ok := ParseChildMarker(is.Body); ok && m.Step == "a" {
			n++
		}
	}
	if n != 1 {
		t.Errorf("%d children of a, want 1", n)
	}
}

// A merged PR by a pool bot completes its child; one by anyone else does
// not.
func TestSyncMergedBotPR(t *testing.T) {
	f, opts := planParent(t)
	sync(t, f, opts)
	a := childOf(f, 100, "a")
	f.Issues[300] = &graphtest.Issue{Title: "human PR", Author: "mallory", IsPR: true, Merged: true}
	f.Issues[a].Refs = []int{300}
	sync(t, f, opts)
	if !f.Issues[a].Open {
		t.Fatal("a PR by someone outside the pool completed the child")
	}
	f.Issues[301] = &graphtest.Issue{Title: "bot PR", Author: "hopper-coder-bot", IsPR: true, Merged: true}
	f.Issues[a].Refs = []int{300, 301}
	sync(t, f, opts)
	if f.Issues[a].Open || f.Issues[a].Reason != "completed" || childOf(f, 100, "b") == 0 {
		t.Errorf("a open %v reason %q, b=#%d", f.Issues[a].Open, f.Issues[a].Reason, childOf(f, 100, "b"))
	}
}

func TestSyncDryRun(t *testing.T) {
	f, opts := planParent(t)
	opts.DryRun = true
	var log []string
	opts.Logf = func(format string, args ...any) { log = append(log, fmt.Sprintf(format, args...)) }
	before := f.Writes
	p := sync(t, f, opts)
	if f.Writes != before || childOf(f, 100, "a") != 0 {
		t.Errorf("a dry run wrote %d times", f.Writes-before)
	}
	if !slices.ContainsFunc(log, func(l string) bool { return strings.HasPrefix(l, "would create step a's child") }) || p.Status == "" {
		t.Errorf("log %v, status %q", log, p.Status)
	}
}

func TestSyncTemplate(t *testing.T) {
	f := graphtest.New()
	f.Files[".agents/graphs/two.yaml"] = `apiVersion: factory.gemini.google.com/v1alpha1
kind: IssueGraph
metadata: {name: two}
spec:
  params: [{name: kind, pattern: '^[A-Z][a-z]+$', example: Foo}]
  steps:
    - {id: types, title: 'Types for {{ .Params.kind }}', body: b}
    - {id: controller, after: [types], title: 'Controller for {{ .Params.kind }}', body: c}
`
	f.Issues[100] = &graphtest.Issue{Title: "Kind", Body: "```factory-graph\ngraph: two\nparams: {kind: Spanner}\n```\n", Author: "alice", Assoc: "MEMBER", Open: true}
	opts := Options{Issue: 100, Trigger: "overseer", BotLogin: "watcher"}
	sync(t, f, opts)
	if n := childOf(f, 100, "types"); n == 0 || f.Issues[n].Title != "Types for Spanner" {
		t.Fatalf("types child #%d", n)
	}
	// A required param missing, or one that breaks its pattern: invalid,
	// nothing created.
	for name, body := range map[string]string{
		"missing":   "```factory-graph\ngraph: two\n```\n",
		"bad value": "```factory-graph\ngraph: two\nparams: {kind: lower}\n```\n",
		"unknown":   "```factory-graph\ngraph: two\nparams: {kind: Spanner, colour: red}\n```\n",
		"no file":   "```factory-graph\ngraph: three\n```\n",
	} {
		f.Issues[101] = &graphtest.Issue{Title: "Kind", Body: body, Author: "alice", Assoc: "MEMBER", Open: true}
		o := opts
		o.Issue = 101
		if p := sync(t, f, o); p.Result.Phase != PhaseInvalid || childOf(f, 101, "types") != 0 {
			t.Errorf("%s: phase %s (%s)", name, p.Result.Phase, p.Result.Reason)
		}
	}
}

func TestSyncReopensAParentClosedByACommit(t *testing.T) {
	f, opts := planParent(t)
	sync(t, f, opts)
	f.Issues[100].Open, f.Issues[100].ClosedByCommit = false, true
	sync(t, f, opts)
	if !f.Issues[100].Open {
		t.Error("a parent closed by a commit with steps left was not reopened")
	}
	f.Issues[100].Open, f.Issues[100].ClosedByCommit = false, false
	if p := sync(t, f, opts); p.Result.Phase != PhaseCancelled || f.Issues[100].Open {
		t.Errorf("closed by a person: %s, open %v", p.Result.Phase, f.Issues[100].Open)
	}
}

func TestStartGraph(t *testing.T) {
	f := graphtest.New()
	f.Issues[100] = &graphtest.Issue{Title: "Epic", Body: "x", Author: "alice", Open: true}
	f.Comment(100, "hopper-coder-bot", "NONE", planComment)
	opts := Options{Issue: 100, Trigger: "overseer", BotLogin: "watcher", Pool: []string{"hopper-coder-bot", "watcher"}}
	ctx := context.Background()
	if err := StartGraph(ctx, f, opts, Start{Source: SourcePlan}); err != nil {
		t.Fatal(err)
	}
	s, err := FindStart(f.Issues[100].Body, f.Issues[100].Comments, opts.env())
	if err != nil || s == nil || s.Source != SourcePlan {
		t.Fatalf("start = %+v, %v", s, err)
	}
	before := f.Writes
	if err := StartGraph(ctx, f, opts, Start{Source: SourcePlan}); err != nil || f.Writes != before {
		t.Errorf("started twice: %v, %d writes", err, f.Writes-before)
	}
	// A plan without a graph block, and an issue with no trusted plan,
	// cannot start one.
	f.Issues[101] = &graphtest.Issue{Title: "One PR", Body: "x", Author: "alice", Open: true}
	f.Comment(101, "hopper-coder-bot", "NONE", "**Implementation plan**\n\nOne PR.\n<!-- factory:comment key=plan v=1 -->")
	f.Issues[102] = &graphtest.Issue{Title: "Forged", Body: "x", Author: "alice", Open: true}
	f.Comment(102, "mallory", "NONE", planComment)
	for _, n := range []int{101, 102} {
		o := opts
		o.Issue = n
		if err := StartGraph(ctx, f, o, Start{Source: SourcePlan}); err == nil {
			t.Errorf("#%d started a graph", n)
		}
	}
	// A start block posted by someone outside the trusted kinds is not one.
	f.Issues[103] = &graphtest.Issue{Title: "x", Body: "x", Author: "alice", Open: true}
	f.Comment(103, "mallory", "NONE", Start{Source: SourcePlan}.StartComment())
	if s, _ := FindStart(f.Issues[103].Body, f.Issues[103].Comments, opts.env()); s != nil {
		t.Error("an outsider's start block counted")
	}
}

func TestAdopt(t *testing.T) {
	f, opts := planParent(t)
	f.Issues[160] = &graphtest.Issue{Title: "Already filed", Body: "I did a.", Author: "alice", Open: true}
	ctx := context.Background()
	if err := Adopt(ctx, f, opts, "a", 160); err != nil {
		t.Fatal(err)
	}
	if m, ok := ParseChildMarker(f.Issues[160].Body); !ok || m.Step != "a" || !slices.Contains(f.Issues[100].SubIssues, 160) {
		t.Errorf("adopted: %+v, linked %v", m, f.Issues[100].SubIssues)
	}
	if err := Adopt(ctx, f, opts, "a", 160); err == nil {
		t.Error("adopted twice")
	}
	if err := Adopt(ctx, f, opts, "nope", 161); err == nil {
		t.Error("adopted into a step that is not there")
	}
	// The next pass takes it as a's child.
	sync(t, f, opts)
	if childOf(f, 100, "a") != 160 {
		t.Errorf("a's child = #%d", childOf(f, 100, "a"))
	}
}

func TestParseStart(t *testing.T) {
	for _, tc := range []struct{ body, want string }{
		{"```factory-graph\ngraph: two\nsource: plan\n```", "both"},
		{"```factory-graph\nparams: {a: b}\n```", "neither"},
		{"```factory-graph\nsource: elsewhere\n```", "is not"},
		{"```factory-graph\ngraph: ../x\n```", "bare name"},
		{"```factory-graph\nsource: plan\nparams: {a: b}\n```", "take none"},
		{"```factory-graph\ngraph: two\n", "not closed"},
	} {
		if _, err := ParseStart(tc.body); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%q: %v, want %q", tc.body, err, tc.want)
		}
	}
	s := Start{Graph: "kcc-greenfield", Params: map[string]string{"kind": "Foo", "version": "v1alpha1"}}
	got, err := ParseStart(s.StartBlock())
	if err != nil || got.Graph != s.Graph || got.Params["kind"] != "Foo" || got.Params["version"] != "v1alpha1" {
		t.Errorf("round trip: %+v, %v", got, err)
	}
}
