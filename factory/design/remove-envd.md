# Removing envd from the sandbox

Status: proposed.

A sandbox runs two servers today. The **task server** (`factory daemon`,
[recipe-task-server.md](recipe-task-server.md)) claims recipe tasks from
the spool, runs them, hosts their acpd sessions and serves them on
127.0.0.1:49990, reached over a port-forward. **envd** (e2b's, started by
the daemon as its last act: `exec envd --isnotfc`) serves a Connect-RPC
process service and `/files` on 49983, through the sandbox's `-lb`
Service, with no authentication. As recipe-task-server.md says, anyone
who can reach that Service can run commands in the sandbox, and the
cluster has no NetworkPolicies.

Every recipe (fix, care, review, triage, plan, research, fan-out, warm)
runs on the task server; envd is only their fallback for images without
one. What still needs envd is the classic commands and a few readers.
This note moves each onto the task server, as a recipe or a read, and
then removes envd from the image, the pod and the Service.

## What uses envd

Every `envd.Connect` on main:

| User | What it does with envd | Callers |
|---|---|---|
| `agent create` (agent.go, run_agent.sh) | writes the prompt, script and precondition; runs the script (`RunTaskResilient`); cats `agent-output.txt`; harvests usage | the watch: overseer chores and workflows (`TypeAgentChore`) |
| `run plan\|deploy\|teardown` (run.go, run.sh) | writes, runs, harvests | repo-agent's board (Deploy, runbook instances), the CLI |
| `pr adopt` (pr.go, adopt.sh) | writes, runs, cats `agent-output.txt`, harvests | the CLI |
| `sandbox exec`, `sandbox logs` | exec with stdin; `tail -f` the newest task's log | the CLI |
| the watch's `probeTaskViaEnvd`, `AliasToTaskPR` (watch/sandbox/service.go) | the newest task's status (pid, exit code, 137 for a dead process); cats its `agent-output.txt` | every dispatch, the scanners, the reconciler, recovery: recipe sandboxes included |
| `usagereport.HarvestSandbox`, `HarvestTask` | `ls` the task dirs; cat their usage files | the reconciler's sweep before a delete; agent, run and adopt |
| the old-image fallbacks: `taskapi.EnvdSandbox`, recipe.go's `RunTaskResilient` path, the spool client | everything | any recipe on an image without a task server |

None of the classic commands names its task: run again, it starts another
instead of finding the one before, so a restarted caller cannot pick a
task up. Recipes can (run names).

## The plan

One PR per step, each leaving everything working.

### 0. acpd records token usage

