package scan

import (
	"context"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	githubv39 "github.com/google/go-github/v39/github"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/commands/watch/scan/scantest"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/github"
)

var t0 = time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)

// world is a fake repository with a store over it, and a clock.
type world struct {
	repo  *scantest.Repo
	store *Store
	now   time.Time
}

func newWorld(t *testing.T, cfg Config) *world {
	t.Helper()
	w := &world{repo: scantest.NewRepo("o", "r"), now: t0}
	srv := w.repo.Server()
	t.Cleanup(srv.Close)
	gh := githubv39.NewClient(nil)
	gh.BaseURL, _ = url.Parse(srv.URL + "/")
	if cfg.Repo == "" {
		cfg.Repo = "o/r"
	}
	if cfg.TriggerLabel == "" {
		cfg.TriggerLabel = "overseer"
	}
	if cfg.BotUsers == nil {
		cfg.BotUsers = []string{"bot1", "bot2"}
	}
	if cfg.GitHubLogin == "" {
		cfg.GitHubLogin = "watcher"
	}
	w.store = New(cfg, github.ForRepo(gh, "o", "r"))
	w.store.now = func() time.Time { return w.now }
	return w
}

func numbers(items []*githubv39.Issue) []int {
	var out []int
	for _, it := range items {
		out = append(out, it.GetNumber())
	}
	slices.Sort(out)
	return out
}

func (w *world) seed() {
	w.repo.Put(scantest.Item{Number: 1, Labels: []string{"overseer"}, Author: "alice", Updated: t0.Add(-5 * time.Hour)})
	w.repo.Put(scantest.Item{Number: 2, Assignees: []string{"bot1"}, Author: "alice", Updated: t0.Add(-4 * time.Hour)})
	w.repo.Put(scantest.Item{Number: 3, Author: "watcher", Updated: t0.Add(-3 * time.Hour)})
	w.repo.Put(scantest.Item{Number: 4, PR: true, Assignees: []string{"bot2"}, Author: "bot2", Updated: t0.Add(-2 * time.Hour)})
	w.repo.Put(scantest.Item{Number: 5, Author: "alice", Updated: t0.Add(-1 * time.Hour)})                                                // not tracked
	w.repo.Put(scantest.Item{Number: 6, State: "closed", Labels: []string{"overseer"}, Author: "alice", Updated: t0.Add(-1 * time.Hour)}) // closed
}

// A full refresh tracks the open items labelled, assigned to a bot or opened
// by the watcher, and nothing else.
func TestFullRefreshTracksTheUnion(t *testing.T) {
	w := newWorld(t, Config{})
	w.seed()
	if err := w.store.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := numbers(w.store.view(func(*githubv39.Issue) bool { return true }, 0)); !slices.Equal(got, []int{1, 2, 3, 4}) {
		t.Errorf("tracked = %v, want [1 2 3 4]", got)
	}
	if st := w.store.LastStats(); !st.Full || st.Calls != 4 {
		t.Errorf("stats = %+v, want a full refresh of 4 listings (label, bot1, bot2, creator)", st)
	}
	if got := numbers(w.store.Labelled()); !slices.Equal(got, []int{1}) {
		t.Errorf("labelled = %v", got)
	}
	if got := numbers(w.store.Assigned("bot2")); !slices.Equal(got, []int{4}) {
		t.Errorf("assigned to bot2 = %v", got)
	}
}

// An incremental refresh lists what changed since the last pass, less the
// margin, and moves items in and out of the snapshot; what did not change
// stays as it was.
func TestIncrementalRefresh(t *testing.T) {
	w := newWorld(t, Config{})
	w.seed()
	ctx := context.Background()
	if err := w.store.Refresh(ctx); err != nil {
		t.Fatal(err)
	}

	w.now = t0.Add(2 * time.Minute)
	w.repo.Update(5, t0.Add(time.Minute), func(it *scantest.Item) { it.Labels = []string{"overseer"} }) // becomes tracked
	w.repo.Update(2, t0.Add(time.Minute), func(it *scantest.Item) { it.Assignees = nil })               // no longer tracked
	w.repo.Update(1, t0.Add(time.Minute), func(it *scantest.Item) { it.State = "closed" })              // closed
	w.repo.ResetCounts()
	if err := w.store.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	if got := numbers(w.store.view(func(*githubv39.Issue) bool { return true }, 0)); !slices.Equal(got, []int{3, 4, 5}) {
		t.Errorf("tracked = %v, want [3 4 5]", got)
	}
	st := w.store.LastStats()
	if st.Full || st.Calls != 1 || st.Changed != 3 {
		t.Errorf("stats = %+v, want one incremental listing changing 3 items", st)
	}
	q, _ := url.ParseQuery(w.repo.Queries()[0])
	if q.Get("state") != "all" || q.Get("since") != t0.Add(-DefaultMargin).Format(time.RFC3339) {
		t.Errorf("incremental query = %v, want state=all since the last pass's start less the margin", q)
	}

	// Nothing changed: the next incremental pass changes nothing.
	w.now = t0.Add(5 * time.Minute)
	w.repo.ResetCounts()
	if err := w.store.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	if st := w.store.LastStats(); st.Full || st.Changed != 0 || w.repo.ListCalls() != 1 {
		t.Errorf("stats = %+v, calls %d; want one incremental listing changing nothing", st, w.repo.ListCalls())
	}
}

