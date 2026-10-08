# Care: several starts, one session

**Status:** Design. Builds on [care-recipe.md](care-recipe.md).

care has one start and four revises. To rebase a PR, you first run the start (rebase, then fix checks, then answer comments), and only then the `rebase` revise. A start with `focus: rebase` does just the rebase, but the board never sends `focus`. Its revises are not follow-ups to the start: each is a job of its own, as useful first as fifth.

This note gives care several starts, one per job, all sharing one session.

## Decisions

1. **care's jobs are equal.** `Whatever it needs` (today's start prompt, still the default), `Address comments`, `Fix CI`, `Rebase` and `Iterate` are all jobs. Any of them can run first, and any can run after any other.
2. **One care session per PR.** The first care run on a PR opens it; every later run, whatever its job, continues it. Today's start opens a new one each time; it no longer does.
3. **A new session only when there is none.** That is the first run, a sandbox that was recreated, a session that was lost, or an explicit `--new-session` (the slide-over's ⋯ → New conversation, for a session that grew too long or went wrong).
4. **care never continues the fix's session.** care runs in the PR's fix sandbox (care-recipe.md decision 2), but its session is found by its own recorded run (recipe `care`), never by the fix's (recipe `fix`). Its input is GitHub state, not the fix's reasoning (care-recipe.md decision 1).
5. **Every run takes the PR as it is now.** Each run fetches the PR's branch and resets to its head, leased against that head: the start's behaviour today. Revises today lease against care's last push, so a commit someone else pushed fails them. With every job able to run first, all of them read the push facts from the PR.
6. **This is care only.** fix (start, then `iterate`), review, plan, triage and research keep start + revise. The recipe shape below is general, but no other recipe uses it.

## care.yaml

`start:` and `revise:` give way to `setup:` and `jobs:`. A recipe has one form or the other, never both (`recipe.Validate`).

```yaml
name: care
label: Care
on: [my-pr]

inputs:
  instructions: { type: instructions }
  instruction:  { job: iterate }        # asked for when iterate is launched

context: |      # unchanged

# Every run, first job or fifth: idempotent.
setup:
  - uses: setup-git
  - uses: setup-fork
  - run: sleep 5                        # lib.sh's HACK
  - uses: configure-engine
  - run: *pushed                        # branch/base/lease/pr from the PR's push facts
  - run: fetch the PR's branch; git reset --hard its head

jobs:
  - id: care                            # the default: today's start prompt, no focus
    label: Whatever it needs
  - id: address-comments
    label: Address comments
  - id: fix-ci
    label: Fix CI
  - id: rebase
    label: Rebase
  - id: iterate
    label: Iterate
    inputs: [instruction]
# Each job's steps: its ask, the replies ask (capture: change.yaml), push.

task-output:    # unchanged: Change, preview spec.report, edit/post-replies/reject
```

`focus` is deleted: picking the job replaces it.

## factory

- `factory recipe care --url <PR> [--job <id>] [--new-session] [--input …]`. Without `--job` it runs `care`. factory finds care's recorded run in the PR's sandbox. If that run has a session, factory asks setup + the job into it, as a revise is asked today (`OpenDaemonSession`). If not, it opens a new session with setup + the job (`StartDaemonSession`), and that run becomes the recorded run.
- The run's name, record, harvest, task output and `--apply post-replies` are as a start's today. One chip still shows each run.
- `factory recipe revise <sandbox> <revise>` refuses a jobs recipe: there is nothing to revise. The `revise:` entries in care.yaml are deleted, along with the code that read care's last push for a revise (`changeInputs` stays for fix's iterate).
- `factory pr watch` (auto) runs `care --job address-comments` on new review comments and `care --job fix-ci` on failed checks, instead of starting care or revising it.
- `recipe list` reports a recipe's jobs (id, label, inputs) where it reports revises today.

## repo-agent

- The catalog (`RepoBoard.status.recipes`) carries care's jobs. The `recipe` Request verb's `inputs` carries `job` (and `instruction` for iterate). The controller passes `--job`. The same launch path serves the row and the slide-over, with no revise Requests for care.
- **Row:** `Care ▾`. The button runs `Whatever it needs`; the menu lists the other jobs, and Iterate… prompts for the instruction. It is offered whenever no care run is running or starting, as the bare `Care` is today (#1827).
- **Slide-over:** the same jobs where care's revises are today, plus ⋯ → New conversation (`--new-session`).
- **Chip:** still one per recipe per row, the newest run. Every run continues the one session, so the chip opens that session, and its draft is that run's Change.

## No compatibility

Existing care runs stay as they are. The first care run after the change, on a PR whose recorded care run came from today's start, continues that run's session, which is the same daemon session form. No sandbox needs recreating.

## Risks

- **The session grows** over a PR's life: every comment round and every CI fix is in it. Engines compact long conversations, but the agent can drift; New conversation is the way out.
- **Session retention.** #1762 found gemini's `sessionRetention` deleting resumed chats. A session meant to last days must be checked to survive it, for each engine. A lost session opens a new one (decision 3) rather than failing.

## Steps

1. factory: `setup:` + `jobs:` in the recipe shape, care.yaml, `recipe care --job / --new-session`, `pr watch` by job, `recipe list` jobs.
2. repo-agent: catalog jobs, `job` input on the recipe verb, `Care ▾` on the row and in the slide-over, New conversation.
3. Verify on 1774 / 1780: Rebase as the first run (new session), Fix CI after it (same session), the fix's session untouched, New conversation.
