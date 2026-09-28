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
	// docs-exploration/agent-runs/.
	Name string
	// Mode is plan | deploy | teardown. Each is one engine invocation.
	Mode string
	// Intent is the owner's free text: what to build on the first
	// plan, what to change on a re-plan, what to watch on a teardown.
	Intent string
	// Runbook names the runbook this run was started from. The script
	// has already copied it into the run's directory by the time the
	// engine reads this; the plan makes that copy this run's.
	Runbook string
	// Target is the pull request this run deploys instead of the
	// default branch; 0 for the default branch. Plan only: the script
	// pins the pull request's head commit, and deploy and teardown
	// read the pin.
	Target int
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
