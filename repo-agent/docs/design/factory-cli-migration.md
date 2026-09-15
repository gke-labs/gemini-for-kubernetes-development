# Migrating repo-agent onto the factory CLI

**Status:** proposal
**Date:** 2026-09-14
**Branch:** `refactor-repo-agent`

## 1. Goal

repo-agent and factory each implement the same core loop — watch GitHub, spin up an
agent-sandbox, run `gemini` against an issue/PR, produce a PR/review — with two
independent codebases. Overseer already consumes factory as its engine; this doc plans
the same move for repo-agent: **keep repo-agent's hosted, multi-tenant, user-identity
shell; delete its homegrown agent engine; drive all agent execution through the factory
CLI.**

**Constraints:**

- **factory/ and overseer/ remain unaffected** — no code changes, no new flags or
  subcommands, no contract changes. repo-agent consumes factory exactly as shipped
  (building the binary from monorepo source, as overseer does, is consumption, not
  change). Every gap is closed on the repo-agent side or deferred.
- **Breaking changes inside repo-agent are allowed at every phase** — its API routes,
  CRDs, UI, images, and manifests can change or disappear without compatibility
  shims. The only exception: repo-agent's API/UI also hosts the *Overseer admin
  dashboard* and token-usage views (`pkg/api/handlers_overseer.go`,
  `handlers_usage.go`, the Overseer/TokenUsage UI views) — those serve the overseer
  stack and must keep working throughout.

What differentiates the three after the migration:

| | Identity | Hosting | Engine |
|---|---|---|---|
| factory | bot or user PAT | local CLI | itself |
| overseer | bot pool | hosted, CR-per-repo | factory CLI |
| repo-agent | **per-user OAuth** | hosted, namespace-per-user | **factory CLI (this doc)** |

## 2. The integration contract (proven by overseer)

Overseer never imports factory as a Go library; `overseer/go.mod` has no factory
dependency, and factory's own `design/token-usage-collection.md` codifies "no
cross-module coupling — the wire format is the only contract". The proven pattern is:

1. **Binary bundling** — build factory from monorepo source into the service image
   (`overseer/images/overseer/Dockerfile:19-30,59`) and exec `factory <subcommand>`.
2. **Config** — generate a `.factory.cfg` YAML, export `FACTORY_CONFIG`, pass the rest
   as flags (`overseer/images/overseer/run.sh:4-88,276-282`).
3. **Identity** — k8s Secret `factory-user` (or `user-<name>`) in the execution
   namespace with keys `GITHUB_TOKEN` / `GITHUB_LOGIN` / `GITHUB_EMAIL` /
   `GEMINI_API_KEY` (`factory/pkg/constants`).
4. **Tasks** — either invoke per-task subcommands directly (`fix`, `pr review`,
   `pr investigate`, `pr address-comments`, `pr iterate`, `agent create`) or write
   `QueueTask` YAMLs into `--queue-dir` and let `factory watch` dispatch
   (`factory/pkg/commands/watch/dispatcher/cli_runner.go:57` documents the exact
   task-type→argv mapping).
