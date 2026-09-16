# RepoBoard: a work-queue UI and a shared, discovery-driven CRD

**Status:** proposal
**Date:** 2026-09-16
**Supersedes (incrementally):** the RepoWatch CRD and the Review/Issues tab UI.
**Builds on:** the factory-CLI migration (`factory-cli-migration.md`) — factory
is the only execution engine. `overseer/` is not touched. `factory/` is not
touched by default; strictly additive changes may be proposed with explicit
approval (see §10).

## 1. Motivation

Two problems, one root cause:

- **The UI answers the wrong question.** Today's pages are split by engine
  internals (Review / Issues / Dev), each with its own sidebar, drafts, and
  state. The user's actual question is *"what do I do next?"* — a single,
  high-density queue of issues and PRs with an action per row.
- **The RepoWatch CRD encodes a retired engine.** ~40 spec fields exist to
  pre-configure selection (labels, assignees, handlers) and execution (LLM
  provider, images, per-section limits ×3) for an in-cluster agent engine
  that the factory migration deleted. Most fields are dead; the rest describe
  intent better expressed as a click or a live GitHub query.

This design replaces both with one primitive: a **board** — a repo-scoped
work queue where work is *discovered* from GitHub and sandboxes (the overseer
model), actions are human clicks, and every GitHub-visible artifact is
authored by a human identity.

## 2. The identity thesis (why repo-agent exists next to overseer)

Some repos do not accept bots as code authors. repo-agent's differentiator —
and the invariant of this design — is:

> **Every GitHub write (PR, review, comment, label, assignment, merge) is
> performed under the identity of the human who initiated it, behind that
> human's explicit or standing consent.**

That yields a crisp product split on one shared engine:

| | overseer | repo-agent (this design) |
|---|---|---|
| Identity | bot pool | the acting human |
| Autonomy | full pipeline | draft-gated: agents prepare, humans publish |
| Scope | repo fleet, throughput | a team's repo(s), accountability |
| Engine | factory CLI | factory CLI |
| Discovery | trigger label + bot assignee | trigger label + human assignee (same loop) |

Corollaries used throughout: agents may do *invisible* work under any
identity (drafts write nothing to GitHub); rate limits scale horizontally
(every member brings their own GitHub quota); and long-term the two products
can converge on one CRD differing only in `writePolicy: bot | human-gated`.

## 3. Personas and lenses

One page, one table, two lenses:

- **Contributor lens** (`@me` scope): my assigned issues, my PRs, PRs
  awaiting my review. Verbs: Fix, Review.
- **Maintainer lens** (repo scope; offered when the viewer's own token shows
  `push`/`maintain`): everything inbound, filterable by label/author/state.
  Verbs: Triage, Review, Fix (assign-to-agent), Merge. Extra columns:
  author, claimed-by, CI/approvals chips.

The unit is a **work item**, not an issue-or-PR: an issue that gets a fix PR
is one row that moves through stages
(`triage → fixing → PR open → iterating → needs-you → merged`), linked by
factory's existing sandbox↔PR alias. The primary sort is the attention
state: **Needs you** > *Waiting on others* > *Agent working*.

## 4. A new CRD, not RepoWatch v2

RepoBoard is a new kind rather than a RepoWatch revision because the object
changes identity: user-scoped watch → repo-scoped shared board, with
different placement, access semantics, and lifecycle. A new kind means no
conversion webhooks, clean coexistence during rollout, and RepoWatch remains
untouched as the (temporary) home of the dev-sandbox feature until the
workspace redesign retires it.

