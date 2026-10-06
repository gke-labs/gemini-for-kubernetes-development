package taskoutput

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"slices"
	"strings"

	githubv39 "github.com/google/go-github/v39/github"
	"gopkg.in/yaml.v3"
)

// Change is a Change document's spec: commits a fix pushed to a branch of
// the member's fork, and the PR they are for. The agent writes the title,
// body and, for a revise, replies and report; Fork, Branch and Base are
// the push step's (SetPushed), never the agent's.
type Change struct {
	// Fork is the repository the branch is on, owner/name: the member's
	// fork of the target's repository.
	Fork   string `yaml:"fork,omitempty"`
	Branch string `yaml:"branch,omitempty"`
	// Base is the upstream commit the branch started from.
	Base   string   `yaml:"base,omitempty"`
	Title  string   `yaml:"title"`
	Body   string   `yaml:"body"`
	Labels []string `yaml:"labels,omitempty"`
	// Replies answer review and issue comments (address-comments).
	Replies []Reply `yaml:"replies,omitempty"`
	// Report is a PR comment (fix-ci).
	Report string `yaml:"report,omitempty"`
}

// Reply is an answer to one comment on the PR.
type Reply struct {
	InReplyTo int64  `yaml:"inReplyTo"`
	Body      string `yaml:"body"`
}

// Pushed is what the push step recorded (push.json in the task
// directory): where the commits went, and what they are.
type Pushed struct {
	Fork   string `json:"fork"`
	Branch string `json:"branch"`
	Base   string `json:"base"`
	Head   string `json:"head"`
}

// PushedFile is the task-directory file the push step records Pushed in.
const PushedFile = "push.json"

// ChangeSpec decodes a Change document's spec.
func (d *Document) ChangeSpec() (*Change, error) {
	c, err := d.changeSpec()
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(c.Title) == "" {
		return nil, fmt.Errorf("Change spec has no title")
	}
	return c, nil
}

func (d *Document) changeSpec() (*Change, error) {
	if d.Kind != "Change" {
		return nil, fmt.Errorf("%s task output is not a Change", d.Kind)
	}
	var c Change
	if err := d.Spec.Decode(&c); err != nil {
		return nil, fmt.Errorf("Change spec: %w", err)
	}
	return &c, nil
}

// KeepTitle gives a Change the title and body the PR had, each where the
// agent wrote none: a revise's agent writes them only to change them.
func (d *Document) KeepTitle(title, body string) error {
	c, err := d.changeSpec()
	if err != nil {
		return err
	}
	if c.Title == "" {
		c.Title = strings.TrimSpace(title)
	}
	if c.Body == "" {
		c.Body = strings.TrimSpace(body)
	}
	return d.setSpec(c)
}

// SetPushed puts what the push step recorded on a Change: the fork, branch
// and base in its spec, the head as its commit. Whatever the agent wrote
// for them is replaced.
func (d *Document) SetPushed(p Pushed) error {
	if p.Fork == "" || p.Branch == "" || p.Head == "" {
		return fmt.Errorf("the push step recorded no fork, branch or head")
	}
	c, err := d.ChangeSpec()
	if err != nil {
		return err
	}
	c.Fork, c.Branch, c.Base = p.Fork, p.Branch, p.Base
	d.Target.Commit = p.Head
	return d.setSpec(c)
}

// AddLabels adds labels to a Change's, each once.
func (d *Document) AddLabels(labels []string) error {
	if len(labels) == 0 {
		return nil
	}
	c, err := d.ChangeSpec()
	if err != nil {
		return err
	}
	for _, l := range labels {
		if !slices.Contains(c.Labels, l) {
			c.Labels = append(c.Labels, l)
		}
	}
	return d.setSpec(c)
}

func (d *Document) setSpec(spec any) error {
	var n yaml.Node
	if err := n.Encode(spec); err != nil {
		return err
	}
	d.Spec = n
	return nil
}

