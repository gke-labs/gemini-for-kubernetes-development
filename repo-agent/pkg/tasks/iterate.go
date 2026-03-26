package tasks

import (
	"bytes"
	"fmt"

	reviewv1alpha1 "github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/api/repowatch/v1alpha1"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/github"
)

var _ Task = &IterateModel{}

type IterateModel struct {
	Repo        *github.Repository
	User        *github.User
	AgentPrompt string
	BranchName  string
	PRID        string
	PromptFile  string
	Models      []string
	Extensions  []reviewv1alpha1.Extension
	Metadata    Metadata
	TraceabilityMetadataEnabled bool
}

func (m *IterateModel) Name() string {
	return "iterate"
}

func (m *IterateModel) PreScript() ([]byte, error) {
	tmpl, err := getScriptTemplate("iterate.sh")
	if err != nil {
		return nil, err
	}
	var w bytes.Buffer
	if err := tmpl.Execute(&w, m); err != nil {
		return nil, fmt.Errorf("failed to execute script template: %w", err)
	}
	return w.Bytes(), nil
}

func (m *IterateModel) Prompt() ([]byte, error) {
	tmpl, err := getPromptTemplate("iterate.txt")
	if err != nil {
		return nil, err
	}
	var w bytes.Buffer
	if err := tmpl.Execute(&w, m); err != nil {
		return nil, fmt.Errorf("failed to execute prompt template: %w", err)
	}
	return w.Bytes(), nil
}

func (m *IterateModel) PostScript() ([]byte, error) {
	return nil, nil
}

func (m *IterateModel) DraftState() string {
	return "informational"
}
