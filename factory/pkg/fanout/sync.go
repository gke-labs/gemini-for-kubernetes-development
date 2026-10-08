package fanout

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	githubv39 "github.com/google/go-github/v39/github"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/commands/watch/conventions"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/github"
)

// GitHub is what a pass reads and writes. *github.Client has it.
type GitHub interface {
	GetIssue(ctx context.Context, number int) (*githubv39.Issue, error)
	ListIssueComments(ctx context.Context, number int) ([]*githubv39.IssueComment, error)
	ListCrossReferences(ctx context.Context, number int) ([]github.IssueRef, error)
	GetIssueRef(ctx context.Context, number int) (github.IssueRef, error)
	CreateIssue(ctx context.Context, title, body string, labels []string) (int, error)
	EditIssue(ctx context.Context, number int, title, body string) error
	AddLabels(ctx context.Context, number int, labels []string) error
	AddComment(ctx context.Context, number int, body string) error
	EditComment(ctx context.Context, commentID int64, body string) error
	CloseIssue(ctx context.Context, number int) error
	ListSubIssues(ctx context.Context, number int) ([]int, error)
	AddSubIssue(ctx context.Context, parent, child int) error
	// ReadFile returns a file on the default branch and the commit it was
	// read at; github.ErrUnreadableFile when the file is missing or unusable.
	ReadFile(ctx context.Context, path string) ([]byte, string, error)
}

// SyncOptions are a pass's.
type SyncOptions struct {
	// Issue is the parent.
	Issue int
	// Parent, when set, is the parent as the caller already fetched it, so
	// the pass does not fetch it again.
	Parent       *githubv39.Issue
	TriggerLabel string
	// BotLogin is the account the fan-out runs as: its progress comment is
	// the only one read, and its spec comment is trusted.
	BotLogin string
	// DryRun reads and decides, and writes nothing.
	DryRun bool
	// Logf, when set, is told what the pass does (or would do).
	Logf func(format string, args ...any)
}

// SyncResult is what a pass found and did.
type SyncResult struct {
	// Closed is a parent already closed: there is nothing to do.
	Closed bool
	// NoSpec is a parent with no spec: neither a spec comment nor a body
	// with the standard headings. One has to be proposed.
	NoSpec bool
	// SpecError is a spec that does not parse. The progress comment says
	// so, and nothing else happens until it is fixed.
	SpecError error
	Plan      Plan
}

// trustedAssociations are the authors whose spec comment counts, besides the
// bot: people with write access, who could edit the bot's comment anyway.
var trustedAssociations = []string{"OWNER", "MEMBER", "COLLABORATOR"}

// Sync runs one pass over a parent: read the spec, the children and the
// state, decide, and write.
func Sync(ctx context.Context, gh GitHub, opts SyncOptions) (SyncResult, error) {
	logf := opts.Logf
	if logf == nil {
		logf = func(string, ...any) {}
	}
	would := ""
	if opts.DryRun {
		would = "would "
	}
	stopLabel := conventions.StopLabel(opts.TriggerLabel)

	parent := opts.Parent
	if parent == nil {
		var err error
		if parent, err = gh.GetIssue(ctx, opts.Issue); err != nil {
			return SyncResult{}, err
		}
	}
	if parent.IsPullRequest() {
		return SyncResult{}, fmt.Errorf("#%d is a pull request, not an issue", opts.Issue)
	}
	if parent.GetState() != "open" {
		return SyncResult{Closed: true}, nil
	}
	comments, err := gh.ListIssueComments(ctx, opts.Issue)
	if err != nil {
		return SyncResult{}, fmt.Errorf("listing the comments of #%d: %w", opts.Issue, err)
	}
	specComment, progress := findComments(comments, opts.BotLogin)

	var markdown string
	switch {
	case specComment != nil:
		markdown = specComment.GetBody()
		logf("spec: comment %s", specComment.GetHTMLURL())
	case HasSpecHeadings(parent.GetBody()):
		markdown = parent.GetBody()
		logf("spec: the issue body")
	default:
		logf("no spec: neither a spec comment nor a body with ## Task and ## Items")
		return SyncResult{NoSpec: true}, nil
	}
	spec, err := Parse(markdown)
	if err == nil && spec.Settings.Items != nil {
		err = loadItems(ctx, gh, &spec)
	}
	var rerr retryable
	if errors.As(err, &rerr) {
		return SyncResult{}, rerr.err
	}
	if err != nil {
		body := fmt.Sprintf("%s\n### Fan-out\n\nThe spec does not parse: %s.\n\nFix it, and the fan-out carries on.\n", ProgressMarker, err)
		if st, ok := progressState(progress); ok {
			body += "\n" + st.block() + "\n"
		}
		logf("%swrite the progress comment: the spec does not parse: %v", would, err)
		if !opts.DryRun {
			if err := writeProgress(ctx, gh, opts.Issue, progress, body); err != nil {
				return SyncResult{}, err
			}
		}
		return SyncResult{SpecError: err}, nil
	}

	var state *State
	if st, ok := progressState(progress); ok {
		state = st
	}
	children, err := readChildren(ctx, gh, opts.Issue, opts.TriggerLabel, state)
	if err != nil {
		return SyncResult{}, err
	}
	in := Input{
		Parent:       opts.Issue,
		ParentTitle:  parent.GetTitle(),
		Spec:         spec,
		Children:     children,
		State:        state,
		Stopped:      conventions.HasStopLabel(parent.Labels, opts.TriggerLabel),
		TriggerLabel: opts.TriggerLabel,
		StopLabel:    stopLabel,
	}
	plan := Decide(in)
	logf("%d items, %d children, group %d, window %d, stopped %v", len(spec.Items), len(children), plan.State.Group, plan.State.Window, in.Stopped)
	if opts.DryRun && in.Stopped {
		resumed := in
		resumed.Stopped = false
		logf("once %s is removed, the next pass would:", stopLabel)
		logPlan(logf, "  ", resumed, Decide(resumed))
	}

	if opts.DryRun {
		logPlan(logf, would, in, plan)
		logf("%swrite the progress comment:\n%s", would, Progress(withStop(in, plan), plan.State, children))
		linkSubIssues(ctx, gh, opts.Issue, children, true, logf)
		return SyncResult{Plan: plan}, nil
	}
	err = apply(ctx, gh, in, &plan, &children, logf)
	linkSubIssues(ctx, gh, opts.Issue, children, false, logf)
	// The progress comment is written even after a failed write, so that
	// what was created is remembered.
	if perr := writeProgress(ctx, gh, opts.Issue, progress, Progress(withStop(in, plan), plan.State, children)); perr != nil {
		err = errors.Join(err, perr)
	}
	if err == nil && plan.CloseParent {
		logf("close #%d", opts.Issue)
		if cerr := gh.CloseIssue(ctx, opts.Issue); cerr != nil {
			err = errors.Join(cerr, gh.AddComment(ctx, opts.Issue, "Every child of this fan-out is done, but closing it failed. Close it when you are ready."))
		}
	}
	return SyncResult{Plan: plan}, err
}

