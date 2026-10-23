package api

// QueueTaskItem represents a formatted task representation exposed via the HTTP API.
type QueueTaskItem struct {
	FileName   string   `json:"fileName"`
	QueueState string   `json:"queueState"`
	Type       TaskType `json:"type"`
	// Rule and Recipe are the rule that decided the task and the recipe it
	// runs (QueueTask.Rule, Recipe): what the task is, whatever its type.
	Rule             string        `json:"rule,omitempty"`
	Recipe           string        `json:"recipe,omitempty"`
	URL              string        `json:"url"`
	Number           int           `json:"number"`
	Priority         TaskPriority  `json:"priority"`
	Phase            TaskPhase     `json:"phase"`
	CreatedAt        string        `json:"createdAt"`
	EnqueuedAt       string        `json:"enqueuedAt,omitempty"`
	StartedAt        string        `json:"startedAt,omitempty"`
	CompletedAt      string        `json:"completedAt,omitempty"`
	DurationSeconds  float64       `json:"durationSeconds,omitempty"`
	TriggerEventTime string        `json:"triggerEventTime,omitempty"`
	TriggerReason    TriggerReason `json:"triggerReason,omitempty"`
	TriggerNotes     string        `json:"triggerNotes,omitempty"`
	Assignee         string        `json:"assignee"`
	Status           TaskStatus    `json:"status"`
	CommitSHA        string        `json:"commitSHA"`
	Rank             int           `json:"rank,omitempty"`
}

// QueueSummary contains aggregated counts of tasks in each queue state and breakdown by priority/type.
type QueueSummary struct {
	TotalPending    int                  `json:"totalPending"`
	TotalProcessing int                  `json:"totalProcessing"`
	TotalCompleted  int                  `json:"totalCompleted"`
	ByPriority      map[TaskPriority]int `json:"byPriority"`
	ByType          map[TaskType]int     `json:"byType"`
}

// QueueResponse is the response payload served by GET /api/v1/queue.
type QueueResponse struct {
	Summary    QueueSummary    `json:"summary"`
	Incoming   []QueueTaskItem `json:"incoming"`
	Processing []QueueTaskItem `json:"processing"`
	Processed  []QueueTaskItem `json:"processed"`
	// Cooldowns are the tasks the issue scanner is holding back because
	// the same task finished too recently (Cooldown).
	Cooldowns []QueueCooldown `json:"cooldowns,omitempty"`
	// Drops are the tasks the dispatcher dropped recently, newest first,
	// because a fact they were decided on no longer held (Drop).
	Drops []QueueDrop `json:"drops,omitempty"`
	// Failures are the tasks that failed recently and have not succeeded
	// since, newest first, with their child's error (Failure).
	Failures []QueueFailure `json:"failures,omitempty"`
}

// QueueFailure is a task that failed, as the HTTP API serves it.
type QueueFailure struct {
	FileName string   `json:"fileName"`
	Number   int      `json:"number"`
	URL      string   `json:"url,omitempty"`
	Type     TaskType `json:"type"`
	Rule     string   `json:"rule,omitempty"`
	Recipe   string   `json:"recipe,omitempty"`
	// Error is what the task's child printed as its error, after its exit
	// status, e.g. "exit status 1: applying the Change for …".
	Error string `json:"error"`
	At    string `json:"at"`
}

// QueueDrop is a task dropped before it started, as the HTTP API serves
// it.
type QueueDrop struct {
	FileName string   `json:"fileName"`
	Number   int      `json:"number"`
	URL      string   `json:"url,omitempty"`
	Type     TaskType `json:"type"`
	Rule     string   `json:"rule,omitempty"`
	Recipe   string   `json:"recipe,omitempty"`
	// Reason is the fact that no longer held, e.g. "head moved abc1234→def5678".
	Reason string `json:"reason"`
	At     string `json:"at"`
}

// QueueCooldown is a task held back by its cooldown, as the HTTP API
// serves it.
type QueueCooldown struct {
	FileName string `json:"fileName"`
	Number   int    `json:"number"`
	URL      string `json:"url,omitempty"`
	// Workflow is the workflow the task runs; empty for a standard fix.
	Workflow string `json:"workflow,omitempty"`
	// Until is when the task may be queued again.
	Until string `json:"until"`
	// LastStatus and LastCompletedAt are the run the cooldown follows.
	LastStatus      TaskStatus `json:"lastStatus,omitempty"`
	LastCompletedAt string     `json:"lastCompletedAt,omitempty"`
}
