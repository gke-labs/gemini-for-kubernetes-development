package fanout

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"

	githubv39 "github.com/google/go-github/v39/github"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/github"
)

// fakeGitHub is a repository in memory. An issue's timeline lists the issues
// whose body mentions it, unless hideTimeline is set, as GitHub's can lag.
type fakeGitHub struct {
	issues       map[int]*github.IssueRef
	comments     map[int][]*githubv39.IssueComment
	next         int
	nextComment  int64
	hideTimeline bool
	closed       []int
	files        map[string]string
}

func newFake(body string, labels ...string) *fakeGitHub {
	return &fakeGitHub{
		issues:   map[int]*github.IssueRef{parent: {Number: parent, Title: "P", Body: body, Open: true, Labels: labels}},
		comments: map[int][]*githubv39.IssueComment{},
		next:     parent + 1,
	}
}

func (f *fakeGitHub) GetIssue(_ context.Context, n int) (*githubv39.Issue, error) {
	ref := f.issues[n]
	state := "open"
	if !ref.Open {
		state = "closed"
	}
	issue := &githubv39.Issue{Number: githubv39.Int(n), Title: githubv39.String(ref.Title), Body: githubv39.String(ref.Body), State: githubv39.String(state)}
	for _, l := range ref.Labels {
		issue.Labels = append(issue.Labels, &githubv39.Label{Name: githubv39.String(l)})
	}
	return issue, nil
}

func (f *fakeGitHub) ListIssueComments(_ context.Context, n int) ([]*githubv39.IssueComment, error) {
	return f.comments[n], nil
}

func (f *fakeGitHub) ListCrossReferences(_ context.Context, n int) ([]github.IssueRef, error) {
	if f.hideTimeline {
		return nil, nil
	}
	var out []github.IssueRef
	for i := parent + 1; i < f.next; i++ {
		if ref := f.issues[i]; ref != nil && strings.Contains(ref.Body, fmt.Sprintf("#%d.", n)) {
			out = append(out, *ref)
		}
	}
	return out, nil
}

func (f *fakeGitHub) GetIssueRef(_ context.Context, n int) (github.IssueRef, error) {
	return *f.issues[n], nil
}

func (f *fakeGitHub) CreateIssue(_ context.Context, title, body string, labels []string) (int, error) {
	n := f.next
	f.next++
	f.issues[n] = &github.IssueRef{Number: n, Title: title, Body: body, Open: true, Labels: slices.Clone(labels)}
	return n, nil
}

func (f *fakeGitHub) EditIssue(_ context.Context, n int, title, body string) error {
	f.issues[n].Title, f.issues[n].Body = title, body
	return nil
}

func (f *fakeGitHub) AddLabels(_ context.Context, n int, labels []string) error {
	f.issues[n].Labels = append(f.issues[n].Labels, labels...)
	return nil
}

func (f *fakeGitHub) AddComment(_ context.Context, n int, body string) error {
	f.nextComment++
	f.comments[n] = append(f.comments[n], &githubv39.IssueComment{
		ID: githubv39.Int64(f.nextComment), Body: githubv39.String(body), User: &githubv39.User{Login: githubv39.String("bot")},
	})
	return nil
}

func (f *fakeGitHub) EditComment(_ context.Context, id int64, body string) error {
	for _, cs := range f.comments {
		for _, c := range cs {
			if c.GetID() == id {
				c.Body = githubv39.String(body)
			}
		}
	}
	return nil
}

func (f *fakeGitHub) CloseIssue(_ context.Context, n int) error {
	f.issues[n].Open = false
	f.closed = append(f.closed, n)
	return nil
}

func (f *fakeGitHub) ReadFile(_ context.Context, path string) ([]byte, string, error) {
	data, ok := f.files[path]
	if !ok {
		return nil, "", fmt.Errorf("%s: %w", path, github.ErrUnreadableFile)
	}
	return []byte(data), "0123456789abcdef", nil
}

