# Sessions: a session-centric view

The Work tab starts from GitHub: one row per issue or PR on a board, with a
chip for each recipe run on it. That is the right entry point for GitHub
work, and it stays. But since recipes, the agent's side of that work is a
session: a conversation in a sandbox, about a repository, often but not
always about an issue or PR. A member juggling a fix, a review and a couple
of research questions across boards has no one place that answers "which
of my agents is working, which is waiting on me, which is sitting idle".

The nearest thing is research's list (the board's Research tab and All →
Research), which already asks every running pod what its session is doing.
It covers research sandboxes only, and research is just a session.

This note adds a **Sessions** page: every session in the member's
namespace, whatever started it, sorted by what its agent is doing, with a
box to start a repository-level session. It replaces the Research tabs.

## Decisions

- **Work stays as it is.** Sessions is a separate entry point, an item in
  the ☰ menu below Work. The menu is not flattened.
- **A row is a session**: one agent conversation in one sandbox, the runs
  recorded into the same task session (a revise's runs into the one it
  revised). The sandbox is a column, not the row: pause and delete act on
  the sandbox, so on every session in it.
- **Every session in the namespace is listed**, wherever it was started:
  a Work row's button, this page's box, the factory CLI. The list is built
  from sandboxes, not from launches. Continuity with the board is the point.
- **Namespace-wide**, across boards. A Repo filter narrows it to one.
- **Sorted by the agent, not by GitHub.** Work's "needs you" is GitHub
  waiting on the member; here it is an agent waiting on them: a permission
  request, a ready draft.
- **Sessions start here at repository level.** Item recipes keep starting
  from Work rows, where their context and rules are.
- **The Research tabs go**, after the page has been used (step 3). A
  research conversation is a row here with no item.
- **No LLM** reads what the member types. Recognising an issue or PR in the
  box (step 5) is a regex and the board's own data.

## The page

```
            Repo Agent                                                    ☰
┌──────────────────────────────────────────────────────────────────────────────┐
│ ┌──────────────────────────────────────────────────────────────────────────┐ │
│ │ Ask anything about k8s-config-connector — a question, a subsystem, or    │ │
│ │ 'compare with …'                                                         │ │
│ │ [▣ kcc ▾]  [◆ gemini ▾]   overview · recent changes             [Start ↵]│ │
│ └──────────────────────────────────────────────────────────────────────────┘ │
│ Repo: all ▾   State: All|Live|Needs you   Group: Status|Repo|Sandbox    🔍   │
│                                                                              │
│ NEEDS YOU (2)                                                                │
│ ● ◆ Care · rebase        g4k  PR #1774 ↗  fix-g4k-1774   ⚠ git push      3m  │
│ ● ✦ Plan                 kcc  #13865 ↗    fix-kcc-13865  📝 draft ready  12m │
│ WORKING (3)                                                                  │
│ ◐ ◆ Fix                  kcc  #13889 ↗    fix-kcc-13889  turn 4          1m  │
│ ◐ ✦ Review               g4k  PR #1488 ↗  review-g4k-1488 ⟳ retrying 429  0m  │
│ ◐ ◆ "reconciler deletes" kcc  —           rsch-recon-3f2a turn 2         2m  │
│ IDLE · LIVE (1)                              engine up, nobody talking to it │
│ ○ ◆ Triage               g4k  #1681 ↗     fix-g4k-1681   labels applied  1h  │
│ RECENT — PAUSED / DONE / FAILED (30d)                                ▸ show  │
└──────────────────────────────────────────────────────────────────────────────┘
 ● needs you  ◐ working  ○ idle, live  ◌ paused  ✓ done  ✕ failed
 ◆✦ engine (as on Work's chips)   ↗ the item on GitHub   — no item
```

