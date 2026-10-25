# AI Factory CLI (`factory`)

`factory` runs AI coding agents (Gemini CLI, Claude Code or Antigravity) against real GitHub work: issues, pull requests, deployments and open-ended research. Each task runs inside its own Kubernetes sandbox rather than on a laptop.

**The problem it solves.** Pointing an agent at a repository on your own machine doesn't scale and isn't safe:

- one checkout at a time
- the agent holds your credentials
- a dropped terminal kills the job
- nobody notices CI failures or review comments until a human goes looking

`factory` gives every task its own sandbox. That's an [agent-sandbox](https://github.com/kubernetes-sigs/agent-sandbox) `Sandbox` with a persistent `/workspaces` volume and factory's own daemon, which runs its tasks and serves them over a task server. The CLI drives that sandbox over a port-forward and leaves nothing behind on the host. The same CLI also runs as an unattended loop (`factory watch`, which the [Overseer](../overseer/docs/architecture-overseer-factory.md) runs in-cluster). That loop does several things:

- picks up issues
- reacts to CI failures, review comments and merge conflicts on its own PRs
- reviews PRs that are labelled for it
- cleans up after itself

```
 local / Overseer pod                     Kubernetes namespace
+--------------------+   port-forward   +-------------------------------------------+
|  factory CLI       | ===============> | Sandbox  factory-issue-917                |
|  (or factory watch)|   task server    |   factory daemon (task server) :49990    |
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
- **Fix an issue.** `factory recipe fix --url <issue>` works on the issue in its sandbox, on a branch from the latest default branch. The agent commits; factory pushes the branch to your fork. The result is a Change task output; `--apply --action open-pr` (or `factory apply -f`) opens the PR, a draft unless `--draft false`, and `--labels` / `--pr-labels` label it. `pr watch` then follows the PR.
- **Plan first.** `factory recipe plan --url <issue>` drafts an implementation plan in the issue's sandbox, as a task output, without posting anything. Run again, it revises the plan it finds there, against `--feedback` if given. The agent writes the plan to a file of its task, so asked to in its session (Continue session) it rewrites the plan, and the result follows. `--apply` (or `factory apply -f`) comments it on the issue, and its `run: fix` action starts the fix recipe with the plan, as edited.
- **Triage.** `factory recipe triage --url <issue>` suggests labels, priority, duplicates and an assessment, as a task output. `--apply` waits for it and applies the labels and posts the comment, and so does `factory apply -f` on the output.
- **Fan-out spec.** `factory recipe fanout --url <issue>` writes a fan-out spec (see *One task, many items*) for an issue that lists one task for many items, as a task output. `--apply` adds the fan-out and stop labels and posts it as the spec comment, for maintainers to edit before they remove the stop label.
- **Actions.** A task output lists what can be done with it (`actions:`, declared by the recipe): a triage offers edit, label, comment and reject; a plan offers edit, comment, `run: fix` and reject. `factory apply -f <output> --action <verb>` does one of them; `--action run:fix` starts `recipe fix` with the plan, as edited, as its `plan` input.
- **PR lifecycle.**
  - `recipe care-ci` works on CI failures of a PR of yours (the overseer runs it with `--apply --action post-replies`).
  - `recipe care-comments` answers review feedback, trusted authors' only (the overseer runs it with `--apply --action post-replies`).
  - `recipe care-rebase` rebases a PR of yours onto the default branch and resolves conflicts (the overseer runs it with `--apply --action post-replies`).
  - `pr watch` follows up with care for one PR until it merges or closes.
- **Session continuity.** A sandbox is aliased to its PR with the `factory.gemini.google.com/pr` label. Follow-up PR tasks therefore reuse the sandbox that wrote the code, and `--continue-session` keeps the agent's chat history.
- **Adoption.** `pr adopt open|close` takes over a PR the bot can't push to, through the `adopt` recipe: `--strategy reuse` copies the commits to a branch on your fork; `--strategy reimplement` has the agent re-implement the change on the latest base. `open-pr` then opens the adopted PR and comments on the original (and closes it, with `close`). PR tasks on PRs the bot doesn't own fail until the PR is adopted.
- **The repository's own recipes.** A repository can keep recipes in `.agents/recipes/<name>.yaml`; `factory recipe run --recipe <name> --url <item>` runs one, read at the default branch, and `factory recipe list --url <repo>` lists them beside the built-ins. A repository's recipe can't take a built-in's name or join a built-in's session, and can't yet declare a `sandbox:` of its own ([design/repo-recipes.md](design/repo-recipes.md)).
- **Custom agents.** `factory agent create` runs an agent definition from the repository's `.agents/`, or a local file with `--local`, in the agent's own sandbox. A definition is YAML front-matter plus a prompt, and the same definitions drive scheduled chores. A chore runs on the repository as the `agent` recipe: its commits open (or update) a PR from `agent/<name>`, and `skipPR: true` keeps no changes. `--run-name` names the run, so running again follows it rather than starting another.
- **Workflows.** A definition with `mode: workflow` runs as a long-lived, multi-step process on an issue, as the `workflow` recipe in a `wf-issue-<n>` sandbox, advancing one step per cycle; its journal lives on the fork's `factory-<n>` branch, and its precondition and the agent's shell get `SESSION_ID` and `RELATED_ISSUES`. It is triggered when an issue body references the definition, or with `agent create --url <issue>`.

### Autonomous watch loop (`factory watch`)
- **Rules decide, the scanners carry out.** What the loop does on an issue or PR is decided by its rules: YAML files, prebaked in factory ([`pkg/watch/rules/`](pkg/watch/rules/)), each saying which items it takes (`when:`), what it runs or writes (`run:`) and how it is paced (`policy:` cooldowns, once per head, give-ups). The issue and PR scanners read each item's facts and carry out the engine's decisions; every task carries the rule, recipe, apply and inputs that decided it, and the dispatcher runs exactly that. `factory watch rules` lists them, `--flow` draws how a kind's rules apply, and the [watch rules guide](docs/watch-rules.md) describes the fields.
- **Gates first.** Before any rule, an item that carries the stop label, is closed, is numbered below `MIN_NUMBER` (the Overseer's `spec.minNumber`) or is in the merge queue is left alone.
- **Issue rules**, in order:
  1. `workflow-missing`: an issue that names a workflow definition that cannot be loaded is stopped, with a comment saying which and why.
  2. `workflow`: an issue whose body names a workflow runs its next step (the `workflow` recipe).
  3. The plan flow ([plans and issue graphs](docs/plan-and-graph.md)), by command label: `plan` (`<trigger>/plan`: the `plan` recipe writes or revises the issue's one plan comment), then on `<trigger>/implement` by the plan's shape - `implement` (one PR: `fix-with-plan`), `implement-graph` (a Breakdown: the graph label, for the graph controller), `implement-unplanned` (no plan: a comment).
  4. `fix`: an issue the overseer is pointed at (the trigger label - `triggerLabel`, default `factory`; the Overseer image sets `overseer` - a pool assignee, or opened by the watcher) with no linked open PR, not in the plan flow, is fixed with `recipe fix --apply --action open-pr --pr-labels --input draft=false`: the PR opens ready for review, labelled with the trigger label, the config's `additionalLabels` and the issue's labels.

  An issue has one task at a time; after 3 failed fixes since a human spoke or the last pause, `fix` gives up with the stop label.
- **PR rules**, in order, on the bot's PRs:
  1. `inherit-labels`: the PR takes the labels of the issues it closes.
  2. `inactive`: a PR no human has touched for `--pr-inactivity-timeout` is paused.
  3. `comments`: new trusted feedback → `recipe care-comments`; the feedback is acknowledged (👀) when the task starts and resolved (👍/😕) when it ends; after 3 failed runs since the head, a human or a pause, it gives up.
  4. `conflict`: a merge conflict → `recipe care-rebase`, once per head.
  5. `ci`: failing checks → `recipe care-ci`; after 3 attempts since the head, a human or a pause, it gives up with the stop label.
  6. `review`: green and wanting a review → `recipe review`, once per head.
  7. `ready`: a PR that passed is labelled `<trigger>/ready-for-human` and handed to the human assignees; `ready-unreview` takes `*/review` off it.

  The first of comments, conflict, ci and review that decides to run takes the PR.
- **Decided again at start.** Just before a queued task starts, the engine decides its rule again on the item's facts read then. A gate that now holds, a head that moved, or a rule that no longer decides (feedback gone, checks green, an earlier rule took the item) drops it, with the reason.
- **Watching the rules.** The queue API (`GET /api/v1/queue`, field `rules`) and the overseer page show what each rule ran, gave up on and dropped since the watch started, and a consistency check of every recorded decision. `--rules-disabled review,ci` (the Overseer's `spec.rules.disabled`) is the emergency switch: those rules decide nothing and their queued tasks are dropped. `--rules-record` (the Overseer's `spec.rulesRecord`) writes each decision with its facts to a file, for `watch rules replay` (run a rule change against what the watch saw) and `watch rules explain --record` (why it acted, or did not, on one item).
- **Chores.** Agent definitions under `.agents/` that have a cron `schedule` run as `agent-chore` tasks (`--chores-mode`).
- **Filesystem queue.**
  - Work is queued as task files that move `incoming/ → processing/ → processed/` under `--queue-dir`. A `journal.jsonl` sits alongside.
  - `--mode scan` only discovers and queues work; `--mode run` only processes the queue. Several processes can therefore cooperate through atomic file moves.
  - `--max-actions` and `--max-pending` bound each cycle; `--scan-limit` bounds each scan.
- **Restart-safe.** Every task's command names its run (`--run-name`), so after a restart the watch requeues what was in flight. Run again under the same name, a task follows its run if it's still going, applies the result of one that finished, or finds it already applied. `--task-timeout` fails a stuck task and deletes its sandbox.
- **Garbage collection.** It deletes sandboxes whose issue or PR has closed, and evicts old ones (`--sandbox-eviction-age`). It suspends idle ones by scaling them to zero (`--sandbox-idle-timeout`) and never touches a sandbox with a task in flight.
- **Workflow nudging.** When an issue or PR a workflow is waiting on closes, the workflow's issue is nudged so it advances without a human. Like chores, this is code rather than a rule; the step is a workflow task, decided again at start as the `workflow` rule.
- **Status API on `:13338`.**
  - `GET /api/v1/queue` returns the queue.
  - `DELETE /api/v1/queue/<file>` drops a task, and `POST /api/v1/queue/<file>/priority` re-prioritizes one.
  - `GET /api/v1/status` returns the Gemini key-pool state.

### Runbooks: plan, deploy, tear down (the `run-*` recipes)
Every recipe on a repository declares its sandbox. Most run in one of their own: `sandbox: {prefix, cpu-request, cpu-limit, memory-request, memory-limit, ephemeral-storage, disk}`, every size required, since nothing else sizes it. It is given its sandbox with `--sandbox-key <id>`: runs of any recipe of that prefix with the same key share one, `<prefix>-<repo>-<hash of the key>`, made as the `factory-deployer` service account. Every such repository sandbox carries the same labels and annotations, whatever the prefix: `sandbox.gemini.google.com/type` (the prefix) and `key` (the key's hash), and the annotations `key-id` (the key), `ready` (the receipt written once its task is handed over; a sandbox without it is replaced after 20 minutes) and `engine`. Two special kinds remain: `sandbox: {kind: warm}` (one per repository, design/warm-workspace.md) and `sandbox: {kind: agent}` (`factory agent create`'s). Research's prefix is `rsch`, the run recipes' `run`.

A *run* is a branch of its own, `runs/<name>`, which holds only the run's files: `runbook.md`, `params.env`, `deploy.sh`, `teardown.sh` and receipts. It lives in the run's sandbox, checked out at `.factory-run/` in the clone, and is pushed to your fork after every phase. Every run branch starts from `runs/root`, an empty commit.

Each phase is a recipe on the repository, `factory recipe run-<phase> --url <repo> --run <name> --sandbox-key <name>`, in the run's own sandbox, `run-<repo>-<hash of the key>`.

- `run-plan` writes the procedure and generates the scripts, but executes nothing. `--runbook <name>` starts from `.agents/runbooks/<name>` in the repository, or from one of your earlier runs. `--target-pr <n>` deploys a pull request and pins its head commit.
- `run-deploy` executes the approved plan, repairs scripts that fail, and then reconciles `runbook.md` with what actually worked.
- `run-teardown` removes what the run created, preferring the generated `teardown.sh`.
- If the repository can't be forked (some organisations don't allow forks of private repositories into personal accounts), the run is local-only. Its branch is committed in the run's sandbox and never pushed, so deleting that sandbox deletes the run's records, including its `teardown.sh`. The Runs tab labels such a run.

### Research conversations (`factory recipe research`)
- **Start a conversation.** `recipe research --url <repo> --sandbox-key <id> --topic` creates (or reuses) the research sandbox the key names, sets up git and gh as you and clones the repository (your fork as `origin` when one can be made, the repository as `upstream`), and hands the topic to a task session in the sandbox's daemon. The agent can change files, commit and push a branch to your fork when you ask it to. The sandbox is sized by the recipe's `sandbox:` alone: research.yaml's is 4/8 CPU, 12Gi/16Gi memory, 10Gi ephemeral storage and a 40Gi disk. The sandbox carries a `ready` receipt once setup has finished, and the conversation continues in the task's session; asked to save notes, the agent writes them up as the task's Notes result.
- **Conversation server.** The sandbox daemon serves agent conversations over HTTP using the Agent Client Protocol, under `/v1/sessions` on its loopback task server (`:49990`, reached by port-forward). It handles sessions, prompts, streamed events, permission prompts, cancel and modes (`default`, `auto_edit`, `yolo`), and keeps transcripts under `/workspaces/.acpd/sessions`. It drives `gemini --acp` or Antigravity's `agy_acp_server`; Claude is not supported for research yet.
- **Save notes.** The notes are a task output, and `apply --action push-notes` pushes them to `docs-exploration/research/<note>.md` on the `research/notes` branch of your fork. `apply` holds the GitHub credential, not the agent session.

### Reviews as a recipe (`factory recipe review`)
- **Review a PR.** `recipe review --url <PR> [--instruction …]` reviews the PR in a sandbox of its own (`review-<repo>-<n>`). The clone fetches the PR's head and base, and the agent reviews with `git diff` and no GitHub credentials (`credentials: clone`). The result is a Review task output pinned to the commit reviewed.
- **Post it as a draft.** `apply --action post-review` posts it as your pending review on the PR, at that commit, folding comments that are not on a line of the diff into the body. You read, change and submit it on GitHub. Posting again replaces the pending review factory posted, and refuses to touch one you started yourself.
- **Submit it.** `apply --action submit-review` submits it as a COMMENT review instead, once per task output; it is never offered in the output, and `--apply --action submit-review` is how the watch daemon reviews a PR, as the reviewer identity (`--user`).

### Fan-out (`factory watch fanout`)
- **One task, many items.** A parent issue with `## Task` (a Go template for one item: `{{.item.name}}`, `{{.parent.title}}`), `## Items` (a checklist, or `items.from` a JSON file in the repo or at an https URL, with `select`, `where` and `name`), and optionally `## Finally` and `## Fan-out` (settings) gets a child issue per item, or per growing `group` of items. The spec is the newest comment marked `<!-- factory:fanout-spec -->` by you or a maintainer, else the body.
- **Slow start.** `watch fanout --url <issue>` runs one pass: children are labelled `overseer` within a window that starts at 2, grows by one per child completed and halves per PR closed unmerged. It stops (`overseer/stop`) at checkpoints, rewrites children not yet labelled from an edited spec, runs the `Finally` child last, and keeps a progress comment on the parent. `--dry-run` writes nothing. The watch daemon runs the same pass on every open parent labelled `overseer/fanout` (every 5 minutes, and as soon as a child closes). On a parent with no spec, it runs the `fanout` recipe (with `--apply`) to propose one, held by the stop label for maintainers to edit; see [design/fanout.md](design/fanout.md).

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
- **No tokens in the engine's environment.** The agent's environment never contains a GitHub token: the sandbox daemon starts the engine with the pod's environment less anything that looks like a credential. The task's own steps authenticate git through a URL rewrite in the global git config, which the engine's shell can still read; scoping that per step is not built yet.
- **No tokens in logs.** Tokens are kept out of git config files the agent can read and out of `set -x` traces.
- **Fork check.** Pushes go to the identity's own fork, and a task stops if `origin` is not that fork.
- **Bot identities.** `--user` runs a task as a bot identity (secret `user-<name>`). `roles` in `.factory.cfg` (`coder`, `reviewer`, `agent`) give each task type a pool of bots.
- **Attribution.** `--disclose` (default on) states in PR descriptions, comments and reports that an agent wrote them.

