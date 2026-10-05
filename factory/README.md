# AI Factory CLI (`factory`)

`factory` runs AI coding agents (Gemini CLI, Claude Code or Antigravity) against real GitHub work: issues, pull requests, deployments and open-ended research. Each task runs inside its own Kubernetes sandbox rather than on a laptop.

**The problem it solves.** Pointing an agent at a repository on your own machine doesn't scale and isn't safe:

- one checkout at a time
- the agent holds your credentials
- a dropped terminal kills the job
- nobody notices CI failures or review comments until a human goes looking

`factory` gives every task its own sandbox. That's an [agent-sandbox](https://github.com/kubernetes-sigs/agent-sandbox) `Sandbox` with a persistent `/workspaces` volume and an `envd` daemon. The CLI drives that sandbox over a port-forward and leaves nothing behind on the host. The same CLI also runs as an unattended loop (`factory watch`, which the [Overseer](../overseer/docs/architecture-overseer-factory.md) runs in-cluster). That loop does several things:

- picks up issues
- reacts to CI failures, review comments and merge conflicts on its own PRs
- reviews PRs that are labelled for it
- cleans up after itself

```
 local / Overseer pod                     Kubernetes namespace
+--------------------+   port-forward   +-------------------------------------------+
|  factory CLI       | ===============> | Sandbox  factory-issue-917                |
|  (or factory watch)|   envd :49983    |   envd (Connect-RPC)   daemon :49990     |
+--------------------+                  |   /workspaces (PVC)                       |
                                        |     <repo>/                               |
                                        |     tasks/fix-20261002-101500/            |
                                        |       agent-prompt.txt  execution.log     |
                                        |       pid  exit_code  token-usage.json    |
                                        +-------------------------------------------+
```

---

## Features

### Agents on issues and pull requests
- **Fix an issue.** `factory fix` clones the repository into a sandbox and checks out an issue branch. It runs the agent, then pushes to the bot's fork and opens a PR. It also accepts a bare repository with `--name` and `--instruction` / `--instruction-file`, and `--no-pr` pushes without opening one. `--watch` hands the new PR straight to `pr watch`.
- **Plan first.** `factory recipe plan --url <issue>` drafts an implementation plan in the issue's sandbox, as a task output, without posting anything. Run again, it revises the plan it finds there, against `--feedback` if given. `factory fix --with-plan` folds the approved plan into the fix, and `--apply` (or `factory apply -f`) comments it on the issue.
- **Triage.** `factory recipe triage --url <issue>` suggests labels, priority, duplicates and an assessment, as a task output. `--apply` waits for it and applies the labels and posts the comment, and so does `factory apply -f` on the output.
- **Actions.** A task output lists what can be done with it (`actions:`, declared by the recipe): a triage offers edit, label, comment and reject; a plan offers edit, comment, `run: fix` and reject. `factory apply -f <output> --action <verb>` does one of them; `--action run:fix` writes the plan, as edited, to the issue's sandbox and runs `factory fix --with-plan`.
- **PR lifecycle.**
  - `pr review` reviews the diff using repeatable `--instruction` files or strings. `--publish` takes `no`, `ask`, `yes` or `draft`.
  - `pr investigate` works on CI failures.
  - `pr address-comments` handles review feedback.
  - `pr iterate` rebases and resolves conflicts.
  - `pr watch` loops over all of the above for one PR until it merges or closes.
- **Session continuity.** A sandbox is aliased to its PR with the `factory.gemini.google.com/pr` label. Follow-up PR tasks therefore reuse the sandbox that wrote the code, and `--continue-session` keeps the agent's chat history.
- **Adoption.** `pr adopt open|close` takes over a PR the bot can't push to. `--strategy reuse` copies the commits; `--strategy reimplement` re-implements the change on the latest base. PR tasks on PRs the bot doesn't own fail until the PR is adopted.
- **Custom agents.** `factory agent create` runs an agent definition from the repository's `.agents/`, or a local file with `--local`. A definition is YAML front-matter plus a prompt, and the same definitions drive scheduled chores. `--dry-run` simulates the run without invoking the agent.
- **Workflows.** A definition with `mode: workflow` runs as a long-lived, multi-step process in a `wf-issue-<n>` sandbox, advancing one step per cycle. It is triggered when an issue body references the definition, or with `agent create --session-id`.

