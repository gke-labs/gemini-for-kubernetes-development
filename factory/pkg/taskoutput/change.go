package taskoutput

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"time"

	githubv39 "github.com/google/go-github/v39/github"
	"gopkg.in/yaml.v3"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/commands/watch/conventions"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/commands/watch/feedback"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/github"
)

// Change is a Change document's spec: commits a fix pushed to a branch of
// the member's fork, and the PR they are for. The agent writes the title,
// body and, on a PR (care's), replies and report; Fork, Branch and Base are
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
	// Draft is whether open-pr opens the PR as a draft; unset, it does.
	// The task's draft input sets it (fillChange).
	Draft *bool `yaml:"draft,omitempty"`
	// Adopts is the PR the Change adopts (the adopt recipes): once open-pr
	// has opened the Change's PR, it comments on that one with a link,
	// and closes it when CloseAdopted is set. The recipe writes both, not
	// an agent.
	Adopts       string `yaml:"adopts,omitempty"`
	CloseAdopted bool   `yaml:"closeAdopted,omitempty"`
	// Replies answer review and issue comments (address-comments).
	Replies []Reply `yaml:"replies,omitempty"`
	// Report is a PR comment (fix-ci).
	Report string `yaml:"report,omitempty"`
	// Comment is why a fix changed nothing (the issue is already fixed,
	// is not a bug, or needs a human's decision), when it made no commit:
	// open-pr posts it on the issue instead of opening a PR.
	Comment string `yaml:"comment,omitempty"`
	// Rerun asks for checks that failed through no fault of the change (a
	// flake, an outage) to run again, as post-replies applies them: the
	// agent never reruns anything itself.
	Rerun []Rerun `yaml:"rerun,omitempty"`
	// Rewrites is what of an open PR's title and body the agent rewrote
	// ("title", "body"), which post-replies puts on the PR; KeepTitle
	// fills the rest from the PR, so Title and Body alone do not say.
	Rewrites []string `yaml:"rewrites,omitempty"`
	// Was is a hash of the title and body the PR had when the task
	// started (prTextHash): post-replies rewrites neither if the PR's have
	// changed since, rather than overwrite someone's edit with text written
	// against the old ones.
	Was string `yaml:"was,omitempty"`
	// Feedback is what the task was handed to address (address-comments'
	// comments.json): factory's, never the agent's. post-replies marks each
	// resolved once the replies are posted.
	Feedback []FeedbackRef `yaml:"feedback,omitempty"`
}

// FeedbackRef names one piece of PR feedback: a conversation comment, a
// review body (reached by node ID) or an inline review comment.
type FeedbackRef struct {
	Kind   string `yaml:"kind" json:"kind"`
	ID     int64  `yaml:"id" json:"id"`
	NodeID string `yaml:"nodeId,omitempty" json:"nodeId,omitempty"`
}

// FeedbackFile is the task-directory file address-comments' feedback is
// handed in: a JSON list of feedback.Item.
const FeedbackFile = "comments.json"

// SetFeedback puts on a Change the feedback its task was handed.
func (d *Document) SetFeedback(refs []FeedbackRef) error {
	c, err := d.changeSpec()
	if err != nil {
		return err
	}
	c.Feedback = refs
	return d.setSpec(c)
}

// prTextHash is a PR's title and body, as compared across a task: trimmed,
// with the factory's markers and session hint out of the body (prText).
func prTextHash(title, body string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(title) + "\x00" + prText(body)))
	return hex.EncodeToString(sum[:])
}

// factoryMarkerRE is the factory's HTML-comment markers in a PR body
// (open-pr's, an adoption's).
var factoryMarkerRE = regexp.MustCompile(`<!-- factory:[^>]*-->`)

// Rerun is one check to run again: a GitHub Actions run, whose failed
// jobs are rerun, or a Prow command commented on the PR.
type Rerun struct {
	// Run is a GitHub Actions run of the PR's head.
	Run int64 `yaml:"run,omitempty"`
	// Retest is "/retest" or "/test <job>".
	Retest string `yaml:"retest,omitempty"`
}

