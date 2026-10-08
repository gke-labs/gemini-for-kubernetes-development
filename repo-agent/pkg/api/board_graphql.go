package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/google/go-github/v39/github"
	"k8s.io/klog/v2"

	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/ghquota"
)

// One feed rebuild used to be four REST listings — three of them paginated
// three deep — plus one ListReviews per open pull request the member did
// not author. On a repo the size of k8s-config-connector that is ~110 calls
// against a 5000/hour budget shared with the controller, the factory and
// the member's own git. Thirteen boards rebuilding once a minute cannot fit
// in it, and on the evening of 2026-09-28 they did not: the budget went to
// zero twice.
//
// The same answer is one GraphQL request. Measured against that same repo:
// 9 points, one round trip, out of a separate 5000/hour budget that nothing
// else here spends.
//
// What is given up is conditional requests. GraphQL has no ETag and no 304,
// so this is never free the way a warm REST cache was. It is cheap instead
// of sometimes-free, and cheap every time is what the budget needs.
const boardFeedQuery = `
query($owner:String!,$name:String!,$me:String!) {
  rateLimit { cost remaining resetAt }
  repository(owner:$owner, name:$name) {
    pullRequests(first:100, states:OPEN, orderBy:{field:UPDATED_AT, direction:DESC}) {
      nodes {
        id number title body url updatedAt isDraft
        author { login }
        headRepository { nameWithOwner isFork owner { login } }
        labels(first:20){ nodes { name } }
        reviewRequests(first:20){ nodes { requestedReviewer { ... on User { login } } } }
        reviews(first:10, author:$me){ nodes { state } }
      }
    }
    assigned: issues(first:100, states:OPEN, filterBy:{assignee:$me}, orderBy:{field:UPDATED_AT, direction:DESC}) {
      nodes { number title body url updatedAt author{login} labels(first:20){nodes{name}} assignees(first:10){nodes{login}} }
    }
    created: issues(first:100, states:OPEN, filterBy:{createdBy:$me}, orderBy:{field:UPDATED_AT, direction:DESC}) {
      nodes { number title body url updatedAt author{login} labels(first:20){nodes{name}} assignees(first:10){nodes{login}} }
    }
    triage: issues(first:100, states:OPEN, orderBy:{field:UPDATED_AT, direction:DESC}) {
      nodes { number title body url updatedAt author{login} labels(first:20){nodes{name}} assignees(first:10){nodes{login}} }
    }
  }
}`

// githubGraphQLEndpoint is a var so a test can answer it without reaching
// the network; the REST side does the same through githubClientForToken.
var githubGraphQLEndpoint = "https://api.github.com/graphql"

// A board feed on the largest repo here is ~1.5MB of JSON. The cap is well
// clear of that and stops a surprise from becoming a memory problem.
const maxGraphQLResponse = 64 << 20

// reviewStates is what the member has already done to a pull request, and
// GitHub is the only durable record of it: a parked pending review and a
// submitted one must both survive a lost sandbox breadcrumb.
type reviewStates struct{ pending, reviewed bool }

// boardSnapshot is everything one rebuild needs from GitHub, in the shapes
// the row builders already speak.
type boardSnapshot struct {
	prs      []*github.PullRequest
	assigned []*github.Issue
	created  []*github.Issue
	triage   []*github.Issue
	reviews  map[int]reviewStates
}

type gqlName struct {
	Name string `json:"name"`
}

type gqlLogin struct {
	Login string `json:"login"`
}

type gqlNameNodes struct {
	Nodes []gqlName `json:"nodes"`
}

type gqlLoginNodes struct {
	Nodes []gqlLogin `json:"nodes"`
}

type gqlIssue struct {
	Number    int           `json:"number"`
	Title     string        `json:"title"`
	Body      string        `json:"body"`
	URL       string        `json:"url"`
	UpdatedAt time.Time     `json:"updatedAt"`
	Author    *gqlLogin     `json:"author"`
	Labels    gqlNameNodes  `json:"labels"`
	Assignees gqlLoginNodes `json:"assignees"`
}