### Autonomous watch loop (`factory watch`)
- **Issue intake.** It picks up issues carrying the trigger label (`triggerLabel`, default `factory`; the Overseer image sets `overseer`) and issues assigned to a bot identity, adding the label to the latter. It skips an issue that already has a linked open PR or carries the stop label.
- **PR reactions.** For each bot PR it dispatches, in priority order:
  1. unaddressed feedback → `address-comments`
  2. merge conflict → `iterate`
  3. failing CI → `investigate`
  4. green and labelled for review → `review`

  A PR that passes review is labelled `<trigger>/ready-for-human` and handed to the human assignees. `--pr-inactivity-timeout` pauses PRs that no human has touched for that long.
- **Chores.** Agent definitions under `.agents/` that have a cron `schedule` run as `agent-chore` tasks (`--chores-mode`).
- **Filesystem queue.**
  - Work is queued as task files that move `incoming/ → processing/ → processed/` under `--queue-dir`. A `journal.jsonl` sits alongside.
  - `--mode scan` only discovers and queues work; `--mode run` only processes the queue. Several processes can therefore cooperate through atomic file moves.
  - `--max-actions` and `--max-pending` bound each cycle; `--scan-limit` bounds each scan.
- **Restart-safe.** After a restart it re-adopts tasks whose sandboxes are still running. Tasks that can't be recovered are marked failed. `--task-timeout` fails a stuck task and deletes its sandbox.
- **Garbage collection.** It deletes sandboxes whose issue or PR has closed, and evicts old ones (`--sandbox-eviction-age`). It suspends idle ones by scaling them to zero (`--sandbox-idle-timeout`) and never touches a sandbox with a task in flight.
- **Workflow nudging.** When an issue or PR a workflow is waiting on closes, the workflow's issue is nudged so it advances without a human.
- **Status API on `:13338`.**
  - `GET /api/v1/queue` returns the queue.
  - `DELETE /api/v1/queue/<file>` drops a task, and `POST /api/v1/queue/<file>/priority` re-prioritizes one.
  - `GET /api/v1/status` returns the Gemini key-pool state.

### Runbooks: plan, deploy, tear down (`factory run`)
A *run* is a directory `docs-exploration/agent-runs/<name>/` on the `research/runs` branch of your fork. It holds `runbook.md`, `params.env`, `deploy.sh`, `teardown.sh` and receipts.

- `run plan` writes the procedure and generates the scripts, but executes nothing. `--runbook` starts from `.agents/runbooks/<name>` in the repository, or from one of your earlier runs. `--target <PR number|URL>` deploys a pull request and pins its head commit.
- `run deploy` executes the approved plan, repairs scripts that fail, and then reconciles `runbook.md` with what actually worked.
- `run teardown` removes what the run created, preferring the generated `teardown.sh`.
- If the repository can't be forked (some organisations don't allow forks of private repositories into personal accounts), the run is local-only. The branch is committed in the run's sandbox and never pushed, so deleting that sandbox deletes the run's records, including its `teardown.sh`.

### Research conversations (`factory recipe research`)
- **Start a conversation.** `recipe research --url --session --topic` creates a research sandbox, clones the repository (private repos included; the token is never written to the volume) and hands the topic to a task session in the sandbox's daemon. The sandbox carries a `research-ready` receipt once setup has finished, and the conversation continues through `recipe revise`.
- **Conversation server.** The sandbox daemon serves agent conversations over HTTP using the Agent Client Protocol, under `/v1/sessions` on its loopback task server (`:49990`, reached by port-forward). It handles sessions, prompts, streamed events, permission prompts, cancel and modes (`default`, `auto_edit`, `yolo`), and keeps transcripts under `/workspaces/.acpd/sessions`. It drives `gemini --acp` or Antigravity's `agy_acp_server`; Claude is not supported for research yet.
- **Save notes.** The notes are a task output, and `apply --action push-notes` pushes them to `docs-exploration/research/<note>.md` on the `research/notes` branch of your fork. `apply` holds the GitHub credential, not the agent session.

### Engines and models
- **Choosing an engine.** `--engine gemini|claude|antigravity` (config key `engine`) picks the agent:
  - `gemini --yolo`, which uses `GEMINI_API_KEY`
  - `claude -p`, which needs `ANTHROPIC_API_KEY` in the user secret
  - `agy`, which uses the Gemini key
- **Model fallback.** Each task gets an ordered model list and falls through to the next model when the engine fails. For Gemini that is the current flash models first, then pro. For Claude it is `opus`, then `sonnet`.

