package api

import "time"

// Cooldown is a task the issue scanner is holding back: the same task
// finished at LastCompletedAt (with LastStatus), so it may not be queued
// again before Until.
type Cooldown struct {
	FileName        string
	Number          int
	URL             string
	Workflow        string
	Until           time.Time
	LastStatus      TaskStatus
	LastCompletedAt time.Time
}

// Item is the cooldown as the HTTP API serves it.
func (c Cooldown) Item() QueueCooldown {
	item := QueueCooldown{
		FileName:   c.FileName,
		Number:     c.Number,
		URL:        c.URL,
		Workflow:   c.Workflow,
		Until:      c.Until.UTC().Format(time.RFC3339),
		LastStatus: c.LastStatus,
	}
	if !c.LastCompletedAt.IsZero() {
		item.LastCompletedAt = c.LastCompletedAt.UTC().Format(time.RFC3339)
	}
	return item
}
