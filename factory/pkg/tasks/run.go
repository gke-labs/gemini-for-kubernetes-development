package tasks

import (
	"bytes"
	"fmt"
)

// GetRunScript returns the in-sandbox run task script.
func GetRunScript() ([]byte, error) {
	return getScriptWithDefaults("run.sh")
}

// RunParams parameterize the run prompts.
//
// A run is self-contained: one name, one directory, one procedure. It
// carries no pointer to a shared runbook, which is what lets the
// deploy phase correct the procedure in place — there is no other
// reader to protect it from.
type RunParams struct {
	RepoName string
	HTMLURL  string
	// Name is the run's identity and its directory under
	// docs-exploration/runs/.
	Name string
	// Mode is plan | deploy | teardown. Each is one engine invocation.
	Mode string
	// Intent is the owner's free text: what to build on the first
	// plan, what to change on a re-plan, what to watch on a teardown.
	Intent string
	// From names an existing run whose procedure seeds this one.
	// Empty means plan from the intent alone.
	From string
}

// RenderRunPrompt renders the prompt for one run mode.
func RenderRunPrompt(mode string, params RunParams) ([]byte, error) {
	name := map[string]string{
		"plan":     "run_plan.txt",
		"deploy":   "run_deploy.txt",
		"teardown": "run_teardown.txt",
	}[mode]
	if name == "" {
		return nil, fmt.Errorf("unknown run mode %q (plan|deploy|teardown)", mode)
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
