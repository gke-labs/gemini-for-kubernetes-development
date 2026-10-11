package taskoutput

import (
	"context"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"

	githubv39 "github.com/google/go-github/v39/github"
	"gopkg.in/yaml.v3"
)

// Review is a Review document's spec: a code review of a PR, posted as the
// caller's pending review (post-review), which they read, change and
// submit on GitHub.
type Review struct {
	Body     string          `yaml:"body"`
	Comments []ReviewComment `yaml:"comments,omitempty"`
}

// ReviewComment is one comment of a Review, anchored to a line of the
// PR's diff: Line on Side, or the lines StartLine to Line.
type ReviewComment struct {
	Path      string `yaml:"path"`
	Line      int    `yaml:"line"`
	Side      string `yaml:"side,omitempty"`
	StartLine int    `yaml:"start_line,omitempty"`
	StartSide string `yaml:"start_side,omitempty"`
	Body      string `yaml:"body"`
	// Severity is LOW, MEDIUM, HIGH or CRITICAL; it leads the comment
	// posted.
	Severity string `yaml:"severity,omitempty"`
}

// ReviewSpec decodes a Review document's spec.
func (d *Document) ReviewSpec() (*Review, error) {
	if d.Kind != "Review" {
		return nil, fmt.Errorf("%s task output is not a Review", d.Kind)
	}
	var r Review
	if err := d.Spec.Decode(&r); err != nil {
		return nil, fmt.Errorf("Review spec: %w", err)
	}
	if strings.TrimSpace(r.Body) == "" && len(r.Comments) == 0 {
		return nil, fmt.Errorf("Review spec has neither a body nor comments")
	}
	return &r, nil
}

func parseReview(raw string) (any, error) {
	var out struct {
		Review *Review `yaml:"review"`
	}
	if err := yaml.Unmarshal([]byte(CleanAgentYAML(raw, "review:")), &out); err != nil {
		return nil, err
	}
	r := out.Review
	if r == nil {
		return nil, fmt.Errorf("no review: block")
	}
	r.Body = strings.TrimSpace(r.Body)
	for i := range r.Comments {
		// LLM output is not whitespace-clean: GitHub rejects the whole
		// review over "RIGHT\n" in an enum field.
		c := &r.Comments[i]
		c.Path = strings.TrimSpace(c.Path)
		c.Side = reviewSide(c.Side)
		if c.StartLine != 0 {
			c.StartSide = reviewSide(c.StartSide)
		} else {
			c.StartSide = ""
		}
		c.Body = strings.TrimSpace(c.Body)
		c.Severity = strings.ToUpper(strings.TrimSpace(c.Severity))
	}
	if r.Body == "" && len(r.Comments) == 0 {
		return nil, fmt.Errorf("the review is empty")
	}
	return r, nil
}

// reviewSide is a side as GitHub takes it: LEFT, else RIGHT.
func reviewSide(s string) string {
	if strings.EqualFold(strings.TrimSpace(s), "LEFT") {
		return "LEFT"
	}
	return "RIGHT"
}

// prTarget is the document's target, which must be a PR.
func prTarget(doc *Document) (owner, repo string, num int, err error) {
	owner, repo, num, isPR, err := parseItemURL(doc.Target.URL)
	if err != nil {
		return "", "", 0, err
	}
	if !isPR {
		return "", "", 0, fmt.Errorf("a %s targets a PR, not %s", doc.Kind, doc.Target.URL)
	}
	return owner, repo, num, nil
}

// applyPostReview posts a Review as the caller's pending review on its PR,
// at the commit it reviewed: the draft is GitHub's, where it is read,
// changed and submitted (or discarded). Comments GitHub would refuse —
// not on a line of the diff — are folded into the body rather than
// failing the whole review. A pending review this posted before, for any
// task, is replaced; one the caller started themselves is not touched.
func applyPostReview(ctx context.Context, gh *githubv39.Client, doc *Document, dryRun bool, out io.Writer) error {
	return postReview(ctx, gh, doc, false, dryRun, out)
}

// applySubmitReview posts a Review as post-review does, but submitted, as
// a COMMENT review: the review of a watch, which no one reads as a draft
// first. A pending review factory posted is discarded first (GitHub keeps
// one pending review per person, and refuses a review beside it).
func applySubmitReview(ctx context.Context, gh *githubv39.Client, doc *Document, dryRun bool, out io.Writer) error {
	return postReview(ctx, gh, doc, true, dryRun, out)
}

