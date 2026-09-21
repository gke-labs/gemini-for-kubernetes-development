# Design Note: GitHub App Identity for Overseer/Factory

**Status:** Proposed
**Date:** 2026-07-23
**Related:** `docs/kcc-code-emergency-scaling-plan.md` items 2.3 (webhooks), 2.4 (rate-limit client), 2.6 (GitHub App)

## 1. Problem

The bot fleet currently runs as ~8 free GitHub **user accounts** authenticated with static PATs. Two of them have been suspended for TOS violations (first: issue-creation volume; second: unknown). This is structural, not bad luck:

- GitHub's TOS restricts automated user accounts ("machine accounts"): one per human/organization is tolerated, a pool of eight free automated accounts is squarely in spam-detection territory.
- Our workflows are *designed* to create issues in bursts (a checklist workflow opens an issue per step; the migration will generate thousands), which is exactly the signal GitHub's abuse detection keys on for user accounts.
- Every suspension halts a role's throughput, orphans its open PRs/sandbox aliases, and costs maintainer time on appeals.

**GitHub Apps** are the sanctioned identity for automation: they act as `<app-slug>[bot]`, are exempt from user-account TOS, get their own (larger, per-installation) rate limits, mint short-lived tokens (fixes our cleartext-token-on-PVC issue), and come with webhooks (roadmap item 2.3) for free.

## 2. Current state — where bot identity flows (verified)

| Surface | Location | Mechanism |
|---|---|---|
| Go API client | `factory/pkg/github/github.go:21-63` | go-github v39, static PAT from `MANUAL_PAT`/`GITHUB_TOKEN`/`OAUTH_PAT`/`gh auth token` |
| Bot pool / role selection | `factory/pkg/commands/watch.go:2791-2958` | roles watcher/coder/reviewer/agent from `.factory.cfg`; per-bot k8s Secret `user-<bot>`; random pick (`:2920`) |
| Secrets schema | `factory/pkg/commands/root.go:19-25` | `GITHUB_TOKEN`, `GITHUB_LOGIN`, `GITHUB_EMAIL` (+ Gemini key) in `factory-user` / `user-<bot>` |
| Sandbox `gh` auth | all 7 task scripts (e.g. `fix_issue.sh:47-74`) | write static token into `~/.config/gh/hosts.yml`, `gh auth setup-git` |
| Sandbox git auth | same blocks | `git config url."https://${GH_USER}:${GITHUB_USER_TOKEN}@github.com/".insteadOf` — **static token embedded in git config for the task's lifetime (up to 3h)** |
| Fork/push model | `fix_issue.sh:105-106`, `address_feedback.sh:102-103`, `iterate.sh:109`, `review.sh:98-99`, `investigate_failures.sh:103-104`, `adopt.sh:177` | `gh repo fork --remote` into the **bot user's namespace**, branch pushed to user fork, PR opened cross-fork |
| Commit author | task scripts (`git config user.name/email` from `GITHUB_BOT_NAME/EMAIL`) | bot user identity on commits |
| Onboarding | `factory user onboard`, `overseer/examples/install-bot-users.sh` | one PAT secret per bot, by hand |
| Token refresh precedent | `factory/pkg/geminitokens/geminitokens.go:205,258` (`TOKENSCRIPT_DIR`) | scripts that mint fresh Gemini tokens on demand — the pattern we'll reuse for GitHub |
| API proxy | `github-portal` (`overseer/deps/github-portal.yaml`), `run.sh` HTTPS_PROXY wrapper | caching proxy for sandbox `gh` traffic only |

Constraints that shape the design:

1. **Installation tokens live 1 hour.** Tasks run up to 3h, workflow sandboxes for days. Static-token-at-task-start is dead; auth must become *fetch-on-use*.
2. **Apps cannot `gh repo fork` into their own namespace** (apps have no user namespace). The fork-based push model must move to a pre-created fork in a **bot org** where the app is installed.
3. **Writing to the upstream repo requires the app to be installed there.** Creating issues/PRs/comments/labels on `GoogleCloudPlatform/k8s-config-connector` requires an org owner to install the app with `issues:write`, `pull_requests:write` (metadata read implied). This is the long-pole approval — start it first.
4. Apps are still subject to secondary/abuse rate limits on *content creation* — an app that opens 500 issues in a minute can also get flagged. Self-throttling is required regardless of identity type.

## 3. Design

### 3.1 App topology: one app per role (recommended)

Register **three GitHub Apps** under a dedicated bot org (e.g. `kcc-factory-bots`):

| App | Replaces | Permissions (upstream) | Permissions (bot org) |
|---|---|---|---|
| `kcc-overseer-watcher` | watcher bot | issues:write, pull_requests:write (labels/assign/comments), checks:read, contents:read | — |
| `kcc-overseer-coder` (×N installations or ×N apps if author diversity needed) | coder + agent bots | issues:write, pull_requests:write, contents:read | contents:write (org fork), workflows:write |
| `kcc-overseer-reviewer` | reviewer bot | pull_requests:write (reviews/comments), contents:read | — |

