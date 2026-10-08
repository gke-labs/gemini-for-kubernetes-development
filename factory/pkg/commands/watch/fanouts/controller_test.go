package fanouts

import (
	"context"
	"fmt"
	"testing"

	githubv39 "github.com/google/go-github/v39/github"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/github"
)

// fakeGitHub serves one parent with a spec and records what is written.
type fakeGitHub struct {
	parents  []*githubv39.Issue
	issues   map[int]*githubv39.Issue
	listed   []string
	created  []string
	comments int
}

const spec = "## Task\nDo {{.item.name}}.\n\n## Items\n- [ ] a\n- [ ] b\n- [ ] c\n"

func newFake() *fakeGitHub {
	parent := &githubv39.Issue{
		Number: githubv39.Int(100), Title: githubv39.String("P"), Body: githubv39.String(spec), State: githubv39.String("open"),
		Labels: []*githubv39.Label{{Name: githubv39.String("factory/fanout")}},
	}
	return &fakeGitHub{parents: []*githubv39.Issue{parent}, issues: map[int]*githubv39.Issue{100: parent}}
}

func (f *fakeGitHub) ListIssues(_ context.Context, opts *githubv39.IssueListByRepoOptions) ([]*githubv39.Issue, *githubv39.Response, error) {
	f.listed = append(f.listed, opts.Labels...)
	if opts.Labels[0] != "factory/fanout" {
		return nil, &githubv39.Response{}, nil
	}
	return f.parents, &githubv39.Response{}, nil
}

func (f *fakeGitHub) GetIssue(_ context.Context, n int) (*githubv39.Issue, error) {
	if issue := f.issues[n]; issue != nil {
		return issue, nil
	}
	return nil, fmt.Errorf("no issue #%d", n)
}

func (f *fakeGitHub) ListIssueComments(context.Context, int) ([]*githubv39.IssueComment, error) {
	return nil, nil
}

func (f *fakeGitHub) ListCrossReferences(context.Context, int) ([]github.IssueRef, error) {
	return nil, nil
}

func (f *fakeGitHub) GetIssueRef(_ context.Context, n int) (github.IssueRef, error) {
	return github.IssueRef{Number: n, Open: true}, nil
}

func (f *fakeGitHub) CreateIssue(_ context.Context, title, _ string, _ []string) (int, error) {
	f.created = append(f.created, title)
	return 100 + len(f.created), nil
}

func (f *fakeGitHub) EditIssue(context.Context, int, string, string) error { return nil }
func (f *fakeGitHub) AddLabels(context.Context, int, []string) error       { return nil }
func (f *fakeGitHub) AddComment(context.Context, int, string) error {
	f.comments++
	return nil
}
func (f *fakeGitHub) EditComment(context.Context, int64, string) error { return nil }
func (f *fakeGitHub) CloseIssue(context.Context, int) error            { return nil }
func (f *fakeGitHub) ReadFile(context.Context, string) ([]byte, string, error) {
	return nil, "", github.ErrUnreadableFile
}

func TestSyncOnce(t *testing.T) {
	gh := newFake()
	c := New(Config{TriggerLabel: "factory", GitHubLogin: "bot"}, Deps{GitHub: gh})

	c.SyncOnce(context.Background())

	// Both spellings of the label are listed, and the parent is synced once.
	if len(gh.listed) != 2 {
		t.Errorf("listed %v, want overseer/fanout and factory/fanout", gh.listed)
	}
	if len(gh.created) != 3 || gh.comments != 1 {
		t.Errorf("created %v with %d comments, want 3 children and the progress comment", gh.created, gh.comments)
	}
}

func TestSyncOnce_PausedWhileDraining(t *testing.T) {
	gh := newFake()
	c := New(Config{TriggerLabel: "factory"}, Deps{GitHub: gh, Paused: func() bool { return true }})

	c.SyncOnce(context.Background())

	if len(gh.listed) != 0 {
		t.Errorf("listed %v while draining", gh.listed)
	}
}

func TestSyncOnce_MinNumber(t *testing.T) {
	gh := newFake()
	c := New(Config{TriggerLabel: "factory", MinNumber: 200}, Deps{GitHub: gh})

	c.SyncOnce(context.Background())

	if len(gh.created) != 0 {
		t.Errorf("synced a parent below MinNumber: created %v", gh.created)
	}
}

func TestNudge(t *testing.T) {
	gh := newFake()
	c := New(Config{TriggerLabel: "factory", GitHubLogin: "bot"}, Deps{GitHub: gh})
	ctx := context.Background()

	// Not a child: nothing is woken.
	c.NudgeLinkedWorkflows(ctx, &githubv39.Issue{Number: githubv39.Int(7), Body: githubv39.String("fixed")})
	if len(c.wake) != 0 {
		t.Fatalf("woke %d parents for an issue that is no child", len(c.wake))
	}

	c.NudgeLinkedWorkflows(ctx, &githubv39.Issue{Number: githubv39.Int(101), Body: githubv39.String("<!-- factory:fanout parent=100 item=a -->")})
	if len(c.wake) != 1 {
		t.Fatalf("woke %d parents, want 1", len(c.wake))
	}
	c.syncParent(ctx, <-c.wake)
	if len(gh.created) != 3 {
		t.Errorf("the woken parent created %v, want 3 children", gh.created)
	}

	// A parent whose label was removed is out of the fan-out's hands.
	gh.issues[100].Labels = nil
	gh.created = nil
	c.syncParent(ctx, 100)
	if len(gh.created) != 0 {
		t.Errorf("an unlabelled parent was synced: created %v", gh.created)
	}
}

func TestNudge_NeverBlocks(t *testing.T) {
	c := New(Config{}, Deps{GitHub: newFake()})
	closed := &githubv39.Issue{Number: githubv39.Int(101), Body: githubv39.String("<!-- factory:fanout parent=100 item=a -->")}
	for range cap(c.wake) + 1 {
		c.NudgeLinkedWorkflows(context.Background(), closed)
	}
}
