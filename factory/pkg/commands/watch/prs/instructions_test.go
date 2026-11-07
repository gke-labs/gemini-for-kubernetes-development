package prs

import (
	"strings"
	"testing"

	githubv39 "github.com/google/go-github/v39/github"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/commands/common"
)

// A grouped graph's PR is reviewed with its latest step's instructions;
// any other PR's issues are taken in the order its body names them, the
// rest after, by number.
func TestInstructionOrder(t *testing.T) {
	issue := func(n int, skill string) *githubv39.Issue {
		return &githubv39.Issue{Number: githubv39.Int(n), Body: githubv39.String("## Review Instructions\n\nUse " + skill + ".\n")}
	}
	// refIssues reads them by number: KCC #15207's three steps.
	issues := []*githubv39.Issue{issue(15206, "controller"), issue(15208, "mockgcp"), issue(15216, "release"), issue(9, "branch")}
	order := func(body string) string {
		var names []string
		var bodies []string
		for _, i := range instructionOrder(body, issues) {
			bodies = append(bodies, i.GetBody())
		}
		names = append(names, common.ExtractReviewInstructions(bodies...)...)
		return strings.Join(names, ",")
	}
	first := "Step one.\n\nFixes #15206\n"
	if got := order(first); got != "Use controller." {
		t.Errorf("a group's first step: %q", got)
	}
	second := first + "\n---\n\n### MockGCP\n\nFixes #15208\n\n<!-- factory:group-step issue=15208 -->"
	if got := order(second); got != "Use mockgcp." {
		t.Errorf("the group's second step: %q", got)
	}
	last := second + "\n---\n\n### Release\n\nFixes #15216\n\n<!-- factory:group-step issue=15216 -->"
	if got := order(last); got != "Use release." {
		t.Errorf("the group's last step: %q", got)
	}
	if got := order("Fixes #15216 and fixes #15206"); got != "Use release." {
		t.Errorf("body order: %q", got)
	}
	got := instructionOrder("Fixes #15208", issues)
	if got[0].GetNumber() != 15208 || got[1].GetNumber() != 9 || got[3].GetNumber() != 15216 {
		t.Errorf("the issues the body does not name come after, by number: %v", []int{got[0].GetNumber(), got[1].GetNumber(), got[2].GetNumber(), got[3].GetNumber()})
	}
}