Why per-role apps instead of one app:
- **Quota multiplication**: each installation gets its own rate-limit bucket (5,000 req/h baseline, scaling with repo/org size; 15,000/h on GHEC orgs). Three apps ≈ three buckets, same shape as today's PAT pooling but sanctioned.
- **Identity separation preserved**: PR author (`kcc-overseer-coder[bot]`) ≠ review commenter (`kcc-overseer-reviewer[bot]`). GitHub forbids a PR author approving their own PR — irrelevant today (humans approve, per workflow guardrails) but keeps the door open for tiered auto-review later.
- **Blast radius**: a spam-flag or key compromise on one role doesn't stop the others.

A single-app design remains a valid fallback if registering three apps on the upstream org is a harder sell; everything below works identically with one app and role becomes purely cosmetic.

### 3.2 New component: GitHub token broker

A small cluster-local service (new `factory token-broker` subcommand, deployed like the existing `token-usage` StatefulSet in `overseer-system`):

- Holds app private keys (one k8s Secret per app: `app-<slug>` with `APP_ID`, `INSTALLATION_ID`(s), `PRIVATE_KEY`).
- Mints installation tokens via the Apps API, caches until ~5 min before expiry, refreshes on demand.
- Serves `GET /token?role=coder&repo=owner/name` over ClusterIP with NetworkPolicy limiting callers to overseer/sandbox namespaces (plus a shared bearer secret — sandboxes are LLM-controlled, so scope what the broker will hand out: only tokens for the configured repos/roles).
- Exposes `GET /ratelimit?role=` (from the last API response headers) so the orchestrator can pick the identity with the most headroom — replaces the random pick at `watch.go:2920` and closes roadmap item 2.4's "quota-aware bot selection".
- Metrics: tokens minted, remaining quota per installation, 403/secondary-limit sightings.

