package prompts

import (
	"bytes"
	"context"
	"fmt"
	"text/template"

	_ "embed"

	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/github"
)

//go:embed fix_issue.txt
var FixIssuePromptTemplate string

func FixIssuePrompt(ctx context.Context, githubAPI *github.Client, repo *github.Repo, issueNumber int) ([]byte, error) {

	issue, _, err := githubAPI.Issues.Get(ctx, repo.Owner, repo.Name, issueNumber)
	if err != nil {
		return nil, fmt.Errorf("failed to get github issue: %w", err)
	}

	model := FixIssuePromptModel{
		IssueURL:         issue.GetHTMLURL(),
		IssueNumber:      issue.GetNumber(),
		IssueTitle:       issue.GetTitle(),
		IssueDescription: issue.GetBody(),
	}

	comments, _, err := githubAPI.Issues.ListComments(ctx, repo.Owner, repo.Name, issueNumber, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to list github issue comments: %w", err)
	}
	for _, comment := range comments {
		model.IssueComments = append(model.IssueComments, IssueComment{
			Author: comment.GetUser().GetLogin(),
			Body:   comment.GetBody(),
		})
	}

	tmpl, err := template.New("prompt").Parse(FixIssuePromptTemplate)
	if err != nil {
		return nil, fmt.Errorf("failed to parse prompt template: %w", err)
	}
	var w bytes.Buffer
	if err := tmpl.Execute(&w, &model); err != nil {
		return nil, fmt.Errorf("failed to execute prompt template: %w", err)
	}

	return w.Bytes(), nil
}

type FixIssuePromptModel struct {
	IssueURL         string
	IssueNumber      int
	IssueTitle       string
	IssueDescription string
	IssueComments    []IssueComment
}

type IssueComment struct {
	Author string
	Body   string
}
