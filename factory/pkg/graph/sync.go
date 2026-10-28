package graph

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	githubv39 "github.com/google/go-github/v39/github"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/github"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/watch/rules"
)

// GitHub is what a pass reads and writes. *github.Client implements it.
type GitHub interface {
	GetIssue(ctx context.Context, number int) (*githubv39.Issue, error)
	GetIssueRef(ctx context.Context, number int) (github.IssueRef, error)
	ListIssueComments(ctx context.Context, number int) ([]*githubv39.IssueComment, error)
	ListIssueTimeline(ctx context.Context, number int) ([]*githubv39.Timeline, bool, error)
	ListIssues(ctx context.Context, opts *githubv39.IssueListByRepoOptions) ([]*githubv39.Issue, *githubv39.Response, error)
	ListCrossReferences(ctx context.Context, number int) ([]github.IssueRef, error)
	ListSubIssues(ctx context.Context, number int) ([]int, error)
	ReadFile(ctx context.Context, path string) ([]byte, string, error)
	CreateIssue(ctx context.Context, title, body string, labels []string) (int, error)
	EditIssue(ctx context.Context, number int, title, body string) error
	AddLabels(ctx context.Context, number int, labels []string) error
	AddAssignees(ctx context.Context, number int, assignees []string) error
	AddSubIssue(ctx context.Context, parent, child int) error
	AddComment(ctx context.Context, number int, body string) error
	EditComment(ctx context.Context, commentID int64, body string) error
	SetIssueState(ctx context.Context, number int, state, reason string) error
}

var _ GitHub = (*github.Client)(nil)

// Options are a pass's.
type Options struct {
	// Issue is the parent.
	Issue int
	// Trigger is the deployment's trigger label.
	Trigger string
	// BotLogin is the account the pass writes as: its status comment is the
	// only one read, and the children it recently created are found by it.
	BotLogin string
	// Pool are the pool's bots: their PRs complete a child, and their plan
	// and start comments are trusted. Members are the logins the overseer
	// trusts as members.
	Pool, Members []string
	// MaxDepth is how deep graphs nest; 0 is the default, 3.
	MaxDepth int
	// DryRun reads and decides, and writes nothing.
	DryRun bool
	// Logf, when set, reports each write (or, in a dry run, each write it
	// would make).
	Logf func(format string, args ...any)
}

func (o Options) env() rules.Env { return rules.Env{Pool: o.Pool, Members: o.Members} }

func (o Options) logf(format string, args ...any) {
	if o.Logf != nil {
		o.Logf(format, args...)
	}
}

// Pass is what one pass read, decided and wrote.
type Pass struct {
	// NoGraph is that the issue has no start block: it is no graph's parent.
	NoGraph bool
	Start   *Start
	// Version is the plan's version, for steps from a plan.
	Version  int
	Observed Observed
	Result   Result
	// Status is the status comment as rendered.
	Status string
}

// Sync runs one pass over a parent: read (Observe), decide (Reconcile),
// write (the actions, then the status comment). A failure ends the pass;
// the next one reads again and carries on.
func Sync(ctx context.Context, gh GitHub, opts Options) (*Pass, error) {
	p, statusComment, err := Observe(ctx, gh, opts)
	if err != nil || p.NoGraph {
		return p, err
	}
	p.Result = Reconcile(p.Observed)
	if p.Observed.State.Completed {
		// Finished before this pass: nothing more is written, the status
		// comment included.
		return p, nil
	}
	if err := apply(ctx, gh, opts, p); err != nil {
		return p, err
	}
	p.Status = RenderStatus(p.Start.SourceName(), p.Version, p.Observed.Spec, p.Result)
	switch {
	case statusComment != nil && statusComment.GetBody() == p.Status:
	case opts.DryRun:
		opts.logf("would write the status comment on #%d (%s)", opts.Issue, p.Result.Phase)
	case statusComment != nil:
		if err := gh.EditComment(ctx, statusComment.GetID(), p.Status); err != nil {
			return p, err
		}
	default:
		if err := gh.AddComment(ctx, opts.Issue, p.Status); err != nil {
			return p, err
		}
	}
	return p, nil
}

