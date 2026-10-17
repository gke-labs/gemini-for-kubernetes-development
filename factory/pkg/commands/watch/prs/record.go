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
	reset := investigationResetTime(history.comments, history.lastCommitTime, s.cfg.BotUsers, s.cfg.GitHubLogin, s.cfg.AllowlistedBots, s.cfg.TrustedLogins, s.cfg.TriggerLabel)
	s.rec.Fill(func(it *rules.Item) {
		it.ReviewedAtHead = review != nil
		it.ChangesRequested = review != nil && review.GetState() == "CHANGES_REQUESTED"
		it.LastTrustedActivity = activity
		it.HeadChangedAt = history.lastCommitTime
		// investigationResetTime is the latest of the head, a trusted human
		// and a pause; the rules take the latest of the ones they name.
		it.HumanSpokeAt = reset
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
