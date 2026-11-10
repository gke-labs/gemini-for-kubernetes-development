package termd

import (
	"fmt"
	"strings"
	"sync"

	"github.com/charmbracelet/x/vt"
)

// emulator is a session's model of its terminal: fed every byte of the
// program's output, it draws the screen for a client whose bytes are no
// longer kept, and answers the program's queries (reply) while no client
// is attached. Safe for concurrent use.
type emulator interface {
	// Write feeds it the program's output.
	Write(p []byte)
	Resize(cols, rows int)
	// Snapshot is what, written to a cleared terminal of the same size,
	// draws the terminal as it is now.
	Snapshot() string
	Close()
}

// newEmulator makes a session's emulator, calling reply with its answers
// to the program's queries. A build with the libghostty tag uses
// libghostty-vt (emulator_ghostty.go); others charmbracelet/x/vt.
var newEmulator = newVTEmulator

// EmulatorName is the emulator this build uses, for the daemon's version.
var EmulatorName = "charmbracelet/x/vt"

// vtEmulator is charmbracelet/x/vt: pure Go. Its snapshot is the cells,
// their styles, the cursor and the alternate screen - not the terminal's
// modes (bracketed paste, cursor keys, mouse), its scrolling region or tab
// stops, which libghostty-vt's restores.
type vtEmulator struct {
	emu  *vt.SafeEmulator
	done chan struct{}
	once sync.Once
}

func newVTEmulator(cols, rows int, reply func([]byte)) (emulator, error) {
	e := &vtEmulator{emu: vt.NewSafeEmulator(cols, rows), done: make(chan struct{})}
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := e.emu.Read(buf)
			select {
			case <-e.done:
				return
			default:
			}
			if n > 0 {
				reply(buf[:n])
			}
			if err != nil {
				return
			}
		}
	}()
	return e, nil
}

func (e *vtEmulator) Write(p []byte)        { _, _ = e.emu.Write(p) }
func (e *vtEmulator) Resize(cols, rows int) { e.emu.Resize(cols, rows) }

func (e *vtEmulator) Snapshot() string {
	var b strings.Builder
	if e.emu.IsAltScreen() {
		b.WriteString("\x1b[?1049h")
	}
	b.WriteString("\x1b[0m\x1b[H\x1b[2J")
	b.WriteString(strings.ReplaceAll(e.emu.Render(), "\n", "\r\n"))
	pos := e.emu.CursorPosition()
	fmt.Fprintf(&b, "\x1b[0m\x1b[%d;%dH", pos.Y+1, pos.X+1)
	if e.emu.CursorHidden() {
		b.WriteString("\x1b[?25l")
	} else {
		b.WriteString("\x1b[?25h")
	}
	return b.String()
}

// Close ends the reader of its replies: a cursor position query makes the
// emulator reply, and the reader, reading it, sees done. Not emu.Close:
// vt's Read checks its closed flag without the lock Close sets it under.
func (e *vtEmulator) Close() {
	e.once.Do(func() {
		close(e.done)
		_, _ = e.emu.Write([]byte("\x1b[6n"))
	})
}
