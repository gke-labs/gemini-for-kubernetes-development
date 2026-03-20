package tasks

import (
	"bytes"
	"fmt"

	reviewv1alpha1 "github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/api/repowatch/v1alpha1"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/github"
)

var _ Task = &TriageIssueModel{}

type TriageIssueModel struct {
	Issue         *github.Issue
	IssueComments []github.IssueComment
	User          *github.User
	PromptFile    string
	Models        []string
	AgentName     string
	Extensions    []reviewv1alpha1.Extension

	// Traceability metadata
	GithubTraceability bool
	SandboxTaskName    string
	SandboxTaskUID     string
	SandboxName        string
	RepoWatchName      string
	Namespace          string
	Timestamp          string
}

func (m *TriageIssueModel) Name() string {
	return "triage-issue"
}

func (m *TriageIssueModel) PreScript() ([]byte, error) {
	tmpl, err := getScriptTemplate("triage_issue.sh")
	if err != nil {
		return nil, err
	}
	var w bytes.Buffer
	if err := tmpl.Execute(&w, m); err != nil {
		return nil, fmt.Errorf("failed to execute prompt template: %w", err)
	}
	return w.Bytes(), nil
}

func (m *TriageIssueModel) Prompt() ([]byte, error) {
	tmpl, err := getPromptTemplate("triage_issue.txt")
	if err != nil {
		return nil, err
	}
	var w bytes.Buffer
	if err := tmpl.Execute(&w, m); err != nil {
		return nil, fmt.Errorf("failed to execute prompt template: %w", err)
	}
	return w.Bytes(), nil
}

func (m *TriageIssueModel) PostScript() ([]byte, error) {
	tmpl, err := getScriptTemplate("triage_issue_post.sh")
	if err != nil {
		return nil, err
	}
	var w bytes.Buffer
	if err := tmpl.Execute(&w, m); err != nil {
		return nil, fmt.Errorf("failed to execute post-script template: %w", err)
	}
	return w.Bytes(), nil
}

func (m *TriageIssueModel) DraftState() string {
	return "submittable"
}
