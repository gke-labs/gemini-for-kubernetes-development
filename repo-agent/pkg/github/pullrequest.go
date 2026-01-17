package github

import (
	"fmt"
	"strconv"
	"strings"
)

type PullRequest struct {
	Repo Repo

	PullRequestNumber int
}

func ParsePullRequestURL(s string) (*PullRequest, error) {
	u := strings.TrimPrefix(s, "https://")

	if prefix, suffix, found := strings.Cut(u, "#"); found {
		u = prefix
		_ = suffix // ignore fragment
	}

	// Conveneience: handle URLs that end with /changes (when copy and paste from file view)
	u = strings.TrimSuffix(u, "/changes")

	tokens := strings.Split(u, "/")

	// e.g. https://github.com/GoogleCloudPlatform/k8s-config-connector/pull/6010
	if len(tokens) == 5 && tokens[0] == "github.com" && tokens[3] == "pull" {
		pr := &PullRequest{
			Repo: Repo{
				Host:  "github.com",
				Owner: tokens[1],
				Name:  tokens[2],
			},
		}
		// Parse pull request number
		n, err := strconv.Atoi(tokens[4])
		if err != nil {
			return nil, fmt.Errorf("invalid pull request number %q: %w", tokens[4], err)
		}
		pr.PullRequestNumber = n
		return pr, nil
	}

	return nil, fmt.Errorf("pull-request format %q not recognized", s)
}