type gqlPullRequest struct {
	ID             string    `json:"id"`
	Number         int       `json:"number"`
	Title          string    `json:"title"`
	Body           string    `json:"body"`
	URL            string    `json:"url"`
	UpdatedAt      time.Time `json:"updatedAt"`
	IsDraft        bool      `json:"isDraft"`
	Author         *gqlLogin `json:"author"`
	HeadRepository *struct {
		NameWithOwner string    `json:"nameWithOwner"`
		IsFork        bool      `json:"isFork"`
		Owner         *gqlLogin `json:"owner"`
	} `json:"headRepository"`
	Labels         gqlNameNodes `json:"labels"`
	ReviewRequests struct {
		Nodes []struct {
			RequestedReviewer *gqlLogin `json:"requestedReviewer"`
		} `json:"nodes"`
	} `json:"reviewRequests"`
	Reviews struct {
		Nodes []struct {
			State string `json:"state"`
		} `json:"nodes"`
	} `json:"reviews"`
}

type gqlIssueNodes struct {
	Nodes []gqlIssue `json:"nodes"`
}

type gqlBoardResponse struct {
	Data struct {
		RateLimit struct {
			Cost      int       `json:"cost"`
			Remaining int       `json:"remaining"`
			ResetAt   time.Time `json:"resetAt"`
		} `json:"rateLimit"`
		Repository *struct {
			PullRequests struct {
				Nodes []gqlPullRequest `json:"nodes"`
			} `json:"pullRequests"`
			Assigned gqlIssueNodes `json:"assigned"`
			Created  gqlIssueNodes `json:"created"`
			Triage   gqlIssueNodes `json:"triage"`
		} `json:"repository"`
	} `json:"data"`
	Errors []struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"errors"`
}

// fetchBoardSnapshot reads the whole board in one request.
//
// Any GraphQL error at all fails the rebuild rather than returning what
// arrived. A partial answer here is indistinguishable from a quiet repo,
// and a feed built around a hole shows fewer rows than exist — "nothing
// needs you" is the one wrong answer a work queue must not give. The
// caller keeps serving the previous feed instead.
func fetchBoardSnapshot(ctx context.Context, hc *http.Client, owner, repo, member string) (*boardSnapshot, error) {
	payload, err := json.Marshal(map[string]any{
		"query":     boardFeedQuery,
		"variables": map[string]string{"owner": owner, "name": repo, "me": member},
	})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, githubGraphQLEndpoint, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxGraphQLResponse))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusUnauthorized {
		return nil, fmt.Errorf("github graphql: %w", errGitHubUnauthorized)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("github graphql: %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}

	var out gqlBoardResponse
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("github graphql: %w", err)
	}
	// GraphQL answers a spent budget with 200 and an error in the body, so
	// the transport gate never sees it. Translate it into the error the
	// feed already knows how to hold a board for.
	for _, e := range out.Errors {
		if strings.EqualFold(e.Type, "RATE_LIMITED") {
			until := out.Data.RateLimit.ResetAt
			if !until.After(time.Now()) {
				until = time.Now().Add(time.Minute)
			}
			return nil, &ghquota.Error{Until: until}
		}
	}
	if len(out.Errors) > 0 {
		return nil, fmt.Errorf("github graphql: %s", out.Errors[0].Message)
	}
	if out.Data.Repository == nil {
		return nil, fmt.Errorf("github graphql: no repository %s/%s in the answer", owner, repo)
	}
	klog.FromContext(ctx).V(2).Info("board feed read from graphql",
		"repo", owner+"/"+repo, "cost", out.Data.RateLimit.Cost, "remaining", out.Data.RateLimit.Remaining)

	repository := out.Data.Repository
	snap := &boardSnapshot{
		assigned: issuesOf(repository.Assigned.Nodes),
		created:  issuesOf(repository.Created.Nodes),
		triage:   issuesOf(repository.Triage.Nodes),
		reviews:  map[int]reviewStates{},
	}
	for i := range repository.PullRequests.Nodes {
		node := &repository.PullRequests.Nodes[i]
		snap.prs = append(snap.prs, node.toPullRequest(owner+"/"+repo))
		snap.reviews[node.Number] = node.states()
	}
	return snap, nil
}

// states reads the member's own reviews on one pull request. The query asks
// for theirs alone (reviews(author:$me)), so every state here is the
// member's and no login comparison is needed.
func (p *gqlPullRequest) states() reviewStates {
	var st reviewStates
	for _, rv := range p.Reviews.Nodes {
		switch strings.ToUpper(rv.State) {
		case "PENDING":
			st.pending = true
		case "APPROVED", "CHANGES_REQUESTED", "COMMENTED":
			st.reviewed = true
		}
	}
	return st
}

// toPullRequest fills in the head and base repositories so the feed can tell
// a PR pushed from the member's fork (base is the repository queried).
func (p *gqlPullRequest) toPullRequest(base string) *github.PullRequest {
	pr := &github.PullRequest{
		NodeID:    github.String(p.ID),
		Number:    github.Int(p.Number),
		Title:     github.String(p.Title),
		Body:      github.String(p.Body),
		HTMLURL:   github.String(p.URL),
		Draft:     github.Bool(p.IsDraft),
		State:     github.String("open"),
		UpdatedAt: timePtr(p.UpdatedAt),
		Labels:    labelsOf(p.Labels.Nodes),
		User:      userOf(p.Author),
		Base:      &github.PullRequestBranch{Repo: &github.Repository{FullName: github.String(base)}},
	}
	if h := p.HeadRepository; h != nil {
		pr.Head = &github.PullRequestBranch{Repo: &github.Repository{
			FullName: github.String(h.NameWithOwner),
			Fork:     github.Bool(h.IsFork),
			Owner:    userOf(h.Owner),
		}}
	}
	for _, rr := range p.ReviewRequests.Nodes {
		// A requested team has no login and is not the member; the query
		// asks only for the User case, so the rest arrive empty.
		if u := userOf(rr.RequestedReviewer); u != nil {
			pr.RequestedReviewers = append(pr.RequestedReviewers, u)
		}
	}
	return pr
}

// toIssue leaves PullRequestLinks nil: repository.issues never returns
// pull requests, which is the difference that made the IsPullRequest()
// skip necessary on REST.
func (i *gqlIssue) toIssue() *github.Issue {
	return &github.Issue{
		Number:    github.Int(i.Number),
		Title:     github.String(i.Title),
		Body:      github.String(i.Body),
		HTMLURL:   github.String(i.URL),
		State:     github.String("open"),
		UpdatedAt: timePtr(i.UpdatedAt),
		Labels:    labelsOf(i.Labels.Nodes),
		Assignees: usersOf(i.Assignees.Nodes),
		User:      userOf(i.Author),
	}
}

func issuesOf(nodes []gqlIssue) []*github.Issue {
	out := make([]*github.Issue, 0, len(nodes))
	for i := range nodes {
		out = append(out, nodes[i].toIssue())
	}
	return out
}

func labelsOf(nodes []gqlName) []*github.Label {
	out := make([]*github.Label, 0, len(nodes))
	for _, n := range nodes {
		out = append(out, &github.Label{Name: github.String(n.Name)})
	}
	return out
}

func usersOf(nodes []gqlLogin) []*github.User {
	out := make([]*github.User, 0, len(nodes))
	for i := range nodes {
		out = append(out, &github.User{Login: github.String(nodes[i].Login)})
	}
	return out
}

// userOf is nil-safe: GitHub returns a null author for a ghost account,
// and a deleted user's issue still belongs on the board.
func userOf(l *gqlLogin) *github.User {
	if l == nil {
		return nil
	}
	return &github.User{Login: github.String(l.Login)}
}

func timePtr(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}
