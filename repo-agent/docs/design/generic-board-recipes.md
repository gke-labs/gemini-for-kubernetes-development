# The board runs every recipe one way

Status: proposed.

The RepoBoard controller launches recipes two ways. Any recipe in the
catalog goes through the generic path (`recipes.go`: a recipe Request is
launched in the sandbox factory picks, with the Request's inputs, and its
task output is stored on the sandbox as its run's). Triage, plan, fix and
review have passes of their own (`hookedRecipes`, routed in
`requestMailbox`; `triage.go`, `plan.go`, `fix.go`, `ensureFix`,
`ensureReview`). They are older than recipes as data, and they are most
of the controller: their own run names, harvests, resumes, settle rules,
annotations and automation.

This note moves the four onto the generic path, with what they need that
it lacks as data, and deletes the passes.

## What the passes do that the generic path does not

**Nothing to migrate.** The passes already store their outputs under the
generic keys (`OutputAnnotation(RunAnnotation(<task-type>))`:
`recipe-triage-output`, `plan-output`, `fix-output`,
`recipe-review-output`) and run in the sandboxes the generic path picks
(`fix-<repo>-<n>` on an issue, `review-<repo>-<n>` for a `credentials:
clone` recipe on a PR).

**Dead code.** Nothing writes `plan-feedback`/`plan-feedback-at` (plan's
refine loop and `resumePlans`), `refix-requested-at` (read by
`resumeFixes`, `fixRunUnread`, `pauseFinished`), `agentDraft`/`agentState`
(read by `updateCounts`). Nothing reads `WorkItem.ReviewSession`, and the
controller ignores `policy.draftPR`.

**Bookkeeping the generic path does another way.** `triaged-at`,
`planned-at`, `fix-harvested-at`, `reviewed-at`, the per-pass harvests,
run names (`fix/<board>/<n>/<t>`, …) and resumes (`resumeTriages`,
`resumeFixes`, `resumeReviews`) exist because a hooked click settles
early: on the sandbox existing, or the run starting. A generic click has
its own run name (`request/<uid>`) and stays pending until its output is
stored, so a restart launches it again under the same name and factory
follows the run or returns its result.

**What is real:**

| | today | |
|---|---|---|
| apply on finish | fix `open-pr`, review `post-review` (pass options); care `post-replies` (`recipeApplies`, by kind) | keyed by kind, which cannot tell fix from care: both are Changes |
| timeout | fix 3h, review 45m, default 30m | |
| a busy sandbox | fix waits while any task runs in the issue sandbox (a plan, care) | the generic path launches, and factory refuses |
| failure shown on the row | `fix-error`, `review.gemini.google.com/error` → `WorkItem.Error` | the generic path fails the Request with the reason |
| retry | the passes retry every 30 minutes while a click stands | the generic path fails once |
| review's own pending review | not launched while the member has a pending review factory did not post (GitHub keeps one per person) | |
| claim on click | kickoff assigns the clicker to the issue (fix), requests them as reviewer (review) | |
| review state | `reviewState` pending → submitted, polled from GitHub; abandon deletes the pending review | the API already reads review states from GitHub (GraphQL) |
| automation | `auto.triage` (unclaimed, all), `auto.fix` (assigned), `auto.review` (requested, all), filtered by `auto.labels`, `excludeLabels`, `recencyDays`; run as the board's owner | |
| counts | `updateCounts` keys on triage output and fix sandboxes | |

## Proposal

### A recipe declares how it finishes

Two fields on a recipe, published in the catalog, so the controller needs
no table and a repository's recipe gets them too:

```yaml
timeout: 3h
task-output:
  kind: Change
  on-finish: open-pr   # the action a board's runner applies when the run ends
```

- **`task-output.on-finish`** is one of the output's actions, applied by
  whoever runs the recipe for a board when the run ends: fix `open-pr`,
  review `post-review`, care's recipes `post-replies`. The board's
  `recipeApplies` and the passes' `Apply` go. The overseer is unchanged:
  it passes its own `--apply --action`.
- **`timeout`**, default 30m: fix 3h, review 45m.

Triage's "factory's default image and disk" goes: it runs on the board's,
like every recipe in the issue's sandbox.

### The generic path learns the rest

- **A busy sandbox waits.** A recipe Request whose sandbox is running
  another task stays pending ("waiting for <task>") instead of launching
  into factory's refusal. This replaces fix's wait for a plan.
- **A failure is the run's.** A failed run fails its Request with the
  reason (as now), and the row's error is the item's newest run that
  failed, read from its recorded run and the Request's message, instead of
  `fix-error` and the review error annotations. No 30-minute retries: one
  click, one attempt, and the row offers the recipe again.
- **Guards by kind.** A recipe whose output is a Review is not launched
  while the member has a pending review on the PR that factory did not
  post: it could never be posted. The Request fails saying so.
- **Claims by kind, on click.** Kickoff assigns the clicker to the issue
  for a recipe on an issue whose output is a Change, and requests them as
  reviewer for a Review. Today's per-recipe checks, made general.
- **Review state from GitHub.** `reviewState` and `settleSubmittedReviews`
  go: the API reads pending and submitted reviews from GitHub already.
  Abandon becomes a Review draft's reject, which deletes the pending
  review factory posted.

### Automation files Requests

The auto settings stay board policy, but discovery files recipe Requests
instead of driving a pass, as `fileWatch` files watch Requests:

- `auto.triage`, `auto.fix`, `auto.review` find their items as now, with
  the same filters, and file a recipe Request per item, as the board's
  owner (member = the board's namespace).
- An item gets one only if it has **no recorded run of the recipe** (any
  state: a failed auto run is not retried, a member clicks), **no
  tombstone** (`RejectedAnnotation`, already written by every reject),
  and, for review, no review of the owner's on GitHub. A standing Request
  dedupes by its key until it settles; once it has, the run is recorded.

Then the four are ordinary recipes, and `hookedRecipes`, `settleHooked`,
the typed plans (`fixPlan`, `reviewPlan`, `triageClick`, `planRequest`),
`filterOnboarded`, the per-pass harvests and resumes go.

### What stays

`policy.autoIterate` (a fix's PR gets a watch Request), `limits.maxActive`,
the idle suspender, `stampEngine`/`stampUnpaused`. The overseer is
unchanged: the API reads its runs through the same `factorycli` helpers
and keys, which keep their shapes.

## Risks

- **In-flight runs at the cutover.** Hooked runs (`fix/…`, `review/…`,
  `plan/…`, `auto/…`) are not followed by the generic path. A run in
  flight when the controller rolls is not harvested; its sandbox is
  recreated or the recipe clicked again (no compatibility, as for
  recipes).
- **Auto runs as a Request.** An auto run is visible as a Request filed by
  the board's owner, and counts toward `maxActive` like a click.

## Steps

1. **Delete the dead code:** plan's refine loop and `resumePlans`,
   `refix-requested-at`, `agentDraft`/`agentState`,
   `WorkItem.ReviewSession` and `taskSession`. No behaviour changes.
2. **factory: `timeout` and `task-output.on-finish`,** in the recipe
   model and the catalog; the built-ins declare them (fix, review, care's
   recipes). repo-agent's generic path reads them, and `recipeApplies`
   goes.
3. **The generic path learns** the busy-sandbox wait, the Review guard,
   claims by kind, and row errors from failed runs.
4. **Clicks go generic:** a Triage, Plan, Fix or Review click is an
   ordinary recipe Request. `settleHooked`, the typed plans, the per-pass
   harvests and resumes, `triaged-at`, `planned-at`, `fix-harvested-at`,
   `fix-error`, the review annotations (`reviewState`, `reviewed-at`,
   error, abandoned-at, the re-review marker) and `filterOnboarded` go.
   The passes keep only automation.
5. **Automation files Requests,** and the passes and `hookedRecipes` go.
6. **Counts** (`updateCounts`, the board's needs-you) from run states:
   a ready draft or a failed run, for any recipe.

## Addendum: auto for any recipe

After step 5, automation is recipe-agnostic except for discovery.
Filing (`fileAutoRequests`), dedupe (`autoOwed`: no recorded run of the
recipe on the item, no rejected draft, no Request standing or recently
settled) and the guard (a Review recipe skips a PR with the owner's own
review) know no recipe by name, and a filed Request runs as a click's
does. What still names recipes is three discovery functions:
`discoverTriage` (triage on unclaimed or all issues), `discoverAssigned`
(fix on issues assigned to the owner), `discoverRequestedReviews` and
`discoverAllPRs` (review on PRs requesting the owner, or all).

**Auto rules.** `spec.auto` becomes a list of rules, one per recipe, with
the shared filters beside them:

```yaml
auto:
  rules:
    - recipe: triage
      on: issue
      when: {unclaimed: true}
    - recipe: fix
      on: issue
      when: {assignedToOwner: true}
    - recipe: review
      on: pr
      when: {reviewRequestedFromOwner: true}
    - recipe: summarize
      on: issue
      when: {labels: [needs-summary]}
  labels: []          # the shared filters, as now
  excludeLabels: []
  recencyDays: 7
```

- **Predicates** are a closed set, the ones discovery has now plus labels:
  `unclaimed` (no assignees), `assignedToOwner`,
  `reviewRequestedFromOwner`, `labels` (any of). No predicate is every
  open item of the rule's type, as `auto.triage: all` and
  `auto.review: all` are today.
- **One discovery** lists the board's open issues and PRs once per pass,
  checks each rule against each item, and files through
  `fileAutoRequests`. The three discovery functions go.
- **Validation:** a rule names a recipe in the catalog, and an `on` it
  starts on (`startsOn`): a my-pr recipe cannot be automated on others'
  PRs. A rule for a recipe the catalog no longer has is skipped and
  reported on the board's status.
- **The existing settings** (`auto.triage`, `auto.fix`, `auto.review`)
  are converted once, by the controller, into the equivalent rules:
  boards are live objects, so they are not dropped.
- **The board's settings panel** lists the rules, built from the catalog:
  a recipe, the item type and a condition, instead of three fixed selects.
  A rule for a recipe with an `onFinish` says so ("posts a pending review
  when it finishes"): automating a recipe that writes to GitHub on
  finish is allowed, as auto-fix and auto-review do now, but shown.

**Cost.** Every rule spends tokens as the owner. `limits.maxActive` caps
how many run at once, not how many a day; a per-rule daily cap
(`maxPerDay`) is part of the rule, unset meaning no cap.

**Not now:** the overseer launches recipes on labels too
(`applicable-when`). The two could share the predicate vocabulary later.

7. **Auto rules,** after step 6: the CRD's `auto.rules`, one discovery,
   the conversion of the existing settings, and the settings panel.

## Resolved

`policy.draftPR` was shown and edited on the board but read by nothing:
open-pr always opens a draft. It is deleted: a board's fixes open as
drafts, until a requirement says otherwise.
