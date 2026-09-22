package tasks

import (
	"bytes"
	"fmt"
)

// GetRunbookScript returns the in-sandbox runbook task script.
func GetRunbookScript() ([]byte, error) {
	return getScriptWithDefaults("runbook.sh")
}

// RunbookParams parameterize the runbook prompts.
type RunbookParams struct {
	RepoName string
	HTMLURL  string
	Scenario string // runbook scenario: deploy, upgrade, …
	Path     string // target path within the runbook (gke, local, …); may be empty
	Guidance string // owner's free-text constraints for this run
}

// RenderRunbookPrompt renders the prompt for a try mode: run (execute the
// scenario, reusing a fresh script when nothing drifted) or teardown.
func RenderRunbookPrompt(mode string, params RunbookParams) ([]byte, error) {
	name := map[string]string{
		"run":      "runbook_run.txt",
		"teardown": "runbook_teardown.txt",
	}[mode]
	if name == "" {
		return nil, fmt.Errorf("unknown runbook mode %q (run|teardown)", mode)
	}
	t, err := getPromptTemplate(name)
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	if err := t.Execute(&buf, params); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
