package api

// QueueTaskItem represents a formatted task representation exposed via the HTTP API.
type QueueTaskItem struct {
	FileName         string        `json:"fileName"`
	QueueState       string        `json:"queueState"`
	Type             TaskType      `json:"type"`
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