```yaml
apiVersion: review.gemini.google.com/v1alpha1
kind: RepoBoard
metadata:
  name: kcc
  namespace: board-kcc          # shared board: repo-scoped namespace
                                # personal board: the user's own namespace
spec:
  repoURL: https://github.com/org/repo

  access:
    mode: github                # github: membership = push/maintain permission,
                                #   verified with the viewer's OWN token (cached)
    allow: []                   # mode: list — explicit logins (personal boards
                                #   are `mode: list, allow: [me]`)

  triggers:
    label: agent                # trigger-label prefix ("" disables GitHub-side
                                #   label triggering); labels are the optional
                                #   remote-trigger + explicit-agent marker
    discreet: true              # allow UI-only kickoff (mailbox, no label)

  intake:                       # automation that produces DRAFTS ONLY —
    triageIssues: true          #   zero GitHub writes; suggestions surface on
    draftReviews: true          #   the board until a human publishes/applies
    autoFix:
      enabled: false            # board-side key of the two-key consent
      require: [assigned, label]  # or [assigned] (aggressive; discouraged)
    filters:
      excludeLabels: [no-agent] # hard veto

  ready:                        # what lights the "ready to merge" state
    approvals: 1
    requiredChecks: true

  policy:
    draftPR: true               # fixes open as draft PRs until promoted
    autoIterate: true           # factory pr watch may push iteration commits
                                #   to the author's own PR branch
    disclose: false             # add an "assisted-by" note to agent-drafted
                                #   PR bodies (disclosure lives on the artifact,
                                #   not in labels)

  limits:
    maxActive: 5
    maxActivePerUser: 2

  sandbox:                      # passed through to factory invocations
    image: ""
    diskSize: 10Gi
    idleMinutes: 60

  prompts:                      # optional --instruction overrides
    fix: ""
    review: ""
    triage: ""

  prepIdentity:
    secretName: ""              # identity for draft-only intake tasks; a bot
                                # is acceptable here because intake never
                                # writes to GitHub

status:
  conditions: []                # Auth, IntakeHealthy, ...
  counts: {needsHuman: 0, active: 0}
```

Deliberately absent, and why:

- **No `spec.work` / explicit issue lists.** Work is discovered (see §5);
  the CR is near-static config. No write races between members, no ledger to
  garbage-collect.
- **No selection filters (labels/assignees/handlers).** Those are live query
  parameters on the work feed, adjustable per view.
- **No engine config.** Factory owns models, execution, resilience.
- **No full `status.work`.** The board is computed live by the API
  (GitHub + sandboxes), overseer-dashboard style; status keeps only
  conditions and counts for `kubectl` ergonomics.
- **No dev section.** Dev sandboxes stay on RepoWatch until their own
  redesign (future `Workspace` CRD).

Per-user state (auto-fix opt-in per board, notification prefs) lives in the
member's own namespace, managed via `/api/board/:name/settings` — never in
the shared CR.

## 5. Coordination contract: GitHub is the shared database

The system of record is **GitHub + sandboxes**; the CR stores neither queue
nor claims.

**Claims and audit are universal and GitHub-native, in every mode:**

- **Fix click ⇒ assign the issue to the clicker** (their token). The
  assignee *is* the claim — legible to every maintainer including
  non-users — and the timeline's assignment event is the intent audit.
- **Review click ⇒ self-requested review** on the PR — the native twin:
  its own timeline event, appears in `review-requested:@me`, and is
  naturally satisfied when the draft is published.
- **Releasing a claim = unassign / withdraw the request**; the controller
  observes and pauses the sandbox.
- Best-effort on permission: if the clicker lacks triage rights (personal
  mode on a foreign repo), the GitHub-side claim is skipped and kickoff
  proceeds via the mailbox alone.

**Trigger tiers** (one discovery loop, a consent dial):

| Tier | Predicate | Consent | First GitHub artifact |
|---|---|---|---|
| Manual | UI click (assign + optional label) | the click | assignment event |
| Remote | trigger label applied on GitHub (any surface, incl. mobile) | the labeler's act | label + assignment |
| Auto | issue assigned [+ label] to an opted-in member | **two-key**: `intake.autoFix.enabled` AND the member's opt-in | draft PR |
| *(overseer)* | *assigned to bot* | *n/a* | *bot PR* |

Notes on the tiers:

- **`assigned + label` is the recommended auto gate**: the label says *an
  agent should attempt this*, the assignee says *whose agent, whose
  identity, whose claim*. Assignment alone stays what it always meant.
