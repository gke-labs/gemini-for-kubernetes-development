package api

import (
	"time"
)

// TaskType represents the type of work to be performed by a queue task.
type TaskType string

const (
	TypeIssueFix      TaskType = "issue-fix"
	TypePRInvestigate TaskType = "pr-investigate"
	TypePRComments    TaskType = "pr-comments"
	TypePRIterate     TaskType = "pr-iterate"
	TypePRReview      TaskType = "pr-review"
	TypeAgentChore    TaskType = "agent-chore"
)

// TaskPriority represents the priority or urgency of a queue task.
type TaskPriority string

const (
	PriorityCritical  TaskPriority = "critical"
	PriorityUrgent    TaskPriority = "urgent"
	PriorityImportant TaskPriority = "important"
	PriorityHigh      TaskPriority = "high"
	PriorityMedium    TaskPriority = "medium"
	PriorityLow       TaskPriority = "low"
	PriorityUnknown   TaskPriority = "unknown"
)

// TaskPhase represents the execution phase order of a queue task.
type TaskPhase int

const (
	PhaseRebase      TaskPhase = 1 // Rebase/iterate
	PhaseIterate     TaskPhase = 1
	PhaseComments    TaskPhase = 2 // Comments
	PhaseInvestigate TaskPhase = 3 // Investigate/Fix
	PhaseFix         TaskPhase = 3
	PhaseChores      TaskPhase = 4 // Chores
)

// TaskStatus represents the lifecycle status of a queue task.
type TaskStatus string

const (
	StatusPending   TaskStatus = "Pending"
	StatusRunning   TaskStatus = "Running"
	StatusCompleted TaskStatus = "Completed"
	StatusFailed    TaskStatus = "Failed"
)

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

// QueueTask represents an actionable unit of work to be scheduled and executed in a sandbox.
type QueueTask struct {
	Type       TaskType     `yaml:"type"` // "issue-fix", "pr-investigate", "pr-comments", "pr-iterate", "pr-review", "agent-chore"
	URL        string       `yaml:"url"`
	Number     int          `yaml:"number"`
	Priority   TaskPriority `yaml:"priority"` // "critical", "urgent", "important", "high", "medium", "low"
	Phase      TaskPhase    `yaml:"phase"`    // 1: Rebase/iterate, 2: Comments, 3: Investigate/Fix, 4: Chores
	CreatedAt  time.Time    `yaml:"createdAt"`
	EnqueuedAt time.Time    `yaml:"enqueuedAt,omitempty"`
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
	CompletedAt  time.Time  `yaml:"completedAt,omitempty"`
	Assignee     string     `yaml:"assignee,omitempty"`
	Status       TaskStatus `yaml:"status"` // "Pending", "Running", "Completed", "Failed"
	Error        string     `yaml:"error,omitempty"`
	AgentFile    string     `yaml:"agentFile,omitempty"` // For chore tasks
	SessionID    string     `yaml:"sessionId,omitempty"` // For workflow sessions
	CommitSHA    string     `yaml:"commitSHA,omitempty"`
	Instructions []string   `yaml:"instructions,omitempty"`
	Recovered    bool       `yaml:"recovered,omitempty"`
}

// Duration returns the elapsed execution duration between StartedAt and CompletedAt,
// or 0 if either timestamp is unset or CompletedAt is before StartedAt.
func (t *QueueTask) Duration() time.Duration {
	if !t.StartedAt.IsZero() && !t.CompletedAt.IsZero() && t.CompletedAt.After(t.StartedAt) {
		return t.CompletedAt.Sub(t.StartedAt)
	}
	return 0
}

// TaskItem represents a queue task bundled with its filename.
type TaskItem struct {
	Filename string
	Task     *QueueTask
}

// JournalEvent represents an append-only log record written to journal.jsonl for observability.
type JournalEvent struct {
	Timestamp        time.Time     `json:"timestamp"`
	TaskID           string        `json:"taskId"`
	Event            string        `json:"event"`
	Type             TaskType      `json:"type"`
	URL              string        `json:"url"`
	Priority         TaskPriority  `json:"priority"`
	TriggerEventTime time.Time     `json:"triggerEventTime,omitempty"`
	TriggerReason    TriggerReason `json:"triggerReason,omitempty"`
	TriggerNotes     string        `json:"triggerNotes,omitempty"`
	StartedAt        time.Time     `json:"startedAt,omitempty"`
	CompletedAt      time.Time     `json:"completedAt,omitempty"`
	Error            string        `json:"error,omitempty"`
	DurationSecond   float64       `json:"durationSeconds,omitempty"`
}

// IsPRTask reports whether a given task type corresponds to an automated PR task.
func IsPRTask(taskType TaskType) bool {
	switch taskType {
	case TypePRInvestigate, TypePRComments, TypePRIterate:
		return true
	default:
		return false
	}
}
