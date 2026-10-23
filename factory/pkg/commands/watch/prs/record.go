package prs

import (
	"context"
	"slices"
	"time"

	githubv39 "github.com/google/go-github/v39/github"
	"k8s.io/klog/v2"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/commands/watch/act"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/commands/watch/api"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/commands/watch/conventions"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/watch/rules"
)

// runHistory is the queue's record of the runs that ended, when it keeps
// one (the TaskQueueManager does).
type runHistory interface {
	EndedRuns(filename string) []api.EndedRun
}

// rulesEnv is the overseer's side of a rules decision, as the scanner sees
// it.
func (s *Scanner) rulesEnv() rules.Env {
	return rules.Env{
		Trigger:             s.cfg.TriggerLabel,
		Prefixes:            conventions.Prefixes(s.cfg.TriggerLabel),
		Watcher:             s.cfg.GitHubLogin,
		Pool:                s.cfg.BotUsers,
		PRInactivityTimeout: s.cfg.InactivityTimeout,
		MinNumber:           s.cfg.MinNumber,
		Now:                 time.Now().UTC(),
	}
}

// startRecording begins the rules record of a pull request: its labels as
// listed, before the label sync writes any. nil when not recording.
func (s *Scanner) startRecording(prIssue *githubv39.Issue) *rules.Recording {
	if s.recorder == nil {
		return nil
	}
	return s.recorder.Start("prs", s.rulesEnv(), prItemOf(prIssue))
}

// prItemOf is the facts the listing gives of a pull request.
func prItemOf(prIssue *githubv39.Issue) rules.Item {
	return rules.Item{
		Kind:      rules.OnPR,
		Number:    prIssue.GetNumber(),
		Labels:    labelNames(prIssue.Labels),
		Assignees: logins(prIssue.Assignees),
		Author:    prIssue.GetUser().GetLogin(),
		UpdatedAt: prIssue.GetUpdatedAt(),
		Closed:    prIssue.GetState() == "closed",
	}
}

// fillPR adds what the pull request itself says.
func fillPR(it *rules.Item, pr *githubv39.PullRequest) {
	it.Author, it.Head, it.Draft, it.Mergeable = pr.GetUser().GetLogin(), pr.GetHead().GetSHA(), pr.GetDraft(), pr.Mergeable
}

// recordCloses adds the issues the pull request closes to the record.
func (s *Scanner) recordCloses(ctx context.Context, refs *refIssues) {
	if s.rec == nil {
		return
	}
	closes := closesOf(ctx, refs)
	s.rec.Fill(func(it *rules.Item) { it.Closes = closes })
}

// closesOf are the issues the pull request closes, as the rules read them.
func closesOf(ctx context.Context, refs *refIssues) []rules.Closing {
	var closes []rules.Closing
	for _, ri := range refs.all(ctx) {
		closes = append(closes, rules.Closing{Number: ri.GetNumber(), Labels: labelNames(ri.Labels), Assignees: logins(ri.Assignees)})
	}
	return closes
}

// recordRuns adds the pull request's history, from the queue, to the
// record, and the rules whose task is queued or running.
func (s *Scanner) recordRuns(num int) {
	if s.rec == nil {
		return
	}
	s.rec.Fill(func(it *rules.Item) { s.fillRuns(it, num) })
}

// fillRuns adds the pull request's history from the queue: the runs of
// its tasks that ended, and the rules whose task is queued or running.
func (s *Scanner) fillRuns(it *rules.Item, num int) {
	h, _ := s.queue.(runHistory)
	for _, rule := range s.ruleTaskRules() {
		filename := api.RuleTaskFile(true, num, rule)
		var ended []rules.PastRun
		if h != nil {
			ended = pastRuns(h.EndedRuns(filename))
		}
		var lastStart, lastEnd time.Time
		failed, head := false, ""
		if last := s.queue.GetProcessedTask(filename); last != nil {
			lastStart, lastEnd, failed, head = last.StartedAt, last.CompletedAt, last.Status == api.StatusFailed, last.CommitSHA
		}
		it.Runs = append(it.Runs, rules.PastRuns(rule, ended, lastStart, lastEnd, failed, head)...)
		if s.queue.TaskExists(filename) {
			it.Queued = append(it.Queued, rule)
		}
	}
}