func parseChange(raw string) (any, error) {
	var out struct {
		Change *Change `yaml:"change"`
	}
	clean := CleanAgentYAML(raw, "change:")
	if err := yaml.Unmarshal([]byte(clean), &out); err != nil {
		// A title in the repository's "area: subject" style, written
		// unquoted, is not YAML. Quote it and try once more.
		if yaml.Unmarshal([]byte(quotePlainTitle(clean)), &out) != nil {
			return nil, err
		}
	}
	c := out.Change
	if c == nil {
		return nil, fmt.Errorf("no change: block")
	}
	// The push step's, not the agent's.
	c.Fork, c.Branch, c.Base = "", "", ""
	c.Title = strings.TrimSpace(c.Title)
	c.Body = strings.TrimSpace(c.Body)
	c.Report = strings.TrimSpace(c.Report)
	var labels []string
	for _, l := range c.Labels {
		if l = strings.TrimSpace(l); l != "" {
			labels = append(labels, l)
		}
	}
	c.Labels = labels
	var replies []Reply
	for _, r := range c.Replies {
		if r.Body = strings.TrimSpace(r.Body); r.Body != "" && r.InReplyTo > 0 {
			replies = append(replies, r)
		}
	}
	c.Replies = replies
	// A revise's may have no title: it keeps the PR's (KeepTitle), and
	// ChangeSpec refuses a Change that still has none.
	return c, nil
}

// forkRE is a fork as Change names it: owner/name.
var forkRE = regexp.MustCompile(`^[A-Za-z0-9-]+/[A-Za-z0-9._-]+$`)

// applyOpenPR opens a Change's branch as a draft PR on its repository's
// default branch, with the caller's token, and points the document's
// target at it, for the caller to alias the sandbox to. A branch that
// already has an open PR gets that one, and nothing is opened: a retried
// apply never opens two.
func applyOpenPR(ctx context.Context, gh *githubv39.Client, doc *Document, dryRun bool, out io.Writer) error {
	c, err := doc.ChangeSpec()
	if err != nil {
		return err
	}
	owner, repo, _, _, err := parseItemURL(doc.Target.URL)
	if err != nil {
		return err
	}
	if c.Fork == "" || c.Branch == "" {
		return fmt.Errorf("the Change names no branch to open (spec.fork, spec.branch): nothing was pushed")
	}
	if !forkRE.MatchString(c.Fork) {
		return fmt.Errorf("spec.fork %q is not owner/name", c.Fork)
	}
	forkOwner, _, _ := strings.Cut(c.Fork, "/")
	user, _, err := gh.Users.Get(ctx, "")
	if err != nil {
		return fmt.Errorf("resolving the token's GitHub login: %w", err)
	}
	if !strings.EqualFold(user.GetLogin(), forkOwner) {
		return fmt.Errorf("the branch is on %s, not on your fork (you are %s); not opening a PR from it", c.Fork, user.GetLogin())
	}
	head := forkOwner + ":" + c.Branch
	open, _, err := gh.PullRequests.List(ctx, owner, repo, &githubv39.PullRequestListOptions{State: "open", Head: head})
	if err != nil {
		return fmt.Errorf("looking for an open PR from %s: %w", head, err)
	}
	if len(open) > 0 {
		fmt.Fprintf(out, "%s already has an open PR: %s; not opening another\n", head, open[0].GetHTMLURL())
		doc.Target.URL = open[0].GetHTMLURL()
		return nil
	}
	r, _, err := gh.Repositories.Get(ctx, owner, repo)
	if err != nil {
		return fmt.Errorf("fetching %s/%s: %w", owner, repo, err)
	}
	base := r.GetDefaultBranch()
	fmt.Fprintf(out, "%s a draft PR on %s/%s from %s into %s: %q\n", doing(dryRun, "Opening", "open"), owner, repo, head, base, c.Title)
	if len(c.Labels) > 0 {
		fmt.Fprintf(out, "%s labels %s\n", doing(dryRun, "Adding", "add"), strings.Join(c.Labels, ", "))
	}
	if dryRun {
		return nil
	}
	pr, _, err := gh.PullRequests.Create(ctx, owner, repo, &githubv39.NewPullRequest{
		Title:               githubv39.String(c.Title),
		Head:                githubv39.String(head),
		Base:                githubv39.String(base),
		Body:                githubv39.String(c.Body + marker(doc)),
		Draft:               githubv39.Bool(true),
		MaintainerCanModify: githubv39.Bool(true),
	})
	if err != nil {
		return fmt.Errorf("opening the PR: %w", err)
	}
	fmt.Fprintf(out, "Opened %s\n", pr.GetHTMLURL())
	doc.Target.URL = pr.GetHTMLURL()
	if len(c.Labels) > 0 {
		// The PR is open either way; a label missing from the repository is
		// its configuration, not a reason to fail.
		if _, _, err := gh.Issues.AddLabelsToIssue(ctx, owner, repo, pr.GetNumber(), c.Labels); err != nil {
			fmt.Fprintf(out, "Warning: could not label %s: %v\n", pr.GetHTMLURL(), err)
		}
	}
	return nil
}

