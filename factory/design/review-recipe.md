# Review as a recipe

**Status:** Phase 1 (factory) is in this change: the `review` recipe, the Review kind and `post-review`.

A PR review is the last agent task the board runs on the old mechanism. `factory pr review` renders `structured_review.txt`, runs `review.sh` with the full token, and then either posts a review itself (`--publish yes|draft`) or prints it between stdout markers, which repo-agent greps and keeps as a draft on the board. Triage, plan and research already run as recipes: a task session in the sandbox's daemon, a typed task output, actions that whoever shows the result builds its controls from, and `factory apply` doing the writes with the caller's token.

This note moves review onto recipes too, on two decisions:

1. **The draft is GitHub's pending review.** That is where a review is easiest to reason about: on the diff, next to the code, with GitHub's own controls for editing, deleting and submitting comments. The board only gets the review onto GitHub; it does not keep a draft of its own.
2. **The overseer is left as it is.** The watch's reviews, `factory pr review`, `review.sh`, `structured_review.*` and `EnsureReviewSandbox` keep working exactly as they do today. Nothing here changes what they do. There is no factory or overseer dependency on repo-agent.

## The design

### The `review` recipe

`factory recipe review --url <PR> [--instruction …] [--apply]`, built in (`pkg/recipe/recipes/review.yaml`):

- **`credentials: clone`.** The PR's title, body and code are anyone's, and the session runs with nobody approving its tools. The token therefore reaches the clone and nothing else, as research's does: no `setup-git`, no `gh` auth, nothing on disk.
- **The clone fetches the PR.** With `PR_NUMBER` and `PR_BASE` set, `cloneRepo` (lib.sh) also fetches `refs/pull/<n>/head` and the base branch from `CLONE_URL` into `refs/factory/pr/{head,base}`, and checks the head out, detached. The agent reviews with `git diff refs/factory/pr/base...HEAD`, the PR's diff as GitHub shows it, without `gh`.
- **Start:** clone, configure-engine, an ask that investigates, and an ask that writes the review as YAML, captured to `review.yaml`.
- **Revise:** `review`, labelled *Update review*. After a *Continue session*, it rewrites the review from the conversation, in the same format. The revise has no token, so it does not re-fetch. Its result stays pinned to the commit the start reviewed.
- **No `task-type`.** It runs as `recipe-review`, the main task of its own sandbox (see below).

The prompt comes from `structured_review.txt`. It drops the parts that contradicted each other (`file`/`comment` in the instructions, `path`/`body` in the schema) and the diff-URL fetching, which needs network access to GitHub.

### Its sandbox: `review-<repo>-<n>`

A PR's recipes ran in `recipe-<repo>-<n>`. A `credentials: clone` recipe now gets a sandbox of its own, named after the recipe: `review-<repo>-<n>` (`RecipeSandboxName`), as research's are `rsch-…`. `setup-git` writes the token into gh's `hosts.yml` on the workspace volume. Once any recipe holding the token has run in a sandbox, that sandbox can no longer promise an agent without one.

The PR's legacy review sandbox (`factory-pr-<repo>-<n>`) is ruled out for the same reason. Its review and fix tasks run `setup-git`. It is also the watch's, with `last-task-type` meaning what the watch and board read it as. The new sandbox carries no `factory.gemini.google.com/pr` label, so `EnsureReviewSandbox` never adopts it either.

The same reasoning refuses a `credentials: clone` recipe on an issue: an issue's sandbox is shared with its fix.

### The Review kind

```yaml
kind: Review
target:
  url: https://github.com/o/r/pull/5
  commit: 3f2a…            # the head reviewed
spec:
  body: |
    Short summary.
  comments:
    - path: pkg/a.go
      line: 42
      side: RIGHT           # LEFT for a removed line
      start_line: 40        # optional: a range, within one hunk
      start_side: RIGHT
      severity: HIGH        # LOW | MEDIUM | HIGH | CRITICAL
      body: What is wrong and what to do.
```

