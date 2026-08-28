package watch

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/clients"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/commands/common"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/config"
	githubv39 "github.com/google/go-github/v39/github"
)

type RepoFlag struct {
	Owner string
	Repo  string
}

func (r *RepoFlag) String() string {
	if r == nil || (r.Owner == "" && r.Repo == "") {
		return ""
	}
	return fmt.Sprintf("%s/%s", r.Owner, r.Repo)
}

func (r *RepoFlag) Set(val string) error {
	parts := strings.Split(val, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return fmt.Errorf("invalid repo format, expected owner/repo, got %s", val)
	}
	r.Owner = parts[0]
	r.Repo = parts[1]
	return nil
}

func (r *RepoFlag) Type() string {
	return "string"
}

type Flags struct {
	Repo                RepoFlag
	PollInterval        time.Duration
	Assignee            string
	AssigneeChanged     bool
	Labels              []string
	DryRun              bool
	WatchTimeout        time.Duration
	MaxActions          int
	MaxPending          int
	Mode                string
	QueueDir            string
	Once                bool
	IssueMode           string
	PRMode              string
	ChoresMode          string
	ScanLimit           int
	TaskTimeout         time.Duration
	SandboxEvictionAge  string
	SandboxIdleTimeout  time.Duration
	PRInactivityTimeout time.Duration
}

type Watcher struct {
	common.RootFlags
	Flags

	cfg              *config.FactoryConfig
	triggerLabel     string
	ghClient         *githubv39.Client
	kubeClient       *clients.KubernetesClient
	githubLogin      string
	targetAssignee   string
	allBotUsers      []string
	incomingDir      string
	processingDir    string
	processedDir     string
	processingLogDir string
	processedLogDir  string
	processedIssues  map[int]time.Time
	processedPRs     map[int]prWatchState
	state            *watchState
	timeoutChan      <-chan time.Time
	wg               sync.WaitGroup
}

func NewWatcher(rootFlags common.RootFlags, flags Flags) *Watcher {
	return &Watcher{
		RootFlags: rootFlags,
		Flags:     flags,
	}
}

// TriggerReason represents a machine-readable, PascalCase reason for why a queue task was triggered,
// following Kubernetes API conventions.
type TriggerReason string

const (
	// TriggerReasonIssueCreated indicates the task was triggered because an issue was newly created
	// (either already labeled with the trigger label or auto-labeled by the watcher).
	TriggerReasonIssueCreated TriggerReason = "IssueCreated"

	// TriggerReasonIssueLabeled indicates the task was triggered because the trigger label was
	// added to an existing issue after its creation.
	TriggerReasonIssueLabeled TriggerReason = "IssueLabeled"

	// TriggerReasonPRCommentsAdded indicates the task was triggered by new unaddressed comments or reviews on a PR.
	TriggerReasonPRCommentsAdded TriggerReason = "PRCommentsAdded"

	// TriggerReasonPRCheckFailed indicates the task was triggered by a failed CI check run or commit status.
	TriggerReasonPRCheckFailed TriggerReason = "PRCheckFailed"

	// TriggerReasonPRMergeConflict indicates the task was triggered by merge conflicts with the base branch.
	TriggerReasonPRMergeConflict TriggerReason = "PRMergeConflict"

	// TriggerReasonPRReadyForReview indicates the task was triggered because all CI checks passed and the PR is ready for automated review.
	TriggerReasonPRReadyForReview TriggerReason = "PRReadyForReview"

	// TriggerReasonChoreScheduled indicates the task was triggered by a scheduled cron chore.
	TriggerReasonChoreScheduled TriggerReason = "ChoreScheduled"
)

type QueueTask struct {
	Type       string    `yaml:"type"` // "issue-fix", "pr-investigate", "pr-comments", "pr-iterate", "pr-review", "agent-chore"
	URL        string    `yaml:"url"`
	Number     int       `yaml:"number"`
	Priority   string    `yaml:"priority"` // "critical", "urgent", "important", "high", "medium", "low"
	Phase      int       `yaml:"phase"`    // 1: Rebase/iterate, 2: Comments, 3: Investigate/Fix, 4: Chores
	CreatedAt  time.Time `yaml:"createdAt"`
	EnqueuedAt time.Time `yaml:"enqueuedAt,omitempty"`
	// TriggerEventTime is the timestamp when the original event that triggered this task occurred
	// (e.g. oldest comment time, earliest CI check failure, or issue creation/labeling time).
	TriggerEventTime time.Time `yaml:"triggerEventTime,omitempty"`
	// TriggerReason is a machine-readable enum indicating the type of triggering event (e.g. IssueCreated, PRCommentsAdded).
	TriggerReason TriggerReason `yaml:"triggerReason,omitempty"`
	// TriggerNotes contains human-readable details about the triggering event (e.g. specific check that failed, comment author).
	TriggerNotes string `yaml:"triggerNotes,omitempty"`
	// StartedAt is the timestamp when the task began execution in processing.
	StartedAt time.Time `yaml:"startedAt,omitempty"`
	// CompletedAt is the timestamp when the task completed execution (either Completed or Failed).
	CompletedAt  time.Time `yaml:"completedAt,omitempty"`
	Assignee     string    `yaml:"assignee,omitempty"`
	Status       string    `yaml:"status"` // "Pending", "Running", "Completed", "Failed"
	Error        string    `yaml:"error,omitempty"`
	AgentFile    string    `yaml:"agentFile,omitempty"` // For chore tasks
	SessionID    string    `yaml:"sessionId,omitempty"` // For workflow sessions
	CommitSHA    string    `yaml:"commitSHA,omitempty"`
	Instructions []string  `yaml:"instructions,omitempty"`
	Recovered    bool      `yaml:"recovered,omitempty"`
}

