package taskoutput

import (
	"context"
	"fmt"
	"io"
	"strings"

	githubv39 "github.com/google/go-github/v39/github"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/fanout"
)

// FanOut is a FanOut document's spec: a fan-out spec for its issue, in the
// standard markdown form (## Fan-out, ## Task, ## Items, ## Finally), which
// post-spec posts as the bot's spec comment.
type FanOut struct {
	Markdown string `yaml:"markdown"`
}

// FanOutSpec decodes a FanOut document's spec. A spec edited into one that
// does not parse is refused here, before it is posted.
func (d *Document) FanOutSpec() (*FanOut, error) {
	if d.Kind != "FanOut" {
		return nil, fmt.Errorf("%s task output is not a FanOut", d.Kind)
	}
	var f FanOut
	if err := d.Spec.Decode(&f); err != nil {
		return nil, fmt.Errorf("FanOut spec: %w", err)
	}
	if err := checkFanOut(f.Markdown); err != nil {
		return nil, err
	}
	return &f, nil
}

func parseFanOut(raw string) (any, error) {
	md := strings.TrimSpace(strings.Replace(CleanAgentMarkdown(raw), fanout.SpecMarker, "", 1))
	if err := checkFanOut(md); err != nil {
		return nil, err
	}
	return &FanOut{Markdown: md}, nil
}

// checkFanOut is a spec the fan-out can run: its headings, and Parse's
// checks. Items from a file are read when the fan-out runs, not here.
func checkFanOut(md string) error {
	if !fanout.HasSpecHeadings(md) {
		return fmt.Errorf("the spec has no ## Task with ## Items or ## Fan-out")
	}
	if _, err := fanout.Parse(md); err != nil {
		return fmt.Errorf("the spec does not parse: %w", err)
	}
	return nil
}

// FanOutComment is the spec comment a FanOut is posted as.
func FanOutComment(f *FanOut) string {
	return fanout.SpecMarker + "\n" + strings.TrimSpace(f.Markdown)
}

// fanOutLabels are the labels posting a spec adds to an issue: its fan-out
// label, so the watch daemon carries the fan-out, unless it has one; and the
// stop label, so it does not start before maintainers have read the spec.
// The prefix is the issue's <prefix>/fanout label's, else overseer, which
// the watch daemon honours whatever its trigger label.
func fanOutLabels(labels []*githubv39.Label) []string {
	for _, l := range labels {
		name := l.GetName()
		if i := strings.LastIndex(name, "/"); i > 0 && strings.EqualFold(name[i+1:], "fanout") {
			return []string{name[:i] + "/stop"}
		}
	}
	return []string{"overseer/fanout", "overseer/stop"}
}

// applyPostSpec posts a FanOut as the spec comment on its issue, or edits
// the caller's spec comment there. It adds the fan-out and stop labels
// first (fanOutLabels): the watch daemon carries the fan-out, but does not
// start it before maintainers have read the spec. Once this
// task's spec is posted, applying again does nothing: a maintainer may have
// edited the comment, or removed the stop label to start.
func applyPostSpec(ctx context.Context, gh *githubv39.Client, doc *Document, dryRun bool, out io.Writer) error {
	f, err := doc.FanOutSpec()
	if err != nil {
		return err
	}
	owner, repo, num, err := issueTarget(doc)
	if err != nil {
		return err
	}
	issue, _, err := gh.Issues.Get(ctx, owner, repo, num)
	if err != nil {
		return fmt.Errorf("reading #%d: %w", num, err)
	}
	me, _, err := gh.Users.Get(ctx, "")
	if err != nil {
		return fmt.Errorf("reading the token's user: %w", err)
	}
	mark := strings.TrimSpace(marker(doc))
	var mine *githubv39.IssueComment
	opts := &githubv39.IssueListCommentsOptions{ListOptions: githubv39.ListOptions{PerPage: 100}}
	for {
		comments, resp, err := gh.Issues.ListComments(ctx, owner, repo, num, opts)
		if err != nil {
			return fmt.Errorf("listing comments on #%d: %w", num, err)
		}
		for _, c := range comments {
			body := c.GetBody()
			if !strings.Contains(body, fanout.SpecMarker) || !strings.EqualFold(c.GetUser().GetLogin(), me.GetLogin()) {
				continue
			}
			if mark != "" && strings.Contains(body, mark) {
				fmt.Fprintf(out, "The spec of task %s is already on %s; not posting it again\n", doc.Source.Task, doc.Target.URL)
				return nil
			}
			mine = c
		}
		if resp.NextPage == 0 {
			break
		}
		opts.Page = resp.NextPage
	}

	labels := fanOutLabels(issue.Labels)
	fmt.Fprintf(out, "%s %s to %s\n", doing(dryRun, "Adding", "add"), strings.Join(labels, ", "), doc.Target.URL)
	if !dryRun {
		if _, _, err := gh.Issues.AddLabelsToIssue(ctx, owner, repo, num, labels); err != nil {
			return fmt.Errorf("adding %s: %w", strings.Join(labels, ", "), err)
		}
	}
	comment := FanOutComment(f)
	body := comment + marker(doc)
	if mine != nil {
		fmt.Fprintf(out, "%s the spec comment %s:\n%s\n", doing(dryRun, "Editing", "edit"), mine.GetHTMLURL(), indent(comment))
		if dryRun {
			return nil
		}
		if _, _, err := gh.Issues.EditComment(ctx, owner, repo, mine.GetID(), &githubv39.IssueComment{Body: &body}); err != nil {
			return fmt.Errorf("editing the spec comment: %w", err)
		}
		return nil
	}
	fmt.Fprintf(out, "%s the spec on %s:\n%s\n", doing(dryRun, "Posting", "post"), doc.Target.URL, indent(comment))
	if dryRun {
		return nil
	}
	if _, _, err := gh.Issues.CreateComment(ctx, owner, repo, num, &githubv39.IssueComment{Body: &body}); err != nil {
		return fmt.Errorf("posting the spec comment: %w", err)
	}
	return nil
}