// applyPostReplies posts a Change's replies and report on its PR, with
// the caller's token: a reply to a review comment in that comment's
// thread, a reply to a conversation comment as a new comment quoting and
// linking it (GitHub has no threads there), and the report as a PR
// comment. Each carries a marker of the task and the comment it answers,
// so a retried apply posts nothing twice. A Change with neither — a
// start's — posts nothing.
func applyPostReplies(ctx context.Context, gh *githubv39.Client, doc *Document, dryRun bool, out io.Writer) error {
	c, err := doc.ChangeSpec()
	if err != nil {
		return err
	}
	if len(c.Replies) == 0 && c.Report == "" {
		fmt.Fprintln(out, "The Change has no replies or report; nothing to post")
		return nil
	}
	owner, repo, num, err := prTarget(doc)
	if err != nil {
		return fmt.Errorf("%w: open the PR first (open-pr)", err)
	}
	for _, r := range c.Replies {
		if err := postReply(ctx, gh, doc, owner, repo, num, r, dryRun, out); err != nil {
			return fmt.Errorf("replying to comment %d: %w", r.InReplyTo, err)
		}
	}
	if c.Report == "" {
		return nil
	}
	if doc.Source.Task != "" {
		posted, err := hasComment(ctx, gh, owner, repo, num, marker(doc))
		if err != nil {
			return err
		}
		if posted {
			fmt.Fprintf(out, "The report of task %s is already on %s; not commenting again\n", doc.Source.Task, doc.Target.URL)
			return nil
		}
	}
	fmt.Fprintf(out, "%s the report on %s:\n%s\n", doing(dryRun, "Commenting", "comment"), doc.Target.URL, indent(c.Report))
	if dryRun {
		return nil
	}
	body := c.Report + marker(doc)
	if _, _, err := gh.Issues.CreateComment(ctx, owner, repo, num, &githubv39.IssueComment{Body: &body}); err != nil {
		return fmt.Errorf("commenting the report on #%d: %w", num, err)
	}
	return nil
}

// replyMarker marks a reply by its task and the comment it answers.
func replyMarker(doc *Document, inReplyTo int64) string {
	if doc.Source.Task == "" {
		return ""
	}
	return fmt.Sprintf("\n\n<!-- factory:task-output kind=%s task=%s reply=%d -->", doc.Kind, doc.Source.Task, inReplyTo)
}