// retestRE is the Prow commands a Rerun may comment, and nothing more.
var retestRE = regexp.MustCompile(`^/(retest|test [A-Za-z0-9._-]+)$`)

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
	// Unchanged is a fix's start that made no commit: nothing was pushed,
	// and the Change's Comment says why.
	Unchanged bool `json:"unchanged,omitempty"`
}

// PushedFile is the task-directory file the push step records Pushed in.
const PushedFile = "push.json"

// ChangeSpec decodes a Change document's spec.
func (d *Document) ChangeSpec() (*Change, error) {
	c, err := d.changeSpec()
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(c.Title) == "" && c.Comment == "" {
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
// agent wrote none: an agent on a PR (care's) writes them only to change
// them. What it did write, and differently, is a rewrite (Rewrites), and
// Was records the PR's text it was written against.
func (d *Document) KeepTitle(title, body string) error {
	c, err := d.changeSpec()
	if err != nil {
		return err
	}
	title, body = strings.TrimSpace(title), strings.TrimSpace(body)
	c.Rewrites = nil
	if c.Title == "" {
		c.Title = title
	} else if c.Title != title {
		c.Rewrites = append(c.Rewrites, "title")
	}
	if c.Body == "" {
		c.Body = body
	} else if prText(c.Body) != prText(body) {
		c.Rewrites = append(c.Rewrites, "body")
	}
	c.Was = prTextHash(title, body)
	return d.setSpec(c)
}

// SetPushed puts what the push step recorded on a Change: the fork, branch
// and base in its spec, the head as its commit. Whatever the agent wrote
// for them is replaced.
func (d *Document) SetPushed(p Pushed) error {
	if p.Unchanged {
		c, err := d.ChangeSpec()
		if err != nil {
			return err
		}
		if c.Comment == "" {
			return fmt.Errorf("the fix made no change, and its Change says nothing about why (spec.comment)")
		}
		c.Fork, c.Branch, c.Base = "", "", p.Base
		return d.setSpec(c)
	}
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

// SetDraft says whether open-pr opens a Change's PR as a draft.
func (d *Document) SetDraft(draft bool) error {
	c, err := d.changeSpec()
	if err != nil {
		return err
	}
	c.Draft = &draft
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
	// The push step's and factory's, not the agent's.
	c.Fork, c.Branch, c.Base = "", "", ""
	c.Feedback = nil
	c.Title = strings.TrimSpace(c.Title)
	c.Body = strings.TrimSpace(c.Body)
	c.Report = strings.TrimSpace(c.Report)
	c.Comment = strings.TrimSpace(c.Comment)
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
	var reruns []Rerun
	for _, r := range c.Rerun {
		if r.Retest = strings.TrimSpace(r.Retest); r.Run > 0 || r.Retest != "" {
			reruns = append(reruns, r)
		}
	}
	c.Rerun = reruns
	// One on a PR may have no title: it keeps the PR's (KeepTitle), and
	// ChangeSpec refuses a Change that still has none.
	return c, nil
}

// forkRE is a fork as Change names it: owner/name.
var forkRE = regexp.MustCompile(`^[A-Za-z0-9-]+/[A-Za-z0-9._-]+$`)

// applyOpenPR opens a Change's branch as a PR on its repository's default
// branch, a draft unless the Change says otherwise (draft: false), with the caller's token, and points the document's
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
	if c.Fork == "" && c.Branch == "" && c.Comment != "" {
		return commentUnchanged(ctx, gh, doc, c, owner, repo, dryRun, out)
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
		// Applied again after its labels failed: they go on now.
		if !dryRun {
			addPRLabels(ctx, gh, owner, repo, open[0], c.Labels, out)
			return markAdopted(ctx, gh, c, open[0].GetHTMLURL(), out)
		}
		return nil
	}
	r, _, err := gh.Repositories.Get(ctx, owner, repo)
	if err != nil {
		return fmt.Errorf("fetching %s/%s: %w", owner, repo, err)
	}
	base := r.GetDefaultBranch()
	draft := c.Draft == nil || *c.Draft
	kind := "a PR"
	if draft {
		kind = "a draft PR"
	}
	fmt.Fprintf(out, "%s %s on %s/%s from %s into %s: %q\n", doing(dryRun, "Opening", "open"), kind, owner, repo, head, base, c.Title)
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
		Body:                githubv39.String(withSessionHint(ctx, c.Body) + marker(doc)),
		Draft:               githubv39.Bool(draft),
		MaintainerCanModify: githubv39.Bool(true),
	})
	if err != nil {
		return fmt.Errorf("opening the PR: %w", err)
	}
	fmt.Fprintf(out, "Opened %s\n", pr.GetHTMLURL())
	doc.Target.URL = pr.GetHTMLURL()
	addPRLabels(ctx, gh, owner, repo, pr, c.Labels, out)
	return markAdopted(ctx, gh, c, pr.GetHTMLURL(), out)
}