// Observe reads what a pass decides on: the parent, its start block, its
// steps (a template file, or its plan), its children (its sub-issues, the
// children the state records, and the issues the bot recently created
// carrying its marker) and their PRs, and the status comment's state.
func Observe(ctx context.Context, gh GitHub, opts Options) (*Pass, *githubv39.IssueComment, error) {
	parent, err := gh.GetIssue(ctx, opts.Issue)
	if err != nil {
		return nil, nil, err
	}
	if parent.IsPullRequest() {
		return nil, nil, fmt.Errorf("#%d is a pull request; a graph's parent is an issue", opts.Issue)
	}
	comments, err := gh.ListIssueComments(ctx, opts.Issue)
	if err != nil {
		return nil, nil, err
	}
	start, err := FindStart(parent.GetBody(), comments, opts.env())
	if err == nil && start == nil && implicitPlanStart(parent, comments, opts) {
		start = &Start{Source: SourcePlan}
	}
	p := &Pass{Start: start}
	if err == nil && start == nil {
		p.NoGraph = true
		return p, nil, nil
	}

	o := Observed{
		Parent:   Parent{Number: opts.Issue, Open: parent.GetState() == "open", Labels: labelNames(parent.Labels)},
		Trigger:  opts.Trigger,
		MaxDepth: opts.MaxDepth,
		Children: map[string][]Child{},
	}
	for _, a := range logins(parent.Assignees) {
		if isHuman(opts, a) {
			o.Parent.Humans = append(o.Parent.Humans, a)
		}
	}
	if m, ok := ParseChildMarker(parent.GetBody()); ok {
		o.Parent.Depth = m.Depth
	}
	var statusComment *githubv39.IssueComment
	for _, c := range comments {
		if IsStatusComment(c.GetBody()) && strings.EqualFold(c.GetUser().GetLogin(), opts.BotLogin) {
			statusComment = c
		}
	}
	if statusComment != nil {
		o.State, _ = ParseState(statusComment.GetBody())
	}
	if err != nil {
		o.SpecErr = err
		p.Observed = o
		return p, statusComment, nil
	}
	o.Source = start.SourceName()
	o.Params = start.Params
	o.Spec, p.Version, o.SpecErr = readSteps(ctx, gh, start, comments, opts)

	if !o.Parent.Open {
		o.Parent.ClosedBy = closedBy(ctx, gh, opts.Issue)
	}
	if err := observeChildren(ctx, gh, opts, &o); err != nil {
		return nil, nil, err
	}
	p.Observed = o
	return p, statusComment, nil
}

// implicitPlanStart is whether an issue with no start block is a graph
// started from its plan all the same: it carries the graph label - a
// maintainer's (labelling takes triage access) or the watch's on
// overseer/implement (.agents/watch/implement-graph.yaml) - and its plan
// comment has a Breakdown (PlanShape graph). The label is the approval.
func implicitPlanStart(parent *githubv39.Issue, comments []*githubv39.IssueComment, opts Options) bool {
	labels := labelNames(parent.Labels)
	if !containsFold(labels, opts.Trigger+"/graph") && !containsFold(labels, "overseer/graph") {
		return false
	}
	return PlanShape(comments, opts.env()) == ShapeGraph
}

