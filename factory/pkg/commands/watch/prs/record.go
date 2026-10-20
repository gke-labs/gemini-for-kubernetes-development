package prs

import (
	"context"
	"fmt"
	"time"

	githubv39 "github.com/google/go-github/v39/github"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/commands/watch/api"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/commands/watch/conventions"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/watch/rules"
)

// runHistory is the queue's record of the runs that ended, when it keeps
// one (the TaskQueueManager does).
type runHistory interface {
	RunStarts(filename string) []time.Time
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
		Now:                 time.Now().UTC(),
	}
}

// startRecording begins the rules record of a pull request: its labels as
// listed, before the label sync writes any. nil when not recording.
func (s *Scanner) startRecording(prIssue *githubv39.Issue) *rules.Recording {
	if s.recorder == nil {
		return nil
	}
	it := rules.Item{
		Kind:      rules.OnPR,
		Number:    prIssue.GetNumber(),
		Labels:    labelNames(prIssue.Labels),
		Assignees: logins(prIssue.Assignees),
		Author:    prIssue.GetUser().GetLogin(),
		UpdatedAt: prIssue.GetUpdatedAt(),
	}
	return s.recorder.Start("prs", s.rulesEnv(), it)
}

// recordCloses adds the issues the pull request closes to the record.
func (s *Scanner) recordCloses(ctx context.Context, refs *refIssues) {
	if s.rec == nil {
		return
	}
	var closes []rules.Closing
	for _, ri := range refs.all(ctx) {
		closes = append(closes, rules.Closing{Number: ri.GetNumber(), Labels: labelNames(ri.Labels), Assignees: logins(ri.Assignees)})
	}
	s.rec.Fill(func(it *rules.Item) { it.Closes = closes })
}

// recordRuns adds the pull request's history, from the queue, to the
// record, and the rules whose task is queued or running.
func (s *Scanner) recordRuns(num int) {
	if s.rec == nil {
		return
	}
	h, _ := s.queue.(runHistory)
	for _, t := range prTaskRules {
		filename := fmt.Sprintf("task-pr-%d-%s.yaml", num, t.suffix)
		var starts []time.Time
		if h != nil {
			starts = h.RunStarts(filename)
		}
		var lastStart, lastEnd time.Time
		failed, head := false, ""
		if last := s.queue.GetProcessedTask(filename); last != nil {
			lastStart, lastEnd, failed, head = last.StartedAt, last.CompletedAt, last.Status == api.StatusFailed, last.CommitSHA
		}
		runs := rules.PastRuns(t.rule, starts, lastStart, lastEnd, failed, head)
		queued := s.queue.TaskExists(filename)
		s.rec.Fill(func(it *rules.Item) {
			it.Runs = append(it.Runs, runs...)
			if queued {
				it.Queued = append(it.Queued, t.rule)
			}
		})
	}
}

// recordHistory adds what the pull request's history says: the automated
// review of its head, its last trusted activity, and when a count since
// its head, a human or a pause starts.
func (s *Scanner) recordHistory(pr *githubv39.PullRequest, history *prHistory, headSHA string) {
	if s.rec == nil {
		return
	}
	review := s.automatedReviewOfHead(history.reviews, headSHA, history.lastCommitTime)
	activity := getLastPRActivityTime(pr, history.comments, history.reviews, history.revCommentsMap, s.cfg.GitHubLogin, s.cfg.AllowlistedBots, s.cfg.TrustedLogins, s.cfg.TriggerLabel)
	human := humanSpokeAt(history.comments, s.cfg.BotUsers, s.cfg.GitHubLogin, s.cfg.AllowlistedBots, s.cfg.TrustedLogins, s.cfg.TriggerLabel)
	paused := pausedAt(history.comments, s.cfg.BotUsers, s.cfg.GitHubLogin, s.cfg.TriggerLabel)
	s.rec.Fill(func(it *rules.Item) {
		it.ReviewedAtHead = review != nil
		it.ChangesRequested = review != nil && review.GetState() == "CHANGES_REQUESTED"
		it.LastTrustedActivity = activity
		it.HeadChangedAt = history.lastCommitTime
		it.HumanSpokeAt = human
		it.PausedAt = paused
	})
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
	return s.rec != nil && s.rules.Takes(rule, labelNames(prIssue.Labels))
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
// scanner's own condition. The handler it leads to keeps its own checks
// and outside gates (a busy sandbox).
func (s *Scanner) picks(rule string, prIssue *githubv39.Issue, legacy bool) bool {
	if s.rules.Off(rule) {
		return false
	}
	if s.takes(rule, prIssue) {
		return rules.Decided(s.engineCare, rule)
	}
	return legacy
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

// Record evaluates one pull request, as a scan would, and returns the
// facts its evaluation recorded: what the rules decide on (factory watch
// rules explain). The scanner should be dry-run: what it would write or
// queue is no part of the record, and it takes the scanner's recorder for
// the evaluation.
func (s *Scanner) Record(ctx context.Context, num int) (*rules.Record, error) {
	issue, err := s.gh.GetIssue(ctx, num)
	if err != nil {
		return nil, err
	}
	if issue.PullRequestLinks == nil {
		return nil, fmt.Errorf("#%d is an issue, not a pull request", num)
	}
	var got *rules.Record
	s.recorder = rules.NewSinkRecorder(func(rec rules.Record) { got = &rec })
	s.evaluate(ctx, issue)
	if got == nil {
		return nil, fmt.Errorf("#%d left no record (below the scanner's number floor?)", num)
	}
	return got, nil
}