// commentUnchanged is open-pr for a fix that changed nothing: it posts the
// Change's comment, why, on the issue, once per task, and opens no PR.
func commentUnchanged(ctx context.Context, gh *githubv39.Client, doc *Document, c *Change, owner, repo string, dryRun bool, out io.Writer) error {
	_, _, num, isPR, err := parseItemURL(doc.Target.URL)
	if err != nil {
		return err
	}
	if isPR {
		return fmt.Errorf("the Change made no change and is on a PR (%s): nothing to open", doc.Target.URL)
	}
	if doc.Source.Task != "" {
		posted, err := hasComment(ctx, gh, owner, repo, num, marker(doc))
		if err != nil {
			return err
		}
		if posted {
			fmt.Fprintf(out, "Why task %s changed nothing is already on %s; not commenting again\n", doc.Source.Task, doc.Target.URL)
			return nil
		}
	}
	fmt.Fprintf(out, "The fix made no change; no PR. %s why on %s:\n%s\n", doing(dryRun, "Commenting", "comment"), doc.Target.URL, indent(c.Comment))
	if dryRun {
		return nil
	}
	body := withSessionHint(ctx, c.Comment) + marker(doc)
	if _, _, err := gh.Issues.CreateComment(ctx, owner, repo, num, &githubv39.IssueComment{Body: &body}); err != nil {
		return fmt.Errorf("commenting why the fix changed nothing: %w", err)
	}
	return nil
}

// rewritePR puts the title and body the agent rewrote (Rewrites) on PR
// owner/repo#num: only a PR the applying account opened, and only if its
// title and body are still the ones the task started from (Was). The
// body keeps the factory's markers the PR's had, and one session hint:
// this apply's, else the one the PR's had.
func rewritePR(ctx context.Context, gh *githubv39.Client, c *Change, owner, repo string, num int, dryRun bool, out io.Writer) error {
	if len(c.Rewrites) == 0 {
		return nil
	}
	pr, _, err := gh.PullRequests.Get(ctx, owner, repo, num)
	if err != nil {
		return fmt.Errorf("reading PR %s/%s#%d to rewrite it: %w", owner, repo, num, err)
	}
	me, _, err := gh.Users.Get(ctx, "")
	if err != nil {
		return fmt.Errorf("reading who applies: %w", err)
	}
	if !strings.EqualFold(me.GetLogin(), pr.GetUser().GetLogin()) {
		fmt.Fprintf(out, "Not rewriting %s/%s#%d: it is %s's, not %s's\n", owner, repo, num, pr.GetUser().GetLogin(), me.GetLogin())
		return nil
	}
	if c.Was != "" && prTextHash(pr.GetTitle(), pr.GetBody()) != c.Was {
		fmt.Fprintf(out, "Not rewriting %s/%s#%d: its title or description changed since the task started\n", owner, repo, num)
		return nil
	}
	edit := &githubv39.PullRequest{}
	for _, what := range c.Rewrites {
		switch what {
		case "title":
			if c.Title != "" && c.Title != pr.GetTitle() {
				edit.Title = githubv39.String(c.Title)
			}
		case "body":
			hint := sessionHint(ctx)
			if hint == "" {
				hint = strings.TrimSpace(sessionHintRE.FindString(pr.GetBody()))
			}
			body := strings.TrimRight(sessionHintRE.ReplaceAllString(c.Body, ""), "\n")
			if hint != "" {
				body += "\n\n" + hint
			}
			for _, m := range factoryMarkerRE.FindAllString(pr.GetBody(), -1) {
				if !strings.Contains(body, m) {
					body += "\n\n" + m
				}
			}
			if body != pr.GetBody() {
				edit.Body = githubv39.String(body)
			}
		}
	}
	if edit.Title == nil && edit.Body == nil {
		return nil
	}
	fmt.Fprintf(out, "%s the %s of %s/%s#%d\n", doing(dryRun, "Rewriting", "rewrite"), strings.Join(c.Rewrites, " and "), owner, repo, num)
	if dryRun {
		return nil
	}
	if _, _, err := gh.PullRequests.Edit(ctx, owner, repo, num, edit); err != nil {
		return fmt.Errorf("rewriting PR %s/%s#%d: %w", owner, repo, num, err)
	}
	return nil
}

