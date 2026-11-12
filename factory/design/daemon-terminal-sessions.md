# Daemon terminal sessions

Status: step 1 (factory daemon) and step 2 (repo-agent proxy + UI) built.

## Why

The web terminal today is xterm.js ↔ websocket ↔ repo-agent ↔ `pods/exec`
running `tmux new -As board` (or `chat-<task>`, an engine CLI resuming
the task's conversation). The program lives only as long as tmux keeps
it, a dropped link loses whatever was on the wire, and tmux sits between
the program and the browser's terminal, translating every byte.

Long-horizon agent work - an engine CLI left running for hours, attached
to and walked away from - wants the program owned by something that
outlives every client, and a client that comes back exactly where it
left off.

## What

`pkg/termd`: the sandbox daemon owns programs on PTYs.

- **Session**: a program (shell, `gemini`, `claude`, `agy`) on a PTY. Its
  output goes to two places: the last `keep` (4 MiB) raw bytes, with
  absolute offsets, and a terminal emulator (`charmbracelet/x/vt`) that
  holds the screen.
- **Attach** (websocket) with `?offset=N`:
  - N still kept → the bytes from N, exactly. A client that dropped
    loses nothing and repeats nothing.
  - new client (no offset), nothing trimmed yet → every byte from 0.
  - otherwise → a snapshot of the emulator's screen (cells, styles,
    cursor, alternate screen), then the bytes after it.
- **Many clients** at once; all see the output, any can type, the last
  resize wins.
- **Queries with no client**: a program that asks its terminal something
  (cursor position, device attributes) while nobody is attached is
  answered by the emulator, so a session started ahead of any client
  does not hang. With a client attached, its terminal answers.
- **Attach-or-create by id** (`POST /terminals {"id":"board"}`), as
  `tmux new -A`: a running session of that id is returned, an ended one
  replaced.
- **Environment**: the pod's, less credentials (`acpd.InheritedEnv`),
  plus `HOME`, `TERM=xterm-256color`, `COLORTERM=truecolor`, and the
  engine's key - handed per request in `X-Engine-Api-Key`, as acpd's
  sessions are - in the engine's variable. The daemon keeps no key.

Served by the task server under `/v1/terminals`, advertised as
`Version.Terminals`; reached, like everything in the daemon, through a
port-forward the API server authorises.

```
GET    /v1/terminals                       every session
POST   /v1/terminals                       start one; attach-or-create by id
GET    /v1/terminals/{id}                  one
DELETE /v1/terminals/{id}                  end it (SIGHUP to its group, then SIGKILL)
GET    /v1/terminals/{id}/attach?offset=N  websocket
```

### Wire

Text frames are JSON control (`termd.Frame`), binary frames output.

- daemon → client: `attached {offset, reset}` first; `snapshot {data}`
  when the client's bytes are no longer kept; `exit {code}` when the
  program ends. A client counts the binary bytes from `offset` to know
  where to resume; `reset` means clear the terminal first.
- client → daemon: `input {data}`, `resize {cols, rows}`; a binary frame
  is input too.

repo-agent passes the frames through unchanged, so the browser speaks
the daemon's protocol.

## What the program is doing

Each session reports a state (`Info.State`, and a `state` frame to
attached clients when it changes): `working`, `needs-you`, `idle`,
`done`, `error`, `exited`. It comes from what the terminal hears, so it
needs no engine hooks and is the same for gemini, claude, agy or a shell
(`pkg/termd/activity.go`). Most exact first:

1. **OSC 7501**, the program status protocol (libghostty-vt only):
   working, blocked on the user (permission, question, auth), done,
   error, idle, with a message. Blocked is `needs-you`.
2. **OSC 9;4** progress: `working` while a report is up.
3. A **bell** or a **desktop notification** (OSC 9 / 777): `needs-you`,
   until someone next types in the terminal.
4. Otherwise output in the last 5 seconds is `working`, none `idle`.

The window title is kept for display only. The message and title come
from the program, so they are shown as text, never as markup.

