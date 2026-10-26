// Package graphtest is an in-memory GitHub repository for issue graph
// tests: what graph.Sync reads and writes.
package graphtest

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	githubv39 "github.com/google/go-github/v39/github"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/github"
)

// Issue is one issue or PR of the fake repository.
type Issue struct {
	Title, Body, Author, Assoc string
	Open                       bool
	Reason                     string
	Labels                     []string
	Assignees                  []string
	// Events are timeline events (labeled...), before the closing one.
	Events         []*githubv39.Timeline
	Comments       []*githubv39.IssueComment
	SubIssues      []int
	IsPR, Merged   bool
	Refs           []int // issues or PRs that cross-reference it
	ClosedByCommit bool
}

// Fake is an in-memory repository: GitHub as a pass sees it.
type Fake struct {
	// Login is who the fake's writes are by (CreateIssue, AddComment):
	// "watcher" unless set.
	Login   string
	Issues  map[int]*Issue
	Next    int
	Files   map[string]string
	Writes  int
	nextCID int64
}

func New() *Fake {
	return &Fake{Login: "watcher", Issues: map[int]*Issue{}, Next: 200, Files: map[string]string{}, nextCID: 1}
}

func (f *Fake) issue(n int) (*Issue, error) {
	if is, ok := f.Issues[n]; ok {
		return is, nil
	}
	return nil, fmt.Errorf("no issue #%d", n)
}

func (f *Fake) GetIssue(_ context.Context, n int) (*githubv39.Issue, error) {
	is, err := f.issue(n)
	if err != nil {
		return nil, err
	}
	state := "closed"
	if is.Open {
		state = "open"
	}
	out := &githubv39.Issue{Number: githubv39.Int(n), Title: githubv39.String(is.Title), Body: githubv39.String(is.Body), State: githubv39.String(state),
		User: &githubv39.User{Login: githubv39.String(is.Author)}}
	for _, l := range is.Labels {
		out.Labels = append(out.Labels, &githubv39.Label{Name: githubv39.String(l)})
	}
	for _, a := range is.Assignees {
		out.Assignees = append(out.Assignees, &githubv39.User{Login: githubv39.String(a)})
	}
	if is.IsPR {
		out.PullRequestLinks = &githubv39.PullRequestLinks{}
	}
	return out, nil
}

func (f *Fake) ref(n int) github.IssueRef {
	is := f.Issues[n]
	return github.IssueRef{Number: n, IsPR: is.IsPR, Title: is.Title, Body: is.Body, Open: is.Open, StateReason: is.Reason, Merged: is.Merged, Labels: slices.Clone(is.Labels), Author: is.Author}
}

func (f *Fake) GetIssueRef(_ context.Context, n int) (github.IssueRef, error) {
	if _, err := f.issue(n); err != nil {
		return github.IssueRef{}, err
	}
	return f.ref(n), nil
}

func (f *Fake) ListIssueComments(_ context.Context, n int) ([]*githubv39.IssueComment, error) {
	is, err := f.issue(n)
	if err != nil {
		return nil, err
	}
	return slices.Clone(is.Comments), nil
}

func (f *Fake) ListIssueTimeline(_ context.Context, n int) ([]*githubv39.Timeline, bool, error) {
	is := f.Issues[n]
	events := slices.Clone(is.Events)
	if is.Open {
		return events, true, nil
	}
	ev := &githubv39.Timeline{Event: githubv39.String("closed")}
	if is.ClosedByCommit {
		ev.CommitID = githubv39.String("abc123")
	}
	return append(events, ev), true, nil
}