### Observability and operations
- **Token usage.** Every task records per-engine token usage. The CLI harvests it and posts it to a collector (`$COLLECTOR_URL`) served by the hidden `factory token-daemon`, which offers rollups by issue, PR, day and workflow. See [token usage collection](design/token-usage-collection.md).
- **Sandbox tooling.** `factory sandbox` can list, inspect, stream logs (a task's or the sandbox daemon's), exec, cp, suspend/resume, and connect over tmux, or resume a Gemini chat in a sandbox. `factory cleanup` deletes sandboxes older than a given age.
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
# Fix an issue with Claude Code and open its PR as a draft, then follow it up
factory recipe fix --engine claude --url https://github.com/owner/repo/issues/1 --apply --action open-pr
factory pr watch --pr-url https://github.com/owner/repo/pull/2

# Plan, revise, then implement the approved plan
factory recipe plan --url https://github.com/owner/repo/issues/1
factory recipe plan --url https://github.com/owner/repo/issues/1 --feedback "merge steps 2 and 3"
# then apply its task output (factory sandbox task output): factory apply -f <output> --action run:fix

# Review a PR against guidelines and post it as a pending review
factory recipe review --url https://github.com/owner/repo/pull/1 \
  --instruction docs/guidelines.md --instruction "ignore test-only changes" --apply