func postReview(ctx context.Context, gh *githubv39.Client, doc *Document, submit, dryRun bool, out io.Writer) error {
	r, err := doc.ReviewSpec()
	if err != nil {
		return err
	}
	owner, repo, num, err := prTarget(doc)
	if err != nil {
		return err
	}
	if doc.Target.Commit == "" {
		return fmt.Errorf("the Review names no commit (target.commit), so its lines cannot be placed; run the review again in a sandbox on a newer image")
	}
	pr, _, err := gh.PullRequests.Get(ctx, owner, repo, num)
	if err != nil {
		return fmt.Errorf("fetching PR #%d: %w", num, err)
	}
	if pr.GetState() != "open" {
		return fmt.Errorf("%s is %s; not posting a review", doc.Target.URL, pr.GetState())
	}
	if head := pr.GetHead().GetSHA(); head != doc.Target.Commit {
		fmt.Fprintf(out, "The PR moved on since the review (reviewed %s, head is %s): its comments are placed on the commit reviewed, and GitHub shows those on changed lines as outdated\n", short(doc.Target.Commit), short(head))
	}

	login, existing, err := ownReviews(ctx, gh, owner, repo, num)
	if err != nil {
		return err
	}
	var replace *githubv39.PullRequestReview
	if mark := strings.TrimSpace(marker(doc)); mark != "" {
		for _, rv := range existing {
			if rv.GetState() != "PENDING" && strings.Contains(rv.GetBody(), mark) {
				fmt.Fprintf(out, "The review of task %s was already submitted on %s; not posting it again\n", doc.Source.Task, doc.Target.URL)
				return nil
			}
		}
	}
	for _, rv := range existing {
		if rv.GetState() != "PENDING" {
			continue
		}
		if !reviewMarkerRE.MatchString(rv.GetBody()) {
			return fmt.Errorf("%s has a pending review of yours that factory did not post; submit or discard it on GitHub first (GitHub keeps one pending review per person)", doc.Target.URL)
		}
		replace = rv
	}

	diff, err := diffLines(ctx, gh, owner, repo, pr.GetBase().GetRef(), doc.Target.Commit)
	if err != nil {
		return err
	}
	body, comments, folded := placeComments(r, diff)
	what := "a pending review"
	if submit {
		what = "a review"
	}
	fmt.Fprintf(out, "%s %s by %s on %s at %s: %d comments on the diff", doing(dryRun, "Posting", "post"), what, login, doc.Target.URL, short(doc.Target.Commit), len(comments))
	if folded > 0 {
		fmt.Fprintf(out, ", %d not on it folded into the body", folded)
	}
	fmt.Fprintln(out)
	if replace != nil {
		fmt.Fprintf(out, "%s the pending review factory posted before (%d)\n", doing(dryRun, "Replacing", "replace"), replace.GetID())
	}
	if dryRun {
		return nil
	}
	if replace != nil {
		if _, _, err := gh.PullRequests.DeletePendingReview(ctx, owner, repo, num, replace.GetID()); err != nil {
			return fmt.Errorf("discarding the pending review factory posted before: %w", err)
		}
	}
	req := &githubv39.PullRequestReviewRequest{
		CommitID: githubv39.String(doc.Target.Commit),
		Body:     githubv39.String(body + marker(doc)),
		Comments: comments,
	}
	if submit {
		req.Event = githubv39.String("COMMENT")
	}
	// Without an event the review stays pending, the caller's to submit.
	if _, _, err := gh.PullRequests.CreateReview(ctx, owner, repo, num, req); err != nil {
		return fmt.Errorf("posting %s: %w", what, err)
	}
	if submit {
		fmt.Fprintf(out, "Posted on %s\n", doc.Target.URL)
		return nil
	}
	fmt.Fprintf(out, "Posted; read, change and submit it on %s/files\n", doc.Target.URL)
	return nil
}

// reviewMarkerRE finds the marker of any Review in a review's body.
var reviewMarkerRE = regexp.MustCompile(`<!-- factory:task-output kind=Review task=[^ ]+ -->`)

