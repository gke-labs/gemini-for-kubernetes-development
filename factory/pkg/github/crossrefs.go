package github

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// IssueRef is an issue or pull request as it is now, with what go-github v39
// does not decode: an issue's state_reason and a pull request's merged_at.
type IssueRef struct {
	Number int
	IsPR   bool
	Title  string
	Body   string
	Open   bool
	// StateReason is a closed issue's: "completed" or "not_planned".
	StateReason string
	// Merged is a pull request that was merged.
	Merged bool
	Labels []string
}

// rawIssue is the part of GitHub's issue JSON an IssueRef is read from.
type rawIssue struct {
	Number        int    `json:"number"`
	Title         string `json:"title"`
	Body          string `json:"body"`
	State         string `json:"state"`
	StateReason   string `json:"state_reason"`
	RepositoryURL string `json:"repository_url"`
	Labels        []struct {
		Name string `json:"name"`
	} `json:"labels"`
	PullRequest *struct {
		MergedAt *time.Time `json:"merged_at"`
	} `json:"pull_request"`
}

func (r rawIssue) ref() IssueRef {
	ref := IssueRef{
		Number:      r.Number,
		IsPR:        r.PullRequest != nil,
		Title:       r.Title,
		Body:        r.Body,
		Open:        r.State == "open",
		StateReason: r.StateReason,
		Merged:      r.PullRequest != nil && r.PullRequest.MergedAt != nil,
	}
	for _, l := range r.Labels {
		ref.Labels = append(ref.Labels, l.Name)
	}
	return ref
}

// GetIssueRef returns one issue or pull request as an IssueRef.
func (c *Client) GetIssueRef(ctx context.Context, number int) (IssueRef, error) {
	if !c.Ready() {
		return IssueRef{}, errNoClient
	}

	req, err := c.gh.NewRequest("GET", fmt.Sprintf("repos/%s/%s/issues/%d", c.owner, c.repo, number), nil)
	if err != nil {
		return IssueRef{}, err
	}
	var issue rawIssue
	if _, err := c.gh.Do(ctx, req, &issue); err != nil {
		return IssueRef{}, fmt.Errorf("fetching issue #%d: %w", number, err)
	}
	return issue.ref(), nil
}

// ListCrossReferences returns the issues and pull requests of this
// repository that mention an issue, each once, from the issue's timeline.
// Like ListIssueTimeline, it reads at most maxTimelinePages pages.
func (c *Client) ListCrossReferences(ctx context.Context, number int) ([]IssueRef, error) {
	if !c.Ready() {
		return nil, errNoClient
	}

	repoSuffix := strings.ToLower(fmt.Sprintf("/repos/%s/%s", c.owner, c.repo))
	seen := map[int]bool{}
	var out []IssueRef
	for page := 1; page <= maxTimelinePages; page++ {
		u := fmt.Sprintf("repos/%s/%s/issues/%d/timeline?per_page=%d&page=%d", c.owner, c.repo, number, timelinePageSize, page)
		req, err := c.gh.NewRequest("GET", u, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Accept", "application/vnd.github.mockingbird-preview+json")
		var events []struct {
			Event  string `json:"event"`
			Source *struct {
				Issue *rawIssue `json:"issue"`
			} `json:"source"`
		}
		resp, err := c.gh.Do(ctx, req, &events)
		if err != nil {
			return nil, fmt.Errorf("reading the timeline of #%d: %w", number, err)
		}
		for _, ev := range events {
			if ev.Event != "cross-referenced" || ev.Source == nil || ev.Source.Issue == nil {
				continue
			}
			is := ev.Source.Issue
			if seen[is.Number] || !strings.HasSuffix(strings.ToLower(is.RepositoryURL), repoSuffix) {
				continue
			}
			seen[is.Number] = true
			out = append(out, is.ref())
		}
		if resp == nil || resp.NextPage == 0 {
			break
		}
	}
	return out, nil
}
