package taskoutput

import (
	"context"
	"fmt"
	"io"
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
	if d.Kind != "Change" {
		return nil, fmt.Errorf("%s task output is not a Change", d.Kind)
	}
	var c Change
	if err := d.Spec.Decode(&c); err != nil {
		return nil, fmt.Errorf("Change spec: %w", err)
	}
	if strings.TrimSpace(c.Title) == "" {
		return nil, fmt.Errorf("Change spec has no title")
	}
	return &c, nil
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
	if err := yaml.Unmarshal([]byte(CleanAgentYAML(raw, "change:")), &out); err != nil {
		return nil, err
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
	if c.Title == "" {
		return nil, fmt.Errorf("the change has no title")
	}
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
