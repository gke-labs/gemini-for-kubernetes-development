# Tasks over a one-shot ACP session

Status: design, not built. Scope: factory (engine transport, task streaming)
and the repo-agent consumers. Hard constraint: the fully automated overseer
/ `factory watch` model keeps working unchanged; everything here is opt-in
until parity is shown.

## Why

Today every engine runs headless (`gemini --output-format json`, `claude -p`,
`agy -p`), wrapped by the task scripts (`runEngine` in `pkg/tasks/lib.sh`).
That has three costs:

1. **No live view of the agent.** stdout is one JSON blob written at the
   end; `execution.log` carries only script echoes and engine stderr
   (retries, 503s). What the agent is doing is invisible until it exits.
2. **No uniform resume.** gemini `--resume latest`, claude `--continue`,
   agy `--continue` — three mechanisms, and agy's CLI store
   (`~/.gemini/antigravity-cli/`) is not the store its ACP server loads from
   (`~/.gemini/antigravity-acp/`). An interactive session cannot pick up an
   agy task.
3. **Per-engine CLI quirks in bash**: flags, argv size limits (agy 128 KiB),
   output-field names, root/sandbox env.

Every engine we run already has an ACP server, and factory already has the
ACP client (`pkg/acpd.Session`: spawn, auth, mode, auto-approve, transcript).

| engine | ACP server | `loadSession` | loads the store its tasks would write |
|---|---|---|---|
| gemini | `gemini --acp` (in `acpd.Engines`) | yes (0.62) | yes |
| antigravity | `agy_acp_server` (in `acpd.Engines`) | yes (1.2.1) | yes, once tasks go through ACP too |
| claude | `@agentclientprotocol/claude-agent-acp` (not yet in image/table) | yes (0.84) | yes (Agent SDK store = `claude -p` store) |

## Design

### 1. `factory acp run` — the one-shot client

```
factory acp run --engine <e> --model <m> --prompt-file <f> --cwd /workspaces/<repo> \
    [--resume <session-id>] --out <task_dir>/engine-output.json
```

A second front end on `pkg/acpd.Session` (acpd's HTTP server is the first):

- spawn the engine from `acpd.Engines` with the same env hygiene (key only to
  the engine, HOME `/workspaces/.home`);
- `session/new`, or `session/load` with `--resume`;
- mode `yolo` + auto-approve every `request_permission`;
- send the prompt as one turn; wait for `end_turn` (or error / timeout);
- **write, into the task dir:**
  - `transcript.jsonl` — acpd's transcript format, so the research terminal
    renders a task unchanged;
  - `engine-output.json` — `{response, session_id, usage, stop_reason}`;
    `response` is the concatenated final agent message (what plan
    extraction reads);
  - `llm-usage.json` / `token-usage.json` in the shape `record_*_usage`
    writes today (token daemon and TokenUsage UI unchanged);
  - `engine_session` — the session id, for resume.
- **stdout:** a human-readable line per event (agent text, `▸ tool: …`,
  plan updates). This lands in `execution.log`, so the existing tail finally
  shows the agent working.
- **stderr:** the engine's stderr passed through verbatim (see contract C2).
- signals: SIGTERM/SIGINT → ACP `cancel`, grace, then kill the engine's
  process group; exit non-zero.
- a turn-level timeout, and a hard answer to anything that waits on a human
  (ask-user / plan approval): auto-answer if the engine allows, else fail
  fast with a clear message — a one-shot must never hang.

### 2. `runEngine` switches transport, not behaviour

```bash
if [ "${FACTORY_ENGINE_TRANSPORT:-cli}" = "acp" ]; then
    factory acp run --engine "${ENGINE:-gemini}" --model "$MODEL" \
        --prompt-file "${PROMPT_FILE}" --cwd "/workspaces/${REPO_NAME}" \
        ${resume_id:+--resume "$resume_id"} --out "$out_json"
else
    # existing per-engine CLI branches, untouched
fi
```

The model-fallback loop, `set +x` key hygiene, bot identity, output
extraction to `plan-output.txt` stay where they are. `GEMINI_CONTINUE_SESSION`
maps to `--resume "$(cat <prev task>/engine_session)"` under ACP.

### 3. Compatibility contract (what must not change)

The consumers of a task are: the controller-side factory command (tails
`execution.log`, detects quota, reads the exit code), its caller
(`factory watch` writes the child's stdout to a per-task log and uses the
exit code; repo-agent parses markers from stdout), and the token daemon.

- **C1** `execution.log` is still the script's stdout+stderr, appended
  live. (It gains content; it loses none.)
- **C2** engine stderr still reaches `execution.log` verbatim —
  `geminitokens.QuotaStreamTracker` detects fatal quota / suspension from it
  and the envd loop kills the task on it. In addition the client prints one
  normalized line for ACP-level errors (`factory-acp: error class=quota|
  auth|transient …`) and the tracker learns that line.
- **C3** exit-code semantics unchanged: 0 success, non-zero failure, 137
  quota-kill, 143 abort.
- **C4** the files scripts and the token daemon read keep their names and
  shapes (`*-output.json` `response` field, `plan-output.txt`,
  `llm-usage.json`, `token-usage.json`).
- **C5** markers the callers parse (ISSUE PLAN banners, triage YAML, draft
  posted, research-ready) are printed by the scripts/commands, not the
  engine — unchanged.
- **C6** the task wrapper (`nohup sh -c … echo $? > exit_code`), pid /
  start_time files and reattach logic are untouched: `factory acp run` is
  just another child of the script.

Under these, `factory watch` needs no change at all: it never sees the
engine, only the child factory process.

### 4. Streaming from the sandbox — make it better regardless

Today (`envd.Client.RunTaskResilient`) the controller side polls every 2 s
with **three** envd execs over a port-forward — `tail -c +offset`, pid
check, exit-code check — per running task, reconnecting on flake.

Replace the poll with **one long-lived streaming exec** per task:

```sh
# offset N supplied by the client
tail -c +N -F execution.log & t=$!
while [ ! -s exit_code ] && <pid alive with matching start_time>; do sleep 1; done
sleep 1; kill $t
echo "@@FACTORY_TASK_EXIT@@ $(cat exit_code 2>/dev/null || echo 137)"
```

- envd already streams exec stdout, so bytes arrive as written, not every
  2 s;
- the client counts bytes it has consumed; on a flake it reconnects and
  restarts from that offset (same resilience as today, one exec instead of
  three per tick);
- the sentinel line is stripped by the client, never written to the
  caller's stdout; the exit code comes from it (the existing checks remain
  as the fallback when the stream ends without a sentinel);