5. **Everything downstream is factory's** — worker Sandbox CR lifecycle
   (create/reuse/suspend/evict), envd task execution with `--detached` resilience,
   model fallback, quota handling, and token telemetry (`factory token-daemon`, which
   repo-agent's API already proxies via `COLLECTOR_URL`).

In-cluster operation is supported today: when `KUBERNETES_SERVICE_HOST` is set, factory
dials the sandbox's `<name>-lb` Service directly instead of spawning
`kubectl port-forward` (`factory/pkg/envd/client.go:146-233`).

## 3. What repo-agent keeps vs. deletes

### Keep — the hosted/identity shell (repo-agent's actual value)

- `cmd/review-api`, `pkg/api`, `pkg/auth` — GitHub OAuth, sessions, allowlist/admin,
  the HTTP API surface.
- `pkg/k8s/bootstrap.go` — namespace-per-tenant bootstrap (extended, see §4).
- `api/repowatch/v1alpha1` `RepoWatch` CRD — the per-repo config surface, retained but
  re-interpreted as "input to factory config" the way `Overseer` spec fields map to
  `.factory.cfg`.
- `review-ui/` — with handler-level adaptations (§6).
- Dev-sandbox feature (`reconcileDevSandboxes`, `dev-daemon`'s code-server/dockerd) —
  it is an IDE-hosting feature, not the agent loop; out of scope here (§8, D5).
- `cmd/configdir-*`, `cmd/syncer-controller` — orthogonal controllers.

### Delete — the parallel engine (replaced by factory)

- `pkg/taskrunner` (in-sandbox 5s SandboxTask poll loop) and the `SandboxTask` CRD
  outright — the UI is rewired to factory state directly (§6); no status mirror.
- `cmd/repo-sandbox` agent subcommands: `review`, `github-fix-issue`,
  `github-triage-issue`, `github-feedback`, `github-investigate`, `github-autopoll`,
  `iterate`, `chore`, `rollback` — plus their machinery: `pkg/tasks/*.sh|txt`,
  `pkg/llm` (gemini/claude providers), `pkg/prompts`, `pkg/models`,
  `cmd/gemini-stream-processor`.
- `pkg/sandbox`'s review/issue sandbox creation (factory owns worker sandboxes; the
  inject-agent/`/opt/repo-agent` convention goes with it).
- The dispatch half of `pkg/controllers/repowatch` is progressively hollowed out (§5):
  its GitHub-polling/matching logic is what `factory watch` scan mode does.

### Task-type mapping

| repo-agent task (`pkg/taskrunner`) | factory equivalent |
|---|---|
| `review` | `factory pr review --pr-url … --publish no\|draft\|yes --instruction …` |
| `fix-issue` | `factory fix --url <issue-url>` |
| feedback re-dispatch | `factory pr address-comments` (or `factory pr watch`) |
| CI-failure investigate | `factory pr investigate` (or `factory pr watch`) |
| `iterate` | `factory pr iterate` |
| `chore` | `factory watch` chores (`chores.mode` in `.factory.cfg`) |
| `triage-issue` | **gap** — closest is `factory agent create` with a triage agent definition in `.agents/`; see D4 |
| `rollback` | **gap** — low usage; keep out of v1 or add a factory task |

## 4. Identity: user OAuth → `factory-user` secret

This is the one genuinely new piece. Factory resolves identity from a k8s Secret at
task start; repo-agent holds a per-user OAuth token (secret `github-pat`, keys
`manual_pat` > `oauth_pat` > `pat`, refreshed by `PersistingTokenSource`).

**Plan:** extend tenant bootstrap + the repowatch reconcile to materialize and maintain
a normalized `factory-user` Secret in each tenant namespace:

- `GITHUB_TOKEN` ← the same precedence chain used today
  (`repowatch_controller.go:158-199`).
- `GITHUB_LOGIN` / `GITHUB_EMAIL` ← from the OAuth `read:user,user:email` profile
  (already fetched at login).
- `GEMINI_API_KEY` ← from the tenant's `gemini-vscode-tokens` /
  per-RepoWatch `apiKeySecretRef`.

This mirrors exactly what overseer's controller does in `reconcileFactorySecret`
(`overseer/pkg/controllers/overseer_controller.go:385-421`) — normalize heterogeneous
operator secrets into `factory-user` — just sourced from OAuth instead of PATs.

**Token freshness:** OAuth tokens can rotate; the reconcile loop must re-sync the
secret whenever `PersistingTokenSource` persists a refreshed token, and always sync
before dispatching a task. Factory reads the secret per task start, so a fresh secret
is sufficient; no factory change needed.

**Bot option preserved:** RepoWatch's `robotAccount` maps to `--user <bot>` /
`user-<bot>` secrets — the same mechanism overseer uses — so a tenant can opt a repo
into bot identity without any special casing.

**Future alignment:** `factory/design/github-app-identity.md` proposes a
`factory token-broker` minting installation tokens per role. When that lands, the
repo-agent's user-OAuth flow becomes one more identity source behind the same secret
(or broker) interface; nothing in this migration blocks it.

## 5. Phased migration

The invocation locus: the **repowatch-controller** execs the factory CLI (binary
bundled into `images/repowatch-controller/Dockerfile`), with `--namespace <tenant>`,
`--detached`, and `FACTORY_CONFIG` pointing at a per-RepoWatch generated config. The
controller's ServiceAccount already spans tenant namespaces; in-cluster envd dialing
means no `kubectl` needed. `--detached` + factory's marker-file reattach
(`design/resilient-task-execution.md`) means controller restarts don't orphan tasks —
strictly better than today's taskrunner.

### Phase 1 — issue-fix path (cleanest mapping, proves the plumbing)

- Bundle factory into the controller image; add `.factory.cfg` generation from
  RepoWatch spec (image, sandbox resources, disk size, trigger label, env/secrets —
  the RepoWatch sandbox fields map ~1:1 onto config keys, same as the Overseer CRD).
- Bootstrap writes/refreshes `factory-user` per tenant (§4).
- `ensureIssueTask` → `factory fix --url <issue> -n <tenant> --detached
  [--instruction <spec.issue.prompt>]` instead of creating a SandboxTask.
- Success signal: PR URL in the sandbox's `agent-output.txt` (factory's de-facto
  machine contract) or simply factory's exit code + the PR appearing on the issue —
  the controller already watches PRs.
