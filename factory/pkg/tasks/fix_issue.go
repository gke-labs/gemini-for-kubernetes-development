package tasks

import (
	"bytes"
	"fmt"
)

type Repo struct {
	CloneURL string
}

type Issue struct {
	Number  int
	HTMLURL string
	Title   string
	Body    string
}

type IssueComment struct {
	UserLogin string
	Body      string
}

type FixIssueParams struct {
	Repo          Repo
	Issue         Issue
	IssueComments []IssueComment
	Instruction   string
	Branch        string
	Models        []string
	DraftPR       bool
	PRLabel       string
	NoPR          bool
}

func GetFixIssueScript() ([]byte, error) {
	return scriptsFS.ReadFile("fix_issue.sh")
}

func RenderFixIssuePrompt(params FixIssueParams) ([]byte, error) {
	if len(params.Models) == 0 {
		params.Models = []string{"gemini-2.5-flash"}
	}

	promptTmpl, err := getPromptTemplate("fix_issue.txt")
	if err != nil {
		return nil, fmt.Errorf("getting prompt template: %w", err)
	}
	var pBuf bytes.Buffer
	if err := promptTmpl.Execute(&pBuf, params); err != nil {
		return nil, fmt.Errorf("executing prompt template: %w", err)
	}

	return pBuf.Bytes(), nil
}

type FailedRun struct {
	ID   int64
	Name string
	URL  string
}

type PRComment struct {
	ID        int64
	UserLogin string
	CreatedAt string
	Body      string
}

type PullRequest struct {
	Number int
	URL    string
	Title  string
	Body   string
}

type InvestigateParams struct {
	PullRequest   PullRequest
	FailedRuns    []FailedRun
	IssueComments []PRComment
	Models        []string
}

func GetInvestigateScript() ([]byte, error) {
	return scriptsFS.ReadFile("investigate_failures.sh")
}

func RenderInvestigatePrompt(params InvestigateParams) ([]byte, error) {
	if len(params.Models) == 0 {
		params.Models = []string{"gemini-3.5-flash", "gemini-3-flash-preview", "gemini-3.1-pro-preview", "gemini-2.5-pro"}
	}

	promptTmpl, err := getPromptTemplate("investigate_failures.txt")
	if err != nil {
		return nil, fmt.Errorf("getting prompt template: %w", err)
	}
	var pBuf bytes.Buffer
	if err := promptTmpl.Execute(&pBuf, params); err != nil {
		return nil, fmt.Errorf("executing prompt template: %w", err)
	}

	return pBuf.Bytes(), nil
}

type RepositoryCommit struct {
	SHA     string
	Message string
}

type PullRequestComment struct {
	Path     string
	DiffHunk string
	Body     string
}

type PRReview struct {
	ID                  int64
	UserLogin           string
	Body                string
	PullRequestComments []PullRequestComment
}

type AddressFeedbackParams struct {
	PullRequest           PullRequest
	RepositoryCommits     []RepositoryCommit
	OldIssueComments      []PRComment
	IssueComments         []PRComment
	OldPullRequestReviews []PRReview
	PullRequestReviews    []PRReview
	Models                []string
}

func GetAddressFeedbackScript() ([]byte, error) {
	return scriptsFS.ReadFile("address_feedback.sh")
}

func RenderAddressFeedbackPrompt(params AddressFeedbackParams) ([]byte, error) {
	if len(params.Models) == 0 {
		params.Models = []string{"gemini-3.5-flash", "gemini-3-flash-preview", "gemini-3.1-pro-preview", "gemini-2.5-pro"}
	}

	promptTmpl, err := getPromptTemplate("address_feedback.txt")
	if err != nil {
		return nil, fmt.Errorf("getting prompt template: %w", err)
	}
	var pBuf bytes.Buffer
	if err := promptTmpl.Execute(&pBuf, params); err != nil {
		return nil, fmt.Errorf("executing prompt template: %w", err)
	}

	return pBuf.Bytes(), nil
}
