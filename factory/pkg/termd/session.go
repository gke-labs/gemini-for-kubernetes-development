// Package termd is the sandbox daemon's terminal sessions: programs - a
// shell, an engine's own CLI (gemini, claude, agy) - running on a PTY the
// daemon owns, so they outlive every client. A client attaches over a
// websocket and gets the output from a byte offset; one that dropped
// resumes where it stopped, exactly, while the bytes are still kept, and
// one too far behind (or new to a long session) gets the screen as it is
// now, from a terminal emulator the daemon feeds all the output.
//
// It replaces the web terminal's `tmux new -As board` over pods/exec: the
// session lives in the daemon, not in the exec stream, and needs no tmux.
// See design/daemon-terminal-sessions.md.
package termd

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/creack/pty"
)

// keep is how many bytes of a session's output are kept for resuming
// clients; older output is only on the emulator's screen.
var keep = 4 << 20

// Info is a session as GET /terminals lists it.
type Info struct {
	ID      string    `json:"id"`
	Program string    `json:"program"`
	CWD     string    `json:"cwd"`
	Created time.Time `json:"created"`
	Cols    int       `json:"cols"`
	Rows    int       `json:"rows"`
	// Offset is how many bytes of output the session has written.
	Offset int64 `json:"offset"`
	// Clients is how many clients are attached.
	Clients int `json:"clients"`
	// Exited is set once the program has ended, with its code.
	Exited   bool       `json:"exited,omitempty"`
	ExitCode int        `json:"exitCode,omitempty"`
	Ended    *time.Time `json:"ended,omitempty"`
}

// Session is one program on a PTY.
type Session struct {
	id, program, cwd string
	created          time.Time

	cmd *exec.Cmd
	pty *os.File
	emu emulator
	// replies carries the emulator's answers to the program's queries to
	// the PTY (reply), off the goroutine that feeds the emulator.
	replies chan []byte

	mu   sync.Mutex
	cond *sync.Cond
	// buf holds output bytes [base, base+len(buf)).
	buf        []byte
	base       int64
	cols, rows int
	exited     bool
	exitCode   int
	ended      time.Time
	done       chan struct{}

	// clients is how many clients are attached; atomic, because reply
	// reads it from inside the emulator's write.
	clients atomic.Int32
}

// Spec is what a session runs.
type Spec struct {
	// Argv is the program and its arguments.
	Argv []string
	// Env is its whole environment.
	Env []string
	// Dir is its working directory.
	Dir string
	// Program names it for listings: shell, gemini, claude, agy.
	Program    string
	Cols, Rows int
}

// start runs spec on a new PTY.
func start(id string, spec Spec) (*Session, error) {
	if len(spec.Argv) == 0 {
		return nil, errors.New("no program to run")
	}
	cols, rows := clampSize(spec.Cols, spec.Rows)
	cmd := exec.Command(spec.Argv[0], spec.Argv[1:]...)
	cmd.Env, cmd.Dir = spec.Env, spec.Dir
	f, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: uint16(cols), Rows: uint16(rows)})
	if err != nil {
		return nil, fmt.Errorf("starting %s: %w", spec.Argv[0], err)
	}
	s := &Session{
		id: id, program: spec.Program, cwd: spec.Dir, created: time.Now().UTC(),
		cmd: cmd, pty: f, cols: cols, rows: rows, done: make(chan struct{}),
		replies: make(chan []byte, 64),
	}
	s.cond = sync.NewCond(&s.mu)
	emu, err := newEmulator(cols, rows, s.reply)
	if err != nil {
		_ = cmd.Process.Kill()
		_ = f.Close()
		return nil, fmt.Errorf("terminal emulator: %w", err)
	}
	s.emu = emu
	go s.answer()
	go s.pump()
	return s, nil
}

// reply takes the emulator's answer to a query of the program's (cursor
// position, device attributes, …). With a client attached its terminal
// answers, so the emulator's is dropped; with none - a session started
// ahead of anyone attaching - the emulator answers, so a program that
// waits for a reply does not hang. It never blocks the emulator.
func (s *Session) reply(p []byte) {
	if s.clients.Load() != 0 {
		return
	}
	select {
	case s.replies <- append([]byte(nil), p...):
	default:
	}
}

// answer writes the emulator's replies to the program until it ends.
func (s *Session) answer() {
	for {
		select {
		case <-s.done:
			return
		case p := <-s.replies:
			_, _ = s.pty.Write(p)
		}
	}
}

