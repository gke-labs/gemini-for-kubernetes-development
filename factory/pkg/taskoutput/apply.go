package taskoutput

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	githubv39 "github.com/google/go-github/v39/github"
)

type applyFunc func(ctx context.Context, gh *githubv39.Client, doc *Document, dryRun bool, out io.Writer) error

// Apply does what a document asks of GitHub, with gh's token: each apply
// action it offers, in order. With dryRun it says what it would do and
// does nothing.
func Apply(ctx context.Context, gh *githubv39.Client, doc *Document, dryRun bool, out io.Writer) error {
	if !Known(doc.Kind) {
		return fmt.Errorf("cannot apply task output kind %q", doc.Kind)
	}
	for _, a := range doc.Offered() {
		if v := verbs[a.Verb]; v.class == ClassApply {
			if err := v.apply(ctx, gh, doc, dryRun, out); err != nil {
				return err
			}
		}
	}
	return nil
}

// ApplyAction does one apply action the document offers, or one its offer
// of another allows (submit-review, by post-review).
func ApplyAction(ctx context.Context, gh *githubv39.Client, doc *Document, verbName string, dryRun bool, out io.Writer) error {
	v := verbs[verbName]
	offered := verbName
	if v.offeredAs != "" {
		offered = v.offeredAs
	}
	if _, err := doc.Offer(offered, ""); err != nil {
		return err
	}
	if v.class != ClassApply {
		return fmt.Errorf("%s is a %s action, not one apply writes", verbName, v.class)
	}
	return v.apply(ctx, gh, doc, dryRun, out)
}

// issueTarget is the document's target, which must be an issue.
func issueTarget(doc *Document) (owner, repo string, num int, err error) {
	owner, repo, num, isPR, err := parseItemURL(doc.Target.URL)
	if err != nil {
		return "", "", 0, err
	}
	if isPR {
		return "", "", 0, fmt.Errorf("a %s targets an issue, not %s", doc.Kind, doc.Target.URL)
	}
	return owner, repo, num, nil
}

// itemTarget is the issue or pull request doc's result is commented and
// labelled on (issue comments and labels are a PR's too).
func itemTarget(doc *Document) (owner, repo string, num int, err error) {
	owner, repo, num, _, err = parseItemURL(doc.Target.URL)
	return owner, repo, num, err
}

func doing(dryRun bool, present, conditional string) string {
	if dryRun {
		return "Would " + conditional
	}
	return present
}

// applyLabels adds a Triage's labels to its issue or PR.
func applyLabels(ctx context.Context, gh *githubv39.Client, doc *Document, dryRun bool, out io.Writer) error {
	t, err := doc.TriageSpec()
	if err != nil {
		return err
	}
	owner, repo, num, err := itemTarget(doc)
	if err != nil {
		return err
	}
	if len(t.Labels) == 0 {
		return nil
	}
	fmt.Fprintf(out, "%s labels %v to %s\n", doing(dryRun, "Adding", "add"), t.Labels, doc.Target.URL)
	if dryRun {
		return nil
	}
	if _, _, err := gh.Issues.AddLabelsToIssue(ctx, owner, repo, num, t.Labels); err != nil {
		return fmt.Errorf("adding labels: %w", err)
	}
	return nil
}