func (f *Fake) ListIssues(_ context.Context, opts *githubv39.IssueListByRepoOptions) ([]*githubv39.Issue, *githubv39.Response, error) {
	var nums []int
	for n, is := range f.Issues {
		switch {
		case is.IsPR:
		case opts.Creator != "" && !strings.EqualFold(is.Author, opts.Creator):
		case len(opts.Labels) > 0 && !slices.ContainsFunc(is.Labels, func(l string) bool { return slices.Contains(opts.Labels, l) }):
		case opts.State == "open" && !is.Open:
		default:
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

func (f *Fake) ListCrossReferences(_ context.Context, n int) ([]github.IssueRef, error) {
	var out []github.IssueRef
	for _, r := range f.Issues[n].Refs {
		out = append(out, f.ref(r))
	}
	return out, nil
}

func (f *Fake) ListSubIssues(_ context.Context, n int) ([]int, error) {
	return slices.Clone(f.Issues[n].SubIssues), nil
}

func (f *Fake) ReadFile(_ context.Context, path string) ([]byte, string, error) {
	data, ok := f.Files[path]
	if !ok {
		return nil, "", github.ErrUnreadableFile
	}
	return []byte(data), "sha1", nil
}

func (f *Fake) CreateIssue(_ context.Context, title, body string, labels []string) (int, error) {
	f.Writes++
	f.Next++
	f.Issues[f.Next] = &Issue{Title: title, Body: body, Author: f.Login, Open: true, Labels: slices.Clone(labels)}
	return f.Next, nil
}

func (f *Fake) RemoveLabel(_ context.Context, n int, label string) error {
	f.Writes++
	f.Issues[n].Labels = slices.DeleteFunc(f.Issues[n].Labels, func(l string) bool { return strings.EqualFold(l, label) })
	return nil
}

func (f *Fake) AddAssignees(_ context.Context, n int, assignees []string) error {
	f.Writes++
	f.Issues[n].Assignees = append(f.Issues[n].Assignees, assignees...)
	return nil
}

func (f *Fake) EditIssue(_ context.Context, n int, title, body string) error {
	f.Writes++
	f.Issues[n].Title, f.Issues[n].Body = title, body
	return nil
}

func (f *Fake) AddLabels(_ context.Context, n int, labels []string) error {
	f.Writes++
	for _, l := range labels {
		if !slices.Contains(f.Issues[n].Labels, l) {
			f.Issues[n].Labels = append(f.Issues[n].Labels, l)
		}
	}
	return nil
}

func (f *Fake) AddSubIssue(_ context.Context, parent, child int) error {
	f.Writes++
	if !slices.Contains(f.Issues[parent].SubIssues, child) {
		f.Issues[parent].SubIssues = append(f.Issues[parent].SubIssues, child)
	}
	return nil
}

func (f *Fake) Comment(n int, login, assoc, body string) {
	at := time.Date(2026, 10, 25, 10, 0, 0, 0, time.UTC).Add(time.Duration(f.nextCID) * time.Minute)
	f.Issues[n].Comments = append(f.Issues[n].Comments, &githubv39.IssueComment{ID: githubv39.Int64(f.nextCID), Body: githubv39.String(body),
		User: &githubv39.User{Login: githubv39.String(login)}, AuthorAssociation: githubv39.String(assoc), CreatedAt: &at})
	f.nextCID++
}

func (f *Fake) AddComment(_ context.Context, n int, body string) error {
	f.Writes++
	f.Comment(n, f.Login, "NONE", body)
	return nil
}

func (f *Fake) EditComment(_ context.Context, id int64, body string) error {
	f.Writes++
	for _, is := range f.Issues {
		for _, c := range is.Comments {
			if c.GetID() == id {
				c.Body = githubv39.String(body)
				return nil
			}
		}
	}
	return fmt.Errorf("no comment %d", id)
}

func (f *Fake) SetIssueState(_ context.Context, n int, state, reason string) error {
	f.Writes++
	f.Issues[n].Open, f.Issues[n].Reason = state == "open", reason
	return nil
}

// CloseChild closes an issue as a person or a merged PR would.
func (f *Fake) CloseChild(n int, reason string) { f.Issues[n].Open, f.Issues[n].Reason = false, reason }
