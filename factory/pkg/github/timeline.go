package github

import (
	"context"
	"fmt"

	githubv39 "github.com/google/go-github/v39/github"
	"k8s.io/klog/v2"
)

// timelinePageSize is the maximum page size the GitHub timeline API accepts.
const timelinePageSize = 100

// maxTimelinePages caps timeline pagination so a pathologically noisy issue
// cannot stall a scan cycle or burn the whole API rate limit budget.
const maxTimelinePages = 20

// ListIssueTimeline retrieves the timeline events for an issue, following
// pagination.
//
// The GitHub API returns only 30 events per page by default, so an unpaginated
// call silently truncates the history of long-lived issues. Cross-reference
// events are ordered oldest-first, which means the most recent (and therefore
// most relevant) linked PR is the one most likely to be dropped.
//
// The boolean return reports whether the full timeline was read. It is false
// when pagination was cut short by maxTimelinePages, in which case callers must
// not treat the absence of an event as proof that it does not exist.
func (c *Client) ListIssueTimeline(ctx context.Context, issueNum int) ([]*githubv39.Timeline, bool, error) {
	if !c.Ready() {
		return nil, false, errNoClient
	}

	// Start non-nil so that a successful call never returns nil: callers use a
	// nil timeline to mean "could not be determined".
	all := []*githubv39.Timeline{}
	opts := &githubv39.ListOptions{PerPage: timelinePageSize}
	for page := 0; page < maxTimelinePages; page++ {
		events, resp, err := c.gh.Issues.ListIssueTimeline(ctx, c.owner, c.repo, issueNum, opts)
		if err != nil {
			return nil, false, err
		}
		all = append(all, events...)
		if resp == nil || resp.NextPage == 0 {
			return all, true, nil
		}
		opts.Page = resp.NextPage
	}
	klog.Warningf("Issue #%d has more than %d pages of timeline events; results are truncated", issueNum, maxTimelinePages)
	return all, false, nil
}

// TimelineHasOpenLinkedPR reports whether the timeline contains a
// cross-reference from a pull request that is still open.
func TimelineHasOpenLinkedPR(timeline []*githubv39.Timeline) bool {
	for _, event := range timeline {
		if event.GetEvent() == "cross-referenced" && event.Source != nil {
			if event.Source.Issue != nil && event.Source.Issue.PullRequestLinks != nil {
				if event.Source.Issue.GetState() == "open" {
					return true
				}
			}
		}
	}
	return false
}

// SearchOpenLinkedPR asks the Search API whether any open PR mentions the issue
// number. It is the fallback for when the timeline is unavailable or
// incomplete.
func (c *Client) SearchOpenLinkedPR(ctx context.Context, issueNum int) (bool, error) {
	query := fmt.Sprintf("repo:%s/%s type:pr state:open \"%d\"", c.owner, c.repo, issueNum)
	opts := &githubv39.SearchOptions{
		ListOptions: githubv39.ListOptions{PerPage: 10},
	}
	result, err := c.SearchIssues(ctx, query, opts)
	if err != nil {
		return false, fmt.Errorf("failed to search PRs for issue #%d: %w", issueNum, err)
	}
	return result.GetTotal() > 0, nil
}

// HasLinkedPR reports whether an open pull request references the issue.
func (c *Client) HasLinkedPR(ctx context.Context, issueNum int) (bool, error) {
	// 1. Try timeline check (quick and standard)
	timeline, complete, err := c.ListIssueTimeline(ctx, issueNum)
	if err == nil {
		if TimelineHasOpenLinkedPR(timeline) {
			return true, nil
		}
		if complete {
			return false, nil
		}
		klog.Warningf("Timeline for issue #%d was truncated. Falling back to search API.", issueNum)
	} else {
		klog.Warningf("Failed to list issue timeline for #%d: %v. Falling back to search API.", issueNum, err)
	}

	// 2. Fallback to Search API: search for open PRs referencing the issue number
	return c.SearchOpenLinkedPR(ctx, issueNum)
}

// HasLinkedPRWithTimeline reuses a timeline the caller already fetched to avoid a
// redundant API round trip. A nil timeline means the caller could not fetch one,
// so we fall back to fetching it (and the Search API) ourselves.
func (c *Client) HasLinkedPRWithTimeline(ctx context.Context, issueNum int, timeline []*githubv39.Timeline) (bool, error) {
	if timeline != nil {
		return TimelineHasOpenLinkedPR(timeline), nil
	}
	return c.HasLinkedPR(ctx, issueNum)
}
