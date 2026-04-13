package tasks

import (
	"bytes"
	"fmt"

	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/tasks/metadata"
)

var _ Task = &ChoreModel{}

type ChoreModel struct {
	AgentPrompt                 string
	ChoreName                   string
	ChoreFile                   string
	RepoName                    string
	CloneURL                    string
	RepoOwner                   string
	PromptFile                  string
	SkipPR                      bool
	Metadata                    metadata.Metadata
	TraceabilityMetadataEnabled bool
}

func (m *ChoreModel) Name() string {
	return "chore"
}

func (m *ChoreModel) PreScript() ([]byte, error) {
	tmpl, err := getScriptTemplate("chore.sh")
	if err != nil {
		return nil, err
	}
	var w bytes.Buffer
	if err := tmpl.Execute(&w, m); err != nil {
		return nil, fmt.Errorf("failed to execute script template: %w", err)
	}
	return w.Bytes(), nil
}

func (m *ChoreModel) Prompt() ([]byte, error) {
	tmpl, err := getPromptTemplate("chore.txt")
	if err != nil {
		return nil, err
	}
	var w bytes.Buffer
	if err := tmpl.Execute(&w, m); err != nil {
		return nil, fmt.Errorf("failed to execute prompt template: %w", err)
	}
	return w.Bytes(), nil
}

func (m *ChoreModel) PostScript() ([]byte, error) {
	return nil, nil
}

func (m *ChoreModel) DraftState() string {
	return "informational"
}
