package taskoutput

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"strconv"
	"strings"

	githubv39 "github.com/google/go-github/v39/github"
)

// Apply does what a document asks of GitHub, with gh's token. With dryRun
// it says what it would do and does nothing.
func Apply(ctx context.Context, gh *githubv39.Client, doc *Document, dryRun bool, out io.Writer) error {
	switch doc.Kind {
	case "Triage":
		return applyTriage(ctx, gh, doc, dryRun, out)
	case "Plan":
		return applyPlan(ctx, gh, doc, dryRun, out)
	}
	return fmt.Errorf("cannot apply task output kind %q", doc.Kind)
}

func applyTriage(ctx context.Context, gh *githubv39.Client, doc *Document, dryRun bool, out io.Writer) error {
	t, err := doc.TriageSpec()
	if err != nil {
		return err
	}
	owner, repo, num, isPR, err := parseItemURL(doc.Target.URL)
	if err != nil {
		return err
	}
	if isPR {
		return fmt.Errorf("a Triage targets an issue, not %s", doc.Target.URL)
	}
	doing := func(present, conditional string) string {
		if dryRun {
			return "Would " + conditional
		}
		return present
	}
	if len(t.Labels) > 0 {
		fmt.Fprintf(out, "%s labels %v to %s\n", doing("Adding", "add"), t.Labels, doc.Target.URL)
		if !dryRun {
			if _, _, err := gh.Issues.AddLabelsToIssue(ctx, owner, repo, num, t.Labels); err != nil {
				return fmt.Errorf("adding labels: %w", err)
			}
		}
	}
	if t.Assessment == "" {
		return nil
	}
	body := TriageComment(t) + marker(doc)
	if doc.Source.Task != "" {
		posted, err := hasComment(ctx, gh, owner, repo, num, marker(doc))
		if err != nil {
			return err
		}
		if posted {
			fmt.Fprintf(out, "The triage of task %s is already on %s; not commenting again\n", doc.Source.Task, doc.Target.URL)
			return nil
		}
	}
	fmt.Fprintf(out, "%s on %s:\n%s\n", doing("Commenting", "comment"), doc.Target.URL, indent(TriageComment(t)))
	if dryRun {
		return nil
	}
	if _, _, err := gh.Issues.CreateComment(ctx, owner, repo, num, &githubv39.IssueComment{Body: &body}); err != nil {
		return fmt.Errorf("posting the triage comment: %w", err)
	}
	return nil
}

// applyPlan posts the plan on its issue, once.
func applyPlan(ctx context.Context, gh *githubv39.Client, doc *Document, dryRun bool, out io.Writer) error {
	p, err := doc.PlanSpec()
	if err != nil {
		return err
	}
	owner, repo, num, isPR, err := parseItemURL(doc.Target.URL)
	if err != nil {
		return err
	}
	if isPR {
		return fmt.Errorf("a Plan targets an issue, not %s", doc.Target.URL)
	}
	if doc.Source.Task != "" {
		posted, err := hasComment(ctx, gh, owner, repo, num, marker(doc))
		if err != nil {
			return err
		}
		if posted {
			fmt.Fprintf(out, "The plan of task %s is already on %s; not commenting again\n", doc.Source.Task, doc.Target.URL)
			return nil
		}
	}
	if dryRun {
		fmt.Fprintf(out, "Would comment on %s:\n%s\n", doc.Target.URL, indent(PlanComment(p)))
		return nil
	}
	fmt.Fprintf(out, "Commenting the plan on %s\n", doc.Target.URL)
	body := PlanComment(p) + marker(doc)
	if _, _, err := gh.Issues.CreateComment(ctx, owner, repo, num, &githubv39.IssueComment{Body: &body}); err != nil {
		return fmt.Errorf("posting the plan comment: %w", err)
	}
	return nil
}

// PlanComment is the comment a plan is published as.
func PlanComment(p *Plan) string {
	return "**Implementation plan**\n\n" + strings.TrimSpace(p.Markdown)
}

// TriageComment is the comment a triage is published as.
func TriageComment(t *Triage) string {
	body := fmt.Sprintf("**Triage assessment**\n\n%s", t.Assessment)
	if len(t.Duplicates) > 0 {
		var refs []string
		for _, d := range t.Duplicates {
			refs = append(refs, fmt.Sprintf("#%d", d))
		}
		body += fmt.Sprintf("\n\nPossible duplicates: %s", strings.Join(refs, ", "))
	}
	return body
}

// marker is hidden in what apply writes, to find it again: one per task
// and kind.
func marker(doc *Document) string {
	if doc.Source.Task == "" {
		return ""
	}
	return fmt.Sprintf("\n\n<!-- factory:task-output kind=%s task=%s -->", doc.Kind, doc.Source.Task)
}

func hasComment(ctx context.Context, gh *githubv39.Client, owner, repo string, num int, mark string) (bool, error) {
	mark = strings.TrimSpace(mark)
	opts := &githubv39.IssueListCommentsOptions{ListOptions: githubv39.ListOptions{PerPage: 100}}
	for {
		comments, resp, err := gh.Issues.ListComments(ctx, owner, repo, num, opts)
		if err != nil {
			return false, fmt.Errorf("listing comments on #%d: %w", num, err)
		}
		for _, c := range comments {
			if strings.Contains(c.GetBody(), mark) {
				return true, nil
			}
		}
		if resp.NextPage == 0 {
			return false, nil
		}
		opts.Page = resp.NextPage
	}
}

func parseItemURL(raw string) (owner, repo string, num int, isPR bool, err error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", "", 0, false, fmt.Errorf("target %q: %w", raw, err)
	}
	p := strings.Split(strings.Trim(u.Path, "/"), "/")
	if u.Host != "github.com" || len(p) != 4 || (p[2] != "issues" && p[2] != "pull") {
		return "", "", 0, false, fmt.Errorf("target %q is not a GitHub issue or PR URL", raw)
	}
	num, err = strconv.Atoi(p[3])
	if err != nil || num <= 0 {
		return "", "", 0, false, fmt.Errorf("target %q has no issue or PR number", raw)
	}
	return p[0], p[1], num, p[2] == "pull", nil
}

func indent(s string) string {
	return "  " + strings.ReplaceAll(s, "\n", "\n  ")
}
