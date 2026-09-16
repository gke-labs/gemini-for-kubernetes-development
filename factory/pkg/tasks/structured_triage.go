package tasks

import (
	"bytes"
	"fmt"

	githubv39 "github.com/google/go-github/v39/github"
)

// TriageSuggestion is the structured triage produced for an issue.
type TriageSuggestion struct {
	// Labels suggested for the issue, drawn from the repo's existing labels.
	Labels []string `yaml:"labels,omitempty" json:"labels,omitempty"`
	// Priority is low, medium, or high.
	Priority string `yaml:"priority,omitempty" json:"priority,omitempty"`
	// Duplicates lists issue numbers that look like duplicates.
	Duplicates []int `yaml:"duplicates,omitempty" json:"duplicates,omitempty"`
	// Assessment is a short summary: what the issue is, the likely cause or
	// affected area, and a suggested next step.
	Assessment string `yaml:"assessment,omitempty" json:"assessment,omitempty"`
}

// TriageAgentOutput defines the structure of the triage agent's YAML output.
type TriageAgentOutput struct {
	Triage *TriageSuggestion `yaml:"triage"`
}

// StructuredTriageParams defines the parameters for the triage prompt.
type StructuredTriageParams struct {
	githubv39.Issue
	Instructions []string
	HTMLURL      string
}

// RenderStructuredTriagePrompt renders the structured triage prompt.
func RenderStructuredTriagePrompt(params StructuredTriageParams) ([]byte, error) {
	promptTmpl, err := getPromptTemplate("structured_triage.txt")
	if err != nil {
		return nil, fmt.Errorf("getting prompt template: %w", err)
	}

	var pBuf bytes.Buffer
	if err := promptTmpl.Execute(&pBuf, params); err != nil {
		return nil, fmt.Errorf("executing prompt template: %w", err)
	}

	return pBuf.Bytes(), nil
}

// GetTriageScript returns the in-sandbox triage task script.
func GetTriageScript() ([]byte, error) {
	return getScriptWithDefaults("triage_issue.sh")
}
