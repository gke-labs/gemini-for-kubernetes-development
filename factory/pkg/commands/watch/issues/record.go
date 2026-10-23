package issues

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
		Trigger:   s.cfg.TriggerLabel,
		Prefixes:  conventions.Prefixes(s.cfg.TriggerLabel),
		Watcher:   s.cfg.GitHubLogin,
		Pool:      s.cfg.BotUsers,
		MinNumber: s.cfg.MinNumber,
		Now:       time.Now().UTC(),
	}
}

// startRecording begins the rules record of an issue: the facts the
// scanner has before it decides. nil when not recording.
func (s *Scanner) startRecording(issue *githubv39.Issue) *rules.Recording {
	if s.recorder == nil {
		return nil
	}
	return s.recorder.Start("issues", s.rulesEnv(), s.issueItemOf(issue))
}

// issueItemOf is the facts the listing gives of an issue.
func (s *Scanner) issueItemOf(issue *githubv39.Issue) rules.Item {
	return rules.Item{
		Kind:         rules.OnIssue,
		Number:       issue.GetNumber(),
		Labels:       labelNames(issue.Labels),
		Assignees:    logins(issue.Assignees),
		Author:       issue.GetUser().GetLogin(),
		UpdatedAt:    issue.GetUpdatedAt(),
		Closed:       issue.GetState() == "closed",
		FanoutParent: conventions.HasFanoutLabel(issue.Labels, s.cfg.TriggerLabel),
	}
}

// recordRuns adds the rule's history of the issue, from the queue, to the
// record.
func (s *Scanner) recordRuns(rule, filename string) {
	if s.rec == nil {
		return
	}
	s.rec.Fill(func(it *rules.Item) { s.fillRuns(it, rule, filename) })
}

// fillRuns adds the rule's history of the issue from the queue: the runs
// of its task that ended.
func (s *Scanner) fillRuns(it *rules.Item, rule, filename string) {
	var ended []rules.PastRun
	if h, ok := s.queue.(runHistory); ok {
		ended = pastRuns(h.EndedRuns(filename))
	}
	var lastStart, lastEnd time.Time
	failed := false
	if last := s.queue.GetProcessedTask(filename); last != nil {
		lastStart, lastEnd, failed = last.StartedAt, last.CompletedAt, last.Status == api.StatusFailed
	}
	it.Runs = append(it.Runs, rules.PastRuns(rule, ended, lastStart, lastEnd, failed, "")...)
}

// recordSpoken adds when a trusted human last spoke on the issue and when
// the watcher last paused it: where a give-up's count starts. Only an issue
// with a failed run has a count to start; the others are spared reading
// its comments.
func (s *Scanner) recordSpoken(ctx context.Context, num int) {
	if s.rec == nil {
		return
	}
	if err := s.fillSpoken(ctx, s.rec.Item, num); err != nil {
		klog.Warningf("Reading the comments of issue #%d for its give-up: %v", num, err)
	}
}

// fillSpoken adds when a trusted human last spoke on the issue and when
// the watcher last paused it (recordSpoken), when it has a failed run.
func (s *Scanner) fillSpoken(ctx context.Context, it *rules.Item, num int) error {
	if !slices.ContainsFunc(it.Runs, func(r rules.PastRun) bool { return r.Failed }) {
		return nil
	}
	comments, err := s.gh.ListIssueComments(ctx, num)
	if err != nil {
		return err
	}
	it.HumanSpokeAt = conventions.HumanSpokeAt(comments, s.cfg.BotUsers, s.cfg.GitHubLogin, s.cfg.AllowlistedBots, s.cfg.TrustedLogins, s.cfg.TriggerLabel)
	it.PausedAt = conventions.PausedAt(comments, s.cfg.BotUsers, s.cfg.GitHubLogin, s.cfg.TriggerLabel)
	return nil
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

// ruleTaskRules are the rules on issues that run a recipe and that factory
// does not handle by name (a repository's): each runs as a rule task,
// task-issue-<n>-rule-<name>.yaml.
func (s *Scanner) ruleTaskRules() []string {
	var names []string
	for _, r := range s.rules.Set() {
		if r.On == rules.OnIssue && r.Run.Recipe != "" && !rules.IsBuiltinName(r.Name) {
			names = append(names, r.Name)
		}
	}
	return names
}

// fillRuleTaskRuns adds the history of the issue's rule tasks, and the
// rules whose task is queued or running.
func (s *Scanner) fillRuleTaskRuns(it *rules.Item, num int) {
	for _, rule := range s.ruleTaskRules() {
		filename := api.RuleTaskFile(false, num, rule)
		s.fillRuns(it, rule, filename)
		if s.queue.TaskExists(filename) {
			it.Queued = append(it.Queued, rule)
		}
	}
}

// carryOutRuleTasks carries out what the engine decides on the issue of
// the rules factory does not handle by name (a repository's,
// .agents/watch), and reports whether one took the issue, leaving nothing
// for the fix or workflow rule: a give-up stops the issue; a run queues the
// rule's task (its recipe, apply and inputs, act.Task) in
// api.RuleTaskFile; GitHub writes are made, and take the issue when the
// rule is exclusive; a run is carried out as fix's is (queueIssueRuleTask).
// With a task of the issue queued or running, nothing: the fix's path
// holds the issue back (Item.TaskActive).
func (s *Scanner) carryOutRuleTasks(ctx context.Context, issue *githubv39.Issue) bool {
	if s.rec == nil || !s.hasOtherIssueRules() {
		return false
	}
	num := issue.GetNumber()
	s.rec.Fill(func(it *rules.Item) { s.fillRuleTaskRuns(it, num) })
	if s.queue.HasActiveIssueTask(num) {
		return false
	}
	s.recordSpoken(ctx, num)
	for _, d := range s.rules.Decide(*s.rec.Item, s.rulesEnv()) {
		r := s.rules.Set().Named(d.Rule)
		if r == nil || rules.IsBuiltinName(d.Rule) {
			continue
		}
		if d.Outcome == rules.OutcomeGiveUp || r.Run.Recipe == "" {
			s.rec.Decided(d.Rule, d.Outcome)
			klog.Infof("Issue #%d: the rules engine decides %s %s: %+v", num, d.Rule, d.Outcome, d.Writes)
			act.ApplyWrites(ctx, s.gh, num, d.Writes, s.cfg.TriggerLabel, s.cfg.DryRun)
			if (d.Writes.Stop || d.Writes.Drop) && !s.cfg.DryRun {
				_ = s.queue.RemovePendingTasksForNumber(num)
			}
			if d.Outcome == rules.OutcomeGiveUp || r.IsExclusive() {
				return true
			}
			continue
		}
		s.queueIssueRuleTask(ctx, issue, d.Rule, issueTaskShape{}, nil)
		return true
	}
	return false
}

// hasOtherIssueRules reports whether the set has a rule on issues that
// factory does not handle by name.
func (s *Scanner) hasOtherIssueRules() bool {
	for _, r := range s.rules.Set() {
		if r.On == rules.OnIssue && !rules.IsBuiltinName(r.Name) {
			return true
		}
	}
	return false
}
