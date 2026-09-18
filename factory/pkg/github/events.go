package github

import (
	"context"
	"fmt"

	githubv39 "github.com/google/go-github/v39/github"
	"k8s.io/klog/v2"
)

// issueEventPageSize is the maximum page size the GitHub issue events API
// accepts.
const issueEventPageSize = 100

// maxIssueEventPages caps event pagination so a pathologically noisy issue
// cannot stall a scan cycle or burn the whole API rate limit budget.
const maxIssueEventPages = 10

// ListIssueEvents retrieves the event log of an issue or pull request,
// following pagination.
//
// This is the record of what was done to an item rather than what was said on
// it: label additions and removals, assignments, renames. It is how a caller
// can tell a label that was never applied apart from one that was applied and
// then taken away again, which the current label set alone cannot answer.
//
// The boolean return reports whether the full log was read. It is false when
// pagination was cut short by maxIssueEventPages, in which case callers must
// not treat the absence of an event as proof that it never happened.
func (c *Client) ListIssueEvents(ctx context.Context, number int) ([]*githubv39.IssueEvent, bool, error) {
	if !c.Ready() {
		return nil, false, errNoClient
	}

	// Start non-nil so that a successful call never returns nil: callers use a
	// nil log to mean "could not be determined". One page of capacity covers
	// the common case without a re-allocation.
	all := make([]*githubv39.IssueEvent, 0, issueEventPageSize)
	opts := &githubv39.ListOptions{PerPage: issueEventPageSize}
	for page := 0; page < maxIssueEventPages; page++ {
		events, resp, err := c.gh.Issues.ListIssueEvents(ctx, c.owner, c.repo, number, opts)
		if err != nil {
			return nil, false, fmt.Errorf("listing events for #%d: %w", number, err)
		}
		all = append(all, events...)
		if resp == nil || resp.NextPage == 0 {
			return all, true, nil
		}
		opts.Page = resp.NextPage
	}
	klog.Warningf("Issue #%d has more than %d pages of events; results are truncated", number, maxIssueEventPages)
	return all, false, nil
}
