# Multi-engine sandboxes: Claude Code alongside Gemini CLI

Status: proposed
Owner: barney-s
Scope: factory (primary), repo-agent (plumbing), overseer (untouched)

## Motivation

Every factory task today drives one agent engine, hardcoded in the task
scripts: `gemini --yolo --model $MODEL --output-format json`. Claude Code
(`claude`) is an equivalent headless coding agent with the same shape —
npm-installed CLI, print mode, JSON output, native shell/file tools — and
the sandbox image already installs both (`images/factory-golang/Dockerfile`
line 45: `npm install -g @google/gemini-cli @anthropic-ai/claude-code`).

Making the engine selectable per invocation gives us:

- **A/B on real work**: two RepoBoards on the same repo, one engine each,
  comparing pending-review / fix quality side by side.
- **Engine outages/limits stop being outages**: flip a board's engine.
- **Cost/quality tuning per task type** (e.g. cheap engine for triage,
  strong engine for fixes) as a follow-up.

## Why this is cheap: the contracts are engine-agnostic

Factory's interfaces deliberately live *around* the model CLI, not inside
it. None of these change:

- Harvest contracts: banner printing (`ISSUE TRIAGE` / `ISSUE PLAN` /
  `CODE REVIEW`), plan files (`/workspaces/plan-issue-<n>.md`),
  structured review YAML — all produced by the *script* extracting the
  model's final response and by prompt instructions.
- Task-state stamping, sandbox lifecycle, envd, reattach, factorycli.
- Prompt templates (`pkg/tasks/*.txt`) — plain text, engine-neutral.
- The RepoBoard controller and UI — they consume factory's contracts.

The engine surface is exactly: one invocation line + one JSON field name +
one API key + one usage-stats shape.

## Current state (verified touchpoints)

| Component | Engine coupling |
| --- | --- |
| `images/factory-golang/Dockerfile` | installs both CLIs already ✔ |
| `pkg/tasks/*.sh` (9 scripts: fix_issue, triage_issue, plan_issue, review, run_agent, iterate, address_feedback, investigate_failures, adopt) | `runGemini` function: model-fallback loop over `$MODELS`, invocation line, `.response` extraction from `gemini-output.json`, `record_gemini_usage` |
| `pkg/tasks/models.go` + `pkg/geminitokens` | `DefaultModels` (gemini ids), `GetAvailableModelsForKey` (key-tier probing) |
| `pkg/constants/constants.go` | `KeyGeminiAPIKey = "GEMINI_API_KEY"` |
| `pkg/commands/*.go` | env plumbing: `GEMINI_API_KEY`, `MODELS` into every task |
| usage pipeline (`record_gemini_usage`, `usagereport`, token-daemon) | parses gemini JSON stats + `~/.gemini/tmp/**/session-*.jsonl` transcripts |
| repo-agent | `factory-user` secret carries `GEMINI_API_KEY`; board `spec.sandbox` passthrough |

## Design

### Engine selection

One new dimension, threaded top-down, defaulting to `gemini` everywhere so
merging is a no-op:

```
RepoBoard spec.sandbox.engine: gemini | claude   (default gemini)
  → factorycli: --engine <e> on every invocation
    → factory rootFlags.Engine (persistent flag, default gemini,
      overridable via factory config file like image/diskSize)
      → task env: ENGINE=<e>, MODELS=<per-engine list>,
        engine API key env
```

Per-invocation, not per-sandbox: the same fix sandbox may be planned by
one engine and fixed by another (plan/fix share a checkout, not a model
session — refinement context is reconstructed in the prompt, which is
engine-neutral by construction).

### Script-side: `runEngine`

`runGemini` in each script becomes `runEngine` with a per-engine branch —
identical loop/backoff structure, two lines differ:

```sh
case "${ENGINE:-gemini}" in
  claude)
    claude -p --dangerously-skip-permissions --model "$MODEL" \
      --output-format json < "$PROMPT_FILE" > "$OUT/engine-output.json"
    RESPONSE_FIELD=result ;;
  *)
    gemini --yolo --model "$MODEL" --output-format json \
      < "$PROMPT_FILE" > "$OUT/engine-output.json"
    RESPONSE_FIELD=response ;;
esac
```

The python extraction shim reads `$RESPONSE_FIELD` (gemini: `.response`,
claude: `.result`) into the existing `*-output.txt` files; everything
downstream is unchanged. `gemini-output.json` is renamed
`engine-output.json` internally (the old name kept as a symlink for one
release so the usage harvester and any operator muscle memory survive).

To avoid divergently editing 9 scripts: extract the shared functions
(`setupGit`, `setupGitRepos`, `configureGemini`→`configureEngine`,
`record_usage`, `runEngine`) into `pkg/tasks/lib.sh`, embedded and
prepended by `getScriptWithDefaults`. This is the largest mechanical chunk
of the work and pays for itself the first time we touch engine behavior
again.

### Auth

- Constants: add `KeyAnthropicAPIKey = "ANTHROPIC_API_KEY"`.
- factory commands: pass the selected engine's key env into the task
  (claude: `ANTHROPIC_API_KEY`; gemini: unchanged). Fail fast with a clear
  error when the selected engine's key is missing from the user secret.
