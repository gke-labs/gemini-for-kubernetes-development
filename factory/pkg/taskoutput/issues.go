package taskoutput

import (
	"context"
	"fmt"
	"io"
	"regexp"
	"strings"

	githubv39 "github.com/google/go-github/v39/github"
	"gopkg.in/yaml.v3"
)

// Issues is an Issues document's spec: issues a task found should be filed
// in the target's repository - for example the controller bugs a validation
// run's failures show (KCC's file-validate-bug recipe) - and a note for the
// target. Zero issues is a fine answer: nothing to file.
//
// Each issue has a key. open-issues files an issue once per key: one still
// open with that key (its marker in the body) gets a comment instead, so
// finding the same bug again, in a re-run or a later batch, adds to the
// issue rather than opening a duplicate.
type Issues struct {
	Issues []IssueDraft `yaml:"issues"`
	// Note is said on the target, in the comment that lists the issues
	// filed - why there are none, for one.
	Note string `yaml:"note,omitempty"`
}

// IssueDraft is one issue to file.
type IssueDraft struct {
	// Key identifies what the issue is about, across tasks:
	// "DialogflowGeneratorEvaluation:create-lro-identity".
	Key    string   `yaml:"key"`
	Title  string   `yaml:"title"`
	Body   string   `yaml:"body"`
	Labels []string `yaml:"labels,omitempty"`
}