// readSteps reads and validates the steps: a template's file on the
// default branch (with its params checked), or the issue's plan.
func readSteps(ctx context.Context, gh GitHub, start *Start, comments []*githubv39.IssueComment, opts Options) (*Spec, int, error) {
	if start.Source == SourcePlan {
		plan, err := PlanFromComments(comments, opts.env())
		switch {
		case err != nil:
			return nil, 0, err
		case plan == nil:
			return nil, 0, errors.New("the issue has no plan comment by a trusted author")
		case plan.Steps == nil:
			return nil, plan.Version, fmt.Errorf("plan v%d has no graph block (a one-PR plan)", plan.Version)
		}
		if err := plan.Steps.ValidatePlan(); err != nil {
			return nil, plan.Version, fmt.Errorf("plan v%d: %w", plan.Version, err)
		}
		return plan.Steps, plan.Version, nil
	}
	path := ".agents/graphs/" + start.Graph + ".yaml"
	data, _, err := gh.ReadFile(ctx, path)
	if err != nil {
		return nil, 0, fmt.Errorf("%s: %w", path, err)
	}
	t, err := ParseTemplate(data)
	if err != nil {
		return nil, 0, fmt.Errorf("%s: %w", path, err)
	}
	if err := t.Validate(); err != nil {
		return nil, 0, fmt.Errorf("%s: %w", path, err)
	}
	if err := t.Spec.checkParams(start.Params); err != nil {
		return nil, 0, err
	}
	return &t.Spec, 0, nil
}

// checkParams checks a start block's params against a template's: every
// required one given, nothing unknown, each satisfying its constraint;
// optional ones left out take their default.
func (s *Spec) checkParams(given map[string]string) error {
	for k := range given {
		if !slices.ContainsFunc(s.Params, func(p Param) bool { return p.Name == k }) {
			return fmt.Errorf("param %s is not one of the template's", k)
		}
	}
	for _, p := range s.Params {
		v, ok := given[p.Name]
		if !ok {
			if p.IsRequired() {
				return fmt.Errorf("param %s is required", p.Name)
			}
			continue
		}
		if err := p.Check(v); err != nil {
			return fmt.Errorf("param %s: %w", p.Name, err)
		}
	}
	return nil
}

// closedBy is who closed a closed issue: a commit (a PR's "Fixes #N"), or a
// person.
func closedBy(ctx context.Context, gh GitHub, number int) string {
	events, _, err := gh.ListIssueTimeline(ctx, number)
	if err != nil {
		return ClosedByPerson
	}
	for i := len(events) - 1; i >= 0; i-- {
		if events[i].GetEvent() == "closed" {
			if events[i].GetCommitID() != "" {
				return ClosedByCommit
			}
			return ClosedByPerson
		}
	}
	return ClosedByPerson
}

// observeChildren finds the parent's children: its sub-issues, the ones
// the state records, and the bot's recently created issues carrying its
// marker (a create whose link was lost). Each is read; an open one's merged
// bot PRs too.
func observeChildren(ctx context.Context, gh GitHub, opts Options, o *Observed) error {
	linked, err := gh.ListSubIssues(ctx, opts.Issue)
	if err != nil {
		return err
	}
	candidates := slices.Clone(linked)
	for _, n := range o.State.Children {
		candidates = append(candidates, n)
	}
	if opts.BotLogin != "" {
		recent, _, err := gh.ListIssues(ctx, &githubv39.IssueListByRepoOptions{
			Creator: opts.BotLogin, State: "all", Sort: "created", Direction: "desc",
			ListOptions: githubv39.ListOptions{PerPage: 100},
		})
		if err != nil {
			return err
		}
		for _, is := range recent {
			if m, ok := ParseChildMarker(is.GetBody()); ok && m.Parent == opts.Issue {
				candidates = append(candidates, is.GetNumber())
			}
		}
	}
	slices.Sort(candidates)
	candidates = slices.Compact(candidates)
	for _, n := range candidates {
		ref, err := gh.GetIssueRef(ctx, n)
		if err != nil {
			return err
		}
		m, ok := ParseChildMarker(ref.Body)
		if !ok || m.Parent != opts.Issue || ref.IsPR {
			continue // a sub-issue someone linked by hand: not the graph's
		}
		c := Child{
			Number:     n,
			Open:       ref.Open,
			NotPlanned: !ref.Open && ref.StateReason == "not_planned",
			Linked:     slices.Contains(linked, n),
			Labels:     ref.Labels,
			Assignees:  ref.Assignees,
		}
		if c.Open {
			refs, err := gh.ListCrossReferences(ctx, n)
			if err != nil {
				return err
			}
			for _, r := range refs {
				if r.IsPR && r.Merged && slices.ContainsFunc(opts.Pool, func(b string) bool { return strings.EqualFold(b, r.Author) }) {
					c.MergedBotPR = r.Number
				}
			}
		}
		o.Children[m.Step] = append(o.Children[m.Step], c)
	}
	return nil
}

