//go:build libghostty

package termd

import (
	"sync"

	"go.mitchellh.com/libghostty"
)

// A build with the libghostty tag - the sandbox image's, which links
// libghostty-vt statically (images/factory-golang/Dockerfile) - uses
// Ghostty's terminal emulator. Its snapshot restores what a program set
// up besides the screen: modes (bracketed paste, application cursor keys,
// mouse tracking, kitty keyboard), scrolling region, tab stops, charsets,
// hyperlinks, and the scrollback it keeps.
func init() {
	newEmulator = newGhosttyEmulator
	EmulatorName = "libghostty-vt"
}

// ghosttyScrollback bounds the history an emulator keeps, and so the
// memory of a session and the size of its snapshot.
const ghosttyScrollback = 4 << 20

// ghosttyExtras is what a snapshot restores besides the cells: everything
// but the palette, which would override the client's colours with
// Ghostty's defaults (a program that set its own palette loses it).
const ghosttyExtras = libghostty.FormatterExtrasAll &^ libghostty.FormatterExtraPalette

type ghosttyEmulator struct {
	mu     sync.Mutex
	term   *libghostty.Terminal
	format *libghostty.Formatter
	closed bool

	sig signals
	// titled says the title changed during a write; read after it, not
	// from inside the terminal's callback.
	titled bool
}

func newGhosttyEmulator(cols, rows int, reply func([]byte), sig signals) (emulator, error) {
	e := &ghosttyEmulator{sig: sig}
	// The callbacks run during Write, on its goroutine, with the
	// emulator's lock held: none blocks or calls back in.
	term, err := libghostty.NewTerminal(
		libghostty.WithSize(uint16(cols), uint16(rows)),
		libghostty.WithMaxScrollbackBytes(ghosttyScrollback),
		libghostty.WithWritePty(func(_ *libghostty.Terminal, data []byte) { reply(data) }),
		libghostty.WithTitleChanged(func(*libghostty.Terminal) { e.titled = true }),
		libghostty.WithBell(func(*libghostty.Terminal) { sig.attention("bell") }),
		libghostty.WithDesktopNotification(func(_ *libghostty.Terminal, n libghostty.TerminalDesktopNotification) {
			msg := n.Body
			if n.Title != "" {
				msg = n.Title + ": " + n.Body
			}
			sig.attention(msg)
		}),
		libghostty.WithProgressReport(func(_ *libghostty.Terminal, r libghostty.TerminalProgressReport) {
			sig.progress(r.State != libghostty.TerminalProgressStateRemove)
		}),
		libghostty.WithProgramStatus(func(_ *libghostty.Terminal, st libghostty.ProgramStatus) {
			if st.ID != "" {
				return // a sub-record: the session shows the program's own
			}
			if st.State == libghostty.ProgramStatusStateClear {
				sig.status(nil)
				return
			}
			sig.status(&Status{State: programState(st.State), Kind: programKind(st.Kind), Message: st.Message, App: st.App})
		}),
	)
	if err != nil {
		return nil, err
	}
	format, err := libghostty.NewFormatter(term,
		libghostty.WithFormatterFormat(libghostty.FormatterFormatVT),
		libghostty.WithFormatterExtras(ghosttyExtras),
		// Untrimmed: the screen's blank rows too, so the last rows the
		// snapshot writes are the screen, where its cursor position
		// expects them, after the scrollback above.
		libghostty.WithFormatterTrim(false),
	)
	if err != nil {
		term.Close()
		return nil, err
	}
	e.term, e.format = term, format
	return e, nil
}

func programState(s libghostty.ProgramStatusState) string {
	switch s {
	case libghostty.ProgramStatusStateWorking:
		return "working"
	case libghostty.ProgramStatusStateBlocked:
		return "blocked"
	case libghostty.ProgramStatusStateDone:
		return "done"
	case libghostty.ProgramStatusStateError:
		return "error"
	}
	return "idle"
}

func programKind(k libghostty.ProgramStatusKind) string {
	switch k {
	case libghostty.ProgramStatusKindPermission:
		return "permission"
	case libghostty.ProgramStatusKindQuestion:
		return "question"
	case libghostty.ProgramStatusKindAuth:
		return "auth"
	}
	return ""
}

func (e *ghosttyEmulator) Write(p []byte) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		return
	}
	e.term.VTWrite(p)
	if e.titled {
		e.titled = false
		if t, err := e.term.Title(); err == nil {
			e.sig.title(t)
		}
	}
}

func (e *ghosttyEmulator) Resize(cols, rows int) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if !e.closed {
		_ = e.term.Resize(uint16(cols), uint16(rows), 0, 0)
	}
}

func (e *ghosttyEmulator) Snapshot() string {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		return ""
	}
	s, err := e.format.FormatString()
	if err != nil {
		return ""
	}
	// The formatter draws from the top-left of a terminal it takes as
	// fresh; the client has reset its own, this makes sure.
	return "\x1b[0m\x1b[H\x1b[2J" + s
}

func (e *ghosttyEmulator) Close() {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		return
	}
	e.closed = true
	e.format.Close()
	e.term.Close()
}
