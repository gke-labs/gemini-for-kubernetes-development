package prompts

import (
	"bytes"
	"fmt"

	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/github"
)

type FixIssueModel struct {
	Issue         *github.Issue
	IssueComments []github.IssueComment
	Repo          *github.Repository
}

func FixIssuePrompt(model FixIssueModel) ([]byte, error) {
	tmpl, err := getTemplate("fix_issue.txt")
	if err != nil {
		return nil, err
	}
	var w bytes.Buffer
	if err := tmpl.Execute(&w, model); err != nil {
		return nil, fmt.Errorf("failed to execute prompt template: %w", err)
	}
	return w.Bytes(), nil
}