// apply carries out the result's actions, in order. A child created is
// recorded in the state at once, so a later failure in the pass does not
// lose it.
func apply(ctx context.Context, gh GitHub, opts Options, p *Pass) error {
	for _, a := range p.Result.Actions {
		opts.logf("%s", describe(a, opts.DryRun))
		if opts.DryRun {
			continue
		}
		if err := applyOne(ctx, gh, opts, p, a); err != nil {
			return fmt.Errorf("%s: %w", describe(a, false), err)
		}
	}
	return nil
}

func applyOne(ctx context.Context, gh GitHub, opts Options, p *Pass, a Action) error {
	switch a.Kind {
	case ActCreateChild:
		n, err := gh.CreateIssue(ctx, a.Title, strings.TrimRight(a.Body, "\n")+"\n\n"+a.Child.Marker(), a.Labels)
		if err != nil {
			return err
		}
		if p.Result.State.Children == nil {
			p.Result.State.Children = map[string]int{}
		}
		p.Result.State.Children[a.Step] = n
		for i := range p.Result.Steps {
			if p.Result.Steps[i].ID == a.Step {
				p.Result.Steps[i].Child = n
			}
		}
		if len(a.Labels) > 0 {
			// Labels set at creation are dropped silently without push
			// access; adding them is the write that takes.
			if err := gh.AddLabels(ctx, n, a.Labels); err != nil {
				return err
			}
		}
		assignees := a.Assignees
		if len(assignees) == 0 && a.AskPerson {
			assignees = approverAssignees(ctx, gh, opts, p.Observed.Parent)
		}
		if len(assignees) > 0 {
			// Not the pass's failure: the child stands, and a login GitHub
			// will not assign is the plan's to fix.
			if err := gh.AddAssignees(ctx, n, assignees); err != nil {
				opts.logf("assigning #%d (step %s) to %s: %v", n, a.Step, strings.Join(assignees, ", "), err)
			}
		}
		return gh.AddSubIssue(ctx, opts.Issue, n)
	case ActLinkChild:
		return gh.AddSubIssue(ctx, opts.Issue, a.Issue)
	case ActCloseDuplicate:
		if err := gh.AddComment(ctx, a.Issue, fmt.Sprintf("Duplicate of #%d, this step's child.", a.Of)); err != nil {
			return err
		}
		return gh.SetIssueState(ctx, a.Issue, "closed", "not_planned")
	case ActAddChildLabels, ActAddParentLabels:
		return gh.AddLabels(ctx, a.Issue, a.Labels)
	case ActAddChildAssignees:
		// Not the pass's failure - the rest of the graph goes on - but not
		// forgotten either: the logins are left out of the state this pass
		// writes, so the next pass hands them down again.
		if err := gh.AddAssignees(ctx, a.Issue, a.Assignees); err != nil {
			opts.logf("assigning #%d (step %s) to %s, retried next pass: %v", a.Issue, a.Step, strings.Join(a.Assignees, ", "), err)
			p.Result.State.Inherited = slices.DeleteFunc(p.Result.State.Inherited, func(l string) bool { return containsFold(a.Assignees, l) })
		}
		return nil
	case ActCloseChildCompleted:
		if err := gh.AddComment(ctx, a.Issue, fmt.Sprintf("Completed by #%d.", a.Of)); err != nil {
			return err
		}
		return gh.SetIssueState(ctx, a.Issue, "closed", "completed")
	case ActReopenParent:
		if err := gh.SetIssueState(ctx, a.Issue, "open", ""); err != nil {
			return err
		}
		return gh.AddComment(ctx, a.Issue, "Reopened: this issue's graph still has steps to do. It closes when they are done.")
	case ActCloseParent:
		return gh.SetIssueState(ctx, a.Issue, "closed", "completed")
	}
	return fmt.Errorf("unknown action %q", a.Kind)
}

