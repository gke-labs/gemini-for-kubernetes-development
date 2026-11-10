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

## Why not

- **libghostty-vt** for the emulator: Zig with a C API, so cgo and a Zig
  build for every architecture in a binary that is `CGO_ENABLED=0` today.
  The emulator is only for snapshots - exact replay covers every resume
  within 4 MiB - so a pure-Go one is enough to start; the emulator is one
  field of `Session` to swap.
- **tmux**: what this replaces. Kept only as the fallback for sandboxes
  whose daemon predates `/v1/terminals`.
- **chemuxer / agent-tmux-web**: prior art for the snapshot-on-reconnect
  (a headless emulator server-side) and for harness-aware launchers and
  status; neither is licensed for reuse, and both are single-host Node
  servers.

## Not yet

- Agent state on the rail (busy / needs you) from the engines' hooks.
- Switching a conversation between the research view (ACP) and its
  engine's CLI here: a handoff of the same session id between two
  processes.
- `factory sandbox attach`: a native client in your own terminal.