# Follow up CI failures and review comments on a PR of yours with care, until it merges
factory pr watch --pr-url https://github.com/owner/repo/pull/1

# Take over someone else's PR under the bot identity
factory pr adopt open --pr-url https://github.com/owner/repo/pull/1 --strategy reuse

# Deploy a PR from an existing runbook
factory recipe run-plan   --url https://github.com/owner/repo --run deploy-pr-42 --runbook deploy-gke --target-pr 42
factory recipe run-deploy --url https://github.com/owner/repo --run deploy-pr-42

# Watch a repository (what the Overseer runs)
factory watch --repo owner/repo
factory watch --repo owner/repo --dryrun --once
# One loop in dry run
factory watch --repo owner/repo --pr-mode dryrun
```

Debugging a sandbox:
```bash
factory sandbox list
factory sandbox logs factory-issue-917            # task execution.log (--daemon for the sandbox daemon's)
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
| `recipe fix` | Fix an issue; `--apply --action open-pr` opens the PR (`--draft false`, `--labels`, `--pr-labels`) |
| `recipe plan` | Draft or revise an implementation plan for an issue; `--apply` comments it |
| `recipe triage` | Suggest labels, priority, duplicates; `--apply` applies them |
| `recipe review` | Review a PR in its own sandbox; `--apply` posts it as a pending review, `--action submit-review` submits it |
| `recipe fanout` | Write a fan-out spec for an issue; `--apply` posts it and adds the fan-out and stop labels |
| `recipe research` | Start a research conversation in a sandbox; its notes are written when asked |
| `watch fanout` | One fan-out pass over a parent issue: create, rewrite and label children in a slow start |
| `watch rules` | List the watch's prebaked rules (what each looks at, runs, and as whom; `-o yaml\|json` for the whole rules; `--dir` lays a checkout's `.agents/watch/` over them); see [design/watch-rules.md](design/watch-rules.md) |
| `watch rules replay <decisions.jsonl>` | Replay the decisions the watch recorded (`watch --rules-record`, the Overseer's `spec.rulesRecord`) through the rules and list where they decide differently |
| `watch rules explain <PR or issue URL>` | Walk one item through the rules, read live or from a record (`--record`), and say what each rule decides and why |
| `watch rules decide -f <scenario.yaml>` | Decide items written by hand and check what was expected |
| `apply` | Apply a task output (`factory sandbox task output`) to GitHub |
| `peek --url <repo>` | Print the git tree SHA of each folder under the repository's `.agents/` at its default branch, in one call: whether its recipes (and the like) changed since a SHA was seen |
| `recipe care-ci` | Fix the CI failures of a PR of yours; `--apply --action post-replies` posts the report and reruns flakes |
| `recipe care-comments` | Answer the review feedback on a PR of yours (trusted authors' only); `--apply --action post-replies` posts the replies |
| `recipe care-rebase` | Rebase a PR of yours onto the default branch, resolving conflicts; `--apply --action post-replies` posts the report |
| `pr watch` | Follow up one PR with care until it merges or closes |
| `pr adopt open\|close` | Re-home a third-party PR under the bot (`--strategy reuse\|reimplement`) |
| `agent create` | Run an `.agents/` (or `--local`) agent definition |
| `watch` | The autonomous scan / dispatch / GC loop |
| `sandbox list\|inspect\|logs\|exec\|cp\|connect\|chat\|suspend\|resume\|delete` | Manage individual sandboxes |
| `cleanup` | Delete sandboxes older than `--older-than` (default 24h) |
| `sshd` | Embedded SSH server over stdin/stdout for terminal forwarding |
| `daemon`, `token-daemon` (hidden) | Sandbox entrypoint (spool, task server, agent sessions); token-usage collector |

Global flags (`--engine`, `--user`, `--namespace`, `--timeout`, `--disclose`, resource and injection flags) apply to every command; see `factory --help`.

---

## Further reading

- [Overseer and factory architecture](../overseer/docs/architecture-overseer-factory.md)
- [Configuration guide (`.factory.cfg`)](docs/configuration.md)
- [Watch rules guide](docs/watch-rules.md): the rule files' fields, and how the watch's rules decide
- [Plans and issue graphs](docs/plan-and-graph.md): from an issue to a reviewed plan, then one PR or a graph of sub-issues, steered with labels
- Watch internals: [watch rules design](design/watch-rules.md), [design note](design/watch-design-note.md), [subcontroller architecture](design/watch-subcontrollers-architecture.md)
- [Resilient and reconnectable task execution](design/resilient-task-execution.md)
- [Multi-engine sandboxes](design/multi-engine.md)
- [Automated PR review](design/pr-automated-review.md) · [PR adoption](design/pr-adopt.md)
- [Workflow orchestration and session reconciliation](design/workflow-orchestration-and-session-reconciliation.md)
- [Token usage collection](design/token-usage-collection.md)
- [GitHub App identity](design/github-app-identity.md)
- [Research warm pool](design/research-warm-pool.md) (design, not built)