- repo-agent `ensureFactoryUserSecret`: copy `ANTHROPIC_API_KEY` from the
  member's `anthropic-api-key` secret (same pattern as the gemini key from
  `gemini-vscode-tokens`) when present.
- Claude Code in headless mode authenticates via `ANTHROPIC_API_KEY`; no
  interactive login in the sandbox.

### Models

`MODELS` stays a space-separated fallback list, now per engine:

- gemini: existing `geminitokens.DefaultModels` +
  `GetAvailableModelsForKey` (unchanged).
- claude: `DefaultClaudeModels` (ordered fallback, e.g. a strong model
  then a fast one); no key-tier probing in v1 (`GetAvailableModelsFor` is
  gemini-key-specific — claude uses the static default list, overridable
  by the existing `MODELS` env passthrough).

`pkg/tasks/models.go` gains `ModelsForEngine(engine, geminiKey) string`.

### Usage reporting

v1: **degrade gracefully, don't block.** `record_usage` branches:

- gemini: existing stats parse + `~/.gemini/tmp` transcript mining.
- claude: Claude Code's JSON output carries `usage` and `total_cost_usd`;
  map them into the same `llm-usage.json`/`token-usage.json` shape
  (model → tokens in/out/total; requests). Transcript-derived tool
  telemetry (`tool-telemetry.json`) is gemini-only in v1; claude sessions
  live under `~/.claude/projects/**.jsonl` and get an adapter in phase 4.

The token-daemon and TokenUsage UI already key on the per-task JSON files,
so mapped claude usage flows through with no daemon changes.

### repo-agent plumbing

- CRD: `spec.sandbox.engine` (enum `gemini|claude`, default `gemini`).
- factorycli: `Engine` field on Fix/Review/Triage/Plan/PRWatch options →
  `--engine` arg.
- Gear: an Engine select in the sandbox section of board settings.
- No feed/stage changes: engine is invisible to the board's state machine.
  (Optional cosmetic follow-up: engine name in the Agent-column tooltip.)

### Out of scope (explicitly)

- Overseer: untouched. It inherits the capability implicitly when it
  passes `--engine`, but its flows keep the gemini default.
- Session resume across engines, per-task-type engine routing, agent
  definitions (`factory agent create`) engine selection — all follow-ups
  on the same `ENGINE` plumbing.
- OAuth-style claude login; API key only.

## Implementation plan

Four PRs, each independently shippable, gemini behavior byte-identical
until a caller opts in:

1. **factory: script consolidation (no behavior change).**
   Extract `pkg/tasks/lib.sh` (setupGit, setupGitRepos, configureEngine,
   record_usage, runEngine with only the gemini branch); scripts shrink to
   task-specific pre/post logic. Golden-output test: rendered script for
   each task before/after must be functionally identical (same env
   contract, same output files).
2. **factory: the claude engine.**
   `--engine` root flag + config file key; claude branch in `runEngine`;
   `ANTHROPIC_API_KEY` plumbing + fail-fast validation;
   `ModelsForEngine`; claude usage mapping in `record_usage`.
   Test matrix: unit (flag/env threading, models selection, extraction
   shim on canned gemini/claude JSON), plus one live smoke per task type
   (`factory triage/plan/fix/pr review --engine claude`) on a scratch
   repo.
3. **repo-agent: board plumbing.**
   `spec.sandbox.engine` (CRD regen), factorycli `Engine` passthrough,
   gear select, `ensureFactoryUserSecret` anthropic key copy. Tests:
   options threading, secret materialization with/without the key.
4. **usage/telemetry follow-up.**
   Claude transcript adapter for tool telemetry; TokenUsage UI labels
   engine per task (it already displays per-model rows, so this may be
   free).

Rollout: merge 1–2, rebuild controller image (factory binary + sandbox
image), smoke `--engine claude` by hand from the controller pod; merge 3,
flip one board (e.g. a scratch repo) to `engine: claude`; A/B against a
gemini board on the same repo before recommending defaults.

## Risks & mitigations

- **Prompt drift between engines**: prompts were tuned against gemini.
  Mitigation: contracts are validated by the scripts (empty output = task
  Failed, parked with reason on the board), so a weak engine fails
  loudly, not silently. A/B period before any default change.
- **Usage blind spots** (phase 4 gap): claude cost still lands in
  `llm-usage.json` from the output JSON; only tool telemetry lags.
- **Image size/cold start**: both CLIs are already in the image today, so
  no regression is introduced by this design.
- **`--dangerously-skip-permissions` semantics**: equivalent in spirit to
  `--yolo`; both engines run inside a disposable sandbox pod whose blast
  radius is the workspace PVC + the member's fork. Same trust model.

## Open questions

1. Claude default model list: strongest-first (quality parity for A/B) or
   cost-tiered like the gemini list?
2. Should `spec.sandbox.engine` be mutable freely (next launch uses the
   new engine, in-flight tasks unaffected) — proposed yes — or gated?
3. Do we want per-verb engine overrides (`auto.review` on engine X,
   fixes on Y) enough to justify the extra spec surface later?
