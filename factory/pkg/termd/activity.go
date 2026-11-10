package termd

import (
	"sync"
	"time"
)

// What a session's program is doing, as its terminal hears it - no engine
// hooks, so the same for gemini, claude, agy or a shell. Most exact first:
//
//  1. OSC 7501, the program status protocol (libghostty-vt only): working,
//     blocked on the user (permission, question, auth), done, error, idle,
//     with a message.
//  2. OSC 9;4 progress: working while a progress report is up.
//  3. A bell or a desktop notification (OSC 9 / 777): it wants the user,
//     until someone next types in it.
//  4. Otherwise: output in the last activeFor is working, none is idle.
//
// The window title is kept for display, not read for meaning.

// The states a session reports (Info.State, the attach state frame).
const (
	StateWorking  = "working"
	StateNeedsYou = "needs-you"
	StateIdle     = "idle"
	StateDone     = "done"
	StateError    = "error"
	StateExited   = "exited"
)

// activeFor is how recent output must be for a program with no other
// signal to count as working.
var activeFor = 5 * time.Second

// Status is a program's own report of what it is doing (OSC 7501, its
// record with no ID).
type Status struct {
	// State is working, blocked, done, error or idle.
	State string
	// Kind is what a blocked program needs: permission, question, auth.
	Kind string
	// Message is one line from the program; untrusted, shown as text.
	Message string
	// App names the program (claude-code, cargo, …).
	App string
}

// signals is what an emulator tells its session about the program, from
// inside its Write: each must return quickly and take no lock but the
// activity's own.
type signals struct {
	title     func(string)
	attention func(msg string)
	progress  func(active bool)
	status    func(*Status)
}

// activity is a session's record of those signals. Its own lock: the
// emulator calls in with its lock held, and attach takes the session's
// lock before the emulator's.
type activity struct {
	mu          sync.Mutex
	title       string
	status      *Status
	progress    bool
	attention   string
	attentionAt time.Time
	lastInput   time.Time
	lastOutput  time.Time
}

func (a *activity) signals() signals {
	return signals{
		title: func(t string) { a.mu.Lock(); a.title = t; a.mu.Unlock() },
		attention: func(msg string) {
			a.mu.Lock()
			a.attention, a.attentionAt = msg, time.Now()
			a.mu.Unlock()
		},
		progress: func(on bool) { a.mu.Lock(); a.progress = on; a.mu.Unlock() },
		status:   func(st *Status) { a.mu.Lock(); a.status = st; a.mu.Unlock() },
	}
}

func (a *activity) output(now time.Time) { a.mu.Lock(); a.lastOutput = now; a.mu.Unlock() }

// input is someone typing: they have seen whatever asked for them.
func (a *activity) input(now time.Time) {
	a.mu.Lock()
	a.lastInput = now
	a.attention = ""
	a.mu.Unlock()
}

// state is what the program is doing now, a message to show with it, and
// the window title.
func (a *activity) state(now time.Time, exited bool) (state, message, title string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	title = a.title
	switch {
	case exited:
		return StateExited, "", title
	case a.status != nil:
		switch a.status.State {
		case "blocked":
			return StateNeedsYou, a.status.Message, title
		case "working":
			return StateWorking, a.status.Message, title
		case "done":
			return StateDone, a.status.Message, title
		case "error":
			return StateError, a.status.Message, title
		default:
			return StateIdle, a.status.Message, title
		}
	case a.attention != "" && a.attentionAt.After(a.lastInput):
		return StateNeedsYou, a.attention, title
	case a.progress || now.Sub(a.lastOutput) < activeFor:
		return StateWorking, "", title
	}
	return StateIdle, "", title
}