- quota detection runs on the streamed bytes exactly as now.

This is independent of ACP and benefits the CLI transport too — ship it
first. It stays on envd (port-forward through the kube API, RBAC-gated), so
it needs no new port and no new auth.

Structured events: the task's `transcript.jsonl` is the machine-readable
stream. repo-agent's terminal reads it the way it reads a research
transcript (offset-based), via the same exec/stream path — no acpd needed
for a *task* view.

### 5. What this enables (built on top, separately)

- **Live task terminal** in the repo-agent UI (render `transcript.jsonl`).
- **Continue a task interactively**: acpd `POST /sessions {resume:<id>}` →
  `session/load` of the task's conversation — uniform for all engines.
- **Hand back**: the next task runs `factory acp run --resume <id>`,
  continuing the human-refined conversation; pushes still go through the
  scripts, or a deterministic, model-free **Publish** step (setupGit +
  commitAndPush, `gh pr create` when there is no PR yet).

Rules that make that safe, and keep overseer unchanged:

- **Modes are exclusive in time** on a sandbox: agentic task, interactive
  session, Publish. The controller does not launch a task while a session
  is busy; attaching waits for the task to end.
- **Reset guard keyed on a session marker, not on a dirty tree.** Every
  task starts with `git reset --hard && git clean -fd`, which would wipe
  unpublished session edits. acpd writes a marker when a session first
  prompts; tasks refuse to reset while it exists; Publish / Discard clear
  it. Overseer never runs acpd, so its reset-after-failure is unchanged.
- **acpd authentication first.** Task sandboxes hold the PAT in
  `/workspaces/.home` (gh `hosts.yml`, git `insteadOf`), and acpd's engine
  shares that HOME — which resume needs. acpd must require a bearer token
  only the repo-agent API holds before it runs in any task sandbox (the
  cluster has no NetworkPolicies).

## Rollout

1. **Streaming exec** (§4) for all tasks behind an envd-client switch;
   default on only after it has run on repo-agent boards. Independent of ACP.
2. **Probe** in a throwaway pod, per engine: one-shot turn; usage in the
   prompt result (or `_meta`)?; ACP error shapes for 429/quota/auth; model
   selection per session; a long unattended turn; cancel.
3. **`factory acp run`** + `runEngine` branch, opt-in via
   `FACTORY_ENGINE_TRANSPORT=acp`, which repo-agent sets; **antigravity
   first** (the only engine the CLI transport cannot resume), then gemini.
   claude arrives with its acpd engine entry.
4. **Parity**: same tasks both ways on real issues/PRs — result, pushes,
   exit codes, usage numbers, quota handling.
5. Default stays `cli`. Flipping overseer is a separate, explicit decision.

## Risks / open questions

- **Usage fidelity** over ACP varies by engine — the biggest parity risk.
  If an engine reports nothing, fall back to its telemetry/stat files, or
  keep that engine on `cli` until it does.
- **Quota detection** relies on engine stderr text; ACP servers may log
  differently than the CLIs. C2's normalized line is the durable fix; the
  tracker needs cases for it.
- **Headless vs ACP defaults**: interactive-only tools, settings, trust —
  acpd has already met most of these (trust, auth method, IPv6 flag).
- **Process ownership**: the engine is a grandchild of the task wrapper;
  quota kill (`kill -9 -<pgid>`) and abort must still reach it — the client
  must not setsid the engine.
- **One writer per conversation**: a task resuming a session id that acpd
  also has open would fork it — the controller must not launch while an
  interactive session on that sandbox is live.
