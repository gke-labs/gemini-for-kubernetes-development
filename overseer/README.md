# Overseer

Overseer runs autonomous agents against a GitHub repository, one Overseer per repository. You declare an `Overseer` custom resource, and the controller starts a long-running pod for it in its own namespace. That pod runs [`factory watch`](../factory/) in a loop. It picks up issues, sends coding agents into isolated sandboxes to fix them, looks after the resulting pull requests until they are ready for a human, runs scheduled chores, and cleans up after itself.

## What it solves

Running one coding agent against one issue is easy. Running dozens against a busy repository, unattended, is not. Somebody has to:

- notice new work,
- give each task its own environment and credentials,
- answer CI failures and review comments,
- rebase when the branch conflicts,
- know when to stop,
- tell a human when a PR is ready,
- survive restarts without losing or duplicating work,
- and delete everything once the issue or PR closes.

Overseer is that somebody. It works from GitHub state (labels, assignees, comments, reactions, check results), so maintainers steer it from GitHub, not from the cluster.

## What it does

### Issue intake: from issue to pull request
- Picks up open issues that carry the trigger label (default `overseer`), are assigned to one of its bot accounts, or were filed by its own watcher account. Issues the watcher account files are labelled and assigned automatically. Assigned and bot-filed issues are checked on a fast cadence. A slower sweep covers every labelled issue.
- Queues an `issue-fix` task for each one. The task runs a coding agent in a dedicated sandbox, and the agent opens a pull request.
- Skips issues that already have an open PR referencing them, and issues numbered below `minNumber`.
- `priority/<level>` labels (`critical`, `urgent`, `important`, `high`, `medium`, `low`) set the task's place in the queue.

### Pull request care
For PRs written by its bot pool that carry the trigger label or are assigned to a bot, each sweep picks one action:
- **Address feedback** (`pr-comments`): new comments, reviews and inline comments from humans or allowlisted review bots are handed to the agent. Reactions record what happened to each comment: 👀 picked up, 👍 resolved, 😕 failed. A human adds 🚀 to ask for another pass. A line starting with `/overseer-ignore` (or `/<trigger>-ignore`) opts a comment out. Approvals and `/lgtm` are not treated as feedback.
- **Rebase on conflict** (`pr-iterate`): a PR that conflicts with its base branch gets a rebase task, at most once per head commit.
- **Investigate CI** (`pr-investigate`): failing checks get an investigation that starts from the earliest failure. Both the Checks API and commit statuses are read. After 3 attempts on the same revision, Overseer gives up and applies the stop label. A new commit or a human comment resets the count.
- **Automated review** (`pr-review`, opt-in): when a PR or an issue it closes carries `overseer/review`, the reviewer bot reviews each green, unreviewed head commit. Review instructions are collected from the PR and issue descriptions.
- **Ready for human**: when the PR is mergeable, has no conflicts, CI is green, no comments are outstanding, no task is queued or running, any required review is done, and the PR is not a draft or stopped, Overseer adds `overseer/ready-for-human`. It also assigns the human assignees of the parent issues, unassigns the bot, and removes the review label.
- PRs in the merge queue are left alone. With `prInactivityTimeout` set, a PR that has had no human activity for that long is paused with the stop label and a comment.
- Bot PRs that lost their labels, for example because a task outlived a watch restart, are adopted back when their parent issue carries the trigger label.

### Workflows and chores
- **Workflow issues**: an issue whose description points to a workflow definition (for example a file under `.agents/` or `.gemini/`) runs that workflow instead of a standard fix. When an issue or PR linked to a running workflow closes, Overseer queues the workflow's next run straight away, without waiting for its cooldown.
- **Chores** (`agent-chore`): agent definitions under `.agents/` in the watched repository are run on the cron schedule in their frontmatter. `never` or `paused` turns a chore off. `spec.chores` enables or disables chores, or runs them in dry-run mode, and can include or exclude chores by name. Last-run state survives restarts.

