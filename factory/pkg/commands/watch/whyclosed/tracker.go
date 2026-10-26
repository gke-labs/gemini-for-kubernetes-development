package whyclosed

import (
	"context"
	"fmt"
	"strings"
	"time"

	githubv39 "github.com/google/go-github/v39/github"
	"k8s.io/klog/v2"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/commands/watch/conventions"
)

// Tracker asks why agent PRs were closed unmerged and harvests replies and labels.
type Tracker struct {
	cfg   Config
	deps  Deps
	state *StateStore
}

// New constructs a new Tracker.
func New(cfg Config, deps Deps) *Tracker {
	return &Tracker{
		cfg:   cfg,
		deps:  deps,
		state: NewStateStore(cfg.StateDir),
	}
}

// State returns the backing StateStore.
func (t *Tracker) State() *StateStore {
	return t.state
}

// CloseReason returns the captured close reason for a PR, if any.
func (t *Tracker) CloseReason(prNum int) (string, bool) {
	if t == nil || t.state == nil {
		return "", false
	}
	return t.state.GetCloseReason(prNum)
}

func (t *Tracker) triggerPrefix() string {
	trigger := t.cfg.TriggerLabel
	if trigger == "" {
		trigger = "overseer"
	}
	return trigger + "/closed-"
}

// IsAgentPR reports whether a PR was opened by an agent or bot.
func (t *Tracker) IsAgentPR(pr *githubv39.PullRequest) bool {
	if pr == nil {
		return false
	}
	user := pr.GetUser()
	if user != nil {
		login := user.GetLogin()
		if t.isBot(login) {
			return true
		}
		if strings.EqualFold(user.GetType(), "Bot") {
			return true
		}
	}
	body := pr.GetBody()
	if strings.Contains(body, "<!-- factory:") || strings.Contains(body, "/tasks/") || strings.Contains(body, "/session/") {
		return true
	}
	return false
}

func (t *Tracker) isBot(login string) bool {
	if login == "" {
		return false
	}
	if strings.EqualFold(login, t.cfg.GitHubLogin) {
		return true
	}
	for _, b := range t.cfg.BotUsers {
		if strings.EqualFold(login, b) {
			return true
		}
	}
	lower := strings.ToLower(login)
	return strings.HasSuffix(lower, "[bot]") ||
		strings.HasSuffix(lower, "-bot") ||
		strings.HasSuffix(lower, "-robot") ||
		strings.Contains(lower, "prow")
}

func findCloser(timeline []*githubv39.Timeline) string {
	for i := len(timeline) - 1; i >= 0; i-- {
		ev := timeline[i]
		if ev.GetEvent() == "closed" {
			if actor := ev.GetActor(); actor != nil && actor.GetLogin() != "" {
				return actor.GetLogin()
			}
		}
	}
	return ""
}

func extractCloseReason(body string) string {
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			return line
		}
	}
	return strings.TrimSpace(body)
}

func (t *Tracker) checkLabelReason(labels []*githubv39.Label) string {
	prefix := t.triggerPrefix()
	for _, l := range labels {
		name := l.GetName()
		if strings.HasPrefix(name, prefix) {
			reason := strings.TrimPrefix(name, prefix)
			if reason != "" {
				return reason
			}
		}
	}
	return ""
}

