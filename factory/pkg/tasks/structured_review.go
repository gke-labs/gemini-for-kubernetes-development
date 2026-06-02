package tasks

import (
	"bytes"
	"fmt"

	githubv39 "github.com/google/go-github/v39/github"
)

// DraftReviewComment defines the structure for a review comment with severity
type DraftReviewComment struct {
	Path      *string `yaml:"path,omitempty" json:"path,omitempty"`
	Position  *int    `yaml:"position,omitempty" json:"position,omitempty"`
	Body      *string `yaml:"body,omitempty" json:"body,omitempty"`
	Line      *int    `yaml:"line,omitempty" json:"line,omitempty"`
	Side      *string `yaml:"side,omitempty" json:"side,omitempty"`
	StartLine *int    `yaml:"start_line,omitempty" json:"start_line,omitempty"`
	StartSide *string `yaml:"start_side,omitempty" json:"start_side,omitempty"`
	Severity  string  `yaml:"severity,omitempty" json:"severity,omitempty"`
}

// PullRequestReviewRequest defines the structure for a review request
type PullRequestReviewRequest struct {
	Body     *string               `yaml:"body,omitempty" json:"body,omitempty"`
	Event    *string               `yaml:"event,omitempty" json:"event,omitempty"`
	Comments []*DraftReviewComment `yaml:"comments,omitempty" json:"comments,omitempty"`
}

// ReviewAgentOutput defines the structure for the agent's YAML output.
type ReviewAgentOutput struct {
	Review *PullRequestReviewRequest `yaml:"review"`
	Labels []string                  `yaml:"labels,omitempty"`
}

// StructuredReviewParams defines the parameters for the structured review prompt.
type StructuredReviewParams struct {
	githubv39.PullRequest
	Instructions []string
	Prompt       string
	IgnoreFiles  []string
	DiffURL      string
	HTMLURL      string
}

// RenderStructuredReviewPrompt renders the structured review prompt.
func RenderStructuredReviewPrompt(params StructuredReviewParams) ([]byte, error) {
	promptTmpl, err := getPromptTemplate("structured_review.txt")
	if err != nil {
		return nil, fmt.Errorf("getting prompt template: %w", err)
	}

	var pBuf bytes.Buffer
	if err := promptTmpl.Execute(&pBuf, params); err != nil {
		return nil, fmt.Errorf("executing prompt template: %w", err)
	}

	return pBuf.Bytes(), nil
}