- RBAC: verify tenant SA covers factory's needs (Sandbox CRs + `<name>-lb` Services +
  secrets read); reuse/extend `issue-sandbox-rbac.yaml`.

### Phase 2 — review path (the UI-coupled one)

- `createReviewSandbox…` → `factory pr review --pr-url … --publish no --detached`,
  with `spec.review.prompt` → `--instruction`.
- Rewire the draft-review endpoints to factory's outputs directly (§6): read
  `review-output.txt` and task-state annotations from the sandbox; the old
  `agentDraft`/`agentState` contract is dropped, and the UI updated in the same
  change (breaking repo-agent-internal changes are fine).
- Honor `maxReviewFiles` / `severityThreshold` via review instructions (or accept the
  loss for v1 — these are currently the only RepoWatch review knobs factory lacks
  natively).

### Phase 3 — feedback / investigate / lifecycle → factory watch semantics

- Replace `reconcileIssueFeedback` / `reconcilePRFailures` / `github-autopoll` with
  either per-PR `factory pr watch` (detached) or — preferred end-state — a per-tenant
  `factory watch --mode all --repo <owner/repo> --queue-dir …` runner, at which point
  the repowatch controller shrinks to what overseer's controller is: CR → namespace +
  secrets + config + one watch runner. Sandbox pause/evict logic
  (`manageSandboxLifecycle`) is dropped in favor of factory's
  `--sandbox-idle-timeout` / `--sandbox-eviction-age`.
- Decide watch-runner placement: a per-tenant daemon Sandbox (overseer pattern) vs. a
  controller-spawned `factory watch --once` per reconcile tick. Start with `--once`
  from the controller (no new pods, fits `pollIntervalSeconds`), move to daemon
  sandboxes if scale demands.

### Phase 4 — deletions & cleanup

Scope narrowed during implementation: the dev-sandbox feature (kept per D5)
turned out to share more of the machinery than assumed, so the cut is by
*task type*, not by package:

- Removed: agent subcommands of `cmd/repo-sandbox` (review, review-daemon,
  github-fix/triage/feedback/investigate/autopoll, iterate, chore, rollback),
  their task scripts/templates in `pkg/tasks`, the agent task types in
  `pkg/taskrunner`, the review sandbox builder, and the review-system /
  fix-pr-feedback prompt templates.
- Kept for dev sandboxes: `cmd/repo-sandbox` itself (dev-daemon, dev-init,
  create, threads, sshd, code-server, tmux), `pkg/taskrunner` + the
  SandboxTask CRD (`dev-setup`/`script` types), `pkg/tasks` core
  (`RunTask` + dev_setup), `pkg/llm` (dev workflow provider abstraction),
  `pkg/agentoutput`, `cmd/gemini-stream-processor` (used by dev_setup.sh),
  the agent sandbox builder (dev sandboxes are built on it), and the
  sandbox images. Retiring these is a dev-sandbox redesign (D5 follow-up),
  not part of this migration.
- `pkg/models` stays: the API's submitReview parses `ReviewAgentOutput`.

Each phase is independently shippable. Since breaking repo-agent changes are
allowed, a phase cuts its task type over completely — the old path is removed in the
same phase, not kept as a fallback. Gate by task type, not by tenant, to avoid
Sandbox-CR name collisions on the same issue/PR while phases are in flight.

## 6. UI/API contract bridges

The review UI reads through `pkg/api`, so all bridging is server-side:

- **Draft reviews:** today `pkg/agentoutput` writes `agentDraft`/`agentState`
  annotations that the UI polls. Factory writes `review-output.txt` +
  `Running/Failed/Completed` task annotations on the Sandbox CR instead. `pkg/api`
  reads factory's annotations and fetches the file via envd; the UI switches to the
  new shape in the same change. Extending factory to emit the old contract is off
  the table (factory stays untouched).
- **Logs/telemetry endpoints:** switch paths from `/workspaces/.agent/tasks/<task>/…`
  to factory's `/workspaces/tasks/<type>-<ts>/` (`execution.log`,
  `llm-usage.json`, `gemini-output.json`).
