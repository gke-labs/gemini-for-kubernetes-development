package tasks

import "fmt"

// PlanFilePath is where a plan run (factory recipe plan) leaves its result
// inside the sandbox — the durable in-sandbox contract a later `factory
// fix --with-plan` reads.
func PlanFilePath(issueNum int) string {
	return fmt.Sprintf("/workspaces/plan-issue-%d.md", issueNum)
}
