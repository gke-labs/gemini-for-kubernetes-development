package prompts

import (
	"bytes"
	"fmt"
	"text/template"

	"github.com/google/go-github/v39/github"
)

// ExpandIssueHandlerPrompt expands the issue handler prompt template with the provided issue.
func ExpandIssueHandlerPrompt(templateStr string, issue *github.Issue) (string, error) {
	tmpl, err := template.New("issue_handler").Parse(templateStr)
	if err != nil {
		return "", fmt.Errorf("failed to parse issue handler template: %w", err)
	}

	var buf bytes.Buffer
	err = tmpl.Execute(&buf, issue)
	if err != nil {
		return "", fmt.Errorf("failed to execute issue handler template: %w", err)
	}
	return buf.String(), nil
}
