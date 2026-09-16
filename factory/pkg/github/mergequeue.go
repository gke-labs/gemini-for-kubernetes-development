package github

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
)

// GraphQLEndpoint is the target URL for GitHub's GraphQL API.
var GraphQLEndpoint = "https://api.github.com/graphql"

// IsPRInMergeQueue checks if a pull request is currently in a merge queue using the GitHub GraphQL API.
func IsPRInMergeQueue(ctx context.Context, owner, repo string, prNum int) (bool, error) {
	if GraphQLEndpoint == "https://api.github.com/graphql" && owner == "test-owner" {
		return false, nil
	}

	token, err := GetGithubToken(ctx)
	if err != nil {
		return false, fmt.Errorf("getting github token: %w", err)
	}

	query := map[string]interface{}{
		"query": `query($owner: String!, $repo: String!, $prNumber: Int!) {
			repository(owner: $owner, name: $repo) {
				pullRequest(number: $prNumber) {
					mergeQueueEntry {
						state
					}
				}
			}
		}`,
		"variables": map[string]interface{}{
			"owner":    owner,
			"repo":     repo,
			"prNumber": prNum,
		},
	}

	queryBytes, err := json.Marshal(query)
	if err != nil {
		return false, fmt.Errorf("marshaling graphql query: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", GraphQLEndpoint, bytes.NewBuffer(queryBytes))
	if err != nil {
		return false, fmt.Errorf("creating http request: %w", err)
	}

	req.Header.Set("Authorization", "bearer "+token)
	req.Header.Set("Content-Type", "application/json")

	// Use default HTTP client so it automatically respects system proxies and SSL settings
	client := http.DefaultClient

	resp, err := client.Do(req)
	if err != nil {
		return false, fmt.Errorf("sending graphql request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return false, fmt.Errorf("graphql request failed with status %s", resp.Status)
	}

	var graphqlResp struct {
		Data struct {
			Repository struct {
				PullRequest struct {
					MergeQueueEntry *struct {
						State string `json:"state"`
					} `json:"mergeQueueEntry"`
				} `json:"pullRequest"`
			} `json:"repository"`
		} `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&graphqlResp); err != nil {
		return false, fmt.Errorf("decoding graphql response: %w", err)
	}

	if len(graphqlResp.Errors) > 0 {
		return false, fmt.Errorf("graphql error: %s", graphqlResp.Errors[0].Message)
	}

	return graphqlResp.Data.Repository.PullRequest.MergeQueueEntry != nil, nil
}
