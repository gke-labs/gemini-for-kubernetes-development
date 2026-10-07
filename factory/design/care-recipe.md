# Care: a recipe for your PR

**Status:** Proposed.

The `fix` recipe does two jobs today. Its start fixes an issue and pushes a branch. Its revises look after the PR afterwards: `iterate`, `address-comments`, `fix-ci` and `rebase`. For a PR the fix did not open, or one whose fix sandbox is gone, fix has a PR mode: `on: [issue, my-pr]` with `start.on: [issue]` (`revisesOn: [my-pr]`). There the first revise opens a conversation and reads the push facts from the PR.

The board only reaches those revises through the fix's recorded run. The PR row's `Agent ▾` is keyed to fix (`fixSession`, `fixRevises`). A PR with no fix run, such as one opened by hand or one whose sandbox was deleted, gets nothing. #1774 is such a PR today.

This note splits the two jobs:

- **`fix`** fixes an issue. It keeps `iterate`, the one follow-up that belongs in the conversation that wrote the change.
- **`care`** looks after a PR of yours. Its start brings the PR to mergeable, and its revises are the targeted jobs.

Each row button then starts a recipe, and each revise lives in its run's session. Nothing on the board is keyed to fix.

## Decisions

1. **care is a new conversation.** A start always opens one; only a revise continues one (`StartDaemonSession` / `OpenDaemonSession`). care does not try to continue the fix's: its input is GitHub state (comments, checks, the base branch), not the fix's reasoning.
2. **care runs where a PR's recipes already run:** `recipe-<repo>-<n>` (`EnsureRecipeSandbox`). It does not use the fix's sandbox. It clones the PR's branch from the fork, so it has the code. No sandbox code changes.
3. **care's start does what the PR needs.** It rebases if the PR is behind or conflicting, then fixes failing checks, then works through unanswered review comments. A rebase changes both the checks and the code the comments are on, hence that order. An optional `focus` input (`comments`, `ci`, `rebase`) narrows a run to one job.
4. **Its revises are fix's three PR revises plus `iterate`**, with the same ids and prompts (moved, with "your fix" reworded to "this PR"). They continue care's conversation.
5. **fix loses its PR mode.** It is `on: [issue]`, with one revise. The generic mechanism (`start.on`, `revisesOn`, `firstRevise`) stays in factory for any recipe that wants it. No built-in uses it after this.
6. **The overseer is left as it is.** `factory pr address-comments`, `pr investigate` and the watch's fallback to them do not change.

## care.yaml

```yaml
name: care
label: Care
on: [my-pr]
task-type: care

inputs:
  focus:        # comments | ci | rebase; empty: whatever the PR needs
  instructions: { type: instructions }
  instruction:  { revise: true }        # iterate's
  pr_url:       { revise: true }

context: |      # fix's: commit only; factory pushes; never comment
setup:          # fix's: setup-git, setup-fork, configure-engine

start:
  steps:
    - run: *pushed                      # fix's step: branch/base/lease/pr from pushed_*,
                                        # checking out the PR's branch from the fork
    - ask: rebase if behind, fix checks this PR broke, address unanswered comments; commit
    - ask: the Change (replies, report, new title/body only if they no longer fit)
      capture: change.yaml
    - uses: push                        # leased against the PR's head

revise:         # moved from fix.yaml
  - id: address-comments
  - id: fix-ci
  - id: rebase
  - id: iterate

task-output:
  kind: Change
  from: change.yaml
  actions: [edit, post-replies, reject]
```

The PR's push facts reach the start the way they reach fix's first revise in PR mode today. `prPushInputs` sets `pushed_branch`, `pushed_head`, `pushed_base`, `pushed_fork`, `pushed_title`, `pushed_body` and `pr_url` from the PR. `runRecipe` calls it for a start on a PR whose output is a `Change`, as `firstRevise` does now.

## fix.yaml after

- `on: [issue]`; `start.on` goes.
- `revise:` keeps `iterate` only. Its `*pushed` step loses the "no fix made it: fetch the PR's branch" path, which care's start keeps.
- `task-output.actions`: `edit`, `open-pr`, `reject`. `post-replies` goes, because only the moved revises wrote replies.
- The header comment on PR mode goes.

## factory pr watch

The watch revises a fix run today (`reviseFixRun`: `fix-ci`, `address-comments` in the fix's sandbox). After the split:

- **A care run on the PR** (`care-run` on `recipe-<repo>-<n>`): revise it with `fix-ci` or `address-comments`, then apply `post-replies`, as now.
- **No care run:** start care with `focus: ci` or `focus: comments`, then apply `post-replies`.
- **Neither recipe can run**, for example on an old image: fall back to `pr investigate` / `pr address-comments`, as the watch does for a PR without a fix run today.

The controller keeps launching the watch per fix sandbox aliased to a PR (`followUpPRs`), under the same `auto ⏻` setting. Watching hand-made PRs is not part of this note.

## Board (repo-agent)

care is a catalog recipe like summarize. The controller runs it through the generic path (`settleRecipe`), and its run's chip and session come from the run scan. The changes:

- **Rows know `my-pr`.** A PR row that is `MyPR` matches recipes `on: [my-pr]` as well as `on: [pr]` (factory's `StartsOn` rule). This applies in `rowRecipes` and `boardStarts`.
- **Deleted:** `Agent ▾` (`Work.js`), `WorkItem.FixSession` / `FixRevises`, and what fills them.
- **Unchanged:** the session view (fix's run offers Iterate, care's its four revises), revise Requests, and `auto ⏻`.

## What the rows show

```
Issue, not fixed       #1681  Triage: done ↗                      Fix  Plan  Summarize  Triage again
PR from fix            #1797  Fix: done ↗                         Promote PR  Care  Summarize  Deploy ▾
Your PR, opened by     #1774  Summarize: done ↗  Review: done ↗   Promote PR  Care  Summarize
  hand
Someone else's PR      #1488  Review: ready ↗                     Summarize
```

## Steps

1. **factory**, one PR:
   - add `care.yaml`
   - trim `fix.yaml`
   - have `runRecipe` give a start on a PR the PR's push facts
   - switch `pr watch` to care
   - tests: recipe validation, `recipe list` (care on my-pr; fix with no `revisesOn`), the watch's choice of revise/start/fallback
2. **repo-agent**, one PR:
   - make rows know `my-pr`
   - delete `Agent ▾` and `fixSession` / `fixRevises`
   - tests: a `MyPR` row offers Care, a PR row of someone else's does not
3. **Verify in-cluster:**
   - Care on #1774, which has no fix run
   - Care on a PR the board's fix opened
   - Iterate from a fix's session
   - `auto ⏻` on a fix PR

Old fix sandboxes keep their recorded revise lists. Their sessions offer revises fix no longer has, so recreate them rather than shim ([no back-compat for recipes](recipe-driven-board.md)).