- **Auto-fix consent is two-key** because a third party's assignment causes
  work executed as the assignee (their token, their quota, a PR in their
  name). The board enables the capability; each member opts in per board.
- **Auto-fix safety rails:** draft PR forced regardless of `policy.draftPR`;
  one attempt then park as *needs attention* (re-fix is a human click);
  `no-agent` veto label; `maxActivePerUser`; the board badges
  "auto-started for you" so members learn from the board, not from a GitHub
  email about their own PR.
- **Review-request twin (free):** a review request to an opted-in member
  pre-drafts a review. No consent ceremony needed — drafts write nothing.
- **Discreet mode** (`triggers.label: ""` or per-click): kickoff flows
  through a **mailbox** — a transient annotation on the board CR
  (`board.gemini.google.com/requests`), consumed and cleared by the
  controller the moment the sandbox exists. It is a self-emptying queue for
  the seconds between click and sandbox, not a ledger. Claims remain via
  assignment; only the agent-branded label is omitted. Tradeoff (accepted):
  assignment says *alice took it*, not *an agent is helping* — disclosure,
  where required, is `policy.disclose` on the PR body.

**Dedup:** machine claim = sandbox existence (`fix-<repo>-<n>` in the
executor's namespace). The controller checks the board's member namespaces
before launching; a second click surfaces "claimed by alice" with an
explicit override (which lands in a different namespace — no name
collision).

## 6. Execution: two tiers, tokens never move

- **Intake tier (drafts only)** — triage suggestions, review drafts — runs
  in the board namespace under `prepIdentity`. May be a bot: nothing lands
  on GitHub.
- **Attributed tier (everything visible)** — fixes, publishes, label
  applications, merges — runs as the acting human:
  - Fix execution: the controller launches `factory fix -n <assignee-ns>`;
    factory picks up the assignee's `factory-user` secret (materialized at
    their login by the existing bootstrap). Sandbox, PR authorship, and
    quota are the assignee's. `factory pr watch` follow-up runs the same way.
  - Publishes/merges: through the API server under the clicker's OAuth
    token; tokens never enter a shared namespace or another user's sandbox.

The board aggregates live state by reading members' sandboxes for this repo
(namespace == GitHub login is the existing tenancy convention). On a shared
board, work products are team-visible by design (Bob can read Alice's
in-progress draft); sandbox lifecycle actions are owner-only at first
(terminal access especially).

## 7. API surface

