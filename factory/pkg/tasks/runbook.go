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
	Scenario string // the runbook name: deploy-gcp, upgrade-gcp, …
	Mode     string // plan | deploy | run | teardown
	Instance string // deployment instance name (one runbook, many parameterized deployments)
	Guidance string // owner's free-text constraints for this run
}

// RenderRunbookPrompt renders the prompt for a try mode: run (execute the
// scenario, reusing a fresh script when nothing drifted) or teardown.
func RenderRunbookPrompt(phase string, params RunbookParams) ([]byte, error) {
	name := map[string]string{
		"prepare":  "runbook_prepare.txt",
		"execute":  "runbook_execute.txt",
		"teardown": "runbook_teardown.txt",
	}[phase]
	if name == "" {
		return nil, fmt.Errorf("unknown runbook phase %q (prepare|execute|teardown)", phase)
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
