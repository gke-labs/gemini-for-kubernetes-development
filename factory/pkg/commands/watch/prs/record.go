package prs

import (
	"context"
	"fmt"
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

// prTaskRules are the rules a PR's task files run, by their suffix.
var prTaskRules = []struct{ suffix, rule string }{
	{"comments", "comments"},
	{"iterate", "conflict"},
	{"investigate", "ci"},
	{"review", "review"},
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
	for _, t := range prTaskRules {
		filename := fmt.Sprintf("task-pr-%d-%s.yaml", num, t.suffix)
		var ended []rules.PastRun
		if h != nil {
			ended = pastRuns(h.EndedRuns(filename))
		}
		var lastStart, lastEnd time.Time
		failed, head := false, ""
		if last := s.queue.GetProcessedTask(filename); last != nil {
			lastStart, lastEnd, failed, head = last.StartedAt, last.CompletedAt, last.Status == api.StatusFailed, last.CommitSHA
		}
		it.Runs = append(it.Runs, rules.PastRuns(t.rule, ended, lastStart, lastEnd, failed, head)...)
		if s.queue.TaskExists(filename) {
			it.Queued = append(it.Queued, t.rule)
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

// prCareRules are the pr-care group's rules, in the scanner's order.
var prCareRules = []string{"comments", "conflict", "ci", "review", "ready", "ready-unreview"}

// engineDecide is the engine's decisions on the pull request being
// evaluated, from the facts recorded so far; nil without a record (the
// engine needs the facts the record holds).
func (s *Scanner) engineDecide() []rules.Decision {
	if s.rec == nil {
		return nil
	}
	return s.rules.Decide(*s.rec.Item, s.rulesEnv())
}

// takes is whether the engine decides rule for the scanner on the pull
// request: the engine on, the rule handed to it, the canary label on the
// pull request when there is one, and the facts recorded.
func (s *Scanner) takes(rule string, prIssue *githubv39.Issue) bool {
	return s.rec != nil && s.rules.Takes(rule)
}

// decides is whether the scanner goes on to act on rule, a rule of its own
// step (inherit-labels, inactive): never when the rule is disabled; on the
// engine's decision when the engine decides it; else as the scanner would
// (legacy), whose own checks follow.
func (s *Scanner) decides(rule string, prIssue *githubv39.Issue, legacy bool) bool {
	if s.rules.Off(rule) {
		return false
	}
	if s.takes(rule, prIssue) {
		return rules.Decided(s.engineDecide(), rule)
	}
	return legacy
}

// picks is a pr-care case's condition: never when its rule is disabled; the
// engine's decision (engineCare) when the engine decides the rule; else the
// scanner's own condition. The handler it leads to keeps its outside gates
// (a busy sandbox). An engine's give-up leads only to a handler that gives
// up on it (ci's, given the outcome by engineOutcome); any other handler
// would run instead.
func (s *Scanner) picks(rule string, prIssue *githubv39.Issue, legacy bool) bool {
	if s.rules.Off(rule) {
		return false
	}
	if s.takes(rule, prIssue) {
		// The engine's give-up was acted on before the cases
		// (engineGivesUp): only its run takes a case.
		return s.engineOutcome(rule, prIssue) == rules.OutcomeRun
	}
	return legacy
}

// engineOutcome is the engine's decision on rule for the pull request when
// it decides the rule for the scanner: its outcome, or "" when the engine
// does not decide the rule or decided nothing.
func (s *Scanner) engineOutcome(rule string, prIssue *githubv39.Issue) string {
	if !s.takes(rule, prIssue) {
		return ""
	}
	for _, d := range s.engineCare {
		if d.Rule == rule {
			return d.Outcome
		}
	}
	return ""
}

// recordHandedStep notes, when the engine decides rule for the scanner on
// the pull request, what the scanner's own condition said: legacy.
func (s *Scanner) recordHandedStep(rule string, prIssue *githubv39.Issue, legacy bool) {
	if s.takes(rule, prIssue) {
		s.rec.Handed(rule, legacy)
	}
}

// recordHanded notes, for each pr-care rule the engine decides for the
// scanner, whether the scanner's own pick (legacyPick, the first case its
// conditions take, "" for none) was that rule: what the record compares
// once Decided is the engine's.
func (s *Scanner) recordHanded(prIssue *githubv39.Issue, legacyPick string) {
	for _, rule := range prCareRules {
		if rule == "ready" || rule == "ready-unreview" {
			continue
		}
		if s.takes(rule, prIssue) {
			s.rec.Handed(rule, legacyPick == rule)
		}
	}
}

// picksReadiness is whether readiness is reconciled: not when ready is
// disabled (both its halves go with it); when the engine decides ready, on
// its deciding ready or ready-unreview; else always, as before.
func (s *Scanner) picksReadiness(prIssue *githubv39.Issue) bool {
	if s.rules.Off("ready") {
		return false
	}
	if s.takes("ready", prIssue) {
		return rules.Decided(s.engineCare, "ready") || rules.Decided(s.engineCare, "ready-unreview")
	}
	return true
}

// engineActs is whether the engine's decision on rule is carried out as
// its writes (act.ApplyWrites) rather than by the scanner's handler: the
// rule not disabled, and the engine deciding it for the scanner.
func (s *Scanner) engineActs(rule string, prIssue *githubv39.Issue) bool {
	return !s.rules.Off(rule) && s.takes(rule, prIssue)
}

// engineCareRule is the pr-care rule the engine decided, the first: they
// are exclusive, so at most one of comments, conflict, ci and review.
func (s *Scanner) engineCareRule() string {
	for _, d := range s.engineCare {
		if slices.Contains(prCareRules[:4], d.Rule) {
			return d.Rule
		}
	}
	return ""
}

// engineGivesUp is whether the pr-care rule the engine decided for the
// scanner gives up.
func (s *Scanner) engineGivesUp(prIssue *githubv39.Issue) bool {
	rule := s.engineCareRule()
	return rule != "" && s.engineActs(rule, prIssue) && s.engineOutcome(rule, prIssue) == rules.OutcomeGiveUp
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

// stampDecision puts the engine's run decision on rule, when the engine
// decides it for the scanner, on the task the handler built: the runner
// then runs the rule's recipe, apply and inputs (act.Task).
func (s *Scanner) stampDecision(task *api.QueueTask, rule string, prIssue *githubv39.Issue) {
	if !s.engineActs(rule, prIssue) {
		return
	}
	for _, d := range s.engineCare {
		if d.Rule == rule {
			act.Task(task, d, s.rules.Set().Named(rule))
			return
		}
	}
}