// adoptedMarker ends the comment markAdopted leaves on an adopted PR, so
// that applying again leaves no second.
func adoptedMarker(prURL string) string {
	return fmt.Sprintf("<!-- factory:adopted-by %s -->", prURL)
}

// markAdopted comments on the PR a Change adopts with a link to prURL,
// the PR that adopts it, once, and closes it when the Change says so.
func markAdopted(ctx context.Context, gh *githubv39.Client, c *Change, prURL string, out io.Writer) error {
	if c.Adopts == "" {
		return nil
	}
	owner, repo, num, isPR, err := parseItemURL(c.Adopts)
	if err != nil || !isPR {
		return fmt.Errorf("adopts %q is not a PR", c.Adopts)
	}
	comments, _, err := gh.Issues.ListComments(ctx, owner, repo, num, &githubv39.IssueListCommentsOptions{ListOptions: githubv39.ListOptions{PerPage: 100}})
	if err != nil {
		return fmt.Errorf("reading the comments on %s: %w", c.Adopts, err)
	}
	commented := false
	for _, cm := range comments {
		if strings.Contains(cm.GetBody(), adoptedMarker(prURL)) {
			commented = true
			break
		}
	}
	if !commented {
		body := "This PR has been adopted here: " + prURL
		if c.CloseAdopted {
			body = "This PR has been adopted here: " + prURL + ", and closed."
		}
		if _, _, err := gh.Issues.CreateComment(ctx, owner, repo, num, &githubv39.IssueComment{Body: githubv39.String(body + "\n\n" + adoptedMarker(prURL))}); err != nil {
			return fmt.Errorf("commenting on %s: %w", c.Adopts, err)
		}
		fmt.Fprintf(out, "Commented on %s\n", c.Adopts)
	}
	if c.CloseAdopted {
		if _, _, err := gh.PullRequests.Edit(ctx, owner, repo, num, &githubv39.PullRequest{State: githubv39.String("closed")}); err != nil {
			return fmt.Errorf("closing %s: %w", c.Adopts, err)
		}
		fmt.Fprintf(out, "Closed %s\n", c.Adopts)
	}
	return nil
}

// labelerKey carries the client open-pr labels with (WithLabeler).
type labelerKey struct{}

// WithLabeler is ctx for applies whose open-pr labels the PR with gh
// rather than with the client it opens it with: an overseer opens its
// fix's PR as the bot that wrote it, which may not label the repository's
// issues, and labels it as itself, as factory fix did.
func WithLabeler(ctx context.Context, gh *githubv39.Client) context.Context {
	return context.WithValue(ctx, labelerKey{}, gh)
}