### Housekeeping and resilience
- **Garbage collection**: sandboxes are deleted once their issue or PR closes. Linked workflows are nudged first.
- **Idle suspension and eviction**: sandboxes are scaled to zero after `sandboxIdleTimeout` (default 1h) and deleted after `sandboxEvictionAge`.
- **Task timeout**: a task that runs longer than `taskTimeout` (default 24h) is marked failed and its sandbox is deleted.
- **Restart recovery**: tasks run detached inside their sandboxes. On startup, every task still in `processing` is checked. Tasks still running are adopted and supervised to completion. Finished tasks are recorded. Tasks whose sandbox is gone are requeued.
- **Stop label**: `overseer/stop` (or `<trigger>/stop`) on an issue or PR freezes all activity on it.
- **Drain mode**: a `.drain` or `.do_not_process` marker file, or the matching environment variable, stops new tasks from starting. Running tasks finish normally.

### Identities and credentials
- **Bot roles** (`spec.roles`):
  - `watcher`: the pod's own GitHub identity.
  - `coder`: issue fixes, and PR tasks on PRs written by someone outside the agent pool.
  - `agent`: chores, and PR tasks on PRs the agent pool wrote. Falls back to `coder` when the pool is empty.
  - `reviewer`: reviews.

  A task on an issue that already has a sandbox stays on that sandbox's account.
- **Gemini key pool**: if a `tokenscript` secret exists, its script is run to pick a Gemini API key, skipping keys marked over quota or suspended. Quota exhaustion is tracked per key and per model, so tasks fall back to models that still have quota. Without the script, the key comes from `geminiAPIKeySecretName`.
- GitHub traffic from the pod goes through the in-cluster `github-portal` proxy in `overseer-system`.

### Observability
- **Queue and status API** on port `13338` of the Overseer pod:
  - `GET /api/v1/queue` lists tasks.
  - `POST /api/v1/queue/<task>/priority` changes a task's priority.
  - `DELETE /api/v1/queue/<task>` removes a task.
  - `GET /api/v1/status` reports active, over-quota and suspended keys.
- **Token usage telemetry**: every task reports its Gemini token usage to the `token-usage` collector (`factory token-daemon`, a StatefulSet at `token-usage.overseer-system:8080`, defined in [`k8s/token-usage.yaml`](k8s/token-usage.yaml)). The collector stores the records on a PVC and serves totals over HTTP.
- **Logs**: every cycle is logged to `/workspaces/logs/run-*.log` on the Overseer's PVC. The pod writes Gemini-generated daily and weekly summaries next to them, and deletes run logs older than 15 days.
- **Optional dashboard**: the Review UI in [`repo-agent`](../repo-agent/) has an Overseer page built on these endpoints. It covers sandboxes, queue, chores, logs and "Factory / API Token Status". Overseer and factory do not depend on repo-agent and run fine without it.

## How it works

```
Overseer CR (cluster-scoped)
   │  overseer-controller (overseer-system)
   ▼
namespace overseer-<name>
   ├─ ServiceAccounts + RBAC, copied secrets (tokenscript, github-portal-ca, user-<bot> per role user)
   └─ Sandbox overseer-<name>  (PVC mounted at /workspaces)
        bootstrap.sh → run.sh loop:
          refresh Gemini key → sync default branch → factory watch --mode all
          --watch-timeout <pollInterval> → [optional Gemini orchestrator] → commit + push queue state
             │
             ├─ issue scanner, PR scanner, chore scheduler, sandbox reconciler (GC/idle/nudge)
             └─ dispatcher → one sandbox per issue/PR/chore, running the coding agent
```

- The controller turns the CR into environment variables, and `bootstrap.sh`/`run.sh` write them to `/workspaces/.factory.cfg`. The pod clones the repository, sets up a fork, and runs `factory watch` for `pollInterval` (default `30m`). When the timeout expires, the watch drains its running tasks and exits. `run.sh` then pushes the queue state and starts the next cycle 10 seconds later.
- The task queue (`incoming/`, `processing/`, `processed/`) lives in `overseer/queues` in the clone on the PVC. After each cycle it is committed to a state branch named after the trigger label and force-pushed to the fork.
- `enableGeminiOrchestrator: true` adds an optional, non-deterministic `gemini --yolo` pass after each watch cycle. It is off by default.

