package acpd

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// RecordFile is the session record, beside the transcript in the session's
// directory.
const RecordFile = "session.json"

// Record is what it takes to load a session again after its engine has
// gone: a restarted acpd, a pod that came back, a task whose conversation
// a member continues later. The engine keeps the conversation itself (for
// gemini, under $HOME/.gemini/tmp, keyed by the working directory); this
// is only which one, and where it ran.
type Record struct {
	// ACPSessionID is the agent's id for the conversation, the one
	// session/load takes.
	ACPSessionID string `json:"acpSessionId"`
	Engine       string `json:"engine"`
	CWD          string `json:"cwd"`
}

// ReadRecord is the record in dir, false when there is none or it cannot
// be read: a session without one starts fresh, which is what it would
// have done before records existed.
func ReadRecord(dir string) (Record, bool) {
	data, err := os.ReadFile(filepath.Join(dir, RecordFile))
	if err != nil {
		return Record{}, false
	}
	var rec Record
	if json.Unmarshal(data, &rec) != nil || rec.ACPSessionID == "" {
		return Record{}, false
	}
	return rec, true
}

// writeRecord replaces the record in dir, by rename so a reader never sees
// half of one.
func writeRecord(dir string, rec Record) error {
	data, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, RecordFile+".*")
	if err != nil {
		return fmt.Errorf("writing session record: %w", err)
	}
	_, werr := tmp.Write(append(data, '\n'))
	cerr := tmp.Close()
	if err := errors.Join(werr, cerr); err != nil {
		_ = os.Remove(tmp.Name())
		return fmt.Errorf("writing session record: %w", err)
	}
	if err := os.Rename(tmp.Name(), filepath.Join(dir, RecordFile)); err != nil {
		_ = os.Remove(tmp.Name())
		return fmt.Errorf("writing session record: %w", err)
	}
	return nil
}