// postReply answers one comment on PR num, which it must be on: a review
// comment in its thread, a conversation comment with a new comment.
func postReply(ctx context.Context, gh *githubv39.Client, doc *Document, owner, repo string, num int, r Reply, dryRun bool, out io.Writer) error {
	mark := replyMarker(doc, r.InReplyTo)
	rc, resp, err := gh.PullRequests.GetComment(ctx, owner, repo, r.InReplyTo)
	switch {
	case err == nil:
		if !strings.HasSuffix(rc.GetPullRequestURL(), fmt.Sprintf("/pulls/%d", num)) {
			return fmt.Errorf("it is not on %s", doc.Target.URL)
		}
		if mark != "" {
			posted, err := hasReviewComment(ctx, gh, owner, repo, num, mark)
			if err != nil {
				return err
			}
			if posted {
				fmt.Fprintf(out, "Task %s's reply to %s is already posted; not posting it again\n", doc.Source.Task, rc.GetHTMLURL())
				return nil
			}
		}
		fmt.Fprintf(out, "%s in the thread of %s:\n%s\n", doing(dryRun, "Replying", "reply"), rc.GetHTMLURL(), indent(r.Body))
		if dryRun {
			return nil
		}
		_, _, err := gh.PullRequests.CreateCommentInReplyTo(ctx, owner, repo, num, r.Body+mark, r.InReplyTo)
		return err
	case resp == nil || resp.StatusCode != http.StatusNotFound:
		return err
	}
	ic, _, err := gh.Issues.GetComment(ctx, owner, repo, r.InReplyTo)
	if err != nil {
		return fmt.Errorf("it is neither a review comment nor a comment on %s: %w", doc.Target.URL, err)
	}
	if !strings.HasSuffix(ic.GetIssueURL(), fmt.Sprintf("/issues/%d", num)) {
		return fmt.Errorf("it is not on %s", doc.Target.URL)
	}
	if mark != "" {
		posted, err := hasComment(ctx, gh, owner, repo, num, mark)
		if err != nil {
			return err
		}
		if posted {
			fmt.Fprintf(out, "Task %s's reply to %s is already posted; not posting it again\n", doc.Source.Task, ic.GetHTMLURL())
			return nil
		}
	}
	fmt.Fprintf(out, "%s %s with a comment on %s:\n%s\n", doing(dryRun, "Answering", "answer"), ic.GetHTMLURL(), doc.Target.URL, indent(r.Body))
	if dryRun {
		return nil
	}
	body := quoteComment(ic) + r.Body + mark
	_, _, err = gh.Issues.CreateComment(ctx, owner, repo, num, &githubv39.IssueComment{Body: &body})
	return err
}

// quoteComment heads a reply to a conversation comment: the start of the
// comment, quoted, and a link to it.
func quoteComment(ic *githubv39.IssueComment) string {
	lines := strings.Split(strings.TrimSpace(ic.GetBody()), "\n")
	if len(lines) > 3 {
		lines = append(lines[:3], "…")
	}
	var b strings.Builder
	for _, l := range lines {
		b.WriteString("> " + l + "\n")
	}
	fmt.Fprintf(&b, "\n[In reply to @%s](%s)\n\n", ic.GetUser().GetLogin(), ic.GetHTMLURL())
	return b.String()
}

func hasReviewComment(ctx context.Context, gh *githubv39.Client, owner, repo string, num int, mark string) (bool, error) {
	mark = strings.TrimSpace(mark)
	opts := &githubv39.PullRequestListCommentsOptions{ListOptions: githubv39.ListOptions{PerPage: 100}}
	for {
		comments, resp, err := gh.PullRequests.ListComments(ctx, owner, repo, num, opts)
		if err != nil {
			return false, fmt.Errorf("listing review comments on #%d: %w", num, err)
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

// plainTitle is a title: line whose value is a plain scalar: not quoted,
// not a block (| or >), not a flow collection, not empty.
var plainTitle = regexp.MustCompile(`^(\s*)title:[ \t]+([^'"|>\[{\s].*?)[ \t]*$`)

// quotePlainTitle double-quotes the value of the change's own plain
// title: line, the one indented as the first key under change:. A
// "title:" further in is a block's text, such as the body's.
func quotePlainTitle(s string) string {
	lines := strings.Split(s, "\n")
	keyIndent := ""
	for i, l := range lines {
		if strings.TrimSpace(l) != "" && keyIndent == "" && i > 0 {
			keyIndent = l[:len(l)-len(strings.TrimLeft(l, " \t"))]
		}
		m := plainTitle.FindStringSubmatch(l)
		if m == nil || m[1] != keyIndent || keyIndent == "" {
			continue
		}
		quoted, _ := json.Marshal(m[2])
		lines[i] = m[1] + "title: " + string(quoted)
	}
	return strings.Join(lines, "\n")
}
