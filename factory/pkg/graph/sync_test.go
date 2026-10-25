package graph

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	githubv39 "github.com/google/go-github/v39/github"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/github"
)

// fakeIssue is one issue or PR of the fake repository.
type fakeIssue struct {
	title, body, author, assoc string
	open                       bool
	reason                     string
	labels                     []string
	comments                   []*githubv39.IssueComment
	subIssues                  []int
	isPR, merged               bool
	refs                       []int // issues or PRs that cross-reference it
	closedByCommit             bool
}

// fakeGitHub is an in-memory repository: GitHub as a pass sees it.
type fakeGitHub struct {
	issues  map[int]*fakeIssue
	next    int
	files   map[string]string
	writes  int
	nextCID int64
}

func newFake() *fakeGitHub {
	return &fakeGitHub{issues: map[int]*fakeIssue{}, next: 200, files: map[string]string{}, nextCID: 1}
}

func (f *fakeGitHub) issue(n int) (*fakeIssue, error) {
	if is, ok := f.issues[n]; ok {
		return is, nil
	}
	return nil, fmt.Errorf("no issue #%d", n)
}

func (f *fakeGitHub) GetIssue(_ context.Context, n int) (*githubv39.Issue, error) {
	is, err := f.issue(n)
	if err != nil {
		return nil, err
	}
	state := "closed"
	if is.open {
		state = "open"
	}
	out := &githubv39.Issue{Number: githubv39.Int(n), Title: githubv39.String(is.title), Body: githubv39.String(is.body), State: githubv39.String(state),
		User: &githubv39.User{Login: githubv39.String(is.author)}}
	for _, l := range is.labels {
		out.Labels = append(out.Labels, &githubv39.Label{Name: githubv39.String(l)})
	}
	if is.isPR {
		out.PullRequestLinks = &githubv39.PullRequestLinks{}
	}
	return out, nil
}

func (f *fakeGitHub) ref(n int) github.IssueRef {
	is := f.issues[n]
	return github.IssueRef{Number: n, IsPR: is.isPR, Title: is.title, Body: is.body, Open: is.open, StateReason: is.reason, Merged: is.merged, Labels: slices.Clone(is.labels), Author: is.author}
}

func (f *fakeGitHub) GetIssueRef(_ context.Context, n int) (github.IssueRef, error) {
	if _, err := f.issue(n); err != nil {
		return github.IssueRef{}, err
	}
	return f.ref(n), nil
}

func (f *fakeGitHub) ListIssueComments(_ context.Context, n int) ([]*githubv39.IssueComment, error) {
	is, err := f.issue(n)
	if err != nil {
		return nil, err
	}
	return slices.Clone(is.comments), nil
}

func (f *fakeGitHub) ListIssueTimeline(_ context.Context, n int) ([]*githubv39.Timeline, bool, error) {
	is := f.issues[n]
	if is.open {
		return nil, true, nil
	}
	ev := &githubv39.Timeline{Event: githubv39.String("closed")}
	if is.closedByCommit {
		ev.CommitID = githubv39.String("abc123")
	}
	return []*githubv39.Timeline{ev}, true, nil
}

func (f *fakeGitHub) ListIssues(_ context.Context, opts *githubv39.IssueListByRepoOptions) ([]*githubv39.Issue, *githubv39.Response, error) {
	var nums []int
	for n, is := range f.issues {
		if !is.isPR && strings.EqualFold(is.author, opts.Creator) {
			nums = append(nums, n)
		}
	}
	slices.Sort(nums)
	slices.Reverse(nums)
	var out []*githubv39.Issue
	for _, n := range nums {
		is, _ := f.GetIssue(context.Background(), n)
		out = append(out, is)
	}
	return out, &githubv39.Response{}, nil
}

func (f *fakeGitHub) ListCrossReferences(_ context.Context, n int) ([]github.IssueRef, error) {
	var out []github.IssueRef
	for _, r := range f.issues[n].refs {
		out = append(out, f.ref(r))
	}
	return out, nil
}

func (f *fakeGitHub) ListSubIssues(_ context.Context, n int) ([]int, error) {
	return slices.Clone(f.issues[n].subIssues), nil
}

func (f *fakeGitHub) ReadFile(_ context.Context, path string) ([]byte, string, error) {
	data, ok := f.files[path]
	if !ok {
		return nil, "", github.ErrUnreadableFile
	}
	return []byte(data), "sha1", nil
}

func (f *fakeGitHub) CreateIssue(_ context.Context, title, body string, labels []string) (int, error) {
	f.writes++
	f.next++
	f.issues[f.next] = &fakeIssue{title: title, body: body, author: "watcher", open: true, labels: slices.Clone(labels)}
	return f.next, nil
}

