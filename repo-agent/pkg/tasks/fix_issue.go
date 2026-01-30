package tasks

import (
	"bytes"
	"fmt"

	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/github"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/prompts"
)

var _ Task = &FixIssueModel{}

type FixIssueModel struct {
	Issue         *github.Issue
	Repo          *github.Repository
	IssueComments []github.IssueComment
	User          *github.User
	PromptFile    string
}

func (m *FixIssueModel) Name() string {
	return "fix-issue"
}

func (m *FixIssueModel) PreScript() ([]byte, error) {
	tmpl, err := getScriptTemplate("fix_issue.sh")
	if err != nil {
		return nil, err
	}
	var w bytes.Buffer
	if err := tmpl.Execute(&w, m); err != nil {
		return nil, fmt.Errorf("failed to execute prompt template: %w", err)
	}
	return w.Bytes(), nil
}

func (m *FixIssueModel) Prompt() ([]byte, error) {
	return prompts.FixIssuePrompt(
		prompts.FixIssueModel{
			Issue:         m.Issue,
			Repo:          m.Repo,
			IssueComments: m.IssueComments,
		},
	)
}

func (m *FixIssueModel) PostScript() ([]byte, error) {
	return nil, nil
}
