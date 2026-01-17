package github

import (
	"fmt"
	"strconv"
	"strings"
)

type Issue struct {
	Repo *Repo

	IssueNumber int
}

func ParseIssueURL(s string) (*Issue, error) {
	u := strings.TrimPrefix(s, "https://")
	tokens := strings.Split(u, "/")

	// e.g. https://github.com/GoogleCloudPlatform/k8s-config-connector/issues/6010
	if len(tokens) == 5 && tokens[0] == "github.com" && tokens[3] == "issues" {
		issue := &Issue{
			Repo: &Repo{
				Host:  "github.com",
				Owner: tokens[1],
				Name:  tokens[2],
			},
		}
		// Parse issue number
		n, err := strconv.Atoi(tokens[4])
		if err != nil {
			return nil, fmt.Errorf("invalid issue number %q: %w", tokens[4], err)
		}
		issue.IssueNumber = n
		return issue, nil
	}

	return nil, fmt.Errorf("issue format %q not recognized", s)
}

func (i *Issue) String() string {
	return i.HTMLURL()
}

func (i *Issue) HTMLURL() string {
	return fmt.Sprintf("https://%s/%s/%s/issues/%d", i.Repo.Host, i.Repo.Owner, i.Repo.Name, i.IssueNumber)
}
