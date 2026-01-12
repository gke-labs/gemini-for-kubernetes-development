package prompts

import (
	"bytes"
	"fmt"
	"text/template"

	_ "embed"

	"github.com/google/go-github/v39/github"
)

//go:embed review_system.txt
var ReviewPromptTemplate string

type ReviewPromptModel struct {
	github.PullRequest
	Prompt string
}

// ExpandReviewPrompt expands the review prompt template with the provided model.
// It performs a two-level substitution to allow the user-provided prompt to also use template variables from the PR.
func ExpandReviewPrompt(model ReviewPromptModel) (string, error) {
	// Level 1 substitution
	promptTmpl := ReviewPromptTemplate

	lvl1, err := template.New("lvl1").Parse(promptTmpl)
	if err != nil {
		return "", fmt.Errorf("failed to parse level 1 template: %w", err)
	}

	var level1 bytes.Buffer
	err = lvl1.Execute(&level1, model)
	if err != nil {
		return "", fmt.Errorf("failed to execute level 1 template: %w", err)
	}

	// Level 2 substitution
	// This allows the user provided Prompt to contain template variables that are substituted with PR details.
	tmpl, err := template.New("lvl2").Parse(level1.String())
	if err != nil {
		return "", fmt.Errorf("failed to parse level 2 template: %w", err)
	}

	var buf bytes.Buffer
	err = tmpl.Execute(&buf, model.PullRequest)
	if err != nil {
		return "", fmt.Errorf("failed to execute level 2 template: %w", err)
	}
	return buf.String(), nil
}
