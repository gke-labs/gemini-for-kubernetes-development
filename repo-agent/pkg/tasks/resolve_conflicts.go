package tasks

import (
	"bytes"
	"fmt"

	reviewv1alpha1 "github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/api/repowatch/v1alpha1"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/github"
)

var _ Task = &ResolveConflictsModel{}

type ResolveConflictsModel struct {
	Repo        *github.Repository
	PullRequest *github.PullRequest
	User        *github.User
	PromptFile  string
	Models      []string
	Extensions  []reviewv1alpha1.Extension
	BaseRef     string
	HeadRef     string
}

func (m *ResolveConflictsModel) Name() string {
	return "resolve-conflicts"
}

func (m *ResolveConflictsModel) PreScript() ([]byte, error) {
	tmpl, err := getScriptTemplate("resolve_conflicts.sh")
	if err != nil {
		return nil, err
	}
	var w bytes.Buffer
	if err := tmpl.Execute(&w, m); err != nil {
		return nil, fmt.Errorf("failed to execute script template: %w", err)
	}
	return w.Bytes(), nil
}

func (m *ResolveConflictsModel) Prompt() ([]byte, error) {
	tmpl, err := getPromptTemplate("resolve_conflicts.txt")
	if err != nil {
		return nil, err
	}
	var w bytes.Buffer
	if err := tmpl.Execute(&w, m); err != nil {
		return nil, fmt.Errorf("failed to execute prompt template: %w", err)
	}
	return w.Bytes(), nil
}

func (m *ResolveConflictsModel) PostScript() ([]byte, error) {
	return nil, nil
}

func (m *ResolveConflictsModel) DraftState() string {
	return "informational"
}