// pump reads the program's output into the kept bytes and the emulator,
// until the PTY closes, then waits for the program.
func (s *Session) pump() {
	chunk := make([]byte, 32<<10)
	for {
		n, err := s.pty.Read(chunk)
		if n > 0 {
			s.emu.Write(chunk[:n])
			s.mu.Lock()
			s.buf = append(s.buf, chunk[:n]...)
			if over := len(s.buf) - keep; over > 0 {
				// Trim in slabs, not every write: copying 4 MiB per
				// chunk would be the cost of a busy session.
				over += keep / 4
				s.buf = append(s.buf[:0:0], s.buf[over:]...)
				s.base += int64(over)
			}
			s.cond.Broadcast()
			s.mu.Unlock()
		}
		if err != nil {
			break
		}
	}
	code := 0
	if err := s.cmd.Wait(); err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			code = ee.ExitCode()
		} else {
			code = -1
		}
	}
	_ = s.pty.Close()
	s.mu.Lock()
	s.exited, s.exitCode, s.ended = true, code, time.Now().UTC()
	s.cond.Broadcast()
	s.mu.Unlock()
	close(s.done)
}

// release frees the emulator, once the session is forgotten: an ended
// session still draws its last screen for whoever attaches until then.
func (s *Session) release() {
	s.emu.Close()
}

// Info is the session's listing.
func (s *Session) Info() Info {
	s.mu.Lock()
	defer s.mu.Unlock()
	in := Info{
		ID: s.id, Program: s.program, CWD: s.cwd, Created: s.created,
		Cols: s.cols, Rows: s.rows, Offset: s.base + int64(len(s.buf)),
		Clients: int(s.clients.Load()), Exited: s.exited, ExitCode: s.exitCode,
	}
	if s.exited {
		t := s.ended
		in.Ended = &t
	}
	return in
}

// Input writes a client's keystrokes to the program.
func (s *Session) Input(p []byte) error {
	_, err := s.pty.Write(p)
	return err
}

// Resize sets the PTY's and the emulator's size; the last client to
// resize wins.
func (s *Session) Resize(cols, rows int) {
	cols, rows = clampSize(cols, rows)
	s.mu.Lock()
	if s.exited || (cols == s.cols && rows == s.rows) {
		s.mu.Unlock()
		return
	}
	s.cols, s.rows = cols, rows
	s.mu.Unlock()
	_ = pty.Setsize(s.pty, &pty.Winsize{Cols: uint16(cols), Rows: uint16(rows)})
	s.emu.Resize(cols, rows)
}

// Kill ends the program's process group: SIGHUP, as a closed terminal
// would, then SIGKILL after grace.
func (s *Session) Kill(grace time.Duration) {
	if s.cmd.Process == nil {
		return
	}
	pid := s.cmd.Process.Pid
	_ = syscall.Kill(-pid, syscall.SIGHUP)
	select {
	case <-s.done:
	case <-time.After(grace):
		_ = syscall.Kill(-pid, syscall.SIGKILL)
		<-s.done
	}
}

// Attachment is where a client's output starts.
type Attachment struct {
	// Offset is the byte the stream starts at.
	Offset int64
	// Reset says the client must clear its terminal first: it gets the
	// session from its first byte, or a snapshot.
	Reset bool
	// Snapshot is the screen as it is now, when the bytes the client
	// would need are no longer kept.
	Snapshot string
}

// attach registers a client and says where its output starts: from want,
// when that is kept; from the first byte, when a new client (want < 0)
// can have everything; else a snapshot and what follows it.
func (s *Session) attach(want int64) Attachment {
	s.clients.Add(1)
	return s.position(want)
}

// position is where output from want starts (attach).
func (s *Session) position(want int64) Attachment {
	s.mu.Lock()
	end := s.base + int64(len(s.buf))
	switch {
	case want >= s.base && want <= end:
		s.mu.Unlock()
		return Attachment{Offset: want}
	case s.base == 0:
		s.mu.Unlock()
		return Attachment{Offset: 0, Reset: true}
	}
	// Taken under the lock, so no output lands between the snapshot and
	// the offset it is good for.
	snap := s.emu.Snapshot()
	s.mu.Unlock()
	return Attachment{Offset: end, Reset: true, Snapshot: snap}
}

// detach unregisters a client.
func (s *Session) detach() {
	s.clients.Add(-1)
}

// errGone is read's error once a client has fallen out of the kept bytes.
var errGone = errors.New("output no longer kept")

// read blocks until there is output past off, or the program has ended
// (io.EOF), or stop is closed, and returns it.
func (s *Session) read(off int64, stop <-chan struct{}) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for {
		select {
		case <-stop:
			return nil, io.ErrClosedPipe
		default:
		}
		if off < s.base {
			return nil, errGone
		}
		end := s.base + int64(len(s.buf))
		if off < end {
			out := make([]byte, end-off)
			copy(out, s.buf[off-s.base:])
			return out, nil
		}
		if s.exited {
			return nil, io.EOF
		}
		s.cond.Wait()
	}
}

// wake unblocks every read, so one whose client has gone sees stop.
func (s *Session) wake() {
	s.mu.Lock()
	s.cond.Broadcast()
	s.mu.Unlock()
}

func clampSize(cols, rows int) (int, int) {
	if cols < 2 || cols > 1000 {
		cols = 120
	}
	if rows < 2 || rows > 500 {
		rows = 32
	}
	return cols, rows
}