func (f *fakeGitHub) EditIssue(_ context.Context, n int, title, body string) error {
	f.writes++
	f.issues[n].title, f.issues[n].body = title, body
	return nil
}

func (f *fakeGitHub) AddLabels(_ context.Context, n int, labels []string) error {
	f.writes++
	for _, l := range labels {
		if !slices.Contains(f.issues[n].labels, l) {
			f.issues[n].labels = append(f.issues[n].labels, l)
		}
	}
	return nil
}

func (f *fakeGitHub) AddSubIssue(_ context.Context, parent, child int) error {
	f.writes++
	if !slices.Contains(f.issues[parent].subIssues, child) {
		f.issues[parent].subIssues = append(f.issues[parent].subIssues, child)
	}
	return nil
}

func (f *fakeGitHub) comment(n int, login, assoc, body string) {
	at := time.Date(2026, 10, 25, 10, 0, 0, 0, time.UTC).Add(time.Duration(f.nextCID) * time.Minute)
	f.issues[n].comments = append(f.issues[n].comments, &githubv39.IssueComment{ID: githubv39.Int64(f.nextCID), Body: githubv39.String(body),
		User: &githubv39.User{Login: githubv39.String(login)}, AuthorAssociation: githubv39.String(assoc), CreatedAt: &at})
	f.nextCID++
}

func (f *fakeGitHub) AddComment(_ context.Context, n int, body string) error {
	f.writes++
	f.comment(n, "watcher", "NONE", body)
	return nil
}

func (f *fakeGitHub) EditComment(_ context.Context, id int64, body string) error {
	f.writes++
	for _, is := range f.issues {
		for _, c := range is.comments {
			if c.GetID() == id {
				c.Body = githubv39.String(body)
				return nil
			}
		}
	}
	return fmt.Errorf("no comment %d", id)
}

func (f *fakeGitHub) SetIssueState(_ context.Context, n int, state, reason string) error {
	f.writes++
	f.issues[n].open, f.issues[n].reason = state == "open", reason
	return nil
}

// closeChild closes a child as a person or a merged PR would.
func (f *fakeGitHub) closeChild(n int, reason string) {
	f.issues[n].open, f.issues[n].reason = false, reason
}

// childOf is the open or closed child of step, 0 for none.
func (f *fakeGitHub) childOf(parent int, step string) int {
	for n, is := range f.issues {
		if m, ok := ParseChildMarker(is.body); ok && m.Parent == parent && m.Step == step {
			return n
		}
	}
	return 0
}

