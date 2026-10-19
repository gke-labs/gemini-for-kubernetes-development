package issues

import (
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

// rulesEnv is the overseer's side of a rules decision, as the scanner sees
// it.
func (s *Scanner) rulesEnv() rules.Env {
	return rules.Env{
		Trigger:  s.cfg.TriggerLabel,
		Prefixes: conventions.Prefixes(s.cfg.TriggerLabel),
		Watcher:  s.cfg.GitHubLogin,
		Pool:     s.cfg.BotUsers,
		Now:      time.Now().UTC(),
	}
}

// startRecording begins the rules record of an issue: the facts the
// scanner has before it decides. nil when not recording.
func (s *Scanner) startRecording(issue *githubv39.Issue) *rules.Recording {
	if s.recorder == nil {
		return nil
	}
	it := rules.Item{
		Kind:         rules.OnIssue,
		Number:       issue.GetNumber(),
		Labels:       labelNames(issue.Labels),
		Assignees:    logins(issue.Assignees),
		Author:       issue.GetUser().GetLogin(),
		UpdatedAt:    issue.GetUpdatedAt(),
		FanoutParent: conventions.HasFanoutLabel(issue.Labels, s.cfg.TriggerLabel),
	}
	return s.recorder.Start("issues", s.rulesEnv(), it)
}

// recordRuns adds the rule's history of the issue, from the queue, to the
// record.
func (s *Scanner) recordRuns(rule, filename string) {
	if s.rec == nil {
		return
	}
	var starts []time.Time
	if h, ok := s.queue.(runHistory); ok {
		starts = h.RunStarts(filename)
	}
	var lastStart, lastEnd time.Time
	failed := false
	if last := s.queue.GetProcessedTask(filename); last != nil {
		lastStart, lastEnd, failed = last.StartedAt, last.CompletedAt, last.Status == api.StatusFailed
	}
	runs := rules.PastRuns(rule, starts, lastStart, lastEnd, failed, "")
	s.rec.Fill(func(it *rules.Item) { it.Runs = append(it.Runs, runs...) })
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