// describe is an action in a line, for the log.
func describe(a Action, dryRun bool) string {
	would := ""
	if dryRun {
		would = "would "
	}
	switch a.Kind {
	case ActCreateChild:
		assigned := ""
		switch {
		case len(a.Assignees) > 0:
			assigned = ", assigned to " + strings.Join(a.Assignees, ", ")
		case a.AskPerson:
			assigned = ", assigned to the graph's approver"
		}
		return fmt.Sprintf("%screate step %s's child %q, labelled %s%s", would, a.Step, a.Title, strings.Join(a.Labels, ", "), assigned)
	case ActLinkChild:
		return fmt.Sprintf("%slink #%d (step %s) as a sub-issue", would, a.Issue, a.Step)
	case ActCloseDuplicate:
		return fmt.Sprintf("%sclose #%d as a duplicate of #%d (step %s)", would, a.Issue, a.Of, a.Step)
	case ActAddChildLabels:
		return fmt.Sprintf("%slabel #%d (step %s) %s", would, a.Issue, a.Step, strings.Join(a.Labels, ", "))
	case ActAddChildAssignees:
		return fmt.Sprintf("%sassign #%d (step %s) to %s, from the parent", would, a.Issue, a.Step, strings.Join(a.Assignees, ", "))
	case ActAddParentLabels:
		return fmt.Sprintf("%slabel the parent #%d %s (checkpoint %s)", would, a.Issue, strings.Join(a.Labels, ", "), a.Step)
	case ActCloseChildCompleted:
		return fmt.Sprintf("%sclose #%d (step %s) as completed by #%d", would, a.Issue, a.Step, a.Of)
	case ActReopenParent:
		return fmt.Sprintf("%sreopen the parent #%d", would, a.Issue)
	case ActCloseParent:
		return fmt.Sprintf("%sclose the parent #%d as completed", would, a.Issue)
	}
	return would + a.Kind
}

// isHuman is whether login is a person's: neither the pool's bots, the
// watcher, nor an app.
func isHuman(opts Options, login string) bool {
	return login != "" && !strings.EqualFold(login, opts.BotLogin) && !strings.HasSuffix(login, "[bot]") && !containsFold(opts.Pool, login)
}

// approverAssignees is who an ask step's child goes to when neither its
// step nor the parent names a human: whoever approved the graph - the last
// human to label the parent <trigger>/implement or <trigger>/graph (the
// label takes triage access, so GitHub can assign them). None found, or
// the timeline unreadable, is no one: the label and the body still say it.
func approverAssignees(ctx context.Context, gh GitHub, opts Options, parent Parent) []string {
	human := func(login string) bool { return isHuman(opts, login) }
	events, _, err := gh.ListIssueTimeline(ctx, parent.Number)
	if err != nil {
		opts.logf("reading #%d's timeline for who approved its graph: %v", parent.Number, err)
		return nil
	}
	approvals := []string{opts.Trigger + "/implement", opts.Trigger + "/graph", "overseer/implement", "overseer/graph"}
	approver := ""
	for _, e := range events {
		if e.GetEvent() != "labeled" || !slices.ContainsFunc(approvals, func(l string) bool { return strings.EqualFold(l, e.GetLabel().GetName()) }) {
			continue
		}
		if login := e.GetActor().GetLogin(); human(login) {
			approver = login
		}
	}
	if approver == "" {
		return nil
	}
	return []string{approver}
}