// ownReviews are the caller's reviews on the PR, and their login. GitHub
// lists a pending review only to its author.
func ownReviews(ctx context.Context, gh *githubv39.Client, owner, repo string, num int) (string, []*githubv39.PullRequestReview, error) {
	user, _, err := gh.Users.Get(ctx, "")
	if err != nil {
		return "", nil, fmt.Errorf("resolving the token's GitHub login: %w", err)
	}
	login := user.GetLogin()
	var own []*githubv39.PullRequestReview
	opts := &githubv39.ListOptions{PerPage: 100}
	for {
		reviews, resp, err := gh.PullRequests.ListReviews(ctx, owner, repo, num, opts)
		if err != nil {
			return "", nil, fmt.Errorf("listing the reviews on #%d: %w", num, err)
		}
		for _, rv := range reviews {
			if strings.EqualFold(rv.GetUser().GetLogin(), login) {
				own = append(own, rv)
			}
		}
		if resp.NextPage == 0 {
			return login, own, nil
		}
		opts.Page = resp.NextPage
	}
}

// anchor is a line a comment can sit on: a side of a file's diff.
type anchor struct {
	path, side string
	line       int
}

// diffLines are the lines of the PR's diff at commit — what GitHub takes
// comments on — each with the hunk it is in. It is the diff of commit
// against the base branch, as GitHub shows the PR's.
func diffLines(ctx context.Context, gh *githubv39.Client, owner, repo, base, commit string) (map[anchor]int, error) {
	cmp, _, err := gh.Repositories.CompareCommits(ctx, owner, repo, base, commit, nil)
	if err != nil {
		return nil, fmt.Errorf("reading the diff of %s against %s: %w", short(commit), base, err)
	}
	lines := map[anchor]int{}
	hunk := 0
	for _, f := range cmp.Files {
		hunk = patchLines(lines, f.GetFilename(), f.GetPatch(), hunk)
	}
	return lines, nil
}

var hunkRE = regexp.MustCompile(`^@@ -(\d+)(?:,\d+)? \+(\d+)(?:,\d+)? @@`)

// patchLines adds the lines of one file's patch to lines, numbering its
// hunks from hunk on, and returns the next number.
func patchLines(lines map[anchor]int, path, patch string, hunk int) int {
	var left, right int
	for _, l := range strings.Split(patch, "\n") {
		if m := hunkRE.FindStringSubmatch(l); m != nil {
			left, _ = strconv.Atoi(m[1])
			right, _ = strconv.Atoi(m[2])
			hunk++
			continue
		}
		if hunk == 0 || l == "" {
			continue
		}
		switch l[0] {
		case ' ':
			lines[anchor{path, "LEFT", left}] = hunk
			lines[anchor{path, "RIGHT", right}] = hunk
			left++
			right++
		case '-':
			lines[anchor{path, "LEFT", left}] = hunk
			left++
		case '+':
			lines[anchor{path, "RIGHT", right}] = hunk
			right++
		}
	}
	return hunk
}

// placeComments splits a Review into the comments GitHub takes — on a
// line of the diff, a range within one hunk — and a body with the rest
// folded in, so one misplaced line does not lose the review.
func placeComments(r *Review, diff map[anchor]int) (string, []*githubv39.DraftReviewComment, int) {
	var comments []*githubv39.DraftReviewComment
	var off []string
	for _, c := range r.Comments {
		body := c.Body
		if c.Severity != "" {
			body = "**" + c.Severity + "**: " + body
		}
		h, ok := diff[anchor{c.Path, c.Side, c.Line}]
		if ok && c.StartLine != 0 {
			sh, sok := diff[anchor{c.Path, c.StartSide, c.StartLine}]
			ok = sok && sh == h && c.StartLine < c.Line
		}
		if !ok || body == "" {
			where := c.Path
			if c.Line > 0 {
				where = fmt.Sprintf("%s:%d", c.Path, c.Line)
			}
			off = append(off, fmt.Sprintf("- `%s`: %s", where, indentAfterFirst(body)))
			continue
		}
		dc := &githubv39.DraftReviewComment{
			Path: githubv39.String(c.Path),
			Line: githubv39.Int(c.Line),
			Side: githubv39.String(c.Side),
			Body: githubv39.String(body),
		}
		if c.StartLine != 0 {
			dc.StartLine = githubv39.Int(c.StartLine)
			dc.StartSide = githubv39.String(c.StartSide)
		}
		comments = append(comments, dc)
	}
	body := r.Body
	if len(off) > 0 {
		body = strings.TrimSpace(body + "\n\n**Comments not on a line of the diff**\n\n" + strings.Join(off, "\n"))
	}
	return body, comments, len(off)
}

func indentAfterFirst(s string) string {
	return strings.ReplaceAll(s, "\n", "\n  ")
}

func short(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}