// closeChild closes a child with a merged PR, as a coder bot's merge would.
func (f *fakeGitHub) closeChild(n int) {
	f.issues[n].Open = false
	pr := f.next
	f.next++
	f.issues[pr] = &github.IssueRef{Number: pr, IsPR: true, Body: fmt.Sprintf("Fixes #%d.", n), Merged: true}
}

func (f *fakeGitHub) labelled(label string) []int {
	var out []int
	for i := parent + 1; i < f.next; i++ {
		if ref := f.issues[i]; ref != nil && !ref.IsPR && slices.Contains(ref.Labels, label) {
			out = append(out, i)
		}
	}
	return out
}

func (f *fakeGitHub) removeLabel(n int, label string) {
	f.issues[n].Labels = slices.DeleteFunc(f.issues[n].Labels, func(l string) bool { return l == label })
}

func TestSyncFanOut(t *testing.T) {
	ctx := context.Background()
	body := "## Fan-out\ncreate: all\n\n## Task\nDo {{.item.name}}.\n\n## Items\n- [ ] a\n- [ ] b\n- [ ] c\n- [ ] d\n\n## Finally\nClean up.\n"
	gh := newFake(body, "overseer/fanout")
	opts := SyncOptions{Issue: parent, TriggerLabel: "overseer", BotLogin: "bot"}
	sync := func() SyncResult {
		t.Helper()
		res, err := Sync(ctx, gh, opts)
		if err != nil {
			t.Fatal(err)
		}
		return res
	}

	// A dry run writes nothing.
	dry := opts
	dry.DryRun = true
	if _, err := Sync(ctx, gh, dry); err != nil {
		t.Fatal(err)
	}
	if gh.next != parent+1 || len(gh.comments[parent]) != 0 {
		t.Fatalf("dry run wrote: %d issues, %d comments", gh.next-parent-1, len(gh.comments[parent]))
	}

	// First pass: every child, the first two labelled, and the progress.
	sync()
	if got, want := gh.labelled("overseer"), []int{101, 102}; !slices.Equal(got, want) {
		t.Fatalf("labelled %v, want %v", got, want)
	}
	if gh.next != 105 || len(gh.comments[parent]) != 1 {
		t.Fatalf("got %d children and %d comments, want 4 and 1", gh.next-parent-1, len(gh.comments[parent]))
	}

	// A pass whose timeline lags still knows its children, by the state.
	gh.hideTimeline = true
	sync()
	gh.hideTimeline = false
	if gh.next != 105 {
		t.Fatalf("a lagging timeline made %d more children", gh.next-105)
	}

	// Two done: the checkpoint stops the fan-out.
	gh.closeChild(101)
	gh.closeChild(102)
	res := sync()
	if res.Plan.Stop == "" || !slices.Contains(gh.issues[parent].Labels, "overseer/stop") {
		t.Fatalf("no checkpoint stop: %+v", res.Plan)
	}
	if got := gh.labelled("overseer"); len(got) != 2 {
		t.Fatalf("labelled %v at a checkpoint", got)
	}

	// Stopped, the spec is edited: nothing changes until the stop label goes.
	gh.issues[parent].Body = strings.Replace(body, "Do {{.item.name}}.", "Do {{.item.name}}, carefully.", 1)
	sync()
	if strings.Contains(gh.issues[103].Body, "carefully") {
		t.Fatal("a child was rewritten while stopped")
	}

	// Resumed: the waiting children are rewritten and labelled (window 4).
	gh.removeLabel(parent, "overseer/stop")
	sync()
	if !strings.Contains(gh.issues[103].Body, "carefully") || !strings.Contains(gh.issues[104].Body, "carefully") {
		t.Fatal("the waiting children were not rewritten")
	}
	if got := gh.labelled("overseer"); len(got) != 4 {
		t.Fatalf("labelled %v, want all four", got)
	}

	// Every item done: the final child, then the parent closes.
	gh.closeChild(103)
	gh.closeChild(104)
	sync()
	final := gh.next - 1
	if _, _, isFinal, _ := ParseMarker(gh.issues[final].Body); !isFinal || !slices.Contains(gh.issues[final].Labels, "overseer") {
		t.Fatalf("#%d is not a labelled final child: %+v", final, gh.issues[final])
	}
	gh.closeChild(final)
	sync()
	if !slices.Equal(gh.closed, []int{parent}) {
		t.Fatalf("closed %v, want the parent", gh.closed)
	}
	if len(gh.comments[parent]) != 2 {
		t.Errorf("got %d comments, want progress + one checkpoint", len(gh.comments[parent]))
	}
	if progress := gh.comments[parent][0].GetBody(); !strings.Contains(progress, "4 of 4 done") {
		t.Errorf("progress comment:\n%s", progress)
	}
}