Why a broker instead of minting in each consumer: the private key stays in one place (never on sandbox PVCs — today's cleartext-PAT problem disappears), caching avoids the 5,000/h Apps-API budget being spent on token minting, and both Go code and bash scripts can consume plain HTTP.

### 3.3 Consumer changes

**Go client** (`pkg/github`): swap static-PAT transport for `ghinstallation` (or an equivalent round-tripper that calls the broker and injects/refreshes the token per request). Add the standard rate-limit handling here at the same time (throttle on `X-RateLimit-Remaining`, backoff+retry on secondary-limit 403/429) — one shared client, used by watch loop and CLI.

**Sandbox `gh` auth**: replace the `hosts.yml` write with a `gh` shim on PATH (same technique as the existing github-portal `HTTPS_PROXY` wrapper in `run.sh:146-150`):
```sh
#!/bin/sh
GH_TOKEN=$(curl -sf -H "Authorization: Bearer $BROKER_KEY" "$BROKER_URL/token?role=$FACTORY_ROLE&repo=$REPO") exec /usr/bin/gh.real "$@"
```
`gh` reads `GH_TOKEN` fresh on every invocation, so hour-long expiry never bites.

**Sandbox git auth**: replace the `insteadOf`-with-embedded-token line with a **git credential helper** that calls the broker the same way. Tokens are then never written to disk in the sandbox; a leaked token is worth ≤ 1 hour and only the app's scoped permissions.

**Task scripts**: the auth preamble is copy-pasted across all 7 scripts (`fix_issue.sh`, `iterate.sh`, `review.sh`, `address_feedback.sh`, `investigate_failures.sh`, `run_agent.sh`, `adopt.sh`). Extract it into one sourced `auth.sh` fragment during this change (do it once, not seven times), branching on identity type:
- `pat` (legacy): current behavior, unchanged.
- `app`: install gh shim + credential helper, set commit author to `<app-slug>[bot] <APP_USER_ID+<app-slug>[bot]@users.noreply.github.com>`.

**Fork/push model**: pre-create one fork of kcc in the bot org (`kcc-factory-bots/k8s-config-connector`), app installed with `contents:write`. Scripts push branches `issue-<n>-<ts>` to that org fork instead of `gh repo fork --remote`; PRs open cross-fork exactly as today. Branch-name collisions across concurrent coders are already avoided by the timestamp suffix (`fix.go:210`). `adopt.sh` re-forks likewise into the bot org. (Note: this also composes cleanly with the planned workflow-per-branch change — one org fork, one branch per resource.)

**Config** (`.factory.cfg` / Overseer CRD `roles`): a role's user list gains an identity type:
```yaml
roles:
  watcher: [{app: kcc-overseer-watcher}]
  coder:   [{app: kcc-overseer-coder}, {user: gemini-coder-3}]   # mixed during migration
  reviewer:[{app: kcc-overseer-reviewer}]
```
`selectUserForTask` resolves an app entry to `{login: "<slug>[bot]", tokenSource: broker}` instead of a `user-<bot>` secret. Sandbox pinning by label keeps working (login string is stable).

**Watch-loop scanning**: issue/PR queries filter by assignee/author login — `<app-slug>[bot]` works in search and assignment (apps can be assigned issues) — audit the handful of `bot`-name string matches (e.g. the external-author filter in the tracker workflow greps for `*bot*`, which conveniently still matches).

### 3.4 Content-creation throttle (needed regardless of identity)

The first suspension was for issue-creation volume; apps get abuse-flagged for the same behavior. Add a small token-bucket in the watch loop and broker-side counters:

- Cap issue/comment/PR creation per identity (e.g. ≤ 20 issues/h, ≤ 1 create per 30 s sustained, configurable).
- The workflow prompt layer already batches (one issue per step, cooldown 2h) — the throttle is a backstop for bursts (e.g. 100 workflows all reaching step-boundary after a mass merge).
- Surface throttle events as metrics so bursts are visible instead of silently suspicious.

### 3.5 Webhooks (phase 3, optional here — roadmap 2.3)

Apps come with webhook delivery. A minimal receiver (new `factory webhook-listener`) validates HMAC, translates `issues`, `pull_request`, `check_suite`, `pull_request_review` events into queue files in `incoming/`, and lets the scan loops drop to a slow reconcile cadence (e.g. 30 min). This removes most of the polling quota burn and cuts reaction latency from minutes to seconds. Not required for the identity migration, but the app registration should enable the webhook with a placeholder URL so it's a config change later.

## 4. Migration plan

**Phase A — approvals & scaffolding (start immediately; the org ask is the long pole)**
1. Decide app ownership org; register the three apps (or one); request installation on `GoogleCloudPlatform/k8s-config-connector` from org owners with the minimal permission set in §3.1.
2. Create the bot org fork.
3. Meanwhile (no approvals needed): build the token broker + `ghinstallation` transport + rate-limit handling in `pkg/github`; deploy broker; test end-to-end against a repo in the bot org.

**Phase B — watcher first (highest API volume, lowest content risk)**
4. Config plumbing (`roles` identity type), point the watcher role at the app. Scanning/labeling/assigning now runs on app quota with rate-limit handling. PAT bots keep coding.
5. Add the content-creation throttle.

**Phase C — coder/agent/reviewer**
6. Extract the shared `auth.sh` preamble; add gh shim + credential helper; switch push target to the org fork; app commit author.
7. Migrate one coder to app identity on a small set of issues; verify PR flow, CI triggers (note: some CI systems skip runs for `[bot]` authors — verify kcc's Prow config treats app PRs like bot-user PRs), sandbox aliasing, reaction acks.
8. Migrate remaining roles; keep 1–2 PAT machine accounts dormant as emergency fallback; decommission the rest (and their secrets/onboarding scripts).

**Phase D — webhooks (optional, fold into roadmap Phase 2)**

**Rollback**: identity type is per-role config; any role can revert to PATs by editing `.factory.cfg`/CR — no code rollback needed once dual-mode ships.

## 5. Effort estimate

| Item | Est. hours |
|---|---|
| App registration, org-owner request, bot-org fork setup (mostly waiting/coordination) | 8–16 |
| Token broker service (mint/cache/serve, authz, metrics, deploy manifests) | 40–60 |
| `pkg/github` app transport + rate-limit/backoff handling (roadmap 2.4 folded in) | 24–40 |
| Config/roles plumbing + quota-aware identity selection | 16–24 |
| Sandbox auth rework: shared `auth.sh`, gh shim, git credential helper, commit identity, org-fork push (×7 scripts) | 60–90 |
| Content-creation throttle + metrics | 16–24 |
| Migration runs, CI/Prow verification on kcc, docs, decommission PATs | 16–24 |
| **Core total** | **180–280 h (~1.5–2 engineer-months; 3–4 wks for 2 engineers)** |
| Optional: webhook listener (roadmap 2.3) | +60–80 |

## 6. Risks & open questions

1. **Upstream installation approval** — will GoogleCloudPlatform org owners install a third-party-registered app with issues/PR write? If not: fallback is a hybrid (app for bot-org + API reads via broker; a *single* documented machine account, PAT, for upstream issue/PR creation with strict throttling — one sanctioned machine account is defensible where eight aren't).
2. **Who owns the app registration** (which org, who holds the private keys, key rotation policy — broker makes rotation a secret update).
3. **Prow/CI behavior for `[bot]` authors** on kcc (`ok-to-test` gating, `cla` checks — app commits use the noreply email; confirm CLA/DCO exemption for the app identity).
4. **Search/assignment semantics**: assigning issues to `<app-slug>[bot]` works, but any hardcoded assumptions about bot logins in workflow prompt text (`.agents/workflows/*` reference `factorybot-robot` explicitly) need a sweep.
5. **Number of coder identities**: today 3 coder bots exist partly for parallelism optics; with an app, one identity can hold many concurrent PRs. Decide whether N coder apps are actually needed (quota is the only technical reason).
6. **Broker as new SPOF**: mitigate with 2 replicas + client-side token caching (tokens are valid 1h; consumers can tolerate broker blips).
