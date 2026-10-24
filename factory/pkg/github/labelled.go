package github

import (
	"context"
	"fmt"
	"time"

	githubv39 "github.com/google/go-github/v39/github"
)

// openPRsLabelledQuery lists open pull requests carrying any of $labels -
// GraphQL's label filter is an OR, where the REST issue listing's is an
// AND - a page at a time, with what a listing returns of each.
const openPRsLabelledQuery = `query($owner: String!, $repo: String!, $labels: [String!], $after: String) {
  repository(owner: $owner, name: $repo) {
    pullRequests(states: OPEN, labels: $labels, first: 100, after: $after) {
      pageInfo { hasNextPage endCursor }
      nodes {
        number title createdAt updatedAt url
        author { __typename login } authorAssociation
        labels(first: 100) { nodes { name } }
        assignees(first: 50) { nodes { login } }
      }
    }
  }
}`

// OpenPRsLabelled is every open pull request carrying any of labels, in
// one paginated GraphQL query, as the REST issue listing returns them (an
// issue with its pull request link). No labels lists nothing.
func (c *Client) OpenPRsLabelled(ctx context.Context, labels []string) ([]*githubv39.Issue, error) {
	if !c.Ready() {
		return nil, errNoClient
	}
	if len(labels) == 0 {
		return nil, nil
	}
	var out []*githubv39.Issue
	vars := map[string]interface{}{"owner": c.owner, "repo": c.repo, "labels": labels}
	for {
		var data struct {
			Repository struct {
				PullRequests struct {
					PageInfo struct {
						HasNextPage bool   `json:"hasNextPage"`
						EndCursor   string `json:"endCursor"`
					} `json:"pageInfo"`
					Nodes []struct {
						Number    int       `json:"number"`
						Title     string    `json:"title"`
						CreatedAt time.Time `json:"createdAt"`
						UpdatedAt time.Time `json:"updatedAt"`
						URL       string    `json:"url"`
						Author    *gqlActor `json:"author"`
						// AuthorAssociation is the REST author_association's
						// value (OWNER, MEMBER, CONTRIBUTOR...).
						AuthorAssociation string `json:"authorAssociation"`
						Labels            gqlList[struct {
							Name string `json:"name"`
						}] `json:"labels"`
						Assignees gqlList[struct {
							Login string `json:"login"`
						}] `json:"assignees"`
					} `json:"nodes"`
				} `json:"pullRequests"`
			} `json:"repository"`
		}
		if err := c.graphQL(ctx, openPRsLabelledQuery, vars, &data); err != nil {
			return nil, fmt.Errorf("listing open pull requests labelled %v: %w", labels, err)
		}
		prs := data.Repository.PullRequests
		for _, n := range prs.Nodes {
			issue := &githubv39.Issue{
				Number:            githubv39.Int(n.Number),
				Title:             githubv39.String(n.Title),
				State:             githubv39.String("open"),
				HTMLURL:           githubv39.String(n.URL),
				User:              restUser(n.Author),
				AuthorAssociation: githubv39.String(n.AuthorAssociation),
				CreatedAt:         timePtr(n.CreatedAt),
				UpdatedAt:         timePtr(n.UpdatedAt),
				PullRequestLinks:  &githubv39.PullRequestLinks{HTMLURL: githubv39.String(n.URL)},
			}
			for _, l := range n.Labels.Nodes {
				issue.Labels = append(issue.Labels, &githubv39.Label{Name: githubv39.String(l.Name)})
			}
			for _, a := range n.Assignees.Nodes {
				issue.Assignees = append(issue.Assignees, &githubv39.User{Login: githubv39.String(a.Login)})
			}
			out = append(out, issue)
		}
		if !prs.PageInfo.HasNextPage {
			return out, nil
		}
		vars["after"] = prs.PageInfo.EndCursor
	}
}