// OnClosedPR handles an unmerged closed PR during the closed-PR sweep.
func (t *Tracker) OnClosedPR(ctx context.Context, pr *githubv39.PullRequest) {
	if !t.cfg.Enabled || pr == nil || t.deps.GitHub == nil {
		return
	}
	if pr.GetState() != "closed" || pr.GetMerged() {
		return
	}
	if !t.IsAgentPR(pr) {
		return
	}

	prNum := pr.GetNumber()

	// If the PR already has a <trigger>/closed-<reason> label, record it and do not prompt.
	if reason := t.checkLabelReason(pr.Labels); reason != "" {
		t.state.RecordAsked(prNum, "", pr.GetClosedAt())
		t.state.SetCloseReason(prNum, reason, time.Now())
		return
	}

	// Identify who closed the PR.
	timeline, _, err := t.deps.GitHub.ListIssueTimeline(ctx, prNum)
	if err != nil {
		klog.Warningf("Failed to fetch timeline for closed PR #%d: %v", prNum, err)
		return
	}
	closer := findCloser(timeline)
	if closer == "" || t.isBot(closer) {
		// Bot closed itself, or closer could not be identified: do not comment.
		return
	}

	// Never prompt twice: check local state first.
	if t.state.IsAsked(prNum) {
		return
	}

	// Check if already prompted on GitHub.
	comments, err := t.deps.GitHub.ListIssueComments(ctx, prNum)
	if err != nil {
		klog.Warningf("Failed to list comments for closed PR #%d: %v", prNum, err)
		return
	}
	for _, c := range comments {
		if strings.Contains(c.GetBody(), WhyClosedMarker) {
			t.state.RecordAsked(prNum, closer, c.GetCreatedAt())
			return
		}
	}

	prompt := fmt.Sprintf("@%s closing? One line on why helps the agents learn, e.g. \"wrong approach\", \"duplicate\", \"not needed\", \"superseded by #123\". Optional.\n\n%s", closer, WhyClosedMarker)

	if t.cfg.DryRun {
		fmt.Printf("[DRYRUN] Would post why-closed prompt on PR #%d to @%s\n", prNum, closer)
		t.state.RecordAsked(prNum, closer, time.Now())
		return
	}

	if err := t.deps.GitHub.AddComment(ctx, prNum, prompt); err != nil {
		klog.Errorf("Failed to post why-closed comment on PR #%d: %v", prNum, err)
		return
	}
	t.state.RecordAsked(prNum, closer, time.Now())
}

// Poll checks recent unmerged closed PRs for up to 7 days for replies or labels.
func (t *Tracker) Poll(ctx context.Context) {
	if !t.cfg.Enabled || t.deps.GitHub == nil {
		return
	}

	active := t.state.ActivePRs(DefaultMaxPollAge)
	for _, st := range active {
		if ctx.Err() != nil {
			return
		}

		pr, err := t.deps.GitHub.GetPullRequest(ctx, st.PR)
		if err != nil {
			klog.Warningf("Failed to fetch PR #%d during why-closed poll: %v", st.PR, err)
			continue
		}

		// 1. Check for <trigger>/closed-<reason> label
		if reason := t.checkLabelReason(pr.Labels); reason != "" {
			t.state.SetCloseReason(st.PR, reason, time.Now())
			continue
		}

		// 2. Check comments for replies from closer or maintainers
		comments, err := t.deps.GitHub.ListIssueComments(ctx, st.PR)
		if err != nil {
			klog.Warningf("Failed to list comments for PR #%d during why-closed poll: %v", st.PR, err)
			continue
		}

		var askedTime time.Time
		for _, c := range comments {
			if strings.Contains(c.GetBody(), WhyClosedMarker) {
				askedTime = c.GetCreatedAt()
				break
			}
		}
		if askedTime.IsZero() {
			askedTime = st.AskedAt
		}

		for _, c := range comments {
			if strings.Contains(c.GetBody(), WhyClosedMarker) {
				continue
			}
			// Only consider comments created after the prompt.
			if !c.GetCreatedAt().After(askedTime) {
				continue
			}

			user := c.GetUser()
			if user == nil {
				continue
			}
			author := user.GetLogin()
			if t.isBot(author) {
				continue
			}

			isCloser := st.Closer != "" && strings.EqualFold(author, st.Closer)
			isMaintainer := conventions.IsTrustedAuthor(user, c.GetAuthorAssociation(), t.cfg.TrustedLogins)

			if isCloser || isMaintainer {
				reason := extractCloseReason(c.GetBody())
				if reason != "" {
					t.state.SetCloseReason(st.PR, reason, c.GetCreatedAt())
					break
				}
			}
		}
	}
}