func logins(users []*githubv39.User) []string {
	var out []string
	for _, u := range users {
		out = append(out, u.GetLogin())
	}
	return out
}

func labelNames(labels []*githubv39.Label) []string {
	var out []string
	for _, l := range labels {
		out = append(out, l.GetName())
	}
	return out
}

// Adopt binds an existing issue to a step of the parent's graph: it
// appends the step's marker to the issue's body and links it as a
// sub-issue. The step must be one of the graph's steps.
func Adopt(ctx context.Context, gh GitHub, opts Options, step string, issue int) error {
	p, _, err := Observe(ctx, gh, opts)
	switch {
	case err != nil:
		return err
	case p.NoGraph:
		return fmt.Errorf("#%d has no factory-graph start block: it is no graph's parent", opts.Issue)
	case p.Observed.SpecErr != nil:
		return p.Observed.SpecErr
	case p.Observed.Spec.Step(step) == nil:
		return fmt.Errorf("%s is not a step of #%d's graph", step, opts.Issue)
	}
	ref, err := gh.GetIssueRef(ctx, issue)
	if err != nil {
		return err
	}
	if ref.IsPR {
		return fmt.Errorf("#%d is a pull request; a step's child is an issue", issue)
	}
	if m, ok := ParseChildMarker(ref.Body); ok {
		return fmt.Errorf("#%d is already step %s of #%d", issue, m.Step, m.Parent)
	}
	info := ChildInfo{Parent: opts.Issue, Source: p.Start.SourceName(), Step: step, Depth: p.Observed.Parent.Depth + 1, Params: HashParams(p.Observed.Params)}
	opts.logf("%sadopt #%d as step %s of #%d", map[bool]string{true: "would ", false: ""}[opts.DryRun], issue, step, opts.Issue)
	if opts.DryRun {
		return nil
	}
	if err := gh.EditIssue(ctx, issue, ref.Title, strings.TrimRight(ref.Body, "\n")+"\n\n"+info.Marker()); err != nil {
		return err
	}
	return gh.AddSubIssue(ctx, opts.Issue, issue)
}

// StartGraph makes an issue a graph's parent: it checks the steps the
// start block names (a template and its params, or the issue's plan) and
// posts the block as a keyed comment (key graph). An issue that already has
// a start block is left as it is.
func StartGraph(ctx context.Context, gh GitHub, opts Options, start Start) error {
	if err := start.validate(); err != nil {
		return err
	}
	parent, err := gh.GetIssue(ctx, opts.Issue)
	if err != nil {
		return err
	}
	comments, err := gh.ListIssueComments(ctx, opts.Issue)
	if err != nil {
		return err
	}
	if existing, err := FindStart(parent.GetBody(), comments, opts.env()); err != nil || existing != nil {
		if existing != nil {
			opts.logf("#%d already has a start block (%s): nothing to do", opts.Issue, existing.SourceName())
		}
		return err
	}
	depth := 0
	if m, ok := ParseChildMarker(parent.GetBody()); ok {
		depth = m.Depth
	}
	maxDepth := opts.MaxDepth
	if maxDepth <= 0 {
		maxDepth = 3
	}
	if depth >= maxDepth {
		return fmt.Errorf("#%d is at depth %d; graphs nest at most %d deep", opts.Issue, depth, maxDepth)
	}
	if _, _, err := readSteps(ctx, gh, &start, comments, opts); err != nil {
		return err
	}
	opts.logf("%sstart #%d's graph from its %s", map[bool]string{true: "would ", false: ""}[opts.DryRun], opts.Issue, start.describe())
	if opts.DryRun {
		return nil
	}
	return gh.AddComment(ctx, opts.Issue, start.StartComment())
}