Recipe `ask:` steps record no usage. The recipe runner harvests
`token-usage.json` from the task directory (`HarvestTaskFiles`), but
nothing writes it for an ask: acpd, the ACP client and the runner have no
usage code. So fix, care, review and the rest report no tokens today, and
turning agent and runbooks into recipes would lose the usage they report
now (`runEngine`'s `record_*_usage`).

acpd writes the session's usage to the task directory's
`token-usage.json`, in the shape `record_*_usage` writes, from what the
engine reports: the prompt reply's usage where the engine's ACP server
sends it, else the engine's own session record. Which engine reports what
over ACP is measured first (gemini, claude, antigravity), as the
multi-engine work did.

### 1. Reads through the task server

The task server already serves what these read: `GET /v1/tasks` lists a
sandbox's task directories newest first, marking a dead process's 137,
and `GET /v1/tasks/{id}/files/{name}` serves a file of one. It serves
every task directory, not just the spool's.

- **Usage:** `HarvestSandbox` lists and reads through `taskapi`;
  `HarvestTask` goes with its last callers (steps 2-4).
- **The watch:** `IsTaskRunning` and `RefreshTaskState` ask
  `GET /v1/tasks`; `AliasToTaskPR` reads `agent-output.txt` through the
  files endpoint. This is the hot path (every dispatch), so the client
  keeps its forward per sandbox. Also: `ResolveName(TypeAgentChore, 0)`
  resolves to `fix-<repo>-0`, not the chore's `agent-…` sandbox.
- **The CLI:** `sandbox exec` uses kube exec, as `sandbox connect`
  does; `sandbox logs` follows the newest task's log through the task
  server. `GetSandboxPodName`, which wakes a suspended sandbox, moves
  from shelling out to kubectl to client-go, keeping the wake.

### 2. `pr adopt` as a recipe

The pilot for turning a script into a recipe: small, and the CLI is its
only caller. Recipe `adopt`, `on: [pr]`, input `strategy`
(`reuse` | `reimplement`): setup-git and setup-fork; `reuse` fetches the
PR's head and the push step pushes it to the member's fork;
`reimplement` asks the agent to redo the change from the PR's diff,
committing. Its result is a Change whose `open-pr` opens the adopted PR,
with an action that comments on the original (and closes it, for
`close`).

### 3. Agent chores and workflows as a recipe

The overseer's last classic path. Recipe `agent`, `on: [repo, issue,
pr]`. factory fetches the `.agents/` definition as today and hands the
recipe its parts as inputs: the prompt, the precondition script, the mode
(`workflow` or not) and `skipPR`.

- **Sandbox:** `agent-<repo>-<id>`, and `wf-issue-<n>` for a workflow,
  as `EnsureAgentSandbox` names them.
- **Steps:** setup; the precondition as a `run:` step whose failure
  defers the run ("Deferring", exit 0); the branch: a workflow's
  `factory-<n>` rebased on upstream, else the open `chore: <name>` PR's
  branch, else `agent/<slug>`; one ask.
- **Workflow mode:** the push step pushes the workflow's branch (the
  journal the agent keeps in the repo is its state); with its issue
  closed, the branch is deleted instead. No PR.
- **Other modes:** a Change whose `open-pr` reuses the chore's open PR.
- **The watch:** `TypeAgentChore` joins the recipe table; recovery
  requeues by run name; the classic adoption path, `opensPR` and
  `AliasToTaskPR` go.
- Chores get every engine; today they are gemini only (run_agent.sh has
  its own loop and agent.go never applies the engine).

A workflow's state is in git, its branch's journal, so recreating its
sandbox loses nothing but the clone.

### 4. Runbooks as a recipe

Recipe `runbook`, `task-type: run`, input `phase` (`plan` | `deploy` | `teardown`). `factory run plan|deploy|teardown` stays the entry point (repo-agent calls it unchanged). It renders the phase's prompt (`run_*.txt`) as the `prompt` input and runs the recipe in the run's sandbox, whose naming, labels, disk, ephemeral storage and GCP environment are unchanged.

`run.sh` keeps the deterministic parts as functions, appended to the script that runs a recipe's `uses:` steps. Two named steps take the place of its main section:
- `runbook-prepare` sets up the fork (or local-only), the runs branch, the pinned target and the runbook copy;
- `runbook-finish` clears the target, makes one commit, pushes it, and writes `result.json`.

Each step is a process of its own, so what the first finds reaches the second through `$TASK_DIR/runbook-state.sh`. A failed ask ends the recipe before `runbook-finish`. `result.json` then says `exitCode: -1`, and nothing is committed (before, it gave the exit code). The agent's shell gets `RUN_NAME`, `RUN_RESOURCE_PREFIX`, `GOOGLE_CLOUD_PROJECT`, `CLOUDSDK_CORE_PROJECT` and `CLOUDSDK_COMPUTE_REGION` through `agent-env`; an empty value is left unset.

**repo-agent:** liveness (`runLivenessScript`) and the launch preflight keep reading task directories and their `pid` / `start_time` / `exit_code` files, which the spool writes as envd did. They now look under the recipe's prefix, `recipe-runbook-*` (`factorycli.RunTaskDir`), while the recorded task type stays `run`. Reading a local-only run's files (`git show research/runs:…` over kube exec) stays.

### 5. Removing envd

- Delete `taskapi.EnvdSandbox`, the spool client's envd functions,
  recipe.go's `RunTaskResilient` fallback and `FACTORY_TASK_TRANSPORT`;
  `pkg/envd`'s client and its RPC spec; `run_agent.sh`, `run.sh`,
  `adopt.sh` and the agent, run and adopt commands' envd code.
- What the task server and the spool share moves to `pkg/taskfiles`:
  `TaskFiles`, `DefaultTasksDir`, `TaskScript`, the pid check,
  `QuotaWatch`.
- `factory daemon` blocks on its context instead of exec'ing envd.
- The image stops building e2b's envd; the pod stops exposing 49983, and
  the `-lb` Service its `envd` port.
- A sandbox made before then has no task server as recent as this
  factory needs: it is recreated (no compatibility shims).

## Order

0 → 1 → 2 → 3 → 4 → 5. Step 1's three parts are independent of each
other and of 0. Step 0 comes before 3 and 4, which would otherwise lose
usage reporting. Steps 3 and 4 can swap: 3 finishes the overseer's
classic paths, 4 is mostly repo-agent's.

## Open questions

- Runbooks: one recipe with a `phase` input (proposed), or one per phase.
- Usage over ACP: what each engine reports, measured in step 0.
- Workflow sandboxes made before step 3 are recreated with it: their
  state is in git (proposed: yes).
