# Recipe sessions: a tag that groups recipes

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

1. **A session is a tag.** A recipe may say `session: <name>`. The tag groups recipes, and does two things with the group: on one target, its runs go into one conversation in that target's sandbox (the first opens it, every later run of any of them continues it), and the board shows the group as one chip and one button. A recipe with no tag is a group of its own, as today.
2. **Nothing else is shared.** There is no session file and no inheritance: every tagged recipe is a whole recipe, as today, with its own `on`, `context`, `steps` and `task-output`. Steps several need (the PR checkout, the replies question, the push) are copied into each. Loading the recipes only checks that those with one tag agree on `on` and `credentials`, so that they land in one sandbox, and that one of them is named like the tag.
3. **The recorded run is the session's.** It lives at `RunAnnotation("recipe-<session>")`; the run record keeps which recipe it ran. One run of a session at a time, as one task per sandbox is today.
4. **A run in a session is a start that continues.** It opens the session with its steps if the session has no recorded run, or its recorded run's session is gone. Otherwise it asks its steps into that session (`OpenDaemonSession`). Its setup steps run again each time, so they must be idempotent. `--new-session` opens a new one regardless.
5. **A recipe in a session has no `revise:`.** Its siblings are its follow-ups.
6. **Sessions never cross.** care's session is `care`, the fix's conversation is the fix's own: care never continues the fix, though both run in the PR's fix sandbox.
7. **The tag is the board's group.** One chip per group per row (its newest run). One button, labelled with the recipe named like the tag: it runs that recipe, and the others are its `▾` menu, by their own labels.

## care

care splits into five recipes in session `care`, each a whole recipe: today's care setup, its own ask, the replies ask and the push, and today's task output.

| Recipe | Label | Its own steps |
|---|---|---|
| `care` | Care | today's start prompt: rebase if needed, fix checks, answer comments |
| `care-comments` | Address comments | today's address-comments ask |
| `care-ci` | Fix CI | today's fix-ci ask |
| `care-rebase` | Rebase | fetch upstream → base, today's rebase ask |
| `care-iterate` | Iterate | input `instruction`, today's iterate ask |

```yaml
# recipes/care-rebase.yaml
name: care-rebase
label: Rebase
on: [my-pr]
session: care
context: |                 # care's
steps:
  - uses: setup-git
  - uses: setup-fork
  - run: sleep 5           # lib.sh's HACK
  - uses: configure-engine
  - run: the PR's branch from its push facts, reset to the PR's head, leased against it
  - run: git fetch upstream HEAD && git rev-parse FETCH_HEAD > "$TASK_DIR/base"
  - ask: Rebase the branch onto {{ file "base" }} …
  - ask: the Change (replies, report)
    capture: change.yaml
  - uses: push
task-output:               # care's: Change, preview spec.report, edit / post-replies / reject
```

- `focus` and care's `revise:` are deleted.
- Every run takes the PR as it is now. It reads push facts from the PR (`prPushInputs`) and leases against its head, where today's revises lease against care's last push.
- `factory recipe care-rebase --url <PR>` runs one. `factory pr watch` runs `care-comments` on new review comments and `care-ci` on failed checks, instead of starting care or revising it.
- `recipe list` reports each recipe's session.

## repo-agent

The board keys runs and buttons by recipe today (`latestRuns` in `Work.js` and `rows.go`). It works unchanged with tagged recipes, but it is noisy:

- five rail buttons on every PR of yours;
- up to five chips, all opening the one conversation;
- a button hidden only while its own recipe runs, so Fix CI is offered while Rebase runs, and its click fails on the busy sandbox.

So the board groups by `session || recipe`, where it keys by recipe today:

| Where | Change |
|---|---|
| Catalog (`RepoBoard.status.recipes`), `sessions[]` | carry each recipe's `session` |
| `latestRuns` (`Work.js`, `rows.go`) | key by group: one chip per group, its newest run |
| Rail | one button per group, the recipe named like the tag, and `▾` for the rest (Iterate… prompts for its instruction) |
| Running / ready gate | per group: no button or menu while any run of the group is running or starting |
| Slide-over | the group's recipes where care's revises are today, plus ⋯ → New conversation (`--new-session`) |

Untagged recipes (fix, plan, triage, summarize, review, research) are each their own group, so their rows look as they do now. The launch path is today's (`recipe` Request verb, `POST ./recipes/:recipe`), one recipe per click.

## No compatibility

Session `care` records at `recipe-care`, the annotation care's runs use today. The first run after the change continues the session today's care start opened. No sandbox needs recreating. Requests for care's old revises fail and are not migrated.

## Risks

- **The session grows** over a PR's life. Engines compact long conversations, but the agent can drift; New conversation is the way out.
- **Session retention.** #1762 found gemini's `sessionRetention` deleting resumed chats. A conversation meant to last days must survive it, on each engine. A lost session opens a new one (decision 4) rather than failing.

## Steps

1. factory: the `session:` tag in the recipe shape (validate, recorded run by session, continue-or-open, `--new-session`); care split into five recipes; `pr watch` by recipe; `recipe list`.
2. repo-agent: the tag in the catalog and `sessions[]`, chips, buttons and the gate by group, `Care ▾`, New conversation.
3. Verify on 1774 / 1780:
   - Rebase as the first run (opens the session);
   - Fix CI after it (continues the session);
   - the fix's session untouched;
   - New conversation.
