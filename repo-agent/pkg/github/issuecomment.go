package github

import (
	githubv39 "github.com/google/go-github/v39/github"
)

type IssueComment struct {
	issuecomment *githubv39.IssueComment
}

func (ic *IssueComment) Body() string {
	return ic.issuecomment.GetBody()
}

func (ic *IssueComment) UserLogin() string {
	return ic.issuecomment.GetUser().GetLogin()
}