func TestSyncSpecSources(t *testing.T) {
	ctx := context.Background()
	spec := SpecMarker + "\n## Task\nDo {{.item.name}}.\n\n## Items\n- [ ] only\n"
	comment := func(login, assoc, body string) *githubv39.IssueComment {
		return &githubv39.IssueComment{User: &githubv39.User{Login: githubv39.String(login)}, AuthorAssociation: githubv39.String(assoc), Body: githubv39.String(body)}
	}
	tests := []struct {
		name     string
		body     string
		comments []*githubv39.IssueComment
		noSpec   bool
		specErr  bool
	}{
		{name: "no spec", body: "Please migrate these.\n- [ ] a\n", noSpec: true},
		{name: "a spec comment by anyone is ignored", body: "free text", comments: []*githubv39.IssueComment{comment("someone", "NONE", spec)}, noSpec: true},
		{name: "the bot's spec comment", body: "free text", comments: []*githubv39.IssueComment{comment("bot", "NONE", spec)}},
		{name: "a maintainer's spec comment", body: "free text", comments: []*githubv39.IssueComment{comment("maintainer", "MEMBER", spec)}},
		{name: "the body", body: spec},
		{name: "a broken spec", body: "## Task\nx\n## Items\nnone\n", specErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			gh := newFake(tc.body)
			gh.comments[parent] = tc.comments
			res, err := Sync(ctx, gh, SyncOptions{Issue: parent, TriggerLabel: "overseer", BotLogin: "bot"})
			if err != nil {
				t.Fatal(err)
			}
			if res.NoSpec != tc.noSpec || (res.SpecError != nil) != tc.specErr {
				t.Fatalf("NoSpec %v, SpecError %v", res.NoSpec, res.SpecError)
			}
			created := gh.next - parent - 1
			if wantCreated := !tc.noSpec && !tc.specErr; (created == 1) != wantCreated {
				t.Errorf("created %d children", created)
			}
		})
	}
}

func TestSyncItemsFile(t *testing.T) {
	ctx := context.Background()
	body := "## Fan-out\n```yaml\nitems: {from: kinds.json, name: \"{{.kind}}\"}\n```\n\n## Task\nFix {{.item.kind}}.\n"
	opts := SyncOptions{Issue: parent, TriggerLabel: "overseer", BotLogin: "bot"}

	gh := newFake(body)
	gh.files = map[string]string{"kinds.json": `[{"kind": "A"}, {"kind": "B"}, {"kind": "C"}]`}
	if _, err := Sync(ctx, gh, opts); err != nil {
		t.Fatal(err)
	}
	// Lazily: the first window's two.
	if gh.next != parent+3 || !strings.HasPrefix(gh.issues[parent+1].Body, "Fix A.") {
		t.Fatalf("got %d children, the first %q", gh.next-parent-1, gh.issues[parent+1].Body)
	}
	if progress := gh.comments[parent][0].GetBody(); !strings.Contains(progress, "Items from `kinds.json` at 0123456.") {
		t.Errorf("the progress comment does not say where the items came from:\n%s", progress)
	}

	// A missing file is the spec's error, reported in the progress comment.
	gh = newFake(body)
	res, err := Sync(ctx, gh, opts)
	if err != nil || res.SpecError == nil || !strings.Contains(gh.comments[parent][0].GetBody(), "items.from") {
		t.Errorf("a missing file: err %v, SpecError %v", err, res.SpecError)
	}
}