### Gemini key pool and quota handling
- **Key source.** With `TOKENSCRIPT_DIR` set, keys come from a token script instead of the single key in the user secret.
- **Quota tracking.** Quota exhaustion is detected from the task log and recorded per **key and model** for 4h. New tasks skip exhausted models, or the whole key once every model is out. Keys reporting `CONSUMER_SUSPENDED` are set aside.
- **Visibility.** `factory status` and the watch status endpoint report healthy, degraded, quota-exceeded and suspended keys.

### Resilient task execution
- **Survives disconnects.** Tasks are launched detached inside the pod, with `pid`, `execution.log` and `exit_code` in the task directory. The CLI tails the log and re-establishes the port-forward when it drops. Re-running a task reattaches to it if it is still running, and reports the result if it has already finished.
- **Flags.**
  - `--detached` returns immediately after launch.
  - `--abort-on-cancel` (default on) kills the in-pod task when you Ctrl-C.
  - `--background` detaches the CLI itself and logs to `$FACTORY_LOGS`.
- **Container restarts.** If the container restarts, interrupted tasks are marked failed (exit 137) instead of hanging forever.

### Credential hygiene
- **No tokens in the engine's environment.** The agent's environment never contains a GitHub token. The task script authenticates git through a URL rewrite, and the engine gets a git config without that rewrite, falling back to `gh auth git-credential`.
- **No tokens in logs.** Tokens are kept out of git config files the agent can read and out of `set -x` traces.
- **Fork check.** Pushes go to the identity's own fork, and a task stops if `origin` is not that fork.
- **Bot identities.** `--user` runs a task as a bot identity (secret `user-<name>`). `roles` in `.factory.cfg` (`coder`, `reviewer`, `agent`) give each task type a pool of bots.
- **Attribution.** `--disclose` (default on) states in PR descriptions, comments and reports that an agent wrote them.

### Observability and operations
- **Token usage.** Every task records per-engine token usage. The CLI harvests it and posts it to a collector (`$COLLECTOR_URL`) served by the hidden `factory token-daemon`, which offers rollups by issue, PR, day and workflow. See [token usage collection](design/token-usage-collection.md).
- **Sandbox tooling.** `factory sandbox` can list, inspect, stream logs (task or envd), exec, cp, suspend/resume, and connect over tmux, or resume a Gemini chat in a sandbox. `factory cleanup` deletes sandboxes older than a given age.
- **Sandbox resources.** Per-sandbox resources: `--image`, `--workspace-disk-size`, `--workspace-storage-class`, `--ephemeral-storage`, `--cpu-request`/`--cpu-limit` and `--memory-request`/`--memory-limit`. Extra configuration can be injected with `--secret` and `--env`.

---

## Getting started

### Prerequisites
- `kubectl` pointing at a cluster, or `kind` for a local one
- `gh` installed and authenticated (`gh auth login`)
- A Gemini API key in `GEMINI_API_KEY`. Claude Code also needs an Anthropic key in the user secret.

### Build
```bash
cd factory/
make build                     # produces bin/factory
alias factory="$PWD/bin/factory"

# or run without building
alias factory="go run github.com/gke-labs/gemini-for-kubernetes-development/factory@main"
```

### Bootstrap the cluster and onboard yourself
```bash
# create (or reuse) a kind cluster named "factory" and install agent-sandbox
GEMINI_API_KEY=yourkey factory up

# use the current kubectl context instead of kind
GEMINI_API_KEY=yourkey factory up --current-context
```
`factory up` then onboards you through `gh`: it detects your login, email and token, and creates your namespace and `factory-user` secret. To onboard manually, or into another namespace:
```bash
factory -n my-namespace user onboard \
  --github-login yourlogin --github-email you@example.com \
  --github-token yourpat --gemini-key yourkey
```

### Check health
```bash
factory status
```
This checks the Kubernetes API, namespace, agent CRDs, user secret, GitHub login and token, Gemini key, and the key-pool state (degraded, quota-exceeded and suspended keys).

### Configuration
Limits, default resources, trigger labels, injected secrets/env and bot role pools live in `.factory.cfg`. It is read from the current directory or from `$FACTORY_CONFIG`. See the [configuration guide](docs/configuration.md).

---

## Common usage