- **Task status lists:** source from factory Sandbox labels/annotations
  (`factory.gemini.google.com/pr|managed|user`, task-state annotations) and, once
  watch mode lands, the watch HTTP API (`:13338`, `GET /api/v1/queue|status`) instead
  of SandboxTask CRs.
- **Token usage:** no change — already proxied to `factory token-daemon`.
- **Overseer admin surfaces:** `handlers_overseer.go`, `handlers_usage.go`, and the
  Overseer/TokenUsage UI views serve the overseer stack — keep them working
  unchanged through every phase (see Constraints, §1).

## 7. Gaps & risks

- **Claude/multi-provider:** repo-agent supports `claude`/`claude-cli` providers;
  factory is gemini-only. Since factory cannot be changed, repo-agent becomes
  gemini-only with the migration (a breaking repo-agent change we accept); revisit
  only if factory grows provider support independently.
- **ConfigDir injection:** repo-agent injects ConfigDir CRD content into sandboxes;
  factory only knows `secrets:`/`env:` in `.factory.cfg`. v1: project ConfigDir
  content into a Secret and pass through `secrets:`. Longer term: factory could grow a
  generic mount hook.
- **Per-user API quotas:** user OAuth tokens get 5k req/h; factory has no rate-limit
  handling (known, tracked as WS2.4 in the KCC scaling plan). Per-tenant polling
  volume is far below overseer's, so acceptable — but set conservative
  `pollIntervalSeconds` defaults.
- **Fork flow with user identity:** factory's scripts `gh repo fork` into the identity
  owner's account — for OAuth users that's the user's own fork, matching repo-agent's
  current behavior. Requires `repo` scope (the existing `?scope=readwrite` OAuth
  path); read-only-scoped users can't run fix tasks — surface that in the UI.
- **Token-on-disk in sandboxes:** factory writes the GitHub token into the sandbox
  (`gh hosts.yml`, git `insteadOf`) — with *user* OAuth tokens this is the user's
  token inside a `--yolo` sandbox. Same exposure as repo-agent today
  (`pkg/tasks/fix_issue.sh` does the identical thing), but worth stating; the
  github-app-identity credential-helper work is the eventual fix.
- **`triggerLabel` semantics:** factory watch is trigger-label/assignee driven;
  RepoWatch has richer label/assignee filters. Phases 1–2 (direct subcommand exec)
  keep repo-agent's matching; only Phase 3's watch adoption needs mapping — most
  filters translate to `--labels`/`--assignee`/`.factory.cfg triggerLabel`.

## 8. Decision points

- **D1 — Invocation locus (recommended: controller-exec, evolve to watch):** start
  with controller-side `factory <task> --detached` per task; adopt per-tenant
  `factory watch` in Phase 3 once trust is built.
- **D2 — Claude support:** *resolved* — dropped; factory is gemini-only and stays
  untouched (repo-agent's own proposal already went CLI-first/gemini-first).
- **D3 — SandboxTask CRD fate:** *resolved (amended in Phase 4)* — the UI was
  rewired directly to factory state for issues and reviews (no status mirror),
  but the CRD itself survives because dev sandboxes still queue `dev-setup`
  tasks through it. It goes away with the dev-sandbox redesign (D5), not this
  migration.
- **D4 — Triage/rollback tasks:** these two task types have no factory subcommand,
  and adding one is ruled out (factory stays untouched). Option A: port their
  prompts (`pkg/tasks/github-triage-issue.*`, `rollback.*`) into agent-definition
  files shipped in the repo-agent controller image and run them via
  `factory agent create --url <issue> --agent <file> --local` — factory's generic
  "run a custom agent task" mechanism, no per-tenant repo changes needed.
  Option B: drop the feature (plausible for `rollback` if rarely used).
  Recommended: A for triage; decide A-vs-B for rollback based on usage.
- **D5 — Dev sandboxes:** keep as-is in repo-agent (recommended — unrelated to the
  agent loop), or converge on `factory sandbox` + `sshd`/`connect` later.

## 9. Non-goals

- Any change to factory/ or overseer/ — code, flags, wire formats, images, or
  manifests (hard constraint, see §1). Where factory lacks a feature repo-agent
  needs, the answer is a repo-agent-side adapter, an `.agents/` definition, or
  dropping the feature — never a factory patch inside this migration.
- GitHub App / token-broker identity (separate design; this migration is compatible).
- Fixing factory-level scaling items (rate limiting, webhooks) — tracked in
  `docs/kcc-code-emergency-scaling-plan.md`.
