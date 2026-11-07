package prs

import (
	"context"
	"strings"
	"testing"

	githubv39 "github.com/google/go-github/v39/github"
)

// A review is for the latest change: a grouped graph's PR is reviewed with
// its latest step's issue's instructions alone - none if that issue has
// none, never an earlier step's, which were reviewed in their turn; any
// other PR with its own, else the first issue its body names.
func TestReviewInstructions(t *testing.T) {
	issue := func(n int, body string) *githubv39.Issue {
		return &githubv39.Issue{Number: githubv39.Int(n), Body: githubv39.String(body)}
	}
	with := func(skill string) string { return "## Review Instructions\n\nUse " + skill + ".\n" }
	// KCC #15207's three steps, as refIssues reads them: by number.
	issues := []*githubv39.Issue{issue(15206, with("controller")), issue(15208, with("mockgcp")), issue(15216, "Release it."), issue(9, with("branch"))}
	got := func(body string) string {
		pc := &prContext{pr: &githubv39.PullRequest{Body: githubv39.String(body)}, refIssues: &refIssues{resolved: issues, loaded: true}}
		return strings.Join(reviewInstructions(context.Background(), pc), ",")
	}
	first := "Step one.\n\nFixes #15206\n"
	second := first + "\n---\n\n### MockGCP\n\nFixes #15208\n\n<!-- factory:group-step issue=15208 -->"
	last := second + "\n---\n\n### Release\n\nFixes #15216\n\n<!-- factory:group-step issue=15216 -->"
	for name, tc := range map[string]struct{ body, want string }{
		"a group's first step":                         {first, "Use controller."},
		"its second step":                              {second, "Use mockgcp."},
		"its last step, whose issue has none: none":    {last, ""},
		"a PR's own instructions":                      {"Fixes #15206\n\n" + with("own"), "Use own."},
		"no group: the first issue named":              {"Fixes #15208 and fixes #15206", "Use mockgcp."},
		"no group, the first named has none: none":     {"Fixes #15216, fixes #15206", ""},
		"a group's own instructions do not count":      {with("own") + "\n" + second, "Use mockgcp."},
		"a marker of an issue it does not close: none": {first + "<!-- factory:group-step issue=1 -->", ""},
	} {
		if g := got(tc.body); g != tc.want {
			t.Errorf("%s: %q, want %q", name, g, tc.want)
		}
	}
}
