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

- Switching a conversation between the research view (ACP) and its
  engine's CLI here: a handoff of the same session id between two
  processes.
- `factory sandbox attach`: a native client in your own terminal.
