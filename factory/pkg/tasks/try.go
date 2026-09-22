package tasks

import (
	"bytes"
	"fmt"
)

// GetTryScript returns the in-sandbox try task script.
func GetTryScript() ([]byte, error) {
	return getScriptWithDefaults("try.sh")
}

// TryParams parameterize the try prompts.
type TryParams struct {
	RepoName string
	HTMLURL  string
	Scenario string // runbook scenario: deploy, upgrade, …
	Path     string // target path within the runbook (gke, local, …); may be empty
	Guidance string // owner's free-text constraints for this run
}

// RenderTryPrompt renders the prompt for a try mode: run (execute the
// scenario, reusing a fresh script when nothing drifted) or teardown.
func RenderTryPrompt(mode string, params TryParams) ([]byte, error) {
	name := map[string]string{
		"run":      "try_run.txt",
		"teardown": "try_teardown.txt",
	}[mode]
	if name == "" {
		return nil, fmt.Errorf("unknown try mode %q (run|teardown)", mode)
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