- `GET  /api/boards` — boards visible to the session (access check with the
  viewer's token, cached ~15m).
- `GET  /api/board/:name/work?lens=me|repo&labels=&attention=&author=` —
  the feed: GitHub involvement/search + sandbox state + claims merged into
  rows `{type, number, title, stage, attention, claimedBy, prURL,
  sandbox{name, replicas, taskState}, updatedAt}`.
- `POST /api/board/:name/issues/:n/fix` — assign + label/mailbox per mode.
- `POST /api/board/:name/prs/:n/review` — self-review-request + kickoff.
- `POST /api/board/:name/prs/:n/publish|merge|promote` — human-gated writes
  under the clicker's token (draft review publish, merge, draft-PR promote).
- `POST /api/board/:name/issues/:n/refix` — re-run (annotation, as today).
- `PUT  /api/board/:name/settings` — per-member opt-ins (auto-fix, etc.).
- Sandbox subresources (pause/resume/logs/terminal) reuse today's endpoints,
  membership-gated.

The legacy per-tab endpoints (`/prs`, `/issues`, task/draft plumbing) are
absorbed by the feed + detail data and retire with the old UI.

## 8. UI: four pages

1. **Work** (home): tabs = All + one per board. High-density table — type
   icon, number+title, stage chip, attention chip, claimed-by, sandbox chip
   (with pause/resume inline), age, and one action button per row (Fix /
   Review / Merge per lens and stage). Filters: labels, attention, author.
   Badge counts per tab (`Needs you: 3`).
2. **Item detail** (row click, full width, no sidebar): event timeline (fix
   launched → PR opened → CI failed → investigated → ready), draft-review
   editor when applicable, triage suggestions with apply-buttons
   (label writes under the clicker's token), sandbox controls + terminal,
   logs.
3. **Add board**: paste a repo URL + a handful of toggles. The YAML editor
   survives only as a power-user escape hatch.
4. **Settings**: tokens, per-board opt-ins.

Untouched: Overseer admin, TokenUsage, Terminal component, and a Workspaces
tab holding the current dev-sandbox UI as-is.

## 9. Rollout

Each phase is an independently shippable PR series; breaking changes inside
repo-agent are acceptable throughout (per the migration constraints).
`overseer/` is never touched; any factory ask goes through the §10 gate.

- **Phase 1 — read path.** RepoBoard CRD + controller (reusing the
  factorycli launcher and reconcile machinery from the migration); the
  `/work` feed; the Work page shipped alongside the existing UI. Personal
  boards only (`mode: list`).
- **Phase 2 — actions.** Fix/Review clicks (assign + self-review-request +
  label/mailbox), item detail page, publish/promote/merge; Work becomes the
  default page; legacy Review/Issues tabs retire.
- **Phase 3 — shared boards + intake.** `access: github`, board namespaces,
  cross-member aggregation and claims UX; triage agent definition (the
  factory-CLI migration's D4 item, shipped as an `.agents/` definition run
  via `factory agent create --local`) and draft-review intake under
  `prepIdentity`.
- **Phase 4 — auto tier + cleanup.** Two-key auto-fix, review-request
  pre-drafting; delete legacy endpoints/models; RepoWatch shrinks to the
  dev-sandbox feature pending the Workspace redesign.

## 10. Non-goals and boundaries

**RepoBoard is not overseer.** It borrows overseer's discovery *pattern*
(trigger label + assignee → factory) but not its *job*. Explicit non-goals:
workflow meta-skills, chores/cron, durable queues, bot identity pools, fleet
operations, and any form of auto-publishing. The §2 invariant is the fence:
the moment the board publishes to GitHub without a human key-turn, it has
become a worse overseer. The creep test for future feature requests
("auto-publish LOW-severity reviews?") is: *does the repo accept bot authors
and want hands-off throughput?* Then the answer is "install overseer on that
repo", not a board flag.

**Dependency rules for this effort:**

- `overseer/` — never modified, in any way.
- `factory/` — not modified by default. Strictly **additive** changes (a new
  flag or subcommand; no behavior change for existing callers, no wire-format
  changes) may be proposed, each requiring explicit owner approval before any
  factory PR is opened. Until approved, the repo-agent side ships with a
  workaround or the feature waits.

Foreseeable additive-factory candidates (flagged now, not approved):

1. **Draft-PR support in `factory fix`** (e.g. a `--draft-pr` flag or env).
   `policy.draftPR: true` — and the forced draft-PR rail on auto-fix — can
   interim-ship via `--instruction` ("open the PR as a draft"), but that is
   prompt-enforced, not guaranteed; a flag would make the safety rail hard.
2. **Machine-readable output mode** (e.g. `--output json` on `fix`/
   `pr review`). Today the controller parses the CODE REVIEW stdout banners
   and `agent-output.txt` PR-URL lines — workable, but brittle as a
   long-term contract.

## 11. Open questions

1. **Trigger-label taxonomy** — `agent`, `agent/fix`, `agent/review`,
   `agent/ready-for-human`? Becomes repo-visible vocabulary; pick once.
   (Overseer precedent: `<triggerLabel>/…` sub-labels.)
2. **prepIdentity default** — installing maintainer's token vs. a dedicated
   read-only bot. Leaning bot: intake never writes, and it decouples the
   board from one person's quota/tenure.
3. **Stage detection for "needs you"** — computed in the feed (checks +
   reviews + draft-ready) vs. stamped by the controller as an annotation.
   Leaning controller-stamped for consistency with `kubectl` views.
4. **Overseer convergence** — whether RepoBoard eventually absorbs the
   Overseer CR as `writePolicy: bot`. Out of scope here; the design keeps
   the door open.
