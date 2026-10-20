package issues

import (
	"context"
	"fmt"
	"slices"
	"time"

	githubv39 "github.com/google/go-github/v39/github"
	"k8s.io/klog/v2"

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
	var ended []rules.PastRun
	if h, ok := s.queue.(runHistory); ok {
		ended = pastRuns(h.EndedRuns(filename))
	}
	var lastStart, lastEnd time.Time
	failed := false
	if last := s.queue.GetProcessedTask(filename); last != nil {
		lastStart, lastEnd, failed = last.StartedAt, last.CompletedAt, last.Status == api.StatusFailed
	}
	runs := rules.PastRuns(rule, ended, lastStart, lastEnd, failed, "")
	s.rec.Fill(func(it *rules.Item) { it.Runs = append(it.Runs, runs...) })
}

// recordSpoken adds when a trusted human last spoke on the issue and when
// the watcher last paused it: where a give-up's count starts. Only an issue
// with a failed run has a count to start; the others are spared reading
// its comments.
func (s *Scanner) recordSpoken(ctx context.Context, num int) {
	if s.rec == nil || !slices.ContainsFunc(s.rec.Item.Runs, func(r rules.PastRun) bool { return r.Failed }) {
		return
	}
	comments, err := s.gh.ListIssueComments(ctx, num)
	if err != nil {
		klog.Warningf("Reading the comments of issue #%d for its give-up: %v", num, err)
		return
	}
	human := conventions.HumanSpokeAt(comments, s.cfg.BotUsers, s.cfg.GitHubLogin, s.cfg.AllowlistedBots, s.cfg.TrustedLogins, s.cfg.TriggerLabel)
	paused := conventions.PausedAt(comments, s.cfg.BotUsers, s.cfg.GitHubLogin, s.cfg.TriggerLabel)
	s.rec.Fill(func(it *rules.Item) { it.HumanSpokeAt, it.PausedAt = human, paused })
}

// decisionOf is the decision on rule among ds, if there is one.
func decisionOf(ds []rules.Decision, rule string) (rules.Decision, bool) {
	for _, d := range ds {
		if d.Rule == rule {
			return d, true
		}
	}
	return rules.Decision{}, false
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

// Record evaluates one issue, as a scan would, and returns the facts its
// evaluation recorded: what the rules decide on (factory watch rules
// explain). It lists the open pull requests itself, as the issue scan reads
// them from the cache, to tell whether one closes the issue. The scanner
// should be dry-run: what it would write or queue is no part of the record,
// and it takes the scanner's recorder for the evaluation.
func (s *Scanner) Record(ctx context.Context, num int) (*rules.Record, error) {
	issue, err := s.gh.GetIssue(ctx, num)
	if err != nil {
		return nil, err
	}
	if issue.PullRequestLinks != nil {
		return nil, fmt.Errorf("#%d is a pull request, not an issue", num)
	}
	prs, err := s.gh.ListOpenPRs(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing open pull requests: %w", err)
	}
	s.entities.UpdateOpenPRs(prs)
	var got *rules.Record
	s.recorder = rules.NewSinkRecorder(func(rec rules.Record) { got = &rec })
	s.queueTask(ctx, issue, s.entities.GetReferencedIssuesMap())
	if got == nil {
		return nil, fmt.Errorf("#%d left no record (below the scanner's number floor?)", num)
	}
	return got, nil
}
