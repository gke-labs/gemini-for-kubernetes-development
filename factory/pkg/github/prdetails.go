package github

import (
	"context"
	"fmt"
	"strings"
	"time"

	githubv39 "github.com/google/go-github/v39/github"
)

// PRDetailsBatch is how many pull requests one PullRequestDetails query
// reads. The nested lists (reviews and their inline comments) are what bound
// it: twenty pull requests at the page sizes below stay far inside GitHub's
// node limit and cost a handful of points.
const PRDetailsBatch = 20

// The page sizes the details query reads. A pull request with more of any of
// them is reported Complete == false and read the REST way instead, which
// paginates: the query never answers with a list cut short.
const (
	prDetailsCommits        = 100
	prDetailsComments       = 100
	prDetailsReviews        = 50
	prDetailsReviewComments = 50
	prDetailsLabels         = 100
	prDetailsAssignees      = 50
)

// PRDetails is what the PR scanner reads about a pull request before deciding
// what it needs, in the shapes the REST calls it replaces return: the pull
// request (GetPullRequest), whether it is in the merge queue
// (IsInMergeQueue), the last commit time (ListCommits), its comments
// (ListIssueComments), its reviews (ListReviews) and their inline comments
// (ListAllReviewComments).
type PRDetails struct {
	PR             *githubv39.PullRequest
	InMergeQueue   bool
	LastCommitTime time.Time
	Comments       []*githubv39.IssueComment
	Reviews        []*githubv39.PullRequestReview
	ReviewComments []*githubv39.PullRequestComment
	// Complete is false when a list was longer than the query reads; Why
	// says which. The caller then reads the pull request the REST way.
	Complete bool
	Why      string
}

const prDetailsFragment = `fragment prDetails on PullRequest {
  number headRefOid headRefName baseRefName title body state isDraft mergeable createdAt updatedAt url
  author { __typename login }
  labels(first: %[5]d) { totalCount nodes { name } }
  assignees(first: %[6]d) { totalCount nodes { login } }
  mergeQueueEntry { state }
  commits(last: %[1]d) { totalCount nodes { commit { oid committedDate } } }
  comments(first: %[2]d) { totalCount nodes { id databaseId author { __typename login } authorAssociation body createdAt updatedAt url } }
  reviews(first: %[3]d) { totalCount nodes { id databaseId author { __typename login } authorAssociation state body submittedAt url commit { oid }
    comments(first: %[4]d) { totalCount nodes { id databaseId author { __typename login } authorAssociation body createdAt updatedAt path url commit { oid } } } } }
}`

type gqlActor struct {
	Typename string `json:"__typename"`
	Login    string `json:"login"`
}

type gqlPRDetails struct {
	Number      int       `json:"number"`
	HeadRefOid  string    `json:"headRefOid"`
	HeadRefName string    `json:"headRefName"`
	BaseRefName string    `json:"baseRefName"`
	Title       string    `json:"title"`
	Body        string    `json:"body"`
	State       string    `json:"state"`
	IsDraft     bool      `json:"isDraft"`
	Mergeable   string    `json:"mergeable"`
	CreatedAt   time.Time `json:"createdAt"`
	UpdatedAt   time.Time `json:"updatedAt"`
	URL         string    `json:"url"`
	Author      *gqlActor `json:"author"`
	Labels      gqlList[struct {
		Name string `json:"name"`
	}] `json:"labels"`
	Assignees gqlList[struct {
		Login string `json:"login"`
	}] `json:"assignees"`
	MergeQueue *struct {
		State string `json:"state"`
	} `json:"mergeQueueEntry"`
	Commits gqlList[struct {
		Commit struct {
			Oid           string    `json:"oid"`
			CommittedDate time.Time `json:"committedDate"`
		} `json:"commit"`
	}] `json:"commits"`
	Comments gqlList[gqlComment] `json:"comments"`
	Reviews  gqlList[struct {
		gqlComment
		State       string     `json:"state"`
		SubmittedAt *time.Time `json:"submittedAt"`
		Commit      *struct {
			Oid string `json:"oid"`
		} `json:"commit"`
		Comments gqlList[struct {
			gqlComment
			Path   string `json:"path"`
			Commit *struct {
				Oid string `json:"oid"`
			} `json:"commit"`
		}] `json:"comments"`
	}] `json:"reviews"`
}