// recordHistory adds what the pull request's history says: the automated
// review of its head, its last trusted activity, and when a count since
// its head, a human or a pause starts.
func (s *Scanner) recordHistory(pr *githubv39.PullRequest, history *prHistory, headSHA string) {
	if s.rec == nil {
		return
	}
	s.rec.Fill(func(it *rules.Item) { s.fillHistory(it, pr, history, headSHA) })
}

// fillHistory adds what the pull request's history says (recordHistory).
func (s *Scanner) fillHistory(it *rules.Item, pr *githubv39.PullRequest, history *prHistory, headSHA string) {
	review := s.automatedReviewOfHead(history.reviews, headSHA, history.lastCommitTime)
	it.ReviewedAtHead = review != nil
	it.ChangesRequested = review != nil && review.GetState() == "CHANGES_REQUESTED"
	it.LastTrustedActivity = getLastPRActivityTime(pr, history.comments, history.reviews, history.revCommentsMap, s.cfg.GitHubLogin, s.cfg.AllowlistedBots, s.cfg.TrustedLogins, s.cfg.TriggerLabel)
	it.HeadChangedAt = history.lastCommitTime
	it.HumanSpokeAt = conventions.HumanSpokeAt(history.comments, s.cfg.BotUsers, s.cfg.GitHubLogin, s.cfg.AllowlistedBots, s.cfg.TrustedLogins, s.cfg.TriggerLabel)
	it.PausedAt = conventions.PausedAt(history.comments, s.cfg.BotUsers, s.cfg.GitHubLogin, s.cfg.TriggerLabel)
}

// fillChecks adds the head's checks, and whether the pull request is
// approved: read only when it merges (evaluate).
func fillChecks(it *rules.Item, a prCheckAnalysis, approved bool) {
	it.Approved = approved
	switch {
	case a.hasFailure:
		it.Checks = "failing"
	case a.hasPending:
		it.Checks = "pending"
	default:
		it.Checks = "green"
	}
}

// pastRuns are the queue's ended runs as the rules read them.
func pastRuns(ended []api.EndedRun) []rules.PastRun {
	out := make([]rules.PastRun, 0, len(ended))
	for _, r := range ended {
		out = append(out, rules.PastRun{StartedAt: r.StartedAt, Failed: r.Failed})
	}
	return out
}

func labelNames(labels []*githubv39.Label) []string {
	out := make([]string, 0, len(labels))
	for _, l := range labels {
		out = append(out, l.GetName())
	}
	return out
}

func logins(users []*githubv39.User) []string {
	out := make([]string, 0, len(users))
	for _, u := range users {
		out = append(out, u.GetLogin())
	}
	return out
}

// engineDecide is the engine's decisions on the pull request being
// evaluated, from the facts recorded so far.
func (s *Scanner) engineDecide() []rules.Decision {
	return s.rules.Decide(*s.rec.Item, s.rulesEnv())
}

// engineOutcome is the outcome of the engine's decision on rule among the
// pull request's pr-care decisions, "" when it decided none.
func (s *Scanner) engineOutcome(rule string) string {
	for _, d := range s.engineCare {
		if d.Rule == rule {
			return d.Outcome
		}
	}
	return ""
}

// engineCareRule is the rule the engine decided that takes the PR and
// runs a recipe (a pr-care rule, factory's or a repository's), "" when it
// decided none: its run is a task, its give-up its writes. A rule of
// writes alone, taking the PR or not, is the write walk's (actOnWrites).
func (s *Scanner) engineCareRule() string {
	for _, d := range s.engineCare {
		if r := s.rules.Set().Named(d.Rule); r != nil && r.IsExclusive() && r.Run.Recipe != "" {
			return d.Rule
		}
	}
	return ""
}

