package acpd

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// A conversation handed to a terminal: the member goes on in the engine's
// own CLI (factory's termd, resuming the same conversation by its id), and
// comes back to the session view later. One process drives a conversation
// at a time - the ACP engine or the CLI - so the hand-off stops the one
// before starting the other.
//
// To the terminal (Release): the session's engine is stopped (not while a
// turn or a permission is in flight, nor while its task runs), and a
// marker beside the record says the conversation is in a terminal and how
// many turns the transcript had then. While the terminal runs, a create
// is refused. Back to the view (Return): the marker says so, and the next
// create's load writes the turns the engine replays past that count - the
// ones made in the terminal - into the transcript, which otherwise drops
// a replay as what it already has.

// HandoffTerminalID is the terminal session (termd) a conversation is
// handed to.
func HandoffTerminalID(id string) string { return "handoff-" + id }

// HandoffFile is the marker, beside the record in the session's directory.
const HandoffFile = "handoff.json"

// Handoff is the marker.
type Handoff struct {
	// To is where the conversation is: terminal, or view (handed back,
	// not loaded yet).
	To string `json:"to"`
	// ACPSessionID is the conversation handed off.
	ACPSessionID string `json:"acpSessionId"`
	// UserTurns is how many user turns the transcript had when it went.
	UserTurns int       `json:"userTurns"`
	At        time.Time `json:"at"`
}

// Errors of a hand-off, for the caller's status codes.
var (
	// ErrBusy: a turn or permission is in flight, or the task runs.
	ErrBusy = errors.New("the session is busy")
	// ErrNoConversation: the session has no recorded conversation to hand
	// off yet.
	ErrNoConversation = errors.New("the session has no conversation yet")
	// ErrNotHandedOff: it is not in a terminal.
	ErrNotHandedOff = errors.New("the conversation is not in a terminal")
)

// ReadHandoff is the marker in dir, false when there is none.
func ReadHandoff(dir string) (Handoff, bool) {
	data, err := os.ReadFile(filepath.Join(dir, HandoffFile))
	if err != nil {
		return Handoff{}, false
	}
	var h Handoff
	if json.Unmarshal(data, &h) != nil || h.ACPSessionID == "" {
		return Handoff{}, false
	}
	return h, true
}

func writeHandoff(dir string, h Handoff) error {
	data, err := json.Marshal(h)
	if err != nil {
		return err
	}
	return replaceFile(dir, HandoffFile, data)
}

// SetHandoffTerminal tells the server whether a session's conversation is
// live in a terminal, which then refuses its create. Without it a marker
// saying terminal is taken at its word.
func (s *Server) SetHandoffTerminal(live func(id string) bool) {
	s.mu.Lock()
	s.terminalLive = live
	s.mu.Unlock()
}

// inTerminal reports whether the conversation in dir is live in a
// terminal.
func (s *Server) inTerminal(id, dir string) bool {
	h, ok := ReadHandoff(dir)
	if !ok || h.To != "terminal" {
		return false
	}
	s.mu.Lock()
	live := s.terminalLive
	s.mu.Unlock()
	return live == nil || live(id)
}

// sessionDir is where session id keeps its transcript and record: its
// task's directory for a task's, the state directory's for another.
func (s *Server) sessionDir(id string, task bool) (string, error) {
	if !task {
		return filepath.Join(s.stateDir, id), nil
	}
	if s.tasks == nil {
		return "", errors.New("this acpd runs no tasks")
	}
	dir, err := s.tasks.Dir(id)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "session"), nil
}

// Release hands session id's conversation to a terminal: its engine stops
// and the marker is written. It returns the record, for the terminal to
// resume the same conversation by.
func (s *Server) Release(id string, task bool) (Record, error) {
	dir, err := s.sessionDir(id, task)
	if err != nil {
		return Record{}, err
	}
	rec, ok := ReadRecord(dir)
	if !ok {
		return Record{}, ErrNoConversation
	}
	s.mu.Lock()
	sess, live := s.sessions[id]
	switch {
	case live && sess == nil:
		s.mu.Unlock()
		return Record{}, fmt.Errorf("%w: it is starting", ErrBusy)
	case sess != nil && (sess.Busy() || sess.Waiting() || s.held(sess)):
		s.mu.Unlock()
		return Record{}, ErrBusy
	}
	delete(s.sessions, id)
	s.mu.Unlock()
	if sess != nil {
		_ = sess.Close()
	}
	turns, err := userTurns(dir)
	if err != nil {
		return Record{}, err
	}
	if err := writeHandoff(dir, Handoff{To: "terminal", ACPSessionID: rec.ACPSessionID, UserTurns: turns, At: time.Now().UTC()}); err != nil {
		return Record{}, err
	}
	return rec, nil
}

// Return hands session id's conversation back from its terminal: the next
// create loads it with the terminal's turns. The caller has ended the
// terminal.
func (s *Server) Return(id string, task bool) error {
	dir, err := s.sessionDir(id, task)
	if err != nil {
		return err
	}
	h, ok := ReadHandoff(dir)
	if !ok {
		return ErrNotHandedOff
	}
	h.To = "view"
	return writeHandoff(dir, h)
}

// userTurns counts the user turns in dir's transcript.
func userTurns(dir string) (int, error) {
	f, err := os.Open(filepath.Join(dir, "stream.ndjson"))
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	defer f.Close()
	n := 0
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64<<10), 256<<20)
	for sc.Scan() {
		if !strings.Contains(sc.Text(), `"kind":"`+KindUserPrompt+`"`) {
			continue
		}
		var ev Event
		if json.Unmarshal(sc.Bytes(), &ev) == nil && ev.Kind == KindUserPrompt {
			n++
		}
	}
	return n, sc.Err()
}

// replayKeep is a load's replay after a terminal: the turns past skip are
// the terminal's, written into the transcript as turns of their own.
type replayKeep struct {
	mu     sync.Mutex
	skip   int // user turns the transcript already has
	turns  int // user turns replayed so far
	inUser bool
	text   strings.Builder
	open   bool // a written turn awaits its turn_end
	timer  *time.Timer
}

// replayQuiet is how long a replay is quiet before its last turn is ended:
// nothing marks a replay's end but the next prompt, which may never come.
const replayQuiet = 2 * time.Second

// update takes one replayed update, writing what is past skip.
func (k *replayKeep) update(t *Transcript, kind string, data json.RawMessage) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if kind == "user_message_chunk" {
		if !k.inUser {
			k.inUser = true
			k.turns++
			k.text.Reset()
		}
		var c struct {
			Content struct {
				Text string `json:"text"`
			} `json:"content"`
		}
		if json.Unmarshal(data, &c) == nil {
			k.text.WriteString(c.Content.Text)
		}
		return
	}
	endsUser := k.inUser
	k.inUser = false
	if k.turns <= k.skip {
		return
	}
	if endsUser {
		if k.open {
			_ = t.AppendValue(KindTurnEnd, TurnEnd{StopReason: "end_turn"})
		}
		_ = t.AppendValue(KindUserPrompt, map[string]string{"text": k.text.String(), "from": "terminal"})
		k.open = true
	}
	_ = t.Append(kind, data)
	if k.timer != nil {
		k.timer.Stop()
	}
	k.timer = time.AfterFunc(replayQuiet, func() { k.end(t) })
}

// end closes the last written turn.
func (k *replayKeep) end(t *Transcript) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.timer != nil {
		k.timer.Stop()
	}
	if k.open {
		_ = t.AppendValue(KindTurnEnd, TurnEnd{StopReason: "end_turn"})
		k.open = false
	}
}
