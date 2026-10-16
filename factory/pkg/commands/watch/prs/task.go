package prs

import (
	"fmt"
	"time"

	githubv39 "github.com/google/go-github/v39/github"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/commands/watch/api"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/commands/watch/conventions"
)

// taskOptions specifies the parts of a pull request task that vary between the
// rebase, investigate, address-comments and review phases.
type taskOptions struct {
	Type             api.TaskType
	PR               *githubv39.PullRequest
	PRIssue          *githubv39.Issue
	Phase            api.TaskPhase
	Assignee         string
	CommitSHA        string
	RetryCount       int
	TriggerEventTime time.Time
	TriggerReason    api.TriggerReason
	TriggerNotes     string
	Instructions     []string
}

// newTask constructs the queue task for a pull request with consistent defaults.
//
// The trigger event time is what the queue orders by, so it falls back through
// the pull request's own timestamps rather than defaulting to now: a task
// queued for something that happened an hour ago should not jump ahead of one
// queued for something that happened two hours ago.
func (s *Scanner) newTask(opts taskOptions) *api.QueueTask {
	num := opts.PR.GetNumber()
	eventTime := opts.TriggerEventTime
	if eventTime.IsZero() {
		eventTime = opts.PR.GetUpdatedAt()
	}
	if eventTime.IsZero() {
		eventTime = opts.PR.GetCreatedAt()
	}
	return &api.QueueTask{
		Type:             opts.Type,
		URL:              fmt.Sprintf("https://github.com/%s/%s/pull/%d", s.gh.Owner(), s.gh.Repo(), num),
		Number:           num,
		Priority:         conventions.Priority(opts.PRIssue.Labels),
		Phase:            opts.Phase,
		CreatedAt:        opts.PR.GetCreatedAt(),
		EnqueuedAt:       time.Now(),
		TriggerEventTime: eventTime,
		TriggerReason:    opts.TriggerReason,
		TriggerNotes:     opts.TriggerNotes,
		Assignee:         opts.Assignee,
		Status:           api.StatusPending,
		CommitSHA:        opts.CommitSHA,
		RetryCount:       opts.RetryCount,
		Instructions:     opts.Instructions,
		Expect:           expectFor(opts.Type, opts.CommitSHA),
	}
}

// expectFor is what a PR task of type t is decided on, beyond the PR being
// open and not stopped (checked for every task): a rebase, an investigation
// or a review is of the head it was decided at, and a PR that moved on
// since is not what it was decided on. Feedback is not addressed by a new
// commit, so a comments task holds across one.
func expectFor(t api.TaskType, commitSHA string) []string {
	if commitSHA == "" {
		return nil
	}
	switch t {
	case api.TypePRIterate, api.TypePRInvestigate, api.TypePRReview:
		return []string{api.ExpectHead}
	}
	return nil
}
