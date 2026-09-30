package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	githubv39 "github.com/google/go-github/v39/github"
)

// graphQLPath locates the GraphQL endpoint relative to the REST base URL.
//
// It is resolved as a sibling of the REST root rather than beneath it because
// the two diverge on GitHub Enterprise, which serves REST at /api/v3/ and
// GraphQL at /api/graphql. On github.com both hang off the root and the ".."
// is a no-op. Going through the REST client's base URL, rather than a fixed
// endpoint, is what lets these requests reuse its authentication and lets a
// test server stand in for both APIs at once.
const graphQLPath = "../graphql"

// reactionContentToGraphQL maps the REST reaction content strings - the
// vocabulary the rest of the watcher speaks - to GraphQL's ReactionContent
// enum. The two APIs name the same eight reactions differently.
var reactionContentToGraphQL = map[string]string{
	"+1":       "THUMBS_UP",
	"-1":       "THUMBS_DOWN",
	"laugh":    "LAUGH",
	"hooray":   "HOORAY",
	"confused": "CONFUSED",
	"heart":    "HEART",
	"rocket":   "ROCKET",
	"eyes":     "EYES",
}

// reactionContentFromGraphQL is the inverse of reactionContentToGraphQL.
var reactionContentFromGraphQL = func() map[string]string {
	m := make(map[string]string, len(reactionContentToGraphQL))
	for rest, gql := range reactionContentToGraphQL {
		m[gql] = rest
	}
	return m
}()

// ReviewReactions returns the reactions recorded on the top-level body of a
// pull request review.
//
// The REST API has no reactions endpoint for reviews - only for the inline
// comments inside them - so this goes through GraphQL, where a review is a
// Reactable like any other comment. The review is therefore addressed by its
// node ID (PullRequestReview.NodeID), the only identifier GraphQL accepts.
//
// The result is translated back into REST-shaped reactions so that callers
// interpret them with the same attribution policy as every other comment.
//
// Two limits of the GraphQL shape are worth knowing:
//   - Only the first reactionPageSize reactions are read, as for every other
//     kind of comment.
//   - GraphQL reports a reaction's author through a User-typed field, which is
//     null when the reactor is a Bot account (a GitHub App, or GITHUB_TOKEN in
//     Actions). Such reactions come back with no User, so a watcher running as
//     a bot identity would not recognise its own marks on review bodies. The
//     watcher authenticates as a user today; reading reactionGroups'
//     viewerHasReacted would be the way to lift this if that changes.
func (c *Client) ReviewReactions(ctx context.Context, reviewNodeID string) ([]*githubv39.Reaction, error) {
	if !c.Ready() {
		return nil, errNoClient
	}

	const query = `query($id: ID!, $first: Int!) {
		node(id: $id) {
			... on PullRequestReview {
				reactions(first: $first) {
					nodes {
						content
						user { login }
					}
				}
			}
		}
	}`
	var data struct {
		Node *struct {
			Reactions struct {
				Nodes []struct {
					Content string `json:"content"`
					User    *struct {
						Login string `json:"login"`
					} `json:"user"`
				} `json:"nodes"`
			} `json:"reactions"`
		} `json:"node"`
	}
	vars := map[string]interface{}{"id": reviewNodeID, "first": reactionPageSize}
	if err := c.graphQL(ctx, query, vars, &data); err != nil {
		return nil, fmt.Errorf("listing reactions on review %s: %w", reviewNodeID, err)
	}
	if data.Node == nil {
		return nil, fmt.Errorf("listing reactions on review %s: review not found", reviewNodeID)
	}

	reactions := make([]*githubv39.Reaction, 0, len(data.Node.Reactions.Nodes))
	for _, n := range data.Node.Reactions.Nodes {
		content, ok := reactionContentFromGraphQL[n.Content]
		if !ok {
			// A reaction GitHub added after this mapping was written. Nothing
			// the watcher reads could be spelled that way, so it is dropped.
			continue
		}
		r := &githubv39.Reaction{Content: githubv39.String(content)}
		if n.User != nil {
			r.User = &githubv39.User{Login: githubv39.String(n.User.Login)}
		}
		reactions = append(reactions, r)
	}
	return reactions, nil
}

// AddReviewReaction records a reaction on the top-level body of a pull request
// review, addressed by its node ID. See ReviewReactions for why this is GraphQL
// rather than REST.
//
// content is the REST spelling of the reaction (e.g. "+1", "eyes"), so callers
// use the same vocabulary for reviews as for every other kind of comment.
func (c *Client) AddReviewReaction(ctx context.Context, reviewNodeID string, content string) error {
	if !c.Ready() {
		return errNoClient
	}

	gqlContent, ok := reactionContentToGraphQL[content]
	if !ok {
		return fmt.Errorf("adding reaction %q to review %s: unsupported reaction", content, reviewNodeID)
	}

	const mutation = `mutation($subjectId: ID!, $content: ReactionContent!) {
		addReaction(input: {subjectId: $subjectId, content: $content}) {
			reaction { content }
		}
	}`
	vars := map[string]interface{}{
		"subjectId": reviewNodeID,
		"content":   gqlContent,
	}
	if err := c.graphQL(ctx, mutation, vars, nil); err != nil {
		return fmt.Errorf("adding reaction %q to review %s: %w", content, reviewNodeID, err)
	}
	return nil
}

// graphQL issues a GraphQL request with the REST client's credentials and
// decodes its data into out, which may be nil when the caller only cares
// whether the request succeeded.
//
// The request is built by the REST client, for its base URL and headers, but
// deliberately not sent through its Do. go-github files every non-search
// response under the REST "core" rate limit, and GraphQL has a separate limit
// of its own: letting Do see GraphQL's headers would overwrite what it knows of
// the core limit, and once either ran out it would refuse all REST calls
// locally until the other's reset. Sending through the underlying HTTP client
// keeps the REST client's bookkeeping about REST alone.
//
// GraphQL reports most failures in the body of a 200 response rather than in
// the status code, so the errors array is checked explicitly.
func (c *Client) graphQL(ctx context.Context, query string, variables map[string]interface{}, out interface{}) error {
	body := map[string]interface{}{
		"query":     query,
		"variables": variables,
	}
	req, err := c.gh.NewRequest("POST", graphQLPath, body)
	if err != nil {
		return fmt.Errorf("building graphql request: %w", err)
	}

	httpResp, err := c.gh.Client().Do(req.WithContext(ctx))
	if err != nil {
		return err
	}
	defer httpResp.Body.Close()
	if err := githubv39.CheckResponse(httpResp); err != nil {
		return err
	}

	var resp struct {
		Data   interface{} `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	resp.Data = out
	if err := json.NewDecoder(httpResp.Body).Decode(&resp); err != nil {
		return fmt.Errorf("decoding graphql response: %w", err)
	}
	if len(resp.Errors) > 0 {
		return errors.New(resp.Errors[0].Message)
	}
	return nil
}
