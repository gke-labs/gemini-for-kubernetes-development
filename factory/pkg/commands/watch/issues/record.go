package issues

import (
	"context"
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

// issueRuleFile is the task file of rule's task on issue num, "" for a
// rule whose task it has none of: a workflow's step's is named after the
// workflow its body names (none named, no task); any other rule's is its
// rule task's (api.RuleTaskFile, the fix's included).
func issueRuleFile(r *rules.Rule, num int, workflowName string) string {
	if r.Run.Recipe == "workflow" {
		if workflowName == "" {
			return ""
		}
		return issueTaskFilename(num, workflowName)
	}
	return api.RuleTaskFile(false, num, r.Name)
}

// fillIssueRuns adds the issue's history from the queue for every rule on
// issues that runs a recipe: the runs of its task that ended, and the rules
// whose task is queued or running.
func (s *Scanner) fillIssueRuns(it *rules.Item, num int, workflowName string) {
	for _, r := range s.rules.Set() {
		if r.On != rules.OnIssue || r.Run.Recipe == "" {
			continue
		}
		filename := issueRuleFile(r, num, workflowName)
		if filename == "" {
			continue
		}
		s.fillRuns(it, r.Name, filename)
		if s.queue.TaskExists(filename) {
			it.Queued = append(it.Queued, r.Name)
		}
	}
}

// noteCooldowns notes, on the queue for the overseer's work view, each
// rule on the issue its policy holds back until a time (a cooldown after
// its last run), and logs it once per window.
func (s *Scanner) noteCooldowns(issue *githubv39.Issue, workflowName string) {
	if s.rec == nil {
		return
	}
	num := issue.GetNumber()
	for _, r := range s.rules.Set() {
		if r.On != rules.OnIssue || r.Run.Recipe == "" {
			continue
		}
		filename := issueRuleFile(r, num, workflowName)
		last := s.queue.GetProcessedTask(filename)
		if filename == "" || last == nil {
			continue
		}
		why, until := s.rules.Set().Held(r.Name, *s.rec.Item, s.rulesEnv())
		if why == "" || until.IsZero() {
			continue
		}
		held := api.Cooldown{FileName: filename, Number: num, URL: issue.GetHTMLURL(), Until: until, LastStatus: last.Status, LastCompletedAt: last.CompletedAt}
		if r.Run.Recipe == "workflow" {
			held.Workflow = workflowName
		}
		if s.queue.NoteCooldown(held) {
			klog.Infof("Skipping issue #%d: the rules engine holds %s back: %s (until %s)", num, r.Name, why, until.UTC().Format(time.RFC3339))
		}
	}
}