// retryable is a failure to read an items file that is GitHub's, not the
// spec's: it fails the pass instead of being reported as a spec error.
type retryable struct{ err error }

func (r retryable) Error() string { return r.err.Error() }

// loadItems reads the spec's items file and loads its items.
func loadItems(ctx context.Context, gh GitHub, spec *Spec) error {
	path := spec.Settings.Items.From
	data, commit, err := gh.ReadFile(ctx, path)
	switch {
	case errors.Is(err, github.ErrUnreadableFile):
		return fmt.Errorf("items.from: %w", err)
	case err != nil:
		return retryable{err}
	}
	return spec.LoadItems(data, fmt.Sprintf("`%s` at %.7s", path, commit))
}

// withStop is the input as the progress comment shows it: stopped, if the
// pass stopped at a checkpoint.
func withStop(in Input, p Plan) Input {
	in.Stopped = in.Stopped || p.Stop != ""
	return in
}

// findComments returns the spec comment (the newest by a trusted author) and
// the progress comment (the oldest by the bot).
func findComments(comments []*githubv39.IssueComment, botLogin string) (spec, progress *githubv39.IssueComment) {
	for _, c := range comments {
		byBot := botLogin != "" && strings.EqualFold(c.GetUser().GetLogin(), botLogin)
		body := c.GetBody()
		if strings.Contains(body, SpecMarker) && (byBot || slices.Contains(trustedAssociations, c.GetAuthorAssociation())) {
			spec = c
		}
		if progress == nil && byBot && strings.Contains(body, ProgressMarker) {
			progress = c
		}
	}
	return spec, progress
}

func progressState(progress *githubv39.IssueComment) (*State, bool) {
	if progress == nil {
		return nil, false
	}
	return ParseState(progress.GetBody())
}

// readChildren finds the parent's children: the issues whose marker names it
// that mention it on its timeline, and the ones the state remembers, which a
// timeline may not show yet. The PRs of every child started or closed are
// read from its own timeline.
func readChildren(ctx context.Context, gh GitHub, parent int, trigger string, st *State) ([]Child, error) {
	refs, err := gh.ListCrossReferences(ctx, parent)
	if err != nil {
		return nil, err
	}
	found := map[int]bool{}
	var children []Child
	add := func(ref github.IssueRef) {
		p, keys, final, ok := ParseMarker(ref.Body)
		if ref.IsPR || !ok || p != parent || found[ref.Number] {
			return
		}
		found[ref.Number] = true
		children = append(children, Child{
			Number:     ref.Number,
			Keys:       keys,
			Final:      final,
			Title:      ref.Title,
			Body:       ref.Body,
			Open:       ref.Open,
			NotPlanned: !ref.Open && ref.StateReason == "not_planned",
			Labelled:   slices.ContainsFunc(ref.Labels, func(l string) bool { return strings.EqualFold(l, trigger) }),
		})
	}
	for _, ref := range refs {
		add(ref)
	}
	if st != nil {
		remembered := []int{st.Final}
		for _, n := range st.Children {
			remembered = append(remembered, n)
		}
		slices.Sort(remembered)
		remembered = slices.Compact(remembered)
		for _, n := range remembered {
			if n == 0 || found[n] {
				continue
			}
			ref, err := gh.GetIssueRef(ctx, n)
			if github.IsNotFound(err) {
				continue
			}
			if err != nil {
				return nil, err
			}
			add(ref)
		}
	}
	for i := range children {
		c := &children[i]
		if c.Open && !c.Labelled && (st == nil || !slices.Contains(st.Started, c.Number)) {
			continue
		}
		refs, err := gh.ListCrossReferences(ctx, c.Number)
		if err != nil {
			return nil, err
		}
		for _, ref := range refs {
			if ref.IsPR {
				c.PRs = append(c.PRs, PR{Number: ref.Number, Open: ref.Open, Merged: ref.Merged})
			}
		}
	}
	return children, nil
}

