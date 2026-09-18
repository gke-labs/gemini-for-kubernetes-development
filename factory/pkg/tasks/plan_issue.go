package tasks

import (
	"bytes"
	"fmt"

	githubv39 "github.com/google/go-github/v39/github"
)

// PlanParams defines the parameters for the issue-plan prompt. PriorPlan
// and Feedback drive refinement rounds: the maintainer's feedback is
// applied against the previous plan rather than starting over.
type PlanParams struct {
	githubv39.Issue
	Instructions []string
	HTMLURL      string
	PriorPlan    string
	Feedback     string
}

// RenderPlanPrompt renders the issue-plan prompt.
func RenderPlanPrompt(params PlanParams) ([]byte, error) {
	promptTmpl, err := getPromptTemplate("plan_issue.txt")
	if err != nil {
		return nil, fmt.Errorf("getting prompt template: %w", err)
	}

	var pBuf bytes.Buffer
	if err := promptTmpl.Execute(&pBuf, params); err != nil {
		return nil, fmt.Errorf("executing prompt template: %w", err)
	}

	return pBuf.Bytes(), nil
}

// GetPlanScript returns the in-sandbox plan task script.
func GetPlanScript() ([]byte, error) {
	return getScriptWithDefaults("plan_issue.sh")
}

// PlanFilePath is where a plan run leaves its result inside the sandbox —
// the durable in-sandbox contract a later `factory fix --with-plan` reads.
func PlanFilePath(issueNum int) string {
	return fmt.Sprintf("/workspaces/plan-issue-%d.md", issueNum)
}
