package acpd

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Event is one line of a session transcript. Everything the browser ever
// sees about a session arrives as one of these: agent output, tool calls,
// permission requests, the user's own prompts, and the turn boundaries
// between them.
//
// Data is the raw ACP payload where one exists, so a variant the UI does
// not understand yet still lands in the transcript intact rather than
// being dropped at the point it is written.
type Event struct {
	Seq  int64           `json:"seq"`
	Time time.Time       `json:"time"`
	Kind string          `json:"kind"`
	Data json.RawMessage `json:"data,omitempty"`
}

// Event kinds we synthesize. ACP's own session/update variants are written
// through under their wire names (agent_message_chunk, tool_call, plan …),
// so this list is only what has no ACP equivalent.
const (
	// KindUserPrompt records what the user sent, so a transcript read back
	// from offset 0 is a conversation and not just one side of one.
	KindUserPrompt = "user_prompt"
	// KindPermissionRequest carries a PermissionRequest; the turn is
	// blocked until a matching POST /permission arrives.
	KindPermissionRequest = "permission_request"
	// KindPermissionResolved records the answer, including the timeouts
	// and cancellations nobody clicked.
	KindPermissionResolved = "permission_resolved"
	// KindTurnEnd carries the stopReason from session/prompt returning.
	KindTurnEnd = "turn_end"
	// KindError is a failure worth showing the user: the engine died, a
	// prompt call returned an RPC error.
	KindError = "error"
)

// Transcript is an append-only NDJSON file plus the means to follow it.
//
// It is the session's durable state and its event bus at once: readers
// tail the same bytes that persist, so a browser that reconnects and asks
// from its last offset gets exactly what it missed with no separate replay
// path to fall out of sync.
//
// Durability is the PVC and nothing more. A transcript does not outlive
// its sandbox, by design — the artifact of a research session is whatever
// the agent was asked to write to git, not the conversation that produced
// it.
type Transcript struct {
	path string

	mu   sync.Mutex
	f    *os.File
	size int64
	seq  int64
	// changed is closed and replaced on every append. A reader takes the
	// current channel before checking the size, so it cannot miss a write
	// that lands between the check and the wait.
	changed chan struct{}
	closed  bool
}

// OpenTranscript opens (creating if needed) the transcript under dir.
// An existing file is appended to and its size adopted as the starting
// offset, so a restarted acpd does not truncate a session's history.
func OpenTranscript(dir string) (*Transcript, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("creating transcript dir %s: %w", dir, err)
	}
	path := filepath.Join(dir, "stream.ndjson")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, fmt.Errorf("opening transcript %s: %w", path, err)
	}
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, fmt.Errorf("stat transcript %s: %w", path, err)
	}
	return &Transcript{
		path:    path,
		f:       f,
		size:    info.Size(),
		changed: make(chan struct{}),
	}, nil
}

// Path is the transcript's location on disk.
func (t *Transcript) Path() string { return t.path }

// Append writes one event and wakes every follower.
//
// A failed write is returned but not fatal to the session: losing a line of
// transcript is better than killing a turn the user is waiting on, so
// callers log it and continue.
func (t *Transcript) Append(kind string, data json.RawMessage) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return fmt.Errorf("transcript is closed")
	}

	t.seq++
	line, err := json.Marshal(Event{
		Seq:  t.seq,
		Time: time.Now().UTC(),
		Kind: kind,
		Data: data,
	})
	if err != nil {
		return fmt.Errorf("marshaling %s event: %w", kind, err)
	}
	line = append(line, '\n')

	n, err := t.f.Write(line)
	t.size += int64(n)
	// Wake followers even on a short or failed write: the bytes that did
	// land are theirs to read, and a follower blocked forever is worse
	// than one that sees a truncated line.
	close(t.changed)
	t.changed = make(chan struct{})
	if err != nil {
		return fmt.Errorf("writing %s event: %w", kind, err)
	}
	return nil
}

// AppendValue marshals v and appends it as the event's data.
func (t *Transcript) AppendValue(kind string, v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("marshaling %s payload: %w", kind, err)
	}
	return t.Append(kind, data)
}

// Size is the current byte length, which is the offset a reader should ask
// from next.
func (t *Transcript) Size() int64 {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.size
}

// ReadFrom returns the bytes from offset to the current end, with the
// offset to ask from next.
//
// Byte offsets rather than sequence numbers so a reader resumes with a
// seek instead of a scan, and so the wire contract is the same one envd's
// log tailing already uses.
func (t *Transcript) ReadFrom(offset int64) ([]byte, int64, error) {
	t.mu.Lock()
	size := t.size
	t.mu.Unlock()

	if offset < 0 {
		offset = 0
	}
	if offset >= size {
		return nil, size, nil
	}

	f, err := os.Open(t.path)
	if err != nil {
		return nil, offset, fmt.Errorf("opening transcript for read: %w", err)
	}
	defer f.Close()

	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return nil, offset, fmt.Errorf("seeking transcript to %d: %w", offset, err)
	}
	buf := make([]byte, size-offset)
	n, err := io.ReadFull(f, buf)
	if err != nil && err != io.ErrUnexpectedEOF {
		return nil, offset, fmt.Errorf("reading transcript at %d: %w", offset, err)
	}
	return buf[:n], offset + int64(n), nil
}

// Wait blocks until the transcript grows past offset, the context ends, or
// the transcript closes. It reports whether there is something to read.
func (t *Transcript) Wait(ctx context.Context, offset int64) bool {
	for {
		t.mu.Lock()
		// Take the wake channel before releasing the lock: an append that
		// lands after this point closes the channel we are about to select
		// on, so the wait cannot miss it.
		changed, size, closed := t.changed, t.size, t.closed
		t.mu.Unlock()

		if size > offset {
			return true
		}
		if closed {
			return false
		}
		select {
		case <-ctx.Done():
			return false
		case <-changed:
		}
	}
}

// Close releases the file and releases every follower.
func (t *Transcript) Close() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return nil
	}
	t.closed = true
	close(t.changed)
	return t.f.Close()
}