- **Parsing.** `review:` YAML, cleaned the way triage's is. Sides are normalized to `RIGHT`/`LEFT`, since GitHub rejects a whole review over `"RIGHT\n"`. A review with neither a body nor comments fails the task.
- **`target.commit`** is new on `Target`, empty for every other kind. The runner (`writeTaskOutput`) sets it from `refs/factory/pr/head`, not from `HEAD`, which the agent may have moved. A Review wrapped without it, by the client for a sandbox image that predates the kind, cannot be posted. The fix is to run the review again on a newer image; there is no fallback.
- **Default actions:** `edit` (spec, as YAML), `post-review` (*Post as pending review*), `reject`. The recipe adds `revise: review`. There is no `comment`: the review goes up as a review.

### `post-review`

An apply verb, run with the caller's token like `comment` and `push-notes`. It turns the Review into the caller's **pending** review on the PR:

1. **Refuse** a Review with no commit, and a PR that is not open.
2. **Dedupe.** If one of the caller's submitted reviews carries this task's marker (`<!-- factory:task-output kind=Review task=… -->`), it says so and posts nothing. A retried apply, or a second click, never posts the review twice.
3. **Replace or refuse.** GitHub keeps one pending review per person per PR.
   - If the caller's pending review carries any Review marker, factory posted it, and it is deleted and replaced.
   - If the pending review has no marker, the caller started it themselves. post-review refuses and touches nothing: "submit or discard it on GitHub first".
   - Replacing drops anything the caller added to factory's pending review on GitHub. Editing happens either before posting (`edit`) or on GitHub after it, and posting again starts over.
4. **Place the comments.**
   - It reads the diff of the commit against the base branch (`CompareCommits base...commit`), which is GitHub's diff of the PR at that commit. Each hunk's lines are taken per side.
   - A comment GitHub would refuse is folded into the body as a ``- `path:line`: …`` list, rather than failing the whole review. That covers a line outside the diff, a range across hunks, a file not in the PR, and an empty body.
   - Severity leads the comment (`**HIGH**: …`).
5. **Post at the pinned commit.** It calls `CreateReview` with `commit_id` set to the commit and no event, so the review stays pending. If the PR moved on since the review, it says so. GitHub shows the comments on lines that changed since as outdated, and the review is still of what it reviewed.

Submitting, as Comment, Approve or Request changes, stays on GitHub with the caller.

It does not refuse when the caller has already submitted a different review of the same commit. A second opinion is the caller's call, and the dedupe above already covers a re-post of the same task.

## Steps

1. **factory (this change).**
   - The `review` recipe.
   - The PR mode of `cloneRepo`, and `PR_BASE` in a PR recipe's environment.
   - `Target.Commit` and the runner pinning it.
   - The Review kind and `post-review` in the registry and in `factory apply`.
   - `review-<repo>-<n>` (a `credentials: clone` recipe's own sandbox), and the refusal of `credentials: clone` on an issue.
2. **repo-agent.** The board's review moves to the recipe:
   - `ensureReview` runs `factory recipe review` instead of `factory pr review --publish no`. The stdout-marker grep and the board's review draft go.
   - The row's actions come from the Review output: *Post as pending review* files an apply Request, which the controller runs as `factory apply --action post-review`. Permissions are those of the PR's commenters, since the review is the caller's own.
   - *Continue session* and *Update review* use the generic session view (#1766): the run is recorded with its `revises`.
   - The board's ignore-files and review instructions become `--instruction`s.
   - Auto-posting, if a board wants it, is the controller applying `post-review` when the task ends. The review still lands as pending, never submitted.
3. **overseer.** Not planned: per the decision above it stays on `factory pr review`.

## Not done

- `CompareCommits` lists at most 300 files. On a larger PR, comments on files past that are folded into the body rather than placed.
- The recipe does not refuse a URL that is not a PR. On an issue the clone credentials refuse it, and on a repository the prompt's PR inputs fail to render, before a sandbox is made.
