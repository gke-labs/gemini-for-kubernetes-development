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
}

func newGhosttyEmulator(cols, rows int, reply func([]byte)) (emulator, error) {
	term, err := libghostty.NewTerminal(
		libghostty.WithSize(uint16(cols), uint16(rows)),
		libghostty.WithMaxScrollbackBytes(ghosttyScrollback),
		// Called during Write, on its goroutine, with the emulator's
		// lock held: reply must not block or call back in.
		libghostty.WithWritePty(func(_ *libghostty.Terminal, data []byte) { reply(data) }),
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
	return &ghosttyEmulator{term: term, format: format}, nil
}

func (e *ghosttyEmulator) Write(p []byte) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if !e.closed {
		e.term.VTWrite(p)
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
