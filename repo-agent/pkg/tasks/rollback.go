package tasks

import (
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/tasks/metadata"
	"bytes"
	"fmt"

	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/github"
)

var _ Task = &RollbackModel{}

type RollbackModel struct {
	PullRequestID               int
	Repo                        *github.Repository
	PullRequest                 *github.PullRequest
	User                        *github.User
	CommitSHA                   string
	Branch                      string
	Remote                      string
	Metadata                    metadata.Metadata
	TraceabilityMetadataEnabled bool
}

func (m *RollbackModel) PreScript() ([]byte, error) {
	tmpl, err := getScriptTemplate("rollback.sh")
	if err != nil {
		return nil, err
	}
	var w bytes.Buffer
	if err := tmpl.Execute(&w, m); err != nil {
		return nil, fmt.Errorf("failed to execute script template: %w", err)
	}
	return w.Bytes(), nil
}

func (m *RollbackModel) Prompt() ([]byte, error) {
	tmpl, err := getPromptTemplate("rollback.txt")
	if err != nil {
		return nil, err
	}
	var w bytes.Buffer
	if err := tmpl.Execute(&w, m); err != nil {
		return nil, fmt.Errorf("failed to execute prompt template: %w", err)
	}
	return w.Bytes(), nil
}

func (m *RollbackModel) PostScript() ([]byte, error) {
	return nil, nil
}

func (m *RollbackModel) DraftState() string {
	return "informational"
}
