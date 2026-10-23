package api

import "time"

// Drop is a queued task the dispatcher dropped before starting it,
// because the rules no longer decide it on the item's facts read then
// (TaskCoordinator.Redecide). A
// drop is not a run: it leaves no processed task, so it counts toward
// no cooldown, retry or give-up.
type Drop struct {
	FileName string
	Number   int
	URL      string
	Type     TaskType
	Rule     string
	Recipe   string
	Reason   string
	At       time.Time
}

// Item is the drop as the HTTP API serves it.
func (d Drop) Item() QueueDrop {
	return QueueDrop{
		FileName: d.FileName,
		Number:   d.Number,
		URL:      d.URL,
		Type:     d.Type,
		Rule:     d.Rule,
		Recipe:   d.Recipe,
		Reason:   d.Reason,
		At:       d.At.UTC().Format(time.RFC3339),
	}
}