func (f *fakeGitHub) statusComments(n int) []string {
	var out []string
	for _, c := range f.issues[n].comments {
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

func planParent(t *testing.T) (*fakeGitHub, Options) {
	t.Helper()
	f := newFake()
	f.issues[100] = &fakeIssue{title: "Epic", body: "Build it.", author: "alice", assoc: "MEMBER", open: true, labels: []string{"overseer/planned"}}
	f.comment(100, "hopper-coder-bot", "NONE", planComment)
	f.comment(100, "hopper-coder-bot", "NONE", Start{Source: SourcePlan}.StartComment())
	return f, Options{Issue: 100, Trigger: "overseer", BotLogin: "watcher", Pool: []string{"hopper-coder-bot", "watcher"}}
}

func sync(t *testing.T, f *fakeGitHub, opts Options) *Pass {
	t.Helper()
	p, err := Sync(context.Background(), f, opts)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// A plan's graph from start to finish: children created as their steps
// become ready, linked as sub-issues and labelled by their then; one
// status comment, edited in place; the parent closed at the end.
func TestSyncLifecycle(t *testing.T) {
	f, opts := planParent(t)

	p := sync(t, f, opts)
	a := f.childOf(100, "a")
	if p.Result.Phase != PhaseRunning || a == 0 || f.childOf(100, "b") != 0 {
		t.Fatalf("pass 1: phase %s, a=#%d", p.Result.Phase, a)
	}
	if is := f.issues[a]; !slices.Contains(is.labels, "overseer") || !slices.Contains(f.issues[100].subIssues, a) || !strings.Contains(is.body, "do a") {
		t.Errorf("a = %+v", is)
	}
	if m, _ := ParseChildMarker(f.issues[a].body); m.Depth != 1 || m.Source != SourcePlan {
		t.Errorf("a's marker = %+v", m)
	}
	if len(f.statusComments(100)) != 1 || !strings.Contains(f.statusComments(100)[0], fmt.Sprintf("| a | - | fix | #%d | 🔄 Active |", a)) {
		t.Errorf("status:\n%s", strings.Join(f.statusComments(100), "\n---\n"))
	}

	// Nothing changed: nothing written, not even the status.
	before := f.writes
	sync(t, f, opts)
	if f.writes != before {
		t.Errorf("an idle pass wrote %d times", f.writes-before)
	}

	f.closeChild(a, "completed")
	sync(t, f, opts)
	b, c := f.childOf(100, "b"), f.childOf(100, "c")
	if b == 0 || c == 0 {
		t.Fatalf("after a: b=#%d c=#%d", b, c)
	}
	if !strings.Contains(f.issues[b].body, fmt.Sprintf("after #%d", a)) || !slices.Equal(f.issues[c].labels, []string{"overseer/plan"}) {
		t.Errorf("b body %q, c labels %v", f.issues[b].body, f.issues[c].labels)
	}

	f.closeChild(b, "completed")
	f.closeChild(c, "not_planned")
	sync(t, f, opts)
	fin := f.childOf(100, FinallyID)
	if fin == 0 {
		t.Fatal("no finally child")
	}
	f.closeChild(fin, "completed")
	p = sync(t, f, opts)
	if p.Result.Phase != PhaseSucceeded || f.issues[100].open || f.issues[100].reason != "completed" {
		t.Errorf("end: phase %s, parent open %v reason %q", p.Result.Phase, f.issues[100].open, f.issues[100].reason)
	}
	if n := len(f.statusComments(100)); n != 1 {
		t.Errorf("%d status comments, want one edited in place", n)
	}
	if st, _ := ParseState(f.statusComments(100)[0]); !st.Completed || st.Children["a"] != a {
		t.Errorf("state = %+v", st)
	}
	// Completed: a reopened parent is left alone.
	f.issues[100].open = true
	before = f.writes
	if p = sync(t, f, opts); p.Result.Phase != PhaseSucceeded || f.writes != before {
		t.Errorf("reopened after completion: %s, %d writes", p.Result.Phase, f.writes-before)
	}
}

// A child created whose link and status were lost (a crash) is found by
// its marker among the bot's issues and linked, not created again.
func TestSyncRecoversAnUnlinkedChild(t *testing.T) {
	f, opts := planParent(t)
	f.issues[150] = &fakeIssue{title: "A", body: "do a\n\n" + ChildInfo{Parent: 100, Source: SourcePlan, Step: "a", Depth: 1, Params: HashParams(nil)}.Marker(),
		author: "watcher", open: true, labels: []string{"overseer"}}
	sync(t, f, opts)
	if !slices.Contains(f.issues[100].subIssues, 150) {
		t.Error("the lost child is not linked")
	}
	n := 0
	for _, is := range f.issues {
		if m, ok := ParseChildMarker(is.body); ok && m.Step == "a" {
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
	a := f.childOf(100, "a")
	f.issues[300] = &fakeIssue{title: "human PR", author: "mallory", isPR: true, merged: true}
	f.issues[a].refs = []int{300}
	sync(t, f, opts)
	if !f.issues[a].open {
		t.Fatal("a PR by someone outside the pool completed the child")
	}
	f.issues[301] = &fakeIssue{title: "bot PR", author: "hopper-coder-bot", isPR: true, merged: true}
	f.issues[a].refs = []int{300, 301}
	sync(t, f, opts)
	if f.issues[a].open || f.issues[a].reason != "completed" || f.childOf(100, "b") == 0 {
		t.Errorf("a open %v reason %q, b=#%d", f.issues[a].open, f.issues[a].reason, f.childOf(100, "b"))
	}
}

func TestSyncDryRun(t *testing.T) {
	f, opts := planParent(t)
	opts.DryRun = true
	var log []string
	opts.Logf = func(format string, args ...any) { log = append(log, fmt.Sprintf(format, args...)) }
	before := f.writes
	p := sync(t, f, opts)
	if f.writes != before || f.childOf(100, "a") != 0 {
		t.Errorf("a dry run wrote %d times", f.writes-before)
	}
	if !slices.ContainsFunc(log, func(l string) bool { return strings.HasPrefix(l, "would create step a's child") }) || p.Status == "" {
		t.Errorf("log %v, status %q", log, p.Status)
	}
}

func TestSyncTemplate(t *testing.T) {
	f := newFake()
	f.files[".agents/graphs/two.yaml"] = `apiVersion: factory.gemini.google.com/v1alpha1
kind: IssueGraph
metadata: {name: two}
spec:
  params: [{name: kind, pattern: '^[A-Z][a-z]+$', example: Foo}]
  steps:
    - {id: types, title: 'Types for {{ .Params.kind }}', body: b}
    - {id: controller, after: [types], title: 'Controller for {{ .Params.kind }}', body: c}
`
	f.issues[100] = &fakeIssue{title: "Kind", body: "```factory-graph\ngraph: two\nparams: {kind: Spanner}\n```\n", author: "alice", assoc: "MEMBER", open: true}
	opts := Options{Issue: 100, Trigger: "overseer", BotLogin: "watcher"}
	sync(t, f, opts)
	if n := f.childOf(100, "types"); n == 0 || f.issues[n].title != "Types for Spanner" {
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
		f.issues[101] = &fakeIssue{title: "Kind", body: body, author: "alice", assoc: "MEMBER", open: true}
		o := opts
		o.Issue = 101
		if p := sync(t, f, o); p.Result.Phase != PhaseInvalid || f.childOf(101, "types") != 0 {
			t.Errorf("%s: phase %s (%s)", name, p.Result.Phase, p.Result.Reason)
		}
	}
}

func TestSyncReopensAParentClosedByACommit(t *testing.T) {
	f, opts := planParent(t)
	sync(t, f, opts)
	f.issues[100].open, f.issues[100].closedByCommit = false, true
	sync(t, f, opts)
	if !f.issues[100].open {
		t.Error("a parent closed by a commit with steps left was not reopened")
	}
	f.issues[100].open, f.issues[100].closedByCommit = false, false
	if p := sync(t, f, opts); p.Result.Phase != PhaseCancelled || f.issues[100].open {
		t.Errorf("closed by a person: %s, open %v", p.Result.Phase, f.issues[100].open)
	}
}

func TestStartGraph(t *testing.T) {
	f := newFake()
	f.issues[100] = &fakeIssue{title: "Epic", body: "x", author: "alice", open: true}
	f.comment(100, "hopper-coder-bot", "NONE", planComment)
	opts := Options{Issue: 100, Trigger: "overseer", BotLogin: "watcher", Pool: []string{"hopper-coder-bot", "watcher"}}
	ctx := context.Background()
	if err := StartGraph(ctx, f, opts, Start{Source: SourcePlan}); err != nil {
		t.Fatal(err)
	}
	s, err := FindStart(f.issues[100].body, f.issues[100].comments, opts.env())
	if err != nil || s == nil || s.Source != SourcePlan {
		t.Fatalf("start = %+v, %v", s, err)
	}
	before := f.writes
	if err := StartGraph(ctx, f, opts, Start{Source: SourcePlan}); err != nil || f.writes != before {
		t.Errorf("started twice: %v, %d writes", err, f.writes-before)
	}
	// A plan without a graph block, and an issue with no trusted plan,
	// cannot start one.
	f.issues[101] = &fakeIssue{title: "One PR", body: "x", author: "alice", open: true}
	f.comment(101, "hopper-coder-bot", "NONE", "**Implementation plan**\n\nOne PR.\n<!-- factory:comment key=plan v=1 -->")
	f.issues[102] = &fakeIssue{title: "Forged", body: "x", author: "alice", open: true}
	f.comment(102, "mallory", "NONE", planComment)
	for _, n := range []int{101, 102} {
		o := opts
		o.Issue = n
		if err := StartGraph(ctx, f, o, Start{Source: SourcePlan}); err == nil {
			t.Errorf("#%d started a graph", n)
		}
	}
	// A start block posted by someone outside the trusted kinds is not one.
	f.issues[103] = &fakeIssue{title: "x", body: "x", author: "alice", open: true}
	f.comment(103, "mallory", "NONE", Start{Source: SourcePlan}.StartComment())
	if s, _ := FindStart(f.issues[103].body, f.issues[103].comments, opts.env()); s != nil {
		t.Error("an outsider's start block counted")
	}
}

func TestAdopt(t *testing.T) {
	f, opts := planParent(t)
	f.issues[160] = &fakeIssue{title: "Already filed", body: "I did a.", author: "alice", open: true}
	ctx := context.Background()
	if err := Adopt(ctx, f, opts, "a", 160); err != nil {
		t.Fatal(err)
	}
	if m, ok := ParseChildMarker(f.issues[160].body); !ok || m.Step != "a" || !slices.Contains(f.issues[100].subIssues, 160) {
		t.Errorf("adopted: %+v, linked %v", m, f.issues[100].subIssues)
	}
	if err := Adopt(ctx, f, opts, "a", 160); err == nil {
		t.Error("adopted twice")
	}
	if err := Adopt(ctx, f, opts, "nope", 161); err == nil {
		t.Error("adopted into a step that is not there")
	}
	// The next pass takes it as a's child.
	sync(t, f, opts)
	if f.childOf(100, "a") != 160 {
		t.Errorf("a's child = #%d", f.childOf(100, "a"))
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
