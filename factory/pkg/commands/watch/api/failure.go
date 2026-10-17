package api

import "time"

// Failure is a task that failed recently, with what its child said went
// wrong (Error) and where (FailedAt: "run", or "apply" when its run
// succeeded and applying the result failed). A later success of the same
// task clears it.
type Failure struct {
	FileName string
	Number   int
	URL      string
	Type     TaskType
	Error    string
	FailedAt string
	At       time.Time
}

// Item is the failure as the HTTP API serves it.
func (f Failure) Item() QueueFailure {
	return QueueFailure{
		FileName: f.FileName,
		Number:   f.Number,
		URL:      f.URL,
		Type:     f.Type,
		Error:    f.Error,
		FailedAt: f.FailedAt,
		At:       f.At.UTC().Format(time.RFC3339),
	}
}
