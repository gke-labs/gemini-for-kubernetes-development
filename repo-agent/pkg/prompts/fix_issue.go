package prompts

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"text/template"

	"github.com/google/go-github/v39/github"
	"golang.org/x/oauth2"

	_ "embed"
)

//go:embed fix_issue.txt
var FixIssuePromptTemplate string

func FixIssuePrompt(ctx context.Context, repoOwner, repoName string, issueNumber int) ([]byte, error) {
	githubCommand := exec.CommandContext(ctx, "gh", "auth", "token")
	var stdout bytes.Buffer
	githubCommand.Stdout = &stdout
	githubCommand.Stderr = os.Stderr
	if err := githubCommand.Run(); err != nil {
		return nil, fmt.Errorf("unable to get github credentials (with gh auth token command): %w", err)
	}

	token := strings.TrimSpace(stdout.String())
	tokenSource := oauth2.StaticTokenSource(
		&oauth2.Token{AccessToken: token},
	)
	authenticatedClient := oauth2.NewClient(ctx, tokenSource)
	githubAPI := github.NewClient(authenticatedClient)

	issue, _, err := githubAPI.Issues.Get(ctx, repoOwner, repoName, issueNumber)
	if err != nil {
		return nil, fmt.Errorf("failed to get github issue: %w", err)
	}

	model := FixIssuePromptModel{
		IssueURL:         issue.GetHTMLURL(),
		IssueNumber:      issue.GetNumber(),
		IssueTitle:       issue.GetTitle(),
		IssueDescription: issue.GetBody(),
	}

	comments, _, err := githubAPI.Issues.ListComments(ctx, repoOwner, repoName, issueNumber, nil)
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
