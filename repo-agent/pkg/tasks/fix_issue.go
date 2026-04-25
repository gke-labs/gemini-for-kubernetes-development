package tasks

import (
	"bytes"
	"fmt"

	reviewv1alpha1 "github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/api/repowatch/v1alpha1"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/github"
)

var _ Task = &FixIssueModel{}

type FixIssueModel struct {
	Issue         *github.Issue
	Repo          *github.Repository
	IssueComments []github.IssueComment
	User          *github.User
	PromptFile    string
	Models        []string
	Extensions    []reviewv1alpha1.Extension
	Branch        string
	PRLabel       string
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
	tmpl, err := getPromptTemplate("fix_issue.txt")
	if err != nil {
		return nil, err
	}
	var w bytes.Buffer
	if err := tmpl.Execute(&w, m); err != nil {
		return nil, fmt.Errorf("failed to execute prompt template: %w", err)
	}
	return w.Bytes(), nil
}

func (m *FixIssueModel) PostScript() ([]byte, error) {
	return nil, nil
}

func (m *FixIssueModel) DraftState() string {
	return "informational"
}