// taskItem represents a queue task bundled with its filename.
type taskItem struct {
	filename string
	task     *QueueTask
}

type ChoreRunState struct {
	LastRun time.Time `json:"lastRun"`
}

type JournalEvent struct {
	Timestamp        time.Time     `json:"timestamp"`
	TaskID           string        `json:"taskId"`
	Event            string        `json:"event"`
	Type             string        `json:"type"`
	URL              string        `json:"url"`
	Priority         string        `json:"priority"`
	TriggerEventTime time.Time     `json:"triggerEventTime,omitempty"`
	TriggerReason    TriggerReason `json:"triggerReason,omitempty"`
	TriggerNotes     string        `json:"triggerNotes,omitempty"`
	StartedAt        time.Time     `json:"startedAt,omitempty"`
	CompletedAt      time.Time     `json:"completedAt,omitempty"`
	Error            string        `json:"error,omitempty"`
	DurationSecond   float64       `json:"durationSeconds,omitempty"`
}

// prWatchState tracks the progress and state of automated tasks for a monitored pull request.
type prWatchState struct {
	// lastInvestigatedTime is the timestamp when a CI failure investigation was last queued or completed.
	lastInvestigatedTime time.Time
	// lastInvestigatedSHA is the head commit SHA when CI failures were last investigated.
	lastInvestigatedSHA string
	// lastCommentAddressedTime is the timestamp when PR comments were last addressed by the bot.
	lastCommentAddressedTime time.Time
	// lastCommentAddressedSHA is the head commit SHA when review comments were last addressed, preventing duplicate comment processing on the same commit.
	lastCommentAddressedSHA string
	// lastReviewedSHA is the commit SHA for which an automated PR review was last queued or completed.
	lastReviewedSHA string
	// lastIteratedSHA is the commit SHA for which a rebase/conflict-resolution task was last queued or completed.
	lastIteratedSHA string
	// lastIteratedTime is the timestamp when a rebase/conflict-resolution task was last queued or completed.
	lastIteratedTime time.Time
}

type QueueTaskItem struct {
	FileName         string        `json:"fileName"`
	QueueState       string        `json:"queueState"`
	Type             string        `json:"type"`
	URL              string        `json:"url"`
	Number           int           `json:"number"`
	Priority         string        `json:"priority"`
	Phase            int           `json:"phase"`
	CreatedAt        string        `json:"createdAt"`
	EnqueuedAt       string        `json:"enqueuedAt,omitempty"`
	StartedAt        string        `json:"startedAt,omitempty"`
	CompletedAt      string        `json:"completedAt,omitempty"`
	DurationSeconds  float64       `json:"durationSeconds,omitempty"`
	TriggerEventTime string        `json:"triggerEventTime,omitempty"`
	TriggerReason    TriggerReason `json:"triggerReason,omitempty"`
	TriggerNotes     string        `json:"triggerNotes,omitempty"`
	Assignee         string        `json:"assignee"`
	Status           string        `json:"status"`
	CommitSHA        string        `json:"commitSHA"`
	Rank             int           `json:"rank,omitempty"`
}

type QueueSummary struct {
	TotalPending    int            `json:"totalPending"`
	TotalProcessing int            `json:"totalProcessing"`
	TotalCompleted  int            `json:"totalCompleted"`
	ByPriority      map[string]int `json:"byPriority"`
	ByType          map[string]int `json:"byType"`
}

type QueueResponse struct {
	Summary    QueueSummary    `json:"summary"`
	Incoming   []QueueTaskItem `json:"incoming"`
	Processing []QueueTaskItem `json:"processing"`
	Processed  []QueueTaskItem `json:"processed"`
}

type watchState struct {
	mu               sync.Mutex
	openPRs          []*githubv39.PullRequest
	referencedIssues map[int]bool
	lastPRScan       time.Time
	lastIssueScan    time.Time
	lastRunnerRun    time.Time
	shuttingDown     bool
}