// issueKeyRE is what an issue key may be: it goes into an HTML comment.
var issueKeyRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/-]{0,127}$`)

// maxIssues bounds one result: a run that finds more is better read by a
// person than filed.
const maxIssues = 10

// IssuesSpec decodes an Issues document's spec.
func (d *Document) IssuesSpec() (*Issues, error) {
	if d.Kind != "Issues" {
		return nil, fmt.Errorf("%s task output is not an Issues", d.Kind)
	}
	var s Issues
	if err := d.Spec.Decode(&s); err != nil {
		return nil, fmt.Errorf("Issues spec: %w", err)
	}
	return &s, validateIssues(&s)
}

func validateIssues(s *Issues) error {
	if len(s.Issues) > maxIssues {
		return fmt.Errorf("%d issues: at most %d", len(s.Issues), maxIssues)
	}
	seen := map[string]bool{}
	for i, is := range s.Issues {
		switch {
		case !issueKeyRE.MatchString(is.Key):
			return fmt.Errorf("issue %d: key %q is not letters, digits and . _ : / - (at most 128)", i+1, is.Key)
		case seen[is.Key]:
			return fmt.Errorf("issue %d: key %q twice", i+1, is.Key)
		case strings.TrimSpace(is.Title) == "":
			return fmt.Errorf("issue %d (%s): no title", i+1, is.Key)
		case strings.TrimSpace(is.Body) == "":
			return fmt.Errorf("issue %d (%s): no body", i+1, is.Key)
		}
		seen[is.Key] = true
	}
	return nil
}

func parseIssues(raw string) (any, error) {
	var out struct {
		Issues *Issues `yaml:"issues_output"`
	}
	text := CleanAgentYAML(raw, "issues_output:")
	if err := yaml.Unmarshal([]byte(text), &out); err != nil {
		return nil, err
	}
	if out.Issues == nil {
		return nil, fmt.Errorf("no issues_output: block")
	}
	for i := range out.Issues.Issues {
		is := &out.Issues.Issues[i]
		is.Key, is.Title, is.Body = strings.TrimSpace(is.Key), strings.TrimSpace(is.Title), strings.TrimSpace(is.Body)
	}
	if err := validateIssues(out.Issues); err != nil {
		return nil, err
	}
	return out.Issues, nil
}

// issueKeyMarker is hidden in a filed issue's body, to find it by its key.
func issueKeyMarker(key string) string {
	return fmt.Sprintf("<!-- factory:issue key=%s -->", key)
}

// findIssueByKey is the open issue of owner/repo whose body carries key's
// marker, among those with the draft's labels (all open issues without).
func findIssueByKey(ctx context.Context, gh *githubv39.Client, owner, repo string, draft IssueDraft) (*githubv39.Issue, error) {
	mark := issueKeyMarker(draft.Key)
	opts := &githubv39.IssueListByRepoOptions{State: "open", Labels: draft.Labels, ListOptions: githubv39.ListOptions{PerPage: 100}}
	for {
		issues, resp, err := gh.Issues.ListByRepo(ctx, owner, repo, opts)
		if err != nil {
			return nil, fmt.Errorf("listing %s/%s's open issues: %w", owner, repo, err)
		}
		for _, is := range issues {
			if !is.IsPullRequest() && strings.Contains(is.GetBody(), mark) {
				return is, nil
			}
		}
		if resp.NextPage == 0 {
			return nil, nil
		}
		opts.Page = resp.NextPage
	}
}

// applyOpenIssues files an Issues document's issues in its target's
// repository - each once per key, an open one with the key getting a
// comment instead - and then says on the target (an issue or PR) what it
// filed, and the note. Applied again, it files and says nothing twice.
func applyOpenIssues(ctx context.Context, gh *githubv39.Client, doc *Document, dryRun bool, out io.Writer) error {
	s, err := doc.IssuesSpec()
	if err != nil {
		return err
	}
	owner, repo, err := parseRepoURL(doc.Target.URL)
	if err != nil {
		return err
	}
	from := fmt.Sprintf("Found by %s", doc.Target.URL)
	if doc.Source.Task != "" {
		from += fmt.Sprintf(" (task %s)", doc.Source.Task)
	}
	var lines []string
	for _, draft := range s.Issues {
		existing, err := findIssueByKey(ctx, gh, owner, repo, draft)
		if err != nil {
			return err
		}
		if existing != nil {
			num := existing.GetNumber()
			lines = append(lines, fmt.Sprintf("- #%d (%s): already open; seen again", num, draft.Key))
			if doc.Source.Task != "" {
				posted, err := hasComment(ctx, gh, owner, repo, num, marker(doc))
				if err != nil {
					return err
				}
				if posted {
					fmt.Fprintf(out, "#%d (%s) already has task %s's comment\n", num, draft.Key, doc.Source.Task)
					continue
				}
			}
			fmt.Fprintf(out, "%s on #%d, open for %s\n", doing(dryRun, "Commenting", "comment"), num, draft.Key)
			if dryRun {
				continue
			}
			body := withSessionHint(ctx, fmt.Sprintf("Seen again. %s.\n\n%s", from, draft.Body)) + marker(doc)
			if _, _, err := gh.Issues.CreateComment(ctx, owner, repo, num, &githubv39.IssueComment{Body: &body}); err != nil {
				return fmt.Errorf("commenting on #%d: %w", num, err)
			}
			continue
		}
		fmt.Fprintf(out, "%s an issue on %s/%s for %s: %q%s\n", doing(dryRun, "Opening", "open"), owner, repo, draft.Key, draft.Title, labelNote(draft.Labels))
		if dryRun {
			lines = append(lines, fmt.Sprintf("- (new) %s: %s", draft.Key, draft.Title))
			continue
		}
		body := withSessionHint(ctx, fmt.Sprintf("%s\n\n%s.", draft.Body, from)) + "\n\n" + issueKeyMarker(draft.Key) + marker(doc)
		req := &githubv39.IssueRequest{Title: githubv39.String(draft.Title), Body: &body}
		if len(draft.Labels) > 0 {
			req.Labels = &draft.Labels
		}
		created, _, err := gh.Issues.Create(ctx, owner, repo, req)
		if err != nil {
			return fmt.Errorf("opening the issue for %s: %w", draft.Key, err)
		}
		fmt.Fprintf(out, "Opened %s\n", created.GetHTMLURL())
		lines = append(lines, fmt.Sprintf("- #%d (%s): %s", created.GetNumber(), draft.Key, draft.Title))
	}
	return sayIssuesOnTarget(ctx, gh, doc, s, lines, dryRun, out)
}

// sayIssuesOnTarget comments once on the document's target (an issue or a
// PR) with the issues filed and the note.
func sayIssuesOnTarget(ctx context.Context, gh *githubv39.Client, doc *Document, s *Issues, lines []string, dryRun bool, out io.Writer) error {
	owner, repo, num, _, err := parseItemURL(doc.Target.URL)
	if err != nil {
		return nil // a repository's: there is no item to say it on
	}
	var b strings.Builder
	switch len(lines) {
	case 0:
		b.WriteString("No issues filed.")
	default:
		b.WriteString("Issues filed:\n")
		b.WriteString(strings.Join(lines, "\n"))
	}
	if note := strings.TrimSpace(s.Note); note != "" {
		b.WriteString("\n\n" + note)
	}
	comment := b.String()
	if doc.Source.Task != "" {
		posted, err := hasComment(ctx, gh, owner, repo, num, marker(doc))
		if err != nil {
			return err
		}
		if posted {
			fmt.Fprintf(out, "Task %s already said on %s what it filed\n", doc.Source.Task, doc.Target.URL)
			return nil
		}
	}
	fmt.Fprintf(out, "%s on %s:\n%s\n", doing(dryRun, "Commenting", "comment"), doc.Target.URL, indent(comment))
	if dryRun {
		return nil
	}
	body := doc.Comment.withPrefix(withSessionHint(ctx, withAttachments(comment, doc))) + marker(doc)
	if _, _, err := gh.Issues.CreateComment(ctx, owner, repo, num, &githubv39.IssueComment{Body: &body}); err != nil {
		return fmt.Errorf("commenting on %s: %w", doc.Target.URL, err)
	}
	return nil
}

func labelNote(labels []string) string {
	if len(labels) == 0 {
		return ""
	}
	return " labelled " + strings.Join(labels, ", ")
}