// The views are the shapes the scanners' own listings had: AssignedFirst is
// the first page of the assignee query, newest updated first.
func TestAssignedFirstIsTheFirstPage(t *testing.T) {
	w := newWorld(t, Config{BotUsers: []string{"bot1"}})
	for i := 1; i <= 5; i++ {
		w.repo.Put(scantest.Item{Number: i, Assignees: []string{"bot1"}, Updated: t0.Add(time.Duration(i) * time.Minute)})
	}
	if err := w.store.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	var got []int
	for _, it := range w.store.AssignedFirst("bot1", 3) {
		got = append(got, it.GetNumber())
	}
	if !slices.Equal(got, []int{5, 4, 3}) {
		t.Errorf("first page = %v, want [5 4 3]", got)
	}
}

// A refresh within ReuseFor serves the last one: the scanners on their own
// tickers share a pass instead of each listing.
func TestRefreshIsReused(t *testing.T) {
	w := newWorld(t, Config{})
	w.seed()
	ctx := context.Background()
	_ = w.store.Refresh(ctx)
	w.repo.ResetCounts()
	w.now = w.now.Add(10 * time.Second)
	_ = w.store.Refresh(ctx)
	if n := w.repo.ListCalls(); n != 0 {
		t.Errorf("a refresh within ReuseFor made %d calls", n)
	}
}

// A failed refresh leaves the snapshot as it was and says so.
func TestFailedRefreshKeepsTheSnapshot(t *testing.T) {
	w := newWorld(t, Config{})
	w.seed()
	ctx := context.Background()
	_ = w.store.Refresh(ctx)
	w.store.gh = failingLister{}
	w.now = w.now.Add(time.Minute)
	if err := w.store.Refresh(ctx); err == nil {
		t.Fatal("a failed listing was not reported")
	}
	if got := numbers(w.store.Labelled()); !slices.Equal(got, []int{1}) {
		t.Errorf("labelled after a failed refresh = %v, want the last good [1]", got)
	}
}

type failingLister struct{}

func (failingLister) ListIssues(context.Context, *githubv39.IssueListByRepoOptions) ([]*githubv39.Issue, *githubv39.Response, error) {
	return nil, nil, os.ErrDeadlineExceeded
}

// The snapshot persists: a new store (the overseer's restart) continues
// incrementally from it; one it cannot trust is no snapshot, and the next
// refresh lists in full.
func TestSnapshotPersists(t *testing.T) {
	path := filepath.Join(t.TempDir(), "scan_state.json")
	w := newWorld(t, Config{StatePath: path})
	w.seed()
	ctx := context.Background()
	if err := w.store.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "\"etag\"") || !strings.Contains(string(data), "\"cursor\"") {
		t.Errorf("snapshot = %s", data)
	}

	restarted := func() *world {
		w2 := &world{repo: w.repo, now: w.now.Add(time.Minute)}
		w2.store = New(w.store.cfg, w.store.gh)
		w2.store.now = func() time.Time { return w2.now }
		return w2
	}
	w2 := restarted()
	w.repo.ResetCounts()
	if err := w2.store.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	if st := w2.store.LastStats(); st.Full || st.Items != 4 {
		t.Errorf("after a restart: %+v, want an incremental refresh over the 4 persisted items", st)
	}

	// An unchanged snapshot is not rewritten (the queue directory is
	// committed to git).
	info, _ := os.Stat(path)
	w2.now = w2.now.Add(time.Minute)
	w2.store.refreshed = time.Time{}
	before := w2.store.written
	_ = w2.store.Refresh(ctx)
	if info2, _ := os.Stat(path); !slices.Equal(before, w2.store.written) && info2.ModTime().Equal(info.ModTime()) {
		t.Error("the snapshot changed but was not written")
	}

	for name, content := range map[string]string{
		"corrupt":       "{not json",
		"other version": `{"version": 99, "repo": "o/r"}`,
		"other repo":    `{"version": 1, "repo": "x/y", "triggerLabel": "overseer"}`,
	} {
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		w3 := restarted()
		if err := w3.store.Refresh(ctx); err != nil {
			t.Fatal(err)
		}
		if st := w3.store.LastStats(); !st.Full {
			t.Errorf("%s snapshot: %+v, want a full listing", name, st)
		}
	}
}

// A full rebuild comes due every FullEvery, as a safety net.
func TestFullRebuildComesDue(t *testing.T) {
	w := newWorld(t, Config{FullEvery: 10 * time.Minute})
	w.seed()
	ctx := context.Background()
	_ = w.store.Refresh(ctx)
	w.now = w.now.Add(11 * time.Minute)
	_ = w.store.Refresh(ctx)
	if st := w.store.LastStats(); !st.Full {
		t.Errorf("stats = %+v, want a full rebuild once FullEvery passed", st)
	}
}

