package spool

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// EngineLogFile is where a task's agent session keeps the engine's stderr,
// relative to the task directory (acpd writes it in the session's dir).
const EngineLogFile = "session/engine.stderr.log"

// EngineRetriesFile is the agent session's retried model calls, which
// acpd counts from the engine log with the engine's own parser (see
// acpd.RetriesFile), relative to the task directory.
const EngineRetriesFile = "session/engine-retries.json"

// EngineRetries is how often the engine's model calls failed and were
// retried, by HTTP status, as the engine logged them. A task that is slow
// for no visible reason is usually one whose engine is retrying: a 429 is
// a rate limit or an exhausted quota, a 503 an overloaded model. It is
// acpd.EngineRetries, as acpd writes it.
type EngineRetries struct {
	Statuses map[string]int `json:"statuses"`
	// QuotaExceeded is set when a 429 said the quota is used up: retrying
	// will not help, somebody has to raise it or change the key.
	QuotaExceeded bool `json:"quota_exceeded,omitempty"`
}

// Total is the number of retries.
func (r *EngineRetries) Total() int {
	n := 0
	for _, c := range r.Statuses {
		n += c
	}
	return n
}

// engineRetries is a task's engine retries, nil when there are none: no
// session, an engine acpd has no parser for, or nothing retried.
func engineRetries(taskDir string) *EngineRetries {
	data, err := os.ReadFile(filepath.Join(taskDir, EngineRetriesFile))
	if err != nil {
		return nil
	}
	var r EngineRetries
	if json.Unmarshal(data, &r) != nil || (r.Total() == 0 && !r.QuotaExceeded) {
		return nil
	}
	return &r
}
