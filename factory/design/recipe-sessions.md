# Recipe sessions: recipes that share a conversation

**Status:** Design. Changes care ([care-recipe.md](care-recipe.md)); the other recipes are unchanged.

care has one start and four revises. To rebase a PR you first run the start (rebase, then fix checks, then answer comments) and only then the `rebase` revise. Its revises are not follow-ups to the start. Each is a job of its own, as useful first as fifth.

## Where recipes run today

A recipe never names its sandbox. factory picks one from the target kind in its `on:` and from its `credentials:` (`recipe.go`, `runRecipe`):

| Target | Recipes | Sandbox |
|---|---|---|
| issue | triage, plan, summarize, fix | `fix-<repo>-<issue>` (`EnsureFixSandbox`) |
| PR | care | the fix sandbox aliased to the PR, else `fix-<repo>-<pr>` (`EnsurePRSandbox`) |
| PR, `credentials: clone` | review | `<recipe>-<repo>-<pr>` (`EnsureRecipeSandbox`) |
| repo | research | `rsch-<slug>-<id>`, one per conversation (`EnsureResearchSandbox`) |

Recipes on one target already share a sandbox. Each keeps its own conversation: a recipe's recorded run is the annotation `RunAnnotation("recipe-<name>")`, a start opens a new session, and a revise continues the recorded run's session.

## Decisions

1. **A session is a named conversation that recipes join.** A recipe may say `session: <name>`. Recipes with the same session, on the same target, share one conversation in that target's sandbox. The first run opens it and every later run, of any recipe in it, continues it. A recipe with no `session:` keeps one of its own, as today.
2. **A session is defined once,** in `recipes/sessions/<name>.yaml`, carrying what its recipes share:
   - `label`: the board's name for the group;
   - `on`, `credentials` (the target, so one sandbox);
   - `context`;
   - `setup`: steps run before every recipe's own;
   - `finish`: steps run after them;
   - `task-output`.

   A recipe in a session gives its own `name`, `label`, `inputs` and `steps`; the rest comes from the session, and `recipe.Validate` refuses a recipe that sets it too. This is how care's recipes share the PR checkout, the replies question and the push without copying them: `uses:` is a closed set of token-holding steps (`NamedSteps`), not a place for recipe steps.
3. **The recorded run is the session's.** It lives at `RunAnnotation("recipe-<session>")`; the run record keeps which recipe it ran. One run of a session at a time, as one task per sandbox is today.
4. **A run in a session is a start that continues.** It opens the session with setup + its steps + finish if the session has no recorded run, or its recorded run's session is gone. Otherwise it asks setup + its steps + finish into that session (`OpenDaemonSession`). `--new-session` opens a new one regardless.
5. **A recipe in a session has no `revise:`.** Its siblings are its follow-ups.
6. **Sessions never cross.** care's session is `care`, the fix's conversation is the fix's own: care never continues the fix, though both run in the PR's fix sandbox.
7. **The session is the board's group.** One chip per session per row (its newest run), labelled with the session's `label`. One button: the recipe named like the session is the default, and the others are its `▾` menu. A recipe with no session is its own group, as today.

## care

care splits into five recipes in session `care`:

```yaml
# recipes/sessions/care.yaml
name: care
label: Care
on: [my-pr]
context: |                   # today's care context
setup:                       # before every run: idempotent
  - uses: setup-git
  - uses: setup-fork
  - run: sleep 5             # lib.sh's HACK
  - uses: configure-engine
  - run: the PR's branch from its push facts, reset to the PR's head, leased against it
finish:                      # after every run
  - ask: the Change (replies, report)
    capture: change.yaml
  - uses: push
task-output:                 # today's: Change, preview spec.report, edit / post-replies / reject
```

| Recipe | Label | Steps |
|---|---|---|
| `care` | Whatever it needs | today's start prompt: rebase if needed, fix checks, answer comments |
| `care-comments` | Address comments | today's address-comments ask |
| `care-ci` | Fix CI | today's fix-ci ask |
| `care-rebase` | Rebase | fetch upstream → base, today's rebase ask |
| `care-iterate` | Iterate | input `instruction`, today's iterate ask |

```yaml
# recipes/care-rebase.yaml
name: care-rebase
label: Rebase
session: care
steps:
  - run: git fetch upstream HEAD && git rev-parse FETCH_HEAD > "$TASK_DIR/base"
  - ask: Rebase the branch onto {{ file "base" }} …
```

- `focus` and care's `revise:` are deleted.
- Every run takes the PR as it is now. It reads push facts from the PR (`prPushInputs`) and leases against its head, where today's revises lease against care's last push.
- `factory recipe care-rebase --url <PR>` runs one. `factory pr watch` runs `care-comments` on new review comments and `care-ci` on failed checks, instead of starting care or revising it.
- `recipe list` reports each recipe's session, and each session's label.

## repo-agent

- **Catalog.** `RepoBoard.status.recipes` carries each recipe's `session` and the session's label. The launch path is today's (`recipe` Request verb, `POST ./recipes/:recipe`), one recipe per click.
- **Row.** `Care ▾`: the button runs `care`, and the menu lists the session's other recipes (Iterate… prompts for its instruction). The menu is offered while no run of the session is running or starting.
- **Chip.** Rows group runs by session, else by recipe. Care's one chip opens the care session; its draft is the newest run's Change.
- **Slide-over.** The session's recipes where care's revises are today, plus ⋯ → New conversation (`--new-session`).

## No compatibility

Session `care` records at `recipe-care`, the annotation care's runs use today. The first run after the change continues the session today's care start opened. No sandbox needs recreating. Requests for care's old revises fail and are not migrated.

## Risks

- **The session grows** over a PR's life. Engines compact long conversations, but the agent can drift; New conversation is the way out.
- **Session retention.** #1762 found gemini's `sessionRetention` deleting resumed chats. A conversation meant to last days must survive it, on each engine. A lost session opens a new one (decision 4) rather than failing.

## Steps

1. factory: `session:` and session files in the recipe shape (load, validate, recorded run by session, continue-or-open, `--new-session`); care split into five recipes; `pr watch` by recipe; `recipe list`.
2. repo-agent: catalog sessions, group chips and buttons by session, `Care ▾`, New conversation.
3. Verify on 1774 / 1780:
   - Rebase as the first run (opens the session);
   - Fix CI after it (continues the session);
   - the fix's session untouched;
   - New conversation.