// apply makes the plan's writes, recording what it creates and labels in
// the plan's state and in children. It stops at the first write that fails.
func apply(ctx context.Context, gh GitHub, in Input, p *Plan, children *[]Child, logf func(string, ...any)) error {
	for _, r := range p.Rewrite {
		logf("rewrite #%d (%s) from the spec", r.Number, strings.Join(r.Keys, ","))
		if err := gh.EditIssue(ctx, r.Number, r.Title, r.Body); err != nil {
			return err
		}
		for i := range *children {
			if (*children)[i].Number == r.Number {
				(*children)[i].Title, (*children)[i].Body = r.Title, r.Body
			}
		}
	}
	for _, c := range p.Create {
		n, err := gh.CreateIssue(ctx, c.Title, c.Body, c.Labels)
		if err != nil {
			return err
		}
		logf("created #%d %q %s", n, c.Title, labelNote(c.Labels))
		p.State.Created(c, n)
		*children = append(*children, Child{Number: n, Keys: c.Keys, Final: c.Final, Title: c.Title, Body: c.Body, Open: true, Labelled: len(c.Labels) > 0})
	}
	for _, l := range p.Label {
		logf("label #%d %s", l.Number, strings.Join(l.Labels, ", "))
		if err := gh.AddLabels(ctx, l.Number, l.Labels); err != nil {
			return err
		}
		for i := range *children {
			if (*children)[i].Number == l.Number {
				(*children)[i].Labelled = true
			}
		}
	}
	if p.Stop != "" {
		logf("stop at a checkpoint: label #%d %s", in.Parent, in.StopLabel)
		if err := gh.AddLabels(ctx, in.Parent, []string{in.StopLabel}); err != nil {
			return err
		}
		if err := gh.AddComment(ctx, in.Parent, p.Stop); err != nil {
			return err
		}
	}
	return nil
}

// linkSubIssues makes every child a sub-issue of the parent, so GitHub lists
// them under it with a progress bar. It is for show: the children are found
// by their markers, so a failure (a child with another parent, GitHub's limit
// on sub-issues) is logged and the pass carries on.
func linkSubIssues(ctx context.Context, gh GitHub, parent int, children []Child, dryRun bool, logf func(string, ...any)) {
	if len(children) == 0 {
		return
	}
	linked, err := gh.ListSubIssues(ctx, parent)
	if err != nil {
		logf("sub-issues: %v", err)
		return
	}
	for _, c := range children {
		if slices.Contains(linked, c.Number) {
			continue
		}
		if dryRun {
			logf("would add #%d as a sub-issue", c.Number)
			continue
		}
		if err := gh.AddSubIssue(ctx, parent, c.Number); err != nil {
			logf("sub-issues: %v", err)
			continue
		}
		logf("added #%d as a sub-issue", c.Number)
	}
}

func logPlan(logf func(string, ...any), prefix string, in Input, p Plan) {
	acted := false
	for _, r := range p.Rewrite {
		logf("%srewrite #%d (%s) from the spec", prefix, r.Number, strings.Join(r.Keys, ","))
		acted = true
	}
	for _, c := range p.Create {
		logf("%screate %q %s", prefix, c.Title, labelNote(c.Labels))
		acted = true
	}
	for _, l := range p.Label {
		logf("%slabel #%d %s", prefix, l.Number, strings.Join(l.Labels, ", "))
		acted = true
	}
	if p.Stop != "" {
		logf("%sstop at a checkpoint (label #%d %s) and comment:\n%s", prefix, in.Parent, in.StopLabel, p.Stop)
		acted = true
	}
	if p.CloseParent {
		logf("%sclose #%d", prefix, in.Parent)
		acted = true
	}
	if !acted {
		logf("%schange no issue", prefix)
	}
}

func labelNote(labels []string) string {
	if len(labels) == 0 {
		return "(unlabelled)"
	}
	return "labelled " + strings.Join(labels, ", ")
}

// writeProgress edits the progress comment in place, or posts it the first
// time. A comment that already says the same is left alone.
func writeProgress(ctx context.Context, gh GitHub, parent int, progress *githubv39.IssueComment, body string) error {
	if progress == nil {
		return gh.AddComment(ctx, parent, body)
	}
	if sameText(progress.GetBody(), body) {
		return nil
	}
	return gh.EditComment(ctx, progress.GetID(), body)
}