// addPRLabels adds labels to pr, with the client WithLabeler set, else
// gh. The PR is open either way; a label missing from the repository, or
// a client that may not label, is a warning, not a failure. A failure
// GitHub may not repeat - a 5xx, a rate limit, the connection - is tried
// again (labelRetries): an overseer's PR left without its labels is one
// no listing of the watch finds. Adding a label it has is a no-op, so a
// retry after a write that landed does no harm.
func addPRLabels(ctx context.Context, gh *githubv39.Client, owner, repo string, pr *githubv39.PullRequest, labels []string, out io.Writer) {
	if len(labels) == 0 {
		return
	}
	if l, ok := ctx.Value(labelerKey{}).(*githubv39.Client); ok && l != nil {
		gh = l
	}
	var err error
	for i := 0; ; i++ {
		if _, _, err = gh.Issues.AddLabelsToIssue(ctx, owner, repo, pr.GetNumber(), labels); err == nil {
			return
		}
		if i == len(labelRetries) || !transient(err) {
			break
		}
		fmt.Fprintf(out, "Labelling %s failed (%v); trying again in %s\n", pr.GetHTMLURL(), err, labelRetries[i])
		select {
		case <-ctx.Done():
			fmt.Fprintf(out, "Warning: could not label %s: %v\n", pr.GetHTMLURL(), ctx.Err())
			return
		case <-time.After(labelRetries[i]):
		}
	}
	fmt.Fprintf(out, "Warning: could not label %s: %v\n", pr.GetHTMLURL(), err)
}

// labelRetries are the waits before each new try of a PR's labels.
var labelRetries = []time.Duration{2 * time.Second, 5 * time.Second, 15 * time.Second}

// transient reports whether a GitHub call's failure may not happen again:
// a rate limit, a 429 or 5xx, or no answer at all. A 4xx GitHub gave - a
// client that may not write, a label that does not exist - would.
func transient(err error) bool {
	var rate *githubv39.RateLimitError
	var abuse *githubv39.AbuseRateLimitError
	var resp *githubv39.ErrorResponse
	switch {
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return false
	case errors.As(err, &rate), errors.As(err, &abuse):
		return true
	case errors.As(err, &resp):
		return resp.Response != nil && (resp.Response.StatusCode == http.StatusTooManyRequests || resp.Response.StatusCode >= 500)
	}
	return true
}

// applyPostReplies posts a Change's replies and report on its PR, with
// the caller's token: a reply to a review comment in that comment's
// thread, a reply to a conversation comment as a new comment quoting and
// linking it (GitHub has no threads there), and the report as a PR
// comment. Each carries a marker of the task and the comment it answers,
// so a retried apply posts nothing twice. It then reruns the checks the
// Change asks to (applyReruns). Then it marks the feedback the
// task was handed resolved (the watch's 👍, conventions.ReactionResolved),
// so neither `factory pr watch` nor the next Address comments picks it up
// again. A Change with none of these — a start's — posts nothing.
func applyPostReplies(ctx context.Context, gh *githubv39.Client, doc *Document, dryRun bool, out io.Writer) error {
	c, err := doc.ChangeSpec()
	if err != nil {
		return err
	}
	if len(c.Replies) == 0 && c.Report == "" && len(c.Rerun) == 0 && len(c.Rewrites) == 0 && len(c.Feedback) == 0 {
		fmt.Fprintln(out, "The Change has no replies, report, reruns, rewrites or feedback; nothing to post")
		return nil
	}
	owner, repo, num, err := prTarget(doc)
	if err != nil {
		return fmt.Errorf("%w: open the PR first (open-pr)", err)
	}
	// First, so the report is true when it is posted and a rerun check
	// (a release note's) sees the new description.
	if err := rewritePR(ctx, gh, c, owner, repo, num, dryRun, out); err != nil {
		return err
	}
	for _, r := range c.Replies {
		if err := postReply(ctx, gh, doc, owner, repo, num, r, dryRun, out); err != nil {
			return fmt.Errorf("replying to comment %d: %w", r.InReplyTo, err)
		}
	}
	if err := postReport(ctx, gh, doc, owner, repo, num, c.Report, dryRun, out); err != nil {
		return err
	}
	if err := applyReruns(ctx, gh, doc, owner, repo, num, c.Rerun, dryRun, out); err != nil {
		return err
	}
	if len(c.Feedback) == 0 {
		return nil
	}
	fmt.Fprintf(out, "%s %d piece(s) of feedback on %s addressed (%s)\n", doing(dryRun, "Marking", "mark"), len(c.Feedback), doc.Target.URL, conventions.ReactionResolved)
	if dryRun {
		return nil
	}
	items := make([]feedback.Item, len(c.Feedback))
	for i, f := range c.Feedback {
		items[i] = feedback.Item{Kind: feedback.Kind(f.Kind), ID: f.ID, NodeID: f.NodeID}
	}
	return feedback.React(ctx, github.ForRepo(gh, owner, repo), items, conventions.ReactionResolved)
}

