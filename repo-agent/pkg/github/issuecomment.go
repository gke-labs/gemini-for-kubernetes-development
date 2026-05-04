package github

import (
	"time"

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

func (ic *IssueComment) ID() int64 {
	return ic.issuecomment.GetID()
}

func (ic *IssueComment) NodeID() string {
	return ic.issuecomment.GetNodeID()
}

func (ic *IssueComment) CreatedAt() time.Time {
	return ic.issuecomment.GetCreatedAt()
}