// ruleTaskRules are the rules on pull requests that run a recipe: each
// runs as a rule task, task-pr-<n>-rule-<name>.yaml.
func (s *Scanner) ruleTaskRules() []string {
	var names []string
	for _, r := range s.rules.Set() {
		if r.On == rules.OnPR && r.Run.Recipe != "" {
			names = append(names, r.Name)
		}
	}
	return names
}

// read notes that the evaluation read fact: the rules may decide on it.
func (s *Scanner) read(fact string) {
	s.rec.Fill(func(it *rules.Item) {
		it.Unknown = slices.DeleteFunc(it.Unknown, func(f string) bool { return f == fact })
	})
}

// actOnSettled carries out the writes the rules decide on the facts read
// so far (rules.Set.Settled: up to the first rule a fact not read yet could
// change), each once, and reports whether the PR now has the stop label -
// written, or inherited from a closing issue - which ends the evaluation.
func (s *Scanner) actOnSettled(ctx context.Context, prIssue *githubv39.Issue) bool {
	if s.rec == nil {
		return false
	}
	var writes []rules.Decision
	for _, d := range s.rules.Set().Settled(*s.rec.Item, s.rulesEnv()) {
		// A rule that takes the PR is decided on every fact (engineCare).
		if r := s.rules.Set().Named(d.Rule); r != nil && !r.IsExclusive() {
			writes = append(writes, d)
		}
	}
	s.actOnWrites(ctx, prIssue, writes)
	if !conventions.HasStopLabel(prIssue.Labels, s.cfg.TriggerLabel) {
		return false
	}
	num := prIssue.GetNumber()
	klog.Infof("Skipping PR #%d because the rules gave it the stop label ('overseer/stop' or '%s/stop')", num, s.cfg.TriggerLabel)
	_ = s.queue.RemovePendingTasksForNumber(num)
	return true
}

// actOnWrites carries out the decisions of rules of writes alone among
// decided, in order, those not carried out yet.
func (s *Scanner) actOnWrites(ctx context.Context, prIssue *githubv39.Issue, decided []rules.Decision) {
	for _, d := range decided {
		if r := s.rules.Set().Named(d.Rule); r == nil || r.Run.Recipe != "" || s.acted[d.Rule] {
			continue
		}
		s.actOn(ctx, prIssue, d.Rule)
	}
}

// actOn carries out the engine's decision on rule for the pull request
// being evaluated, if it made one: its writes, the PR's labels brought up
// to date with them (later steps read them: an inherited stop label stops
// the PR), its queued tasks dropped on a stop, and the decision recorded.
// It reports whether there was one.
func (s *Scanner) actOn(ctx context.Context, prIssue *githubv39.Issue, rule string) bool {
	var d rules.Decision
	found := false
	for _, e := range s.engineDecide() {
		if e.Rule == rule {
			d, found = e, true
			break
		}
	}
	if !found {
		return false
	}
	num := prIssue.GetNumber()
	if s.acted != nil {
		s.acted[rule] = true
	}
	klog.Infof("PR #%d: the rules engine decides %s %s: %+v", num, rule, d.Outcome, d.Writes)
	s.rec.Decided(rule, d.Outcome)
	done := act.ApplyWrites(ctx, s.gh, num, d.Writes, s.cfg.TriggerLabel, s.cfg.DryRun)
	for _, l := range done.Labeled {
		prIssue.Labels = append(prIssue.Labels, &githubv39.Label{Name: githubv39.String(l)})
	}
	prIssue.Labels = slices.DeleteFunc(prIssue.Labels, func(l *githubv39.Label) bool { return slices.Contains(done.Unlabeled, l.GetName()) })
	if (d.Writes.Stop || d.Writes.Drop) && !s.cfg.DryRun {
		_ = s.queue.RemovePendingTasksForNumber(num)
	}
	return true
}

// stampDecision puts the engine's run decision on rule on the task the
// handler built: the runner then runs the rule's recipe, apply and inputs
// (act.Task).
func (s *Scanner) stampDecision(task *api.QueueTask, rule string) {
	for _, d := range s.engineCare {
		if d.Rule == rule {
			act.Task(task, d, s.rules.Set().Named(rule))
			return
		}
	}
}