type gqlList[T any] struct {
	TotalCount int `json:"totalCount"`
	Nodes      []T `json:"nodes"`
}

type gqlComment struct {
	ID                string    `json:"id"`
	DatabaseID        int64     `json:"databaseId"`
	Author            *gqlActor `json:"author"`
	AuthorAssociation string    `json:"authorAssociation"`
	Body              string    `json:"body"`
	CreatedAt         time.Time `json:"createdAt"`
	UpdatedAt         time.Time `json:"updatedAt"`
	URL               string    `json:"url"`
}

// PullRequestDetails reads the details of the given pull requests, one
// GraphQL query per PRDetailsBatch of them, in place of the six REST calls
// per pull request the scanner otherwise makes. A batch whose query fails is
// left out of the result, and the first such error is returned with whatever
// the other batches read: the caller reads what is missing the REST way.
func (c *Client) PullRequestDetails(ctx context.Context, numbers []int) (map[int]*PRDetails, error) {
	if !c.Ready() {
		return nil, errNoClient
	}
	out := make(map[int]*PRDetails, len(numbers))
	var firstErr error
	for start := 0; start < len(numbers); start += PRDetailsBatch {
		end := min(start+PRDetailsBatch, len(numbers))
		if err := c.pullRequestDetailsBatch(ctx, numbers[start:end], out); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return out, firstErr
}

func (c *Client) pullRequestDetailsBatch(ctx context.Context, numbers []int, out map[int]*PRDetails) error {
	if len(numbers) == 0 {
		return nil
	}
	var b strings.Builder
	b.WriteString("query($owner: String!, $repo: String!) {\n  repository(owner: $owner, name: $repo) {\n")
	for _, n := range numbers {
		fmt.Fprintf(&b, "    pr%d: pullRequest(number: %d) { ...prDetails }\n", n, n)
	}
	b.WriteString("  }\n}\n")
	fmt.Fprintf(&b, prDetailsFragment, prDetailsCommits, prDetailsComments, prDetailsReviews, prDetailsReviewComments, prDetailsLabels, prDetailsAssignees)

	var data struct {
		Repository map[string]*gqlPRDetails `json:"repository"`
	}
	vars := map[string]interface{}{"owner": c.owner, "repo": c.repo}
	if err := c.graphQL(ctx, b.String(), vars, &data); err != nil {
		return fmt.Errorf("reading details of %d pull requests: %w", len(numbers), err)
	}
	for _, n := range numbers {
		g := data.Repository[fmt.Sprintf("pr%d", n)]
		if g == nil {
			continue
		}
		out[n] = g.details()
	}
	return nil
}

// details converts a pull request read by the details query into the REST
// shapes, and says whether any list was longer than the query read.
func (g *gqlPRDetails) details() *PRDetails {
	d := &PRDetails{Complete: true}
	short := func(what string, total, read int) {
		if total > read && d.Complete {
			d.Complete = false
			d.Why = fmt.Sprintf("%d %s, more than the %d the query reads", total, what, read)
		}
	}
	short("commits", g.Commits.TotalCount, len(g.Commits.Nodes))
	short("comments", g.Comments.TotalCount, len(g.Comments.Nodes))
	short("reviews", g.Reviews.TotalCount, len(g.Reviews.Nodes))
	short("labels", g.Labels.TotalCount, len(g.Labels.Nodes))
	short("assignees", g.Assignees.TotalCount, len(g.Assignees.Nodes))

	pr := &githubv39.PullRequest{
		Number:    githubv39.Int(g.Number),
		Title:     githubv39.String(g.Title),
		Body:      githubv39.String(g.Body),
		State:     githubv39.String(restState(g.State)),
		Draft:     githubv39.Bool(g.IsDraft),
		HTMLURL:   githubv39.String(g.URL),
		CreatedAt: timePtr(g.CreatedAt),
		UpdatedAt: timePtr(g.UpdatedAt),
		User:      restUser(g.Author),
		Head:      &githubv39.PullRequestBranch{SHA: githubv39.String(g.HeadRefOid), Ref: githubv39.String(g.HeadRefName)},
		Base:      &githubv39.PullRequestBranch{Ref: githubv39.String(g.BaseRefName)},
	}
	// GitHub answers UNKNOWN while it is still computing mergeability, which
	// the REST API reports as a missing mergeable.
	switch g.Mergeable {
	case "MERGEABLE":
		pr.Mergeable = githubv39.Bool(true)
	case "CONFLICTING":
		pr.Mergeable = githubv39.Bool(false)
	}
	for _, l := range g.Labels.Nodes {
		pr.Labels = append(pr.Labels, &githubv39.Label{Name: githubv39.String(l.Name)})
	}
	for _, a := range g.Assignees.Nodes {
		pr.Assignees = append(pr.Assignees, &githubv39.User{Login: githubv39.String(a.Login)})
	}
	d.PR = pr
	d.InMergeQueue = g.MergeQueue != nil

	for _, c := range g.Commits.Nodes {
		if c.Commit.CommittedDate.After(d.LastCommitTime) {
			d.LastCommitTime = c.Commit.CommittedDate
		}
	}
	for _, c := range g.Comments.Nodes {
		d.Comments = append(d.Comments, &githubv39.IssueComment{
			ID:                githubv39.Int64(c.DatabaseID),
			NodeID:            githubv39.String(c.ID),
			User:              restUser(c.Author),
			AuthorAssociation: githubv39.String(c.AuthorAssociation),
			Body:              githubv39.String(c.Body),
			CreatedAt:         timePtr(c.CreatedAt),
			UpdatedAt:         timePtr(c.UpdatedAt),
			HTMLURL:           githubv39.String(c.URL),
		})
	}
	for _, r := range g.Reviews.Nodes {
		short(fmt.Sprintf("inline comments on review %d", r.DatabaseID), r.Comments.TotalCount, len(r.Comments.Nodes))
		rv := &githubv39.PullRequestReview{
			ID:                githubv39.Int64(r.DatabaseID),
			NodeID:            githubv39.String(r.ID),
			User:              restUser(r.Author),
			AuthorAssociation: githubv39.String(r.AuthorAssociation),
			Body:              githubv39.String(r.Body),
			State:             githubv39.String(r.State),
			SubmittedAt:       r.SubmittedAt,
			HTMLURL:           githubv39.String(r.URL),
		}
		if r.Commit != nil {
			rv.CommitID = githubv39.String(r.Commit.Oid)
		}
		d.Reviews = append(d.Reviews, rv)
		for _, rc := range r.Comments.Nodes {
			prc := &githubv39.PullRequestComment{
				ID:                  githubv39.Int64(rc.DatabaseID),
				NodeID:              githubv39.String(rc.ID),
				PullRequestReviewID: githubv39.Int64(r.DatabaseID),
				User:                restUser(rc.Author),
				AuthorAssociation:   githubv39.String(rc.AuthorAssociation),
				Body:                githubv39.String(rc.Body),
				Path:                githubv39.String(rc.Path),
				CreatedAt:           timePtr(rc.CreatedAt),
				UpdatedAt:           timePtr(rc.UpdatedAt),
				HTMLURL:             githubv39.String(rc.URL),
			}
			if rc.Commit != nil {
				prc.CommitID = githubv39.String(rc.Commit.Oid)
			}
			d.ReviewComments = append(d.ReviewComments, prc)
		}
	}
	return d
}

// restState is a pull request's GraphQL state (OPEN, CLOSED, MERGED) as the
// REST API spells it: open or closed.
func restState(s string) string {
	if s == "OPEN" {
		return "open"
	}
	return "closed"
}

// restUser is an author as the REST API reports it. GraphQL names a GitHub
// App's account without the "[bot]" suffix the REST API adds and reports it
// as a Bot; a deleted account is null in GraphQL and "ghost" in REST.
func restUser(a *gqlActor) *githubv39.User {
	if a == nil {
		return &githubv39.User{Login: githubv39.String("ghost"), Type: githubv39.String("User")}
	}
	login, typ := a.Login, "User"
	switch a.Typename {
	case "Bot":
		typ = "Bot"
		if !strings.HasSuffix(login, "[bot]") {
			login += "[bot]"
		}
	case "Organization":
		typ = "Organization"
	}
	return &githubv39.User{Login: githubv39.String(login), Type: githubv39.String(typ)}
}

func timePtr(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}
