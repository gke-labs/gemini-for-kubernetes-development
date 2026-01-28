package github

import (
	githubv39 "github.com/google/go-github/v39/github"
)

type Issue struct {
	issue *githubv39.Issue

	IssueComments []IssueComment
	//Upstream:  repo.GitCloneURL(),
}

func (i *Issue) String() string {
	return i.HTMLURL()
}

func (i *Issue) HTMLURL() string {
	return i.issue.GetHTMLURL()
}

func (i *Issue) Number() int {
	return i.issue.GetNumber()
}

func (i *Issue) Title() string {
	return i.issue.GetTitle()
}

func (i *Issue) Body() string {
	return i.issue.GetBody()
}