func postReport(ctx context.Context, gh *githubv39.Client, doc *Document, owner, repo string, num int, report string, dryRun bool, out io.Writer) error {
	if report == "" {
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
	fmt.Fprintf(out, "%s the report on %s:\n%s\n", doing(dryRun, "Commenting", "comment"), doc.Target.URL, indent(report))
	if dryRun {
		return nil
	}
	body := withSessionHint(ctx, report) + marker(doc)
	if _, _, err := gh.Issues.CreateComment(ctx, owner, repo, num, &githubv39.IssueComment{Body: &body}); err != nil {
		return fmt.Errorf("commenting the report on #%d: %w", num, err)
	}
	return nil
}

// applyReruns runs again the checks a Change asks to, on PR num:
//   - a GitHub Actions run's failed jobs, if the run is of the PR's head
//     now and failed; one queued or running again, or that passed, is left
//     alone, so a retried apply does not rerun it twice;
//   - a Prow command ("/retest", "/test <job>"; anything else is only
//     warned about), as a comment carrying a marker of the task, posted
//     once.
func applyReruns(ctx context.Context, gh *githubv39.Client, doc *Document, owner, repo string, num int, reruns []Rerun, dryRun bool, out io.Writer) error {
	if len(reruns) == 0 {
		return nil
	}
	pr, _, err := gh.PullRequests.Get(ctx, owner, repo, num)
	if err != nil {
		return fmt.Errorf("fetching #%d: %w", num, err)
	}
	head := pr.GetHead().GetSHA()
	for i, r := range reruns {
		switch {
		case r.Run > 0:
			if err := rerunFailedJobs(ctx, gh, owner, repo, r.Run, head, dryRun, out); err != nil {
				return fmt.Errorf("rerunning run %d: %w", r.Run, err)
			}
		case retestRE.MatchString(r.Retest):
			mark := ""
			if doc.Source.Task != "" {
				mark = fmt.Sprintf("\n\n<!-- factory:task-output kind=%s task=%s rerun=%d -->", doc.Kind, doc.Source.Task, i)
				posted, err := hasComment(ctx, gh, owner, repo, num, mark)
				if err != nil {
					return err
				}
				if posted {
					fmt.Fprintf(out, "Task %s's %s is already on %s; not commenting it again\n", doc.Source.Task, r.Retest, doc.Target.URL)
					continue
				}
			}
			fmt.Fprintf(out, "%s %s on %s\n", doing(dryRun, "Commenting", "comment"), r.Retest, doc.Target.URL)
			if dryRun {
				continue
			}
			body := r.Retest + mark
			if _, _, err := gh.Issues.CreateComment(ctx, owner, repo, num, &githubv39.IssueComment{Body: &body}); err != nil {
				return fmt.Errorf("commenting %s on #%d: %w", r.Retest, num, err)
			}
		default:
			fmt.Fprintf(out, "Warning: rerun %q is neither a run nor /retest or /test <job>; not commenting it\n", r.Retest)
		}
	}
	return nil
}

// rerunFailedJobs reruns the failed jobs of GitHub Actions run id, if it
// is of head and failed.
func rerunFailedJobs(ctx context.Context, gh *githubv39.Client, owner, repo string, id int64, head string, dryRun bool, out io.Writer) error {
	run, _, err := gh.Actions.GetWorkflowRunByID(ctx, owner, repo, id)
	if err != nil {
		return err
	}
	switch {
	case run.GetHeadSHA() != head:
		fmt.Fprintf(out, "Run %d (%s) is of %.7s, not of the PR's head %.7s; not rerunning it\n", id, run.GetName(), run.GetHeadSHA(), head)
		return nil
	case run.GetStatus() != "completed":
		fmt.Fprintf(out, "Run %d (%s) is %s already; not rerunning it\n", id, run.GetName(), run.GetStatus())
		return nil
	case run.GetConclusion() == "success":
		fmt.Fprintf(out, "Run %d (%s) passed; not rerunning it\n", id, run.GetName())
		return nil
	}
	fmt.Fprintf(out, "%s the failed jobs of run %d (%s): %s\n", doing(dryRun, "Rerunning", "rerun"), id, run.GetName(), run.GetHTMLURL())
	if dryRun {
		return nil
	}
	// go-github v39 has no call for it.
	req, err := gh.NewRequest(http.MethodPost, fmt.Sprintf("repos/%s/%s/actions/runs/%d/rerun-failed-jobs", owner, repo, id), nil)
	if err != nil {
		return err
	}
	_, err = gh.Do(ctx, req, nil)
	return err
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
		_, _, err := gh.PullRequests.CreateCommentInReplyTo(ctx, owner, repo, num, withSessionHint(ctx, r.Body)+mark, r.InReplyTo)
		return err
	case resp == nil || resp.StatusCode != http.StatusNotFound:
		return err
	}
	ic, resp, err := gh.Issues.GetComment(ctx, owner, repo, r.InReplyTo)
	switch {
	case err == nil:
		if !strings.HasSuffix(ic.GetIssueURL(), fmt.Sprintf("/issues/%d", num)) {
			return fmt.Errorf("it is not on %s", doc.Target.URL)
		}
		return postAnswer(ctx, gh, doc, owner, repo, num, r, mark, ic.GetHTMLURL(), quoteComment(ic), dryRun, out)
	case resp == nil || resp.StatusCode != http.StatusNotFound:
		return err
	}
	// Not a comment: a review's own id, which the agent reads beside the
	// comments, has no thread to reply in; it is answered with a comment
	// quoting the review. Anything else is answered with a plain comment
	// rather than failing the whole apply, whose agent run is long over.
	rv, resp, err := gh.PullRequests.GetReview(ctx, owner, repo, num, r.InReplyTo)
	switch {
	case err == nil:
		return postAnswer(ctx, gh, doc, owner, repo, num, r, mark, rv.GetHTMLURL(), quoteReview(rv), dryRun, out)
	case resp == nil || resp.StatusCode != http.StatusNotFound:
		return err
	}
	fmt.Fprintf(out, "Warning: %d is neither a review comment, a comment nor a review on %s; answering it with a plain comment\n", r.InReplyTo, doc.Target.URL)
	return postAnswer(ctx, gh, doc, owner, repo, num, r, mark, doc.Target.URL, "", dryRun, out)
}