repo-agent lists the engine terminals (not shells) of running sandboxes
as rows on the Sessions page with that state, and opens one there as its
live terminal.

## A conversation between the session view and a terminal

A recipe's agent session (research, plan, review…) runs on acpd: the
engine in ACP mode, drawn by the session view. "Continue in terminal"
hands the same conversation to the engine's own CLI, in a termd session,
and "back to the view" hands it back. One process drives a conversation
at a time (`pkg/acpd/handoff.go`, `pkg/taskapi/handoff.go`,
`POST /v1/sessions/{id}/handoff`):

- **To the terminal:** refused while a turn or a permission is in
  flight, or while the session's task runs. Otherwise acpd stops the
  engine and writes `handoff.json` beside the session record: the
  conversation id and how many user turns the transcript has. termd then
  starts the CLI in `handoff-<id>`, resuming that conversation
  (`gemini --resume <id>`; agy, which takes no id, `--continue`, its
  latest). While that terminal runs, acpd refuses to open the session.
- **Back to the view:** the terminal ends and the marker says `view`.
  The next open loads the conversation (`session/load`). The engine
  replays all of it; acpd normally drops a replay, since the transcript
  has it, but here it writes the user turns past the recorded count, and
  what follows them, as turns of their own. Those are the ones made in
  the terminal.

## A session that is a terminal

The board's Sessions page starts a session as either kind: **Rich** (the
session view, `factory recipe research`) or **Terminal** (the engine's
CLI, free-form). A terminal session gets research's sandbox — `rsch-…`,
the repository cloned, git and gh as the member, the engine configured —
from `factory recipe terminal`, which runs research's setup and asks
nothing. The board's Request says `terminal: true`; the controller runs
that recipe instead of research's, on the picked engine, claude included
(a CLI needs no acpd).

What the member typed in the box is the CLI's first prompt: the
controller leaves it on the sandbox (`terminal-prompt`), and the board
creates the terminal (`agent-<program>`) with `prompt` the first time it
is opened — a new conversation, the prompt an argument of its own
(`gemini -i`, `claude <prompt>`, `agy -i`) — and removes it then, so a CLI
started again does not ask it again. An empty box is a bare CLI.

The session is one row, keyed by its session id as research's: the row
asked for becomes the sandbox's, whose setup run says it is starting,
and then its terminal's, with the terminal's state.

## The emulator

Two, behind one interface (`emulator`, `pkg/termd/emulator.go`):

- **libghostty-vt** (`go.mitchellh.com/libghostty`, build tag
  `libghostty`), in the sandbox image: Ghostty's terminal emulator,
  linked statically (it depends only on libc). Its snapshot is its VT
  formatter with every extra but the palette: modes (bracketed paste,
  application cursor keys, mouse tracking, kitty keyboard), scrolling
  region, tab stops, charsets, hyperlinks, cursor, and its scrollback - so
  a client reattaching to a TUI types and pastes the way the program
  expects. Built in `images/factory-golang/Dockerfile` from the signed
  release tarballs with Zig.
- **charmbracelet/x/vt**, every other build (the factory CLI on a laptop,
  `go test`): pure Go, no cgo. Its snapshot is cells, styles, cursor and
  alternate screen only. It is also far slower - a few MB/s on a coloured
  build log against hundreds for libghostty-vt - and the emulator is fed
  on the goroutine that reads the PTY, so it is not for sessions with
  heavy output.

The palette is left out because it would override the client's colours
with Ghostty's defaults.

## Why not

- **libghostty-vt as WebAssembly** (wazero, no cgo): native through cgo
  is faster and has maintained Go bindings; the cgo build is contained
  to the sandbox image by the build tag.
- **tmux**: what this replaces. Kept only as the fallback for sandboxes
  whose daemon predates `/v1/terminals`.
- **chemuxer / agent-tmux-web**: prior art for the snapshot-on-reconnect
  (a headless emulator server-side) and for harness-aware launchers and
  status; neither is licensed for reuse, and both are single-host Node
  servers.

## Not yet

- `factory sandbox attach`: a native client in your own terminal.