```bash
# Fix an issue, then keep watching the PR it opens
factory fix --url https://github.com/owner/repo/issues/1 --watch --watch-timeout 1h --cleanup

# Repository task without an issue, using Claude Code
factory fix --engine claude --url https://github.com/owner/repo --name refactor-auth \
  --instruction-file ./prompt.txt

# Plan, revise, then implement the approved plan
factory recipe plan --url https://github.com/owner/repo/issues/1
factory recipe plan --url https://github.com/owner/repo/issues/1 --feedback "merge steps 2 and 3"
factory fix --url https://github.com/owner/repo/issues/1 --with-plan

# Review a PR against guidelines and post it as a pending review
factory pr review --pr-url https://github.com/owner/repo/pull/1 \
  --instruction docs/guidelines.md --instruction "ignore test-only changes" --publish draft

# React to CI failures and review comments on a PR until it merges
factory pr watch --pr-url https://github.com/owner/repo/pull/1 --continue-session

# Take over someone else's PR under the bot identity
factory pr adopt open --pr-url https://github.com/owner/repo/pull/1 --strategy reuse

# Deploy a PR from an existing runbook
factory run plan   --url https://github.com/owner/repo --name deploy-pr-42 --runbook deploy-gke --target 42
factory run deploy --url https://github.com/owner/repo --name deploy-pr-42

# Watch a repository (what the Overseer runs)
factory watch --repo owner/repo
factory watch --repo owner/repo --dryrun --once
```

Debugging a sandbox:
```bash
factory sandbox list
factory sandbox logs factory-issue-917            # task execution.log (--daemon for envd)
factory sandbox exec -w /workspaces/repo factory-issue-917 -- make test
factory sandbox chat factory-issue-917 -r latest  # resume the Gemini session
```

---

## Command reference

| Command | What it does |
|---|---|
| `up` | Create/reuse a kind cluster (or `--current-context`), install agent-sandbox, onboard via `gh` |
| `status` | Pre-flight checks for cluster, identity, keys and the key pool |
| `user onboard` | Create a namespace and `factory-user` secret |
| `fix` | Fix an issue (or run an instruction on a repo) and open a PR |
| `recipe plan` | Draft or revise an implementation plan for an issue; `--apply` comments it |
| `recipe triage` | Suggest labels, priority, duplicates; `--apply` applies them |
| `recipe research` | Start a research conversation in a sandbox; `recipe revise` continues it |
| `apply` | Apply a task output (`factory sandbox task output`) to GitHub |
| `pr review` | Review a PR; `--publish no\|ask\|yes\|draft` |
| `pr investigate` | Investigate and fix CI failures on a PR |
| `pr address-comments` | Address review feedback on a PR |
| `pr iterate` | Rebase, resolve conflicts, or apply a `--prompt` |
| `pr watch` | Loop over investigate / address-comments for one PR |
| `pr adopt open\|close` | Re-home a third-party PR under the bot (`--strategy reuse\|reimplement`) |
| `agent create` | Run an `.agents/` (or `--local`) agent definition |
| `run plan\|deploy\|teardown` | Runbook-driven deployments (`--runbook`, `--target`) |
| `watch` | The autonomous scan / dispatch / GC loop |
| `sandbox list\|inspect\|logs\|exec\|cp\|connect\|chat\|suspend\|resume\|delete` | Manage individual sandboxes |
| `cleanup` | Delete sandboxes older than `--older-than` (default 24h) |
| `acpd` | Agent Client Protocol server (runs inside research sandboxes) |
| `sshd` | Embedded SSH server over stdin/stdout for terminal forwarding |
| `daemon`, `token-daemon` (hidden) | Sandbox entrypoint (envd + acpd); token-usage collector |

Global flags (`--engine`, `--user`, `--namespace`, `--timeout`, `--disclose`, resource and injection flags) apply to every command; see `factory --help`.

---

## Further reading

- [Overseer and factory architecture](../overseer/docs/architecture-overseer-factory.md)
- [Configuration guide (`.factory.cfg`)](docs/configuration.md)
- Watch internals: [design note](design/watch-design-note.md), [subcontroller architecture](design/watch-subcontrollers-architecture.md)
- [Resilient and reconnectable task execution](design/resilient-task-execution.md)
- [Multi-engine sandboxes](design/multi-engine.md)
- [Automated PR review](design/pr-automated-review.md) · [PR adoption](design/pr-adopt.md)
- [Workflow orchestration and session reconciliation](design/workflow-orchestration-and-session-reconciliation.md)
- [Token usage collection](design/token-usage-collection.md)
- [GitHub App identity](design/github-app-identity.md)
- [Research warm pool](design/research-warm-pool.md) (design, not built)
