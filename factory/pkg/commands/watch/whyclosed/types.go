package whyclosed

import (
	"context"
	"time"

	githubv39 "github.com/google/go-github/v39/github"
)

// WhyClosedMarker is the hidden marker in the prompt comment ensuring it is
// never posted more than once.
const WhyClosedMarker = "<!-- factory:why-closed -->"

// DefaultMaxPollAge is how long recently closed unmerged PRs are polled for replies.
const DefaultMaxPollAge = 7 * 24 * time.Hour

// GitHub defines the repository operations needed for asking and harvesting
// why a closed PR was closed.
type GitHub interface {
	GetPullRequest(ctx context.Context, number int) (*githubv39.PullRequest, error)
	ListIssueTimeline(ctx context.Context, issueNum int) ([]*githubv39.Timeline, bool, error)
	AddComment(ctx context.Context, number int, body string) error
	ListIssueComments(ctx context.Context, number int) ([]*githubv39.IssueComment, error)
}

// Config holds configuration for why-closed prompting and reply harvesting.
type Config struct {
	// Enabled reports whether why-closed tracking is opted in.
	Enabled bool
	// TriggerLabel is the namespace of repository trigger labels, e.g. "overseer".
	TriggerLabel string
	// GitHubLogin is the account the watcher acts as.
	GitHubLogin string
	// BotUsers are logins of bot accounts in the pool.
	BotUsers []string
	// TrustedLogins are accounts trusted as maintainers or feedback authors.
	TrustedLogins []string
	// StateDir is where why_closed_state.json is persisted. Empty means in-memory only.
	StateDir string
	// DryRun skips mutating GitHub while logging planned actions.
	DryRun bool
}

// Deps holds the dependencies needed by Tracker.
type Deps struct {
	GitHub GitHub
}

// PRState records the why-closed status of a pull request in the watch state.
type PRState struct {
	PR          int       `json:"pr"`
	Closer      string    `json:"closer,omitempty"`
	Asked       bool      `json:"asked"`
	AskedAt     time.Time `json:"askedAt,omitempty"`
	ClosedAt    time.Time `json:"closedAt,omitempty"`
	CloseReason string    `json:"closeReason,omitempty"`
	AnsweredAt  time.Time `json:"answeredAt,omitempty"`
}
