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
}