// Views are copies: adding a label to one does not change the snapshot.
func TestViewsAreCopies(t *testing.T) {
	w := newWorld(t, Config{})
	w.seed()
	_ = w.store.Refresh(context.Background())
	v := w.store.Assigned("bot1")
	v[0].Labels = append(v[0].Labels, &githubv39.Label{Name: githubv39.String("overseer")})
	if got := numbers(w.store.Labelled()); !slices.Equal(got, []int{1}) {
		t.Errorf("labelled = %v after changing a view's copy", got)
	}
}

// The labels the rules are listed by track a pull request carrying one -
// listed in one GraphQL query by a full refresh, seen by the incremental
// listing once labelled - and only pull requests; a changed set of labels
// is a snapshot of something else.
func TestRuleLabelsTrackPullRequests(t *testing.T) {
	dir := t.TempDir()
	cfg := Config{Labels: []string{"overseer/adopt", "overseer/review"}, StatePath: filepath.Join(dir, "scan_state.json")}
	w := newWorld(t, cfg)
	w.seed()
	w.repo.Put(scantest.Item{Number: 7, PR: true, Labels: []string{"overseer/adopt"}, Author: "alice", Updated: t0.Add(-time.Hour)})
	w.repo.Put(scantest.Item{Number: 8, Labels: []string{"overseer/adopt"}, Author: "alice", Updated: t0.Add(-time.Hour)}) // an issue
	ctx := context.Background()
	if err := w.store.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	if got := numbers(w.store.RuleLabelled()); !slices.Equal(got, []int{7}) {
		t.Errorf("rule labelled = %v, want [7]", got)
	}
	if n, labels := w.repo.GraphQLCalls(); n != 1 || !slices.Equal(labels[0], cfg.Labels) {
		t.Errorf("graphql listings %d %v, want one of %v", n, labels, cfg.Labels)
	}
	if st := w.store.LastStats(); !st.Full || st.Calls != 5 {
		t.Errorf("stats = %+v, want 4 listings and the label query", st)
	}

	// Labelled since: the incremental listing brings it in, no label query.
	w.now = t0.Add(2 * time.Minute)
	w.repo.Put(scantest.Item{Number: 9, PR: true, Author: "bob", Updated: t0.Add(-time.Hour)})
	w.repo.Update(9, t0.Add(time.Minute), func(it *scantest.Item) { it.Labels = []string{"overseer/review"} })
	w.repo.ResetCounts()
	if err := w.store.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	if got := numbers(w.store.RuleLabelled()); !slices.Equal(got, []int{7, 9}) {
		t.Errorf("after labelling, rule labelled = %v, want [7 9]", got)
	}
	if n, _ := w.repo.GraphQLCalls(); n != 0 {
		t.Errorf("an incremental refresh made %d label queries", n)
	}

	// Another set of labels: the persisted snapshot is not reused.
	other := cfg
	other.Labels = []string{"overseer/adopt"}
	w2 := newWorld(t, other)
	w2.store.load()
	if !w2.store.cursor.IsZero() {
		t.Error("a snapshot of other labels was read")
	}
	same := newWorld(t, cfg)
	same.store.load()
	if same.store.cursor.IsZero() {
		t.Error("a snapshot of the same labels was not read")
	}
}

// IssueLabels track an issue carrying one of them, as Labels do a pull
// request: listed in full by one GraphQL query of issues; a pull request
// with the label is not tracked for it.
func TestRuleLabelsTrackIssues(t *testing.T) {
	cfg := Config{IssueLabels: []string{"overseer/triage"}, StatePath: filepath.Join(t.TempDir(), "scan_state.json")}
	w := newWorld(t, cfg)
	w.seed()
	w.repo.Put(scantest.Item{Number: 7, PR: true, Labels: []string{"overseer/triage"}, Author: "alice", Updated: t0.Add(-time.Hour)}) // a pull request
	w.repo.Put(scantest.Item{Number: 8, Labels: []string{"overseer/triage"}, Author: "erin", Association: "CONTRIBUTOR", Updated: t0.Add(-time.Hour)})
	if err := w.store.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	got := w.store.RuleLabelled()
	if !slices.Equal(numbers(got), []int{8}) {
		t.Fatalf("rule labelled = %v, want [8]", numbers(got))
	}
	if got[0].GetAuthorAssociation() != "CONTRIBUTOR" {
		t.Errorf("the snapshot dropped the author association: %q", got[0].GetAuthorAssociation())
	}
	if n, labels := w.repo.GraphQLCalls(); n != 1 || !slices.Equal(labels[0], cfg.IssueLabels) {
		t.Errorf("graphql listings %d %v, want one of %v", n, labels, cfg.IssueLabels)
	}
}
