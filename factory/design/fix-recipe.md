# Fix as a recipe

**Status:** Proposed.

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
5. **The overseer is left as it is.** `factory fix`, every `factory pr` subcommand, their scripts and prompts, and the watch's dispatcher keep working exactly as they do today.

## The design

### The `fix` recipe

`factory recipe fix --url <issue|PR> [--instruction …] [--with-plan] [--apply]`, built in (`pkg/recipe/recipes/fix.yaml`):

- **`task-type: fix`**, in the issue's sandbox (`fix-<repo>-<n>`), where triage and plan run and the approved plan is left. Its run is recorded under `fix-run`.
- **Credentials: full**, as plan's are in the same sandbox: the agent reads the issue, its comments and linked PRs with `gh`. It is told not to push, comment or open PRs, but the token is in the sandbox. The issue's sandbox going token-free is a later step (see [Not planned here](#not-planned-here)).
- **Start:**
  - `setup-git`, then **`setup-fork`** (new): clone, and make `origin` the member's fork and `upstream` the repository. It fails if the fork cannot be made (the repository forbids forks, or the member cannot fork). This is `fix_issue.sh`'s `ensureForkRemote`. A fix never falls back to pushing to the repository.
  - `checkout-default-branch`, then a branch `issue-<n>-<unix>`.
  - `configure-engine`.
  - With `--with-plan`, the approved plan (`/workspaces/plan-issue-<n>.md`) goes into the first ask.
  - An ask that fixes the issue and commits, with `fix_issue.txt`'s rules minus the push and `gh pr create`.
  - An ask that writes the PR's title and body (with `Fixes #<n>`), captured to `change.yaml`.
  - **`push`** (new): see below.
- **The task output is a `Change`** (below), with actions `open-pr`, `post-replies`, `reject`, and one per revise.

`fix_issue.txt`, `iterate.txt`, `address_feedback.txt` and `investigate_failures.txt` stay for the overseer. The recipe's prompts are copies, cut down to what a turn in a running conversation needs.

### `push`, a named step

A new `uses` step, like `clone`: factory's code, not the recipe's or the workspace's.

- It pushes `HEAD` to `refs/heads/<branch>` on **the member's fork**: `origin`, checked again to be a fork of the repository owned by the token's login. It refuses any other remote.
- A start pushes a new branch. A revise pushes to the branch the start recorded, `--force-with-lease` against the head it last pushed, so a commit someone else pushed to the PR in between is never overwritten. The revise fails instead and says so.
- It records the branch, the base and the pushed head in the task directory, where the runner puts them on the `Change`.
- It runs with the repository's hooks off (`core.hooksPath=/dev/null`) and the checkout's git config reset, as `resetRepoGitConfig` does today.
- Nothing to push (the agent made no commit) is not an error for a revise: an answer that changes nothing is a valid answer to a comment. For a start it fails the task: "the fix made no change".

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

`factory recipe fix --url <PR>` starts a fix on an existing PR, for a PR the member opened by hand.

- It runs in the PR's labelled sandbox, or else in `fix-<repo>-pr-<n>`, labelled with the PR.
- `setup-fork` checks the PR out instead of branching. **It refuses a PR whose head is not on the member's fork**, whether that is a branch of the repository itself or of somebody else's fork: "adopt it first" (`factory pr adopt`). This matches the fork rule above.
- The ask is the instruction, as `iterate`'s. There is nothing to open, so the Change has no `open-pr`.

### `factory pr watch`

It stays, and the board keeps launching it for Auto. It learns one thing. When the PR's sandbox records a fix run (`fix-run`), it does not run `pr investigate` or `pr address-comments`. Instead:

- On failing checks it runs the `fix-ci` revise, then applies `post-replies`.
- On new comments or reviews it runs the `address-comments` revise, then applies `post-replies`.

Its triggers (a new head with failures, 30 minutes since the last attempt, comments since the last commit), its end on merge or close, and everything it does for a sandbox without a fix run are unchanged. `factory fix --watch` is one of those.

### `run:fix`

The Plan kind's *Fix with this plan* (`factory apply --action run:fix`) starts the `fix` recipe with `--with-plan` instead of `factory fix`. It still writes the edited plan to `/workspaces/plan-issue-<n>.md` first. The overseer does not use `run:fix`.

## Board (repo-agent)

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

## Not planned here

- **The overseer.** Per the decision above, it stays on `factory fix` and `factory pr *`.
- **A token-free issue sandbox.** Triage, plan and fix share `fix-<repo>-<n>`, and all hold the token. Making the agent token-free there needs all three moved together:
  - plan and triage on `credentials: clone`;
  - the revises' GitHub context fetched by factory and handed in;
  - `push` the only step after the clone that is handed the token, after every process the session left running is killed.
- **Local-only fixes.** A repository the member cannot fork gets no fix. `--runbook`'s local-only mode (#1704) stays for runbooks.
