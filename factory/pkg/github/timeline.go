package github

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"

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

// TimelineHasOpenLinkedPR reports whether the timeline contains a connected pull request that is still open.
func TimelineHasOpenLinkedPR(timeline []*githubv39.Timeline) bool {
	linkedPRs := make(map[int]bool)
	for _, event := range timeline {
		if event.Source == nil || event.Source.Issue == nil {
			continue
		}
		if event.Source.Issue.PullRequestLinks == nil {
			continue
		}
		prNum := event.Source.Issue.GetNumber()
		if prNum == 0 {
			continue
		}
		switch event.GetEvent() {
		case "connected":
			if event.Source.Issue.GetState() == "open" {
				linkedPRs[prNum] = true
			}
		case "disconnected":
			delete(linkedPRs, prNum)
		}
	}
	return len(linkedPRs) > 0
}

var (
	// branchIssueRe matches strict branch names like issue-1234, issue_1234, factory-issue-1234, etc.
	branchIssueRe = regexp.MustCompile(`\b(?:issue|factory-issue)[-_](\d+)\b`)

	// closingKwRe matches closing keywords.
	closingKwRe = regexp.MustCompile(`(?i:\b(?:close|closes|closed|fix|fixes|fixed|resolve|resolves|resolved)\b)`)

	// hashIssueRe matches hash references, e.g., #123.
	hashIssueRe = regexp.MustCompile(`#(\d+)\b`)

	// urlIssueRe matches issue URL references, e.g., /issues/123.
	urlIssueRe = regexp.MustCompile(`/issues/(\d+)\b`)
)

// GetClosingIssues scans a pull request's branch name, title, and body for closing references to issue numbers.
func GetClosingIssues(pr *githubv39.PullRequest) map[int]bool {
	closing := make(map[int]bool)

	// Check branch name, ignoring epoch timestamps (num >= 10000000)
	// We restrict this to strict branch formats (e.g. matching branchIssueRe) to avoid false positives.
	if pr.GetHead().GetRef() != "" {
		for _, match := range branchIssueRe.FindAllStringSubmatch(pr.GetHead().GetRef(), -1) {
			if len(match) > 1 {
				if num, err := strconv.Atoi(match[1]); err == nil && num < 10000000 {
					closing[num] = true
				}
			}
		}
	}

	for _, text := range []string{pr.GetTitle(), pr.GetBody()} {
		if text == "" {
			continue
		}

		// Find all occurrences of closing keywords
		matches := closingKwRe.FindAllStringIndex(text, -1)
		for _, match := range matches {
			startIndex := match[1] // right after the keyword

			// Determine the end of the scope in a UTF-8 safe manner
			runes := []rune(text[startIndex:])
			if len(runes) > 150 {
				runes = runes[:150]
			}
			scopeText := string(runes)

			// Truncate at sentence boundaries like period followed by space, semicolon, or newline
			if idx := strings.Index(scopeText, ". "); idx != -1 {
				scopeText = scopeText[:idx]
			}
			if idx := strings.Index(scopeText, ";"); idx != -1 {
				scopeText = scopeText[:idx]
			}
			if idx := strings.Index(scopeText, "\n"); idx != -1 {
				scopeText = scopeText[:idx]
			}

			// Now find all issue numbers inside the scope text
			// e.g. #123 or /issues/123
			for _, hashMatch := range hashIssueRe.FindAllStringSubmatch(scopeText, -1) {
				if len(hashMatch) > 1 {
					if num, err := strconv.Atoi(hashMatch[1]); err == nil && num < 10000000 {
						closing[num] = true
					}
				}
			}

			for _, urlMatch := range urlIssueRe.FindAllStringSubmatch(scopeText, -1) {
				if len(urlMatch) > 1 {
					if num, err := strconv.Atoi(urlMatch[1]); err == nil && num < 10000000 {
						closing[num] = true
					}
				}
			}
		}
	}

	return closing
}

func issueIsClosingPR(issue *githubv39.Issue, issueNum int) bool {
	if issue == nil {
		return false
	}
	pr := &githubv39.PullRequest{
		Title: issue.Title,
		Body:  issue.Body,
	}
	closing := GetClosingIssues(pr)
	return closing[issueNum]
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
	if result.GetTotal() > 0 {
		for _, issue := range result.Issues {
			if issueIsClosingPR(issue, issueNum) {
				return true, nil
			}
		}
	}
	return false, nil
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