// applyComment comments the result on its issue (a Triage's or a
// Summary's, or PR), once: a Triage's assessment, a Plan's plan, a
// Summary.
func applyComment(ctx context.Context, gh *githubv39.Client, doc *Document, dryRun bool, out io.Writer) error {
	var comment, what string
	switch doc.Kind {
	case "Triage":
		t, err := doc.TriageSpec()
		if err != nil {
			return err
		}
		if t.Assessment == "" {
			return nil
		}
		comment, what = TriageComment(t), "triage"
	case "Plan":
		p, err := doc.PlanSpec()
		if err != nil {
			return err
		}
		comment, what = PlanComment(p), "plan"
	case "Summary":
		sum, err := doc.SummarySpec()
		if err != nil {
			return err
		}
		comment, what = SummaryComment(sum), "summary"
	default:
		return fmt.Errorf("cannot comment a %s", doc.Kind)
	}
	// A plan is an issue's (fix follows it); a triage or a summary may be
	// a pull request's too.
	target := itemTarget
	if doc.Kind == "Plan" {
		target = issueTarget
	}
	owner, repo, num, err := target(doc)
	if err != nil {
		return err
	}
	if doc.Comment != nil && doc.Comment.Key != "" {
		return applyKeyedComment(ctx, gh, doc, owner, repo, num, comment, what, dryRun, out)
	}
	if doc.Source.Task != "" {
		posted, err := hasComment(ctx, gh, owner, repo, num, marker(doc))
		if err != nil {
			return err
		}
		if posted {
			fmt.Fprintf(out, "The %s of task %s is already on %s; not commenting again\n", what, doc.Source.Task, doc.Target.URL)
			return nil
		}
	}
	fmt.Fprintf(out, "%s on %s:\n%s\n", doing(dryRun, "Commenting", "comment"), doc.Target.URL, indent(comment))
	if dryRun {
		return nil
	}
	body := withSessionHint(ctx, comment) + marker(doc)
	if _, _, err := gh.Issues.CreateComment(ctx, owner, repo, num, &githubv39.IssueComment{Body: &body}); err != nil {
		return fmt.Errorf("posting the %s comment: %w", what, err)
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
	if t.Priority != "" {
		body += fmt.Sprintf("\n\nPriority: %s", t.Priority)
	}
	if len(t.Labels) > 0 {
		body += "\n\nSuggested labels: `" + strings.Join(t.Labels, "`, `") + "`"
	}
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

// keyMarkerRE is a keyed comment's marker: its key and version.
var keyMarkerRE = regexp.MustCompile(`<!-- factory:comment key=([a-z][a-z0-9-]*) v=(\d+) -->`)

func keyMarker(key string, version int) string {
	return fmt.Sprintf("\n\n<!-- factory:comment key=%s v=%d -->", key, version)
}

// KeyedComment is the newest comment on the item carrying key's marker,
// by login ("" for anyone's), and its version; nil when there is none.
func KeyedComment(comments []*githubv39.IssueComment, login, key string) (*githubv39.IssueComment, int) {
	var found *githubv39.IssueComment
	version := 0
	for _, c := range comments {
		if login != "" && !strings.EqualFold(c.GetUser().GetLogin(), login) {
			continue
		}
		for _, m := range keyMarkerRE.FindAllStringSubmatch(c.GetBody(), -1) {
			if m[1] != key {
				continue
			}
			if v, _ := strconv.Atoi(m[2]); found == nil || !c.GetCreatedAt().Before(found.GetCreatedAt()) {
				found, version = c, v
			}
		}
	}
	return found, version
}

// applyKeyedComment writes comment as the item's one comment of doc's
// key: posted the first time, edited by every later task's result, its
// version counted up in the marker and on its heading line. The same
// task's result applied again writes nothing.
func applyKeyedComment(ctx context.Context, gh *githubv39.Client, doc *Document, owner, repo string, num int, comment, what string, dryRun bool, out io.Writer) error {
	key := doc.Comment.Key
	me, _, err := gh.Users.Get(ctx, "")
	if err != nil {
		return fmt.Errorf("reading who writes the %s comment: %w", what, err)
	}
	var comments []*githubv39.IssueComment
	opts := &githubv39.IssueListCommentsOptions{ListOptions: githubv39.ListOptions{PerPage: 100}}
	for {
		page, resp, err := gh.Issues.ListComments(ctx, owner, repo, num, opts)
		if err != nil {
			return fmt.Errorf("listing comments on #%d: %w", num, err)
		}
		comments = append(comments, page...)
		if resp == nil || resp.NextPage == 0 {
			break
		}
		opts.Page = resp.NextPage
	}
	// The comment to edit is the writer's own: a bot may edit only its
	// own (the watch runs the next task as the comment's author where it
	// can). Another's - its author left the pool - is followed by a new
	// one, numbered on.
	if newest, _ := KeyedComment(comments, "", key); newest != nil && doc.Source.Task != "" && strings.Contains(newest.GetBody(), strings.TrimSpace(marker(doc))) {
		fmt.Fprintf(out, "The %s of task %s is already on %s; not commenting again\n", what, doc.Source.Task, doc.Target.URL)
		return nil
	}
	existing, version := KeyedComment(comments, me.GetLogin(), key)
	if existing == nil {
		_, version = KeyedComment(comments, "", key)
	}
	version++
	heading, rest, _ := strings.Cut(comment, "\n")
	body := heading + fmt.Sprintf(" · v%d · updated %s", version, time.Now().UTC().Format("2006-01-02 15:04 UTC"))
	if rest != "" {
		body += "\n" + rest
	}
	body = withSessionHint(ctx, body) + keyMarker(key, version) + marker(doc)
	if existing == nil {
		fmt.Fprintf(out, "%s the %s (v%d) on %s:\n%s\n", doing(dryRun, "Commenting", "comment"), what, version, doc.Target.URL, indent(comment))
		if dryRun {
			return nil
		}
		if _, _, err := gh.Issues.CreateComment(ctx, owner, repo, num, &githubv39.IssueComment{Body: &body}); err != nil {
			return fmt.Errorf("posting the %s comment: %w", what, err)
		}
		return nil
	}
	fmt.Fprintf(out, "%s the %s comment on %s to v%d:\n%s\n", doing(dryRun, "Updating", "update"), what, doc.Target.URL, version, indent(comment))
	if dryRun {
		return nil
	}
	if _, _, err := gh.Issues.EditComment(ctx, owner, repo, existing.GetID(), &githubv39.IssueComment{Body: &body}); err != nil {
		fmt.Fprintf(out, "Updating the %s comment failed (%v); posting v%d as a new one\n", what, err, version)
		if _, _, err := gh.Issues.CreateComment(ctx, owner, repo, num, &githubv39.IssueComment{Body: &body}); err != nil {
			return fmt.Errorf("posting the %s comment: %w", what, err)
		}
	}
	return nil
}