Columns: state, engine, title (the recipe's label, or a research
session's title), item (↗ to it on GitHub; Work has no per-row link yet) and repository, sandbox, what
it is doing now (the turn, the permission asked, the draft, the retry, the
failure), and how long ago it last changed.

**Sections:**

- **Needs you:** a turn stopped on a permission request, or a run ended
  `ready` (a draft nothing but edit was applied to).
- **Working:** a turn in flight, or a run's task still running.
- **Idle · live:** the engine is up and nobody is talking to it. Listed,
  not folded: it holds CPU and counts toward `maxActive`.
- **Recent:** paused, done or failed in the last 30 days. Collapsed.

**Group by** Sandbox shows a sandbox's sessions under it, with its pod
state, for working out the several-sessions-per-sandbox patterns:

```
▾ fix-g4k-1774   running · ◆    g4k PR #1774
    ◐ Care · rebase      turn 3              session care
    ✓ Fix                done · PR opened    session fix
▾ rsch-recon-3f2a  running · ◆  kcc
    ◐ "reconciler deletes"
▸ fix-kcc-13781  paused         kcc #13781   (1 session)
```

## A session open: a split page

Nothing open, the box and the list take the whole width (above). A row
opens its session on the right and the list narrows to a column of
two-line rows: the title and age, then repository, item and what the agent
is doing. The session is `ResearchConversation`, the component Work's
slide-over and the full-window route already use, under a strip with the
session's runs and its sandbox.

```
┌ Sessions ─────────────┬─ runs: care-ci ✓ → care-comments ✓ → rebase ◐ ──────┐
│ [+ New]  2 need you  «║ [▣ fix-g4k-1774] running ◆ g4k  PR #1774 ↗          │
│ all repos ▾  All|Live ║   also here: Fix (done)                               │
│ NEEDS YOU (2)         ║┌ Rebase · fix-g4k-1774 ─────────────────────── ↗ ✕ ┐│
│▌● Rebase           3m ║│ agent › Rebased onto main; 2 conflicts in …       ││
│   g4k PR #1774 ⚠ ask… ║│ ⚠ Permission: git push --force-with-lease         ││
│ ● Plan            12m ║│   [Allow] [Allow always] [Deny]                   ││
│   kcc #13865 📝 draft ║│                                                   ││
│ WORKING (3)           ║│ ❯ message the agent…                       [send] ││
│ ◐ Fix              1m ║└───────────────────────────────────────────────────┘│
└───────────────────────╨──────────────────────────────────────────────────────┘
  ║ drag to resize    « collapse the list to a strip of state marks
```

- **The strip.** The runs recorded into the conversation, oldest first;
  the sandbox (▣ opens its card: wake, pause, terminal, logs, delete), its
  pod, engine, repository and item. "Also here" lists the sandbox's other
  sessions, so Pause and delete say what they touch, and opens them.
- **The column** is dragged wider or narrower, or collapsed (`«`) to a
  strip of state marks, one per row, each opening its session: the
  conversation gets almost the whole window, which is what #1647 took the
  old 260px research rail away for, and switching stays one click.
  Width and collapse are remembered (localStorage).
- **`+ New`** opens the start box in the right pane.
- **Keys:** ↑/↓ walk the list, Escape closes the session and the list is
  the page again.
- **The URL** keeps the open session: `#/sessions/<sandbox>/<task>`, or
  `#/sessions/research/<id>` for a research conversation, so a requested
  one and the sandbox that serves it are one selection.
- The list keeps polling while a session is open, so another one stopping
  on a question shows up on the left.
- Work's chips keep their slide-over: the same component, as an overlay.

## Starting a session

The box is research's landing box moved here: no heading, no name field
(the server names the session from the first line), Overview and Recent
changes filling the box with a canned prompt. It gains:

- **Repo chip:** the member's boards' repositories, defaulting to the last
  one used. The placeholder and the canned prompts follow it. A repository
  with no board is added on Work first, so revises and Save notes always
  have a board to be filed on (pasting a URL here to create one: later).
- **Engine chip:** later. Research follows the board's engine, and the
  start route takes no engine yet.
- **Recipe chip:** only once a second recipe runs `on: repo`. Today only
  `research` does, so there is none. A recipe with required inputs beyond
  the topic gets one-line fields under the box when picked.

Start opens the session at once on the right, pending ("starting
sandbox…"), and the row appears under Working. It posts to the board's
research route, `POST /api/board/:board/research`, until step 4 replaces
it.

## API

- `GET /api/sessions`: the rows. Every sandbox in the member's namespace,
  its recorded runs grouped by session tag (`runSessions` already reads
  them), research's sandboxes as today's list reads them, and live state
  (live, busy, waiting, retrying) from each running pod's daemon:
  `attachResearchLiveState`, widened from research pods to every pod with
  a session, still in parallel and bounded as a whole. Polled every 10s.
- Step 4: `POST /api/board/:board/recipes/:recipe` with `{inputs, engine}`:
  a recipe on the board's repository, beside the item launches
  (`…/issues/:id/recipes/:recipe`, `…/prs/:id/recipes/:recipe`). It
  replaces `POST /api/board/:board/research`; the prompts move to
  `GET /api/board/:board/recipes/research/prompts`.
- Rename keeps research's title annotation, as a session route. End is the
  sandbox card's delete.
- **Removed in step 4:** `GET /api/research`, `PATCH|DELETE /api/research/:session`,
  `POST /api/board/:board/research`.

## Steps

1. **`GET /api/sessions`**, with tests on fixtures of real sandboxes: a
   fix with care runs in one sandbox (two sessions), a review, research, a
   pre-recipe sandbox, an unreachable pod.
2. **The page:** the ☰ entry, the box (on the research route), rows,
   sections, filters, grouping, the split with its strip.
3. **Remove the Research tabs** once the page has been used: the board's
   Research tab, All → Research (`AllResearchPanel`), `ResearchPanel`'s
   list.
4. **One launch route for repo recipes**, `POST
   /api/board/:board/recipes/:recipe`, replacing the research routes
   (`/api/research*`, `POST /api/board/:board/research`); the engine chip.
5. **Issue or PR references in the box.** Typing `#N`, `kcc#N`,
   `owner/repo#N` or an issue/PR URL turns the box into a launcher:

   ```
   ┌──────────────────────────────────────────────────────────────────────┐
   │ review kcc#13873                                                     │
   │ [▣ kcc ▾]  [◆ gemini ▾]                                   [Start ↵]  │
   ├──────────────────────────────────────────────────────────────────────┤
   │  PR #13873 · "fix: KMSKeyHandle drift" · lovelace-coder-bot · open    │
   │  ▸ Review               runs on this PR                ↵              │
   │    Care ▾               (care-ci · care-comments · care-rebase)       │
   │  ─────────────────────────────────────────────────────────────────── │
   │    Ask "review kcc#13873" as a question about the repo                │
   └──────────────────────────────────────────────────────────────────────┘
   ```

   All in the browser: a regex finds the reference, `kcc` resolves against
   the board list (board or repository name), and the suggestions are the
   item's row's own recipe buttons from the board's work feed, so the
   rules are Work's (`on:`, `applicable-when`, Review on one's own PRs
   only with "Review my PRs"). A word typed (`review`, `fix`, `triage`)
   only picks which one is highlighted. A recipe already running on the
   item offers "Open … session ↗" instead. Enter launches through the
   item's existing route, so the sandbox, the Work chip and the Sessions
   row are what a click on Work makes. The last line always asks the text
   as a question. An item not in the board's feed (closed, out of scope)
   says so; a single-item lookup is the only backend this could need.

## Later

- **Overseer sessions**, behind a `Scope: Mine | overseer-<name>` picker.
  The overseer's work view already shares the slide-over.
- **Multi-repo sessions.** A primary repository plus read-only context
  repositories cloned in one sandbox (`on: repos`); outputs go to the
  primary, whose board files the revises. Writes to several repositories
  would be a parent session with single-repo children, as fan-out does.
- **Board work proposed from a session:** an action in a draft ("open an
  issue", "Fix #N") the member clicks, through task-output actions. Not the
  engine launching work itself.
