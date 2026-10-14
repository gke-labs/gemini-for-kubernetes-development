# Overseer: a work view

The Overseer page lists sandboxes: a table of the ones running and the ones
scaled down in `overseer-<name>`, with a drill-down per sandbox. Since the
overseer moved to recipes, a sandbox is no longer the unit anyone thinks
in. An issue's fix, its PR's care-ci, care-comments and care-rebase all run
in one sandbox (`fix-<repo>-N`), its review in another
(`review-<repo>-<pr>`), and a workflow in a third (`wf-issue-N`). What an
operator wants to see is the work: this issue, its PR, what ran on it, what
is running, what failed.

This note replaces the page with a work view: one row per issue (and the
PR its fix opened), per PR, or per agent without an issue, with a chip for
each run on it. It reuses the board's session slide-over and sandbox card
as they are, the same components, pointed at the overseer's namespace.

It is different from the board's Work tab, too. The board starts from
GitHub (a member's issues and PRs) and adds sandboxes; this starts from
the overseer's sandboxes and queue, and needs no GitHub at all in v1.

## Decisions

- v1 is GitHub-free: rows, titles and states come from sandboxes, their
  recorded runs and the watch's queue. No PR state, no CI status.
- The session slide-over is shared with the board: one component, not a
  copy.
- A fan-out parent is one row (its workflow chip, a children counter at
  most); no child expansion yet.
- The work view replaces the current page. The queue, the daemon's logs and
  the key status stay, behind links in the header.

## The page

```
 Overseer: kcc ▾   GoogleCloudPlatform/k8s-config-connector        Queue 7 · Logs · Keys 3/4 ✓
 ──────────────────────────────────────────────────────────────────────────────────────────────
 [ Both | Issues | PRs ]  [ All | Running | Paused | Failed ]   🔍 search…
 ──────────────────────────────────────────────────────────────────────────────────────────────
 ▌NEEDS ATTENTION (2)
   #13870 → PR #13873  Add SQLInstance direct controller        [fix ✓] [care-ci ✗] [review ⏸]
          @coder-bot  ·  care-ci failed: push lease  ·  3h                 ▣ fix-kcc-13870  ⏸
   PR #13778           Fix IAMPolicyMember mapper               [review ✓] [care-comments ✗]
          @coder-bot  ·  care-comments failed  ·  40m                      ▣ fix-kcc-13778  ●

 ▌WORKING (3)
   #13889 → PR #13901  Pub/Sub topic schema settings            [fix ✓] [care-rebase ● 4m] ◆
   #13916              Bigtable app profile routing             [fix ● 22m ⟳1] ◆
   #13781  fan-out     Direct controllers for 12 resources      [workflow ● step 3]
 ▌QUEUED (2)
   #13931              Spanner backup schedule                  [fix ⧗ rank 1 · high]
   PR #13873                                                    [care-ci ⧗ rank 2]
 ▌IDLE (4)                                                                       ▸ expand
 ▌AGENTS (2)
   ⚙ weekly-dependency-bump   .agents/deps.md                   [agent ✓ 2d]   ▣ agent-kcc-7f3a ⏸
   ⚙ warm k8s-config-connector                                   [warm ✓]       ▣ warm-kcc       ⏸
 ──────────────────────────────────────────────────────────────────────────────────────────────
 ● running  ⧗ queued  ✓ done  ✗ failed  ⏸ paused  ◆ engine  ⟳ engine retries
```

A chip opens the session slide-over on that run; ▣ opens the sandbox card
(terminal, logs, wake, pause, delete).

```
 ┌──────────────────────────── care-ci · PR #13873 · fix-kcc-13870 ── gemini ── ↗ ✕ ┐
 │ run investigate-13873-a1b2c3d-…   failed 12m ago   session: care                    │
 │ agent › The unit-tests job fails in pkg/controller/direct/sql: …                   │
 │ tool  › go test ./pkg/controller/direct/sql/...  ✓                                 │
 │ ────────────────────────────────────────────────────────────────────────────────── │
 │ TASK OUTPUT  Change · report: "unit-tests failed because …"                        │
 │ ────────────────────────────────────────────────────────────────────────────────── │
 │ [ message the agent…                                                    ] [Send]   │
 └────────────────────────────────────────────────────────────────────────────────────┘
```

## Rows

A row is keyed by repository and number, built from what the overseer's
namespace holds:

| Source | Row |
|---|---|
| `fix-<repo>-N`, labelled `factory.gemini.google.com/issue=N` | issue N |
| …and labelled `factory.gemini.google.com/pr=M` (open-pr aliased it) | issue N → PR M, one row |
| `review-<repo>-M` | PR M's row (by name: it carries no PR label) |
| `wf-issue-N` | issue N's row, its workflow chip |
| A queue entry (incoming or processing) with no run yet | its issue's or PR's row, a queued chip |
| `agent-*`, chores, `warm-*` | a row of its own under Agents, keyed by sandbox |

Titles come from the sandboxes' `title`/`description` annotations and the
queue entries; a row with neither shows its number. The overseer
controller's own sandbox (`overseer-<name>`) is not a row.

**Chips.** One per run a sandbox recorded: the recorded-run annotations
(`sandbox.gemini.google.com/<type>-run`, `recipe-<session>-run`), each
with its recipe, state, engine and task, so a chip knows the task its
slide-over opens. A sandbox from before recipes, with only
`last-task-type`/`last-task-state`, gets one chip from those. Queue
entries add ⧗ chips.

**Sections**, from the chips alone:

- **Needs attention:** a row whose latest run of some recipe failed, or
  whose sandbox's pod is evicted or failed.
- **Working:** a chip is running.
- **Queued:** only queued chips.
- **Idle:** everything else: finished, paused. Collapsed.
- **Agents:** rows without an issue or PR, whatever their state.

Filters: issues, PRs or both; running, paused, failed; a search over
number, title, sandbox and recipe.

## The shared slide-over

The board's `SessionSlideOver` (Work.js) wraps `ResearchConversation`
(Research.js), which builds its URLs from `/api/task-sessions/…`,
`/api/task-session-events/…` and `/api/sandbox-card/…`. Everything it
offers beyond the conversation comes from the API's answer: the revises it
lists (`revises`), the draft and the actions on it (`draft.actions`).

So sharing it is one prop: `apiBase`, `/api` by default, and
`/api/overseers/<name>` on this page. The overseer's routes answer the
same shapes with no revises and a draft without actions, and the same
component shows a transcript, a read-only output and the message box. The
board is unchanged. `SessionSlideOver` and `SandboxCard` move out of
Work.js into modules of their own, which both pages import.

## API

All under `/api/overseers/:name`, for whoever may see the overseer (an
admin, a maintainer of its repository, or a login on its
`spec.allowlistedUsers`, all of it; `overseerAccess`), in namespace
`overseer-<name>`:

- `GET /work` builds the rows above: the sandboxes (`getOverseerSandboxes`
  already lists them), their recorded runs, and the controller's queue
  (`getOverseerQueue` already proxies it).
- `GET /task-sessions/:sandbox/:task`, `GET /task-session-events/…`,
  `POST …/prompt|permission|mode|cancel`: the board's task-session
  handlers with the namespace from the route instead of the auth session.
  The handlers take a namespace resolver; the board's resolves from the
  session as today.
- `GET /sandbox-card/:sandbox`, `…/log`, `POST …/lifecycle`, and the
  terminal: the same, for the card. The page's terminal moves here from
  `/api/terminal/:namespace/:name`, which goes.

No revise, draft or apply routes: the board files those as Requests on a
board, and overseer sandboxes belong to none. The overseer's CLI already
applies its runs (`--apply`).

## Steps

1. **Namespaced session and card routes** under `/api/overseers/:name`,
   sharing the board's handlers through a namespace resolver.
2. **`GET /api/overseers/:name/work`**, with tests on fixtures of real
   overseer sandboxes (fix with a PR alias, review, workflow, agent, a
   pre-recipe sandbox, queue entries).
3. **Share the components:** `SessionSlideOver` and `SandboxCard` in their
   own modules, `apiBase` on them and on `ResearchConversation`; the board
   passes nothing and is unchanged.
4. **The page:** rows, chips, sections and filters, replacing the table;
   Queue, Logs and Keys behind the header links.

## Later

- GitHub: PR state, CI, `ready-for-human`, titles for rows without one;
  "Needs attention" from GitHub too.
- Writes from the slide-over (apply, reject, rerun, revise) through a path
  of the overseer's own.
- Fan-out children under their parent.
- A Done section, once merged and closed are known.