// postAnswer answers with a conversation comment on PR num: quote (the
// start of what it answers, with a link to it) and the reply. A reply its
// task already posted, found by its marker, is not posted again.
func postAnswer(ctx context.Context, gh *githubv39.Client, doc *Document, owner, repo string, num int, r Reply, mark, url, quote string, dryRun bool, out io.Writer) error {
	if mark != "" {
		posted, err := hasComment(ctx, gh, owner, repo, num, mark)
		if err != nil {
			return err
		}
		if posted {
			fmt.Fprintf(out, "Task %s's reply to %s is already posted; not posting it again\n", doc.Source.Task, url)
			return nil
		}
	}
	fmt.Fprintf(out, "%s %s with a comment on %s:\n%s\n", doing(dryRun, "Answering", "answer"), url, doc.Target.URL, indent(r.Body))
	if dryRun {
		return nil
	}
	body := quote + withSessionHint(ctx, r.Body) + mark
	_, _, err := gh.Issues.CreateComment(ctx, owner, repo, num, &githubv39.IssueComment{Body: &body})
	return err
}

// quoteReview heads a reply to a review: the start of its body (or its
// verdict, when it has none), quoted, and a link to it.
func quoteReview(rv *githubv39.PullRequestReview) string {
	body := strings.TrimSpace(rv.GetBody())
	if body == "" {
		body = strings.ToLower(strings.ReplaceAll(rv.GetState(), "_", " "))
	}
	lines := strings.Split(body, "\n")
	if len(lines) > 3 {
		lines = append(lines[:3], "…")
	}
	var b strings.Builder
	for _, l := range lines {
		b.WriteString("> " + l + "\n")
	}
	fmt.Fprintf(&b, "\n[In reply to @%s's review](%s)\n\n", rv.GetUser().GetLogin(), rv.GetHTMLURL())
	return b.String()
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
