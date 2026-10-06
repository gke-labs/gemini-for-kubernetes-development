# Fix as a recipe

**Status:** Steps 1–3 are built: step 1 (#1771) is the `fix` recipe's start, `setup-fork` and `push`, the Change kind, `open-pr`/`edit`/`reject`, and `run:fix` on the recipe; step 2 (#1772) the revises, `post-replies`, revise inputs and `factory pr watch` revising a fix run; step 3 (#1773) the board's fix, follow-ups and UI. Step 4 (in-cluster verification) is not recorded in this repo. Much has changed since; see [As built](#as-built):
- The PR mode (#1784) and the PR revises (`address-comments`, `fix-ci`, `rebase`) moved to the `care` recipes ([care-recipe.md](care-recipe.md), #1805; split in five by [recipe-sessions.md](recipe-sessions.md), #1834). `iterate` went with revise itself (#1938, [repo-recipes.md](repo-recipes.md#results-are-files-the-agent-writes)); care-iterate changes an open PR. fix is `on: [issue]`, with no revises.
- The overseer fixes issues with this recipe (#1883, its PR opened ready with `--draft false`), and `factory fix` with its scripts is deleted (#1884).
- The board runs fix like any recipe (#1950, #1954): `open-pr` is fix's `task-output.on-finish` (#1946), applied by the runner when the run ends; `ensureFix`, `fix-harvested-at` and `fix-error` are gone, and `policy.draftPR` is removed (#1965).

The board's fix is the last of its agent work on the old mechanism, and the busiest. `factory fix` runs `fix_issue.sh` with the member's token; the agent pushes, runs `gh pr create`, and writes the PR's URL to `agent-output.txt`, which factory reads to alias the sandbox to the PR. Every follow-up is a separate command in the same sandbox, resuming the engine CLI's last chat:

| Board control | Command today | What it does |
|---|---|---|
| Fix, Fix with this plan, Fix again | `factory fix --url <issue> [--with-plan]` | branch, fix, push, open the PR |
| Iterate (with text) | `factory pr iterate --prompt …` | change the PR as asked, push |
| Address review comments | `factory pr address-comments` | answer new comments and reviews, push, reply with `gh` |
| Fix failing CI | `factory pr investigate` | read failed runs' logs, fix, push, report with `gh pr comment` |
| Auto (on by default) | `factory pr watch --continue-session`, relaunched every 10m | runs the two above when checks fail or comments arrive |

Nothing is recorded but `last-task-type/state`, `htmlURL` and the PR label. The board's result is in memory only and is lost with the controller. A fix cannot be opened with *Continue session*, and a follow-up is a new CLI run with `--resume latest`, not a turn in the fix's conversation.

Triage, plan, research and review already run as recipes: a task session in the sandbox's daemon, a typed task output, revises asked into the same session, and `factory apply` doing GitHub writes with the caller's token. This note moves the fix there, on these decisions:

1. **The agent commits; it never pushes.** It makes as many commits as the work needs, on the branch factory checked out.
2. **The task pushes, from inside the sandbox, always to the member's fork.** A factory-owned step ends the task: it pushes the branch to the member's fork and nowhere else. The agent's commits arrive as it made them.
3. **Opening the PR is an apply.** `factory apply --action open-pr` opens it from the fork's branch, with the caller's token, as a draft. GitHub is the draft, as for review.
4. **The follow-ups are revises** of the fix, asked into its session. Each ends with the same push, to the same branch, so the PR follows.
5. **The overseer is left as it is.** `factory fix`, every `factory pr` subcommand, their scripts and prompts, and the watch's dispatcher keep working exactly as they do today. (Superseded: the overseer runs this recipe since #1883, and the classic commands are deleted, #1884.)

## The design

### The `fix` recipe

`factory recipe fix --url <issue|PR> [--instruction …] [--plan <markdown>] [--apply]`, built in (`pkg/recipe/recipes/fix.yaml`):

- **`task-type: fix`**, in the issue's sandbox (`fix-<repo>-<n>`), where triage and plan run and the approved plan is left. Its run is recorded under `fix-run`.
- **Credentials: full**, as plan's are in the same sandbox: the agent reads the issue, its comments and linked PRs with `gh`. It is told not to push, comment or open PRs, but the token is in the sandbox. The issue's sandbox going token-free is a later step (see [Not planned here](#not-planned-here)).
- **Start:**
  - `setup-git`, then **`setup-fork`** (new): clone, and make `origin` the member's fork and `upstream` the repository. It fails if the fork cannot be made (the repository forbids forks, or the member cannot fork). This is `fix_issue.sh`'s `ensureForkRemote`. A fix never falls back to pushing to the repository.
  - `checkout-default-branch`, then a branch `issue-<n>-<unix>`.
  - `configure-engine`.
  - With a `plan` input, the approved plan goes into the first ask. The input is declared `from: Plan`: it is what a Plan's `run: fix` fills (below).
  - An ask that fixes the issue and commits, with `fix_issue.txt`'s rules minus the push and `gh pr create`.
  - An ask that writes the PR's title and body (with `Fixes #<n>`), captured to `change.yaml`.
  - **`push`** (new): see below.
- **The task output is a `Change`** (below), with actions `open-pr`, `post-replies`, `reject`, and one per revise. (Today: `edit`, `open-pr`, `reject`; `post-replies` is care's.)

`fix_issue.txt`, `iterate.txt`, `address_feedback.txt` and `investigate_failures.txt` stay for the overseer (deleted since, with the classic tasks). The recipe's prompts are copies, cut down to what a turn in a running conversation needs.

### `push`, a named step

A new `uses` step, like `clone`: factory's code, not the recipe's or the workspace's.

- It pushes `HEAD` to `refs/heads/<branch>` on **the member's fork**: `origin`, checked again to be a fork of the repository owned by the token's login. It refuses any other remote.
- A start pushes a new branch. A revise pushes to the branch the start recorded, `--force-with-lease` against the head it last pushed, so a commit someone else pushed to the PR in between is never overwritten. The revise fails instead and says so.
- It records the branch, the base and the pushed head in the task directory, where the runner puts them on the `Change`.
- It runs with the repository's hooks off (`core.hooksPath=/dev/null`) and the checkout's git config reset, as `resetRepoGitConfig` does today.
- Nothing to push (the agent made no commit) is not an error for a revise: an answer that changes nothing is a valid answer to a comment. For a start it is an answer too, when the agent says why: it makes no commit and writes the Change's `comment` (already fixed, not a bug, a duplicate, a maintainer's decision); push.json records the start `unchanged`, and open-pr posts the comment on the issue instead of opening a PR. No commit and no comment fails the task: "the fix made no change, and its Change says nothing about why".

### The Change kind

```yaml
kind: Change
target:
  url: https://github.com/o/r/issues/1681    # the PR's URL once one is open
  commit: 9a0c…                              # the head pushed
spec:
  fork: barney-s/r
  branch: issue-1681-1791260169
  base: 5e1e…                                # the upstream commit branched from
  title: "repo-agent: …"
  body: |
    …
    Fixes #1681
  labels: [factory]
  replies:                                   # address-comments only
    - inReplyTo: 2380012345                  # a review comment or an issue comment
      body: Done — moved to ensureForkRemote.
  report: |                                  # fix-ci only
    The e2e failed on …; fixed by …
```

The runner fills `target.commit`, `fork`, `branch` and `base` from what `push` recorded, not from the agent.

**Default actions:**

- **`open-pr`** (*Open draft PR*): opens `fork:branch` → the default branch, as a draft, with the title, body and labels, with the caller's token. It then aliases the sandbox to the PR: the `factory.gemini.google.com/pr` label, and the `pr` and `htmlURL` annotations, which the board and the watch already find a fix's PR by. If the branch already has an open PR, it aliases to that one and opens nothing. A retried apply never opens two PRs.
- **`post-replies`**: posts `replies` as answers to the comments they name, and `report` as a PR comment, with the disclose footer when disclosure is on. It dedupes by a task marker in each body, as `post-review` does.
- **`edit`** (title and body, before `open-pr`), **`reject`**.

`post-replies` on a start's Change posts nothing; there are no replies.

### The revises

Superseded: the last three moved to care (#1805, now `care-comments`, `care-ci`, `care-rebase`), and `iterate` was deleted with revise (#1938; `care-iterate` on the open PR).

Each is asked into the fix's session, after a *Continue session* or without one, and ends with `push` and a new `Change`:

| Revise | Label | Replaces | The ask |
|---|---|---|---|
| `iterate` | Iterate | `pr iterate` | change the PR as the input says (`--input instruction=…`) |
| `address-comments` | Address comments | `pr address-comments` | read the comments and reviews since the last push (`gh pr view --comments`, `gh api …/comments`), change what they ask, write a reply to each |
| `fix-ci` | Fix CI | `pr investigate` | read the failed checks and their logs (`gh pr checks`, `gh run view --log-failed`), fix, write a report |
| `rebase` | Rebase | the overseer's conflict-resolving `pr iterate` | rebase onto the default branch, resolve conflicts |

- The revises read GitHub with `gh`, as plan's start does. That needs the token in the sandbox. A token-free sandbox would hand them what factory fetched instead (see [Not planned here](#not-planned-here)).
- `rebase` force-pushes, since it rewrites the branch. The lease still guards against another pusher.
- Only `iterate` takes an input. `factory recipe revise` gains `--input`, as a start has.

### A PR factory did not open

Superseded: care's recipes are `on: [my-pr]` and run in the PR's fix sandbox ([care-recipe.md](care-recipe.md)); fix is `on: [issue]`.

`factory recipe fix --url <PR>` starts a fix on an existing PR, for a PR the member opened by hand.

- It runs in the PR's labelled sandbox, or else in `fix-<repo>-pr-<n>`, labelled with the PR.
- `setup-fork` checks the PR out instead of branching. **It refuses a PR whose head is not on the member's fork**, whether that is a branch of the repository itself or of somebody else's fork: "adopt it first" (`factory pr adopt`). This matches the fork rule above.
- The ask is the instruction, as `iterate`'s. There is nothing to open, so the Change has no `open-pr`.

### `factory pr watch`

Superseded: the watch runs `care-ci` and `care-comments` ([care-recipe.md](care-recipe.md#auto-on-any-pr-of-yours), [recipe-sessions.md](recipe-sessions.md)).

It stays, and the board keeps launching it for Auto. It learns one thing. When the PR's sandbox records a fix run (`fix-run`), it does not run `pr investigate` or `pr address-comments`. Instead:

- On failing checks it runs the `fix-ci` revise, then applies `post-replies`.
- On new comments or reviews it runs the `address-comments` revise, then applies `post-replies`.

Its triggers (a new head with failures, 30 minutes since the last attempt, comments since the last commit), its end on merge or close, and everything it does for a sandbox without a fix run are unchanged. `factory fix --watch` is one of those.

### `run:fix`

The Plan kind's *Fix with this plan* (`factory apply --action run:fix`) starts the `fix` recipe with the plan, as edited, as its `plan` input. The hand-off is declared by the consumer: fix's input `plan: {from: Plan}` says it takes a Plan's result; the producer's action `run: fix` only names the follow-up, and a recipe refuses a `run:` of a recipe with no input from its kind. No file in the sandbox carries the plan to the fix. The overseer does not use `run:fix`.

## Board (repo-agent)

As built in #1773. Since #1950 and #1954 a Fix click, Fix with this plan and auto-fix are ordinary recipe Requests on the generic path (`ensureRecipe`/`settleRecipe`, run name `request/<uid>`), and the PR row's follow-ups are care's recipes.

- **Fix.**
  - `ensureFix` runs `factory recipe fix --run-name fix/<board>/<n>/<unix>`, with the board's image, disk size, engine, disclose, and the draft-PR policy. It is recorded under `fix-run`, and a restarted controller follows the recorded run by name, as for plan and review.
  - When the run ends, the controller applies `open-pr` as the executor, in the same runner invocation, as review applies `post-review`.
  - Fix with this plan, Fix again and assigned-issue auto-fix all go through this path.
- **Follow-ups.**
  - Iterate, Address comments and Fix CI file sandbox-keyed `revise` Requests, and the controller applies `post-replies` when each ends.
  - `startPRTask`, the `*-requested-at` annotations, `ensurePRTaskClaims` and `ensurePRTaskClicks` go.
  - A PR with no fix run (opened by hand) gets a fix started on it, as above, instead of a `factory-pr-*` sandbox.
- **Auto.** Unchanged: `followUpPRs` launches `factory pr watch`, which now revises.
- **UI.**
  - The issue row gets a fix session: *watch* while it runs, *Continue session* after, the revises as buttons, as plan's, research's and review's.
  - The PR row's follow-up buttons come from the run's recorded revises.
- **What the board reads stays.** The PR label, `pr` and `htmlURL` are what `open-pr` writes. The `fix` task type also matches the prober's `fix` prefix, which `fix-issue` never did.

## Steps

1. **factory.**
   - The `fix` recipe's start, `setup-fork` and `push`.
   - The Change kind, `open-pr`, `edit` and `reject`.
   - `run:fix` on the recipe.
2. **factory.**
   - The revises.
   - `post-replies`, and revise inputs.
   - The PR mode.
   - `factory pr watch` revising a fix run.
3. **repo-agent.** The fix, the follow-ups and the UI, as above. The board's classic fix and PR-task paths are deleted.
4. **Verify in-cluster on a g4k issue:**
   - fix → draft PR from the fork;
   - Iterate, then a review comment answered by Address comments, then a failing check fixed by Fix CI, each a commit on the PR;
   - the controller deleted mid-fix resumes the run;
   - Auto picks up a new comment on its own.

## As built

Step 1 deviates from the above where:

- **The branch is a `run` step** in `fix.yaml`, after `checkout-default-branch`, not part of `setup-fork`. It writes the branch and the base to `$TASK_DIR/branch` and `$TASK_DIR/base`, which `push` reads; `uses` steps now get `TASK_DIR`.
- **`push` records `push.json`** (fork, branch, base, head) in the task directory. Its lease comes from `$TASK_DIR/lease`; nothing writes that until the revises (step 2), so a start's push requires the branch not to exist. It pushes to the fork's URL as the API names it, not to the remote `origin`, so a `pushurl` left in the checkout cannot redirect it.
- **Disclose** reaches the prompts as a standard input, `disclose` (`--disclose`). `open-pr` adds no footer, as the other verbs do not.
- **Labels** are a recipe input, `--labels a,b`, which the runner adds to `spec.labels`.
- **`open-pr` points the document's target at the PR**; `factory apply` (and `--apply`) then aliases the sandbox: the one the run used, else `source.sandbox`, else the issue's.

Step 2 deviates where:

- **A revise is handed the push** as inputs (`pushed_fork`, `pushed_branch`, `pushed_base`, `pushed_head`): `factory recipe revise` reads `push.json` of the newest task in the session that pushed. The revise's first step writes `$TASK_DIR/branch`, `base` and `lease` from them, and checks the branch out. With a lease, a head that did not move pushes nothing new and is not an error. `rebase` writes the new base (upstream's `HEAD`) before its ask.
- **The PR comes from the sandbox's alias**: `pr_url` is the `htmlURL` annotation when it is a PR, else the `pr` annotation. A revise's Change targets it; `address-comments` and `fix-ci` fail without it ("open it first"), `iterate` and `rebase` do not need it.
- **Title and body**: a revise's agent writes `change: {}` unless they no longer fit; the runner fills the previous Change's (`pushed_title`, `pushed_body`). A Change with no title is still refused when it is used (`ChangeSpec`), not when it is wrapped.
- **Revise inputs are marked `revise: true`** (`instruction`, `pr_url`): a built-in's command has no flag for them, and `ForSandbox` drops the mark. `factory recipe revise --input` overrides the *start's* inputs, read from the session's first task, so one revise's input is not the next's.
- **`post-replies` adds no disclose footer**, as no other verb does: the asks say whether to state that an agent wrote the replies (the `disclose` input). Each reply carries a marker of the task and the comment it answers (`reply=<id>`); the report carries the task's. It refuses a comment that is not on the PR. A Change with no replies or report posts nothing, wherever it points.
- **`factory pr watch`** looks for the PR's labelled sandbox with a `fix-run` each poll. With one, failing checks run `fix-ci` and new comments `address-comments`, each followed by `post-replies` (`factory recipe revise` + `factory apply --action post-replies`, in-process), and comments carrying factory's task-output marker are not new feedback. Without one, nothing changed. Which comments are new changed after; see [Auto's feedback check](#autos-feedback-check).

Step 3 (repo-agent) deviates where:

- **The draft-PR policy is not passed.** `open-pr` always opens a draft. The board's `policy.draftPR: false` is ignored for now: the PR is opened as a draft and the member promotes it. The draft-PR instruction the board used to append is gone. (`policy.draftPR` was removed in #1965.)
- **The fix's result is kept on the sandbox** (until #1954, which deleted `fix-harvested-at` and `fix-error`; a run's output and failure are any recipe run's now). `fix-harvested-at` is when the controller last read a fix's result, and `fix-error` is why it failed. A recorded `fix/<board>/<n>/…` run that started after both `fix-harvested-at` and the last Fix again has not been read. A restarted controller follows it by name, so the runner applies `open-pr` to it. A revise recorded under `fix-run` is never followed as a fix: its name is `revise/…`.
- **Follow-ups are sandbox-keyed `revise` Requests** (gone since #1940; care's recipes replace them) (`spec.sandbox`, `spec.revise`, with Iterate's `spec.instruction` as `--input instruction=…`), filed through `POST /api/task-sessions/:sandbox/:task/revise {revise, inputs}`.
  - They are offered once the fix has a PR (the sandbox's `htmlURL` is a `/pull/` URL) and while the sandbox is idle.
  - Only one stands at a time per sandbox, since they all push to one branch.
  - factory does not record a revise's inputs, so the board knows Iterate's (`WorkAction.inputs`).
  - The session view prompts for inputs. The PR row's drawer has a text box for them.
- **The `iterate`, `address` and `investigate` verbs are gone** from the Request CRD, along with `/prs/:id/{iterate,address-comments,investigate}`, the `*-requested-at` annotations, `ensurePRTaskClaims`/`ensurePRTaskClicks`, and factorycli's `StartIterate`/`StartAddressComments`/`StartInvestigate`.
- **A PR with no fix run gets no follow-up buttons.** On a PR of yours, factory can run a revise with no fix run (the PR mode, below), but the board does not offer it yet; `recipe list`'s `revisesOn` is what it will read. Auto (`factory pr watch`) still works on it as before.
- **The PR row's stage comes from the recorded run** (stages were removed by [recipe-driven-board.md](recipe-driven-board.md), #1792). A board revise is named by its id: `address-comments` → addressing, `fix-ci` → investigating, anything else → iterating. The watch's revises carry no run name and read as iterating. The fix itself reads as fixing.
- **A fix session has no draft panel.** Its draft is the PR. *Continue session* is on the PR row's drawer and on a failed fix's error.

The PR mode (deleted by [care-recipe.md](care-recipe.md)) deviated where:

- **On a PR of yours, fix has no start; it has only its revises.** On your PR, the PR already is what fix's start makes. `factory recipe fix --url <PR> --revise <revise>` (with `--input` for the revise) runs a revise straight away. `fix` is `on: [issue, my-pr]`, and the fork-ownership check is `checkOn`'s `my-pr` rule, which points at `factory pr adopt`.
- **Two optional recipe fields express it.**
  - **`setup:`** prepares the workspace and asks nothing. fix's is setup-git, setup-fork and configure-engine.
  - **`start.on`** is where the start runs; fix's is `[issue]`.
  - When both are unset, nothing changes, so the other recipes are as they were.
  - factory folds `setup` into whichever part runs first: into the start (`ForSandbox`), or where the start does not run, into the first revise (`SetupBeforeRevise`). It also drops `start.on`, so a runner sees neither.
- **The first revise opens the session.** It has no `Session`, so it starts the conversation as a start would, and its context goes first, since the session is fresh. The revises after it continue its session (`liveRun` finds the recipe's last run that did not fail), so the daemon and its revise tokens are unchanged. A revise input the first revise was given is reset for the next.
- **The PR stands in for the start's push.** factory sets the first revise's `pushed_*` from the PR:
  - the fork and branch from its head;
  - the lease from its head commit;
  - the base from the compare API's merge base;
  - the title and body from the PR's.
  The revise's checkout step fetches the branch from the fork when it is not there yet. Its push records `push.json`, which the next revise reads, as after a fix. `open-pr` on its Change finds the open PR and opens nothing.
- **It runs in the PR's recipe sandbox**, `recipe-<repo>-<n>`, not in a `fix-<repo>-pr-<n>` of its own. That sandbox's `htmlURL` is the PR already, so the later revises get `pr_url` as they do on a fix's PR.
- **The revises name no issue.** They ask to keep the description's `Fixes #…` line "if it has one", so they render on a PR's inputs. `CheckRender` leaves out the start on a target it does not run on.
- **`factory recipe list` reports it** as `revisesOn: [my-pr]`, the targets where the recipe has no start. A board offers the revises there before any run.
- **Auto does not see it yet.** `factory pr watch` finds a fix run only on the PR's labelled sandbox, and `recipe-<repo>-<n>` is unlabelled so that `factory pr` never adopts it.

## Auto's feedback check

Tried on PR 1774, Auto did not pick up the comments made on it. `factory pr watch` asked "is there new feedback?" its own way:

- It read only the conversation comments. Reviews and comments on the code went unseen.
- It counted a comment only if it was newer than the last commit. A Fix CI commit after a comment hid it.
- What it had handled was a time in memory, lost when the board relaunched the watch, every 10 minutes.
- The revise then read "everything since the last push" by itself, so it and the watch could disagree about what was new.

The overseer's watch (`watch/prs`) solved all of this long ago: all three kinds of feedback, inline comments timed from their review, approvals, `/lgtm` and the ignore prefix skipped, and what was handled recorded as reactions on GitHub. The fix reuses it instead of a second copy.

- **One check, two callers.** `watch/feedback` is the overseer's check for one PR, moved out of `watch/prs`: `Fetch` (the PR's commits, comments, reviews and inline comments), `Pending` (what still needs answering) and `React`. The overseer's `Scanner.evaluate` calls it with the policy it always had, and its behaviour does not change. `factory pr watch` calls it for a fix run.
- **Whose words count** is a `Policy`. The overseer ignores its own login and the PR's author (its bots). On a fix's PR, factory posts as the member and the member reviews their own PR, so the fix's policy counts the member and the PR's author. It skips what factory posted from a task output (the task-output marker) and bots.
- **No time gate.** A comment counts until its reaction says it was handled, whenever it was made.
- **The reactions are the member's.** factory reacts with the member's token, so the reaction interpreter is bound to the token's login (`GET /user`). Only the member's 👀 and 👍 mean "handled"; a reviewer's 👀 does not.
- **The revise is handed the list.** For `address-comments`, `factory recipe revise` computes what is pending and passes it as the `feedback` input (JSON; each body cut at 4000 characters, the whole at 64 KiB, the rest left for the next round). The revise writes it to `$TASK_DIR/comments.json` and the ask lists those comments instead of telling the agent to read everything. The runner names them on the Change (`spec.feedback`: kind, id, node id), from the file and not from the agent.
- **👀 when handed, 👍 when answered.** The pending comments get 👀 as the revise is handed to the sandbox, so the next poll does not hand them again. `post-replies` adds 👍 to each one named on the Change, after posting. A review summary has no thread, so it is answered in the report and still gets its 👍.
- **Same path for the board's button.** *Address comments* goes through the same `factory recipe revise`, so it is handed the same list and leaves the same reactions. With nothing pending, the button still runs, and the agent reads the PR itself, as before.
- **pr watch on a fix run** revises `address-comments` when anything is pending, and does not look at comment times. On failing checks it runs `fix-ci` as before. Without a fix run, pr watch is unchanged.

Limits, as built:

- A revise that fails, or fails to start, leaves 👀 and no 👍. The comments are not handed again by themselves; press *Address comments* to run it again. The overseer's 😕 and bounded retries are not copied.
- 🚀 does not reopen a comment on a fix's PR. The interpreter counts the member's reactions as the watcher's, and on a 👍 a rocket never counts. Use the button.

Not done here, for later: a conflict running `rebase`, a merge-queue or stop label, a limit on Fix CI attempts, and Auto reviewing the PR.

## Not planned here

- **The overseer.** Per the decision above, it stays on `factory fix` and `factory pr *`. (Done later anyway: #1883, #1884.)
- **A token-free issue sandbox.** Triage, plan and fix share `fix-<repo>-<n>`, and all hold the token. Making the agent token-free there needs all three moved together:
  - plan and triage on `credentials: clone`;
  - the revises' GitHub context fetched by factory and handed in;
  - `push` the only step after the clone that is handed the token, after every process the session left running is killed.
- **Local-only fixes.** A repository the member cannot fork gets no fix. `--runbook`'s local-only mode (#1704) stays for runbooks.