## Configuration

The full schema is in [`pkg/api/v1alpha1/overseer_types.go`](pkg/api/v1alpha1/overseer_types.go). Commonly used fields:

| Field | Purpose |
|---|---|
| `repoURL` | Repository to watch (required). |
| `roles` | Bot account pools: `watcher`, `coder`, `agent`, `reviewer`. |
| `robotAccount`, `geminiAPIKeySecretName` | Secrets (in `overseer-system`) that hold the default GitHub identity and the Gemini key. |
| `repo.issueMode` / `repo.prMode` | Turn issue intake or PR care on or off. |
| `chores.mode` / `include` / `exclude` | Chore scheduling. |
| `image` | Worker sandbox image, overriding the repository's devcontainer image. |
| `workspaceDiskSize`, `workspaceStorageClassName`, `ephemeralStorage` | Storage for the Overseer and its sandboxes. |
| `sandboxCPURequest/Limit`, `sandboxMemoryRequest/Limit` | Worker sandbox resources. |
| `secrets`, `env` | Extra secrets and variables for every sandbox, such as cloud credentials for tests. |
| `minNumber`, `pollInterval`, `taskTimeout`, `sandboxIdleTimeout`, `sandboxEvictionAge`, `prInactivityTimeout` | Scope and timing. |
| `enableGeminiOrchestrator` | Optional LLM orchestration pass (default `false`). |

The trigger label is `overseer` in the Overseer image. The CR does not set it.

## Deploying

The [User Guide](docs/user-guide.md) covers prerequisites, robot accounts, the GitHub labels to create, a local `kind` setup, and a walk-through of the KCC configuration. In short:

1. Install the controller and its dependencies. `make install-latest` installs from published images (agent-sandbox, secrets, controller, token-usage collector). `make all` builds and deploys to a local `kind` cluster.
2. Put the bot and Gemini secrets in `overseer-system`, and optionally a `tokenscript` secret for a key pool.
3. Create the `overseer`, `overseer/review`, `overseer/ready-for-human` and `overseer/stop` labels in the repository.
4. Apply an `Overseer` resource:

   ```bash
   kubectl apply -f examples/kcc.yaml
   kubectl get pods -n overseer-kcc
   ```

Example resources: [KCC](examples/kcc.yaml), [Gateway API reference](examples/gwapi-ref.yaml), [AI Factory](examples/ai-factory.yaml), [this repository](examples/repo-agent.yaml), [agent-sandbox](examples/agent-sandbox.yaml).

## Components

- `cmd/overseer-controller`, `pkg/controllers`, `pkg/overseer`: the controller. It reconciles `Overseer` resources into a tenant namespace, RBAC, copied secrets and the Overseer `Sandbox`.
- `pkg/api/v1alpha1`, `k8s/crds`: the `Overseer` API and its CRD.
- `images/overseer`: the Overseer pod image (bundles the `factory` binary): `bootstrap.sh`, `run.sh` (the watch loop), `summarize.sh` (log summaries), and `prompt/` (orchestrator prompt templates).
- `images/overseer-controller`: the controller image.
- `k8s/`: the controller deployment, RBAC for the Overseer and sandboxes, and the [token-usage collector](k8s/token-usage.yaml).
- `deps/github-portal.yaml`: the GitHub API proxy used by Overseer pods.
- `scripts/`: helpers for installing bot users, rotating bot secrets and upgrading.
- `examples/`: ready-to-apply `Overseer` resources.

The logic itself lives in [`factory`](../factory/), mostly under `factory/pkg/commands/watch/`.

## Documentation

- [User Guide & Installation Manual](docs/user-guide.md): prerequisites, robot accounts, labels, local `kind` development, the KCC walk-through, day-to-day operations.
- [System Architecture & Interaction Diagrams](docs/architecture-overseer-factory.md): the supervisor loop, sandbox lifecycle, token usage telemetry, the queue shared by Overseer and factory.
- [Core Design Principles](docs/design-overseer.md): the ideas behind autonomous agent loops and multi-agent orchestration.
