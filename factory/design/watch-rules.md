# The watch loop as rules

Status: Phases 0 to 4 are built (October 2026): the rules engine decides
every issue and PR rule on every overseer, and the scanners only carry
out its decisions. Phase 5 (a repository's own rules) is partly built:
the watch reads a repository's `.agents/watch/` when it starts, if its
overseer lets the repository's `.agents/` in (`spec.repoRecipe.mode`
replace, read at its `gitCommit` from the overseer's clone), and lays it
over the prebaked rules (replace or toggle by name, or add); augment adds
rules of new names only. A rule of a new name is carried out as a rule
task (`task-pr-<n>-rule-<name>`, `task-issue-<n>-rule-<name>`): its
recipe, which may be the repository's own, or its writes. `factory watch
rules --dir` lays a checkout's rules over them for listing, explain and
replay. The rule
files' reference - every field, and how a rule is evaluated - is the
[watch rules guide](../docs/watch-rules.md). "Before the move" is the
watch as it was; "As built" is the watch now.

## As built

- **The rules.** Prebaked YAML files in `factory/pkg/watch/rules/`
  (embedded), one ordered list per kind: on issues workflow-missing,
  workflow, fix; on PRs inherit-labels, inactive, comments, conflict, ci,
  review, ready, ready-unreview. Scheduled chores (the chore scheduler,
  `chores.mode`) and the linked-issue nudge are code, not rules: the
  `chore` and `nudge` files only described them and nothing consulted
  them, so they were deleted. `when:` says which
  items a rule takes, `run:` the recipe (with `apply:` and `inputs:`) or
  the writes, `policy:` the pacing (cooldown, dedupe, give-up, phase).
- **The gates** are code (`rules.Gates`): stop label, closed,
  below-min-number, merge queue. A gated item is decided no further.
- **The scanners read facts and carry out decisions.** The PR and issue
  scanners build each item's `rules.Item` (`Scanner.Facts` for one item),
  ask the engine (`Set.Decide`; on issues the policy first on the cheap
  facts, `Set.Held`), and do what it decided: queue the task, stamped with
  the rule, recipe, apply, inputs and phase (`act.Task`), or make its
  writes - labels, assignees, a stop's label and comment
  (`act.ApplyWrites`). workflow-missing's stop keeps a handler, which posts
  the rule's comment with the definition's path and load error.
- **The dispatcher runs what the rule names.** Every rule's task is a rule
  task - type `pr-rule` or `issue-rule`, file `task-<kind>-<n>-rule-<rule>.yaml`,
  its run named `<rule>-<n>-<sha>-<enqueued>` and stamped when queued -
  whatever the rule; a workflow step or a chore is an `agent-chore`. The
  recipe, `--apply --action`, `--pr-labels` and inputs come from the task
  (`api.RecipeArgs`); the sandbox, from the recipe (`credentials: clone`
  has its own); which bot runs it, from the recipe's `as` (`SelectUser`).
  A watch upgraded from one with a type and a file name per rule
  (`pr-investigate`, `task-pr-<n>-investigate.yaml`, ...) renames and
  converts those tasks when it loads the queue, keeping the run name their
  old type gave them, and reads their journalled runs under the new names
  (`api.MigrateTask`, `api.MigratedTaskID`).
- **The decision at start.** Just before a task starts, `Redecide` reads
  the item's facts again and asks the engine, as if the task were not
  there: a gate, a moved head, or a rule that no longer decides drops it,
  not counted as an attempt. A nudged workflow step is held to the gates
  alone. A task without a stamp is stamped with the decision, or dropped
  when its item cannot be read.
- **Watching it.** `rules.Summary` tallies each rule's runs, give-ups and
  drops at start since the watch started, and checks every recorded
  decision against the engine (consistent); the queue API serves it as
  `rules` and the overseer page shows it. `spec.rulesRecord` writes each
  decision with its facts (`decisions.jsonl`, a file a day, kept a month)
  for `factory watch rules replay` and `explain --record`.
- **The operator's say.** `spec.rules.disabled` (`WATCH_RULES_DISABLED`):
  a disabled rule decides nothing and its queued tasks are dropped at
  start. It does not stop chores, fan-outs or running tasks; `prMode`,
  `issueMode` and `chores.mode` do, per loop.
- **No orphan adoption.** The PR scanner's adoption of a pool bot's PR
  opened without its labels, and the `adopt-pr` rule that described it,
  are gone: `open-pr` labels the PR it opens, tries the labels again when
  GitHub fails on its side, and a task requeued after a restart applies
  `open-pr` again, which labels the PR already open.

`factory watch` (the overseer) decides what to run by code: an issue
scanner, a PR scanner, a fan-out controller, a chores scheduler, a
sandbox reconciler, a dispatcher. Each has its conditions inline, in an
order, with its own dedupe. This note writes what it does as rules: a
GitHub condition and the action taken, the factory recipe it runs and as
whom, plus the policies every rule shares. Then it proposes running the
rules as data, so the loop is one engine and a repository can say what
its overseer does without a Go change, as `.agents/recipes` did for
recipes (design/repo-recipes.md).

## Before the move

### The loops

| loop | every | lists | decides |
|---|---|---|---|
| issue scanner | 2m fast, 10m sweep | fast: open issues assigned to the bot pool (one page, newest updated), and those the watcher's login opened; sweep: every open issue with the trigger label | fix, workflow, adoption |
| PR scanner | 2m fast or 10m sweep | fast: open PRs assigned to the pool, those changed since last evaluated; sweep: assigned and trigger-labelled, all pages | label inheritance, comments, conflict, CI, review, readiness, inactivity |
| fan-out | 5m, and woken when a child closes | open `*/fanout` issues | children, checkpoints, the spec proposal |
| chores | 30s (definitions every 5m) | `.agents/*.md` with `schedule:` | scheduled agents |
| sandbox reconciler | 30s, GC 5m | the namespace's sandboxes | task state, evicted pods, closed items, age, idle; the linked-issue nudge |
| warm workspace | 5m | the warm snapshot | the warm cycle |
| dispatcher | 30s | the queue | what starts, as whom |

Correctness rests on the queue files, GitHub (labels, reactions, the
bot's own comments) and the sandboxes' annotations, since the process is
restarted every few minutes (below).

### What it does, as rules

| rule | when | runs | as | dedupe, give-up |
|---|---|---|---|---|
| fix | issue with the trigger label; no open PR closes or links it; its body names no workflow; updated since its last fix | `recipe fix --apply --action open-pr --input draft=false`, in `fix-<repo>-N` | coder, pinned to the sandbox's user, then the assignee | task `task-issue-N`; 10m cooldown, 20m after a failure |
| workflow | as fix, but the body names a workflow (`.agents/…`, `mode: workflow`) | `agent create --agent <path> --session-id issue-N`, in `wf-issue-N` | agent, pinned likewise | the workflow's `cooldown:` (default 10m), 20m after a failure; no updated-since gate, so it re-runs each cooldown |
| adopt issue | issue the watcher's login opened, without the trigger label or a pool assignee | add the label; assign the target assignee | watcher | — |
| adopt PR | bot-authored PR without the trigger label or a pool assignee, whose referenced issue has it | copy the issue's labels | watcher | — |
| inherit labels | candidate PR | copy the labels of the issues it closes, but `ready-for-human` | watcher | each evaluation |
| comments | PR with trusted feedback after its last commit and last addressing, not yet 👀'd (or 🚀'd to redo); or the last comments run failed on this head | `recipe care-comments --apply --action post-replies`; 👀 on each comment, 👍 or 😕 after | the PR's author | task `task-pr-N-comments`; three tries per head, then the stop label and a pause comment |
| conflict | PR not mergeable | `recipe care-rebase --apply --action post-replies` | the PR's author | once per head |
| CI | PR with a failing check or status | `recipe care-ci --apply --action post-replies` | the PR's author | a new head, a failed last run, or 2h; three tries (counted in the bot's "started investigating" comments), then the stop label |
| review | PR green, no unresolved feedback, not approved, `*/review` on it (or on a closing issue), its head not reviewed by a pool or allowlisted bot | `recipe review --apply --action submit-review [--instruction …]`, in `review-<repo>-N` | reviewer | once per head |
| ready | PR open, not draft, mergeable, green, no feedback pending, no task, the reviewer bot not asking for changes | add `*/ready-for-human`, drop `*/review`, assign the closing issues' human assignees, unassign the bot | watcher | idempotent |
| inactive | PR with no trusted human activity for `prInactivityTimeout` | comment once; stop label | watcher | — |
| fan-out | `*/fanout` issue with a spec | create, rewrite or label children within the window; stop at a checkpoint; the final child; close | watcher | state in its progress comment |
| fan-out spec | `*/fanout` issue without a spec | `recipe fanout --apply` (outside the queue) | coder | one at a time per parent; 1h after a failure |
| chore | `.agents/*.md` whose `schedule:` is due | `agent create --agent <path>` on the repository | agent | `chores_state.json` |
| nudge | an issue's closure confirmed by the GC; an open issue it links runs a workflow and has its sandbox | the linked issue's workflow, past its cooldown | agent | the task's name only |
| stop | issue or PR with the stop label | drop its queued tasks; at dispatch, drop a task whose item closed or stopped | — | — |
| requires human | sandbox annotated `requires-human` | never enqueue; drop at dispatch | — | until a human clears it |
| GC | sandbox of a closed issue or PR; older than the eviction age; idle | harvest usage; delete, or scale to zero | — | the sandbox's lease |
| warm | no warm snapshot younger than the interval | step the warm cycle | coder | one at a time |

On a PR the rules are not independent: one is taken, comments before
conflict before CI before review, and none while a task of the PR runs;
readiness is what holds when none applies.

### The policies they share

- **Gates:** drain (an env var or a marker file); the stop label
  (`<trigger>/stop`), checked at scan and again at dispatch; `minNumber`;
  the issue, PR and chore modes; feedback from trusted authors only, and
  `/<trigger>-ignore` to opt out; PR work only on PRs the pool authored.
- **Dedupe**, outermost first: a task name per item and rule (a second
  enqueue is a no-op); one task per PR; the head SHA last handled, per
  rule; state on GitHub (reactions, labels, the bot's comments); state on
  the sandbox (task state, `requires-human`, the user who owns it).
- **Scheduling:** strict order by the `priority/*` label, then a phase
  per rule (comments and review 1, conflict 2, fix and CI 3, workflows
  and chores 4), then first come; one task per sandbox, under a lease;
  `--max-pending` and `--max-actions` (40 each). No fairness between
  items, users or rules.
- **Identity:** a role per rule (coder, agent, reviewer); a PR's tasks
  run as its author, who must be in the pool; an item is pinned to the
  bot whose sandbox it already has.

### Worth fixing whether or not this goes anywhere

- **Settings that do nothing:** `--poll-interval` and `--labels` are only
  printed; the Overseer's `reviewMode` never reaches the watcher, so
  review is gated by labels alone; `chores.include`/`exclude` are not
  passed on; a `dryrun` mode is not honoured (only `disabled` is).
- **`pollInterval` is the process's lifetime,** not a poll interval:
  `run.sh` runs `factory watch` for it (5m), pushes the queue to git and
  starts it again. Every in-memory gate resets each time (the PR fast
  pass's verdicts, the issue scanner's processed stamps, fan-out's
  proposal backoff, sandbox leases), so the 10-minute sweeps run every
  cycle.
- **No fairness:** the dispatcher calls its ordering fair share; it is
  strict priority, then phase, then first come.
- **Two definitions of "a bot reviewed it":** whether to review counts a
  review by any pool, allowlisted or ignored bot after the last commit;
  readiness counts only the reviewer role's (`IsBotReply` against
  `IsReviewerBot`).

## Proposal

### Rules, order, sources, plugins

The engine has four kinds of thing.

**Rules** are a condition and an action, on one kind of item, one rule
per file (see Where rules live):

```yaml
# .agents/watch/fix.yaml
name: fix
on: issue
when:
  labels: {any: ["{{trigger}}"]}
  linked-pr: none
  connected-pr: none
  workflow: none
run: {recipe: fix, apply: open-pr, inputs: {draft: "false"}}
policy: {dedupe: update, cooldown: {success: 10m, failure: 20m}, phase: 3}
```

**Order** makes the rules on one kind of item one list, and some of
them exclusive. Each rule says its place itself (`order`). A rule that
**takes the item** (`exclusive`; a recipe rule unless it says otherwise)
is decided only if none before it was and no task of the item runs: one
task at a time per item, in priority order, across every rule. A rule
that does not (label writes) decides on its own, and its writes are what
the rules after it see. 2a had named groups (`group: {name, order}`,
`last`, and `policy.before` to run ahead of one); with one task per item
wanted across every rule, a group was one implicit lane per kind, and a
second one only a way round it, so they are flattened into the list. PR
care is the exclusive part of the PR list:

```yaml
# .agents/watch/ci.yaml
name: ci
on: pr
order: 30      # inherit-labels 5, inactive 7, comments 10, conflict 20, ci 30, review 40, ready 50
when: {checks: failing}
run: {recipe: care-ci, apply: post-replies}
policy: {dedupe: head, give-up: {attempts: 3, since: human}}
```

**Sources** are what is listed and how often. A rule does not name one:
the engine derives the listings from the rules' `on` and `when` (see
One scan, events from diffs). The cluster's sandboxes and the
repository's `.agents/` are sources of their own, for the plugins and
the chores.

**Plugins** are the controllers that are not condition and action:
fan-out (a ramp of windows and groups, checkpoints, its state in a
comment), the warm workspace, and the sandbox GC. They keep their code;
the settings turn them on and configure them.

### One scan, events from diffs

Today each loop lists for itself, and the same queries run twice: the
issue scanner lists `issues?assignee=<bot>` per bot and
`issues?labels=<trigger>` and drops the PRs in them (the issues API
returns both); the PR scanner lists the same per-bot assignee queries
again and keeps the PRs, and its sweep lists the assigned and labelled
items once more, besides `pulls?state=open`. Fan-out lists its own
label.

The engine scans once:

- **The listings are the union of the rules' needs,** each distinct
  query once per pass: the trigger label (issues and PRs together), the
  assignee per bot, the open PRs (for head SHAs), the fan-out label.
  That roughly halves the listing calls.
- **They are incremental:** `since=<last scan>`, sorted by `updated`,
  returns what changed, usually a page or nothing, and a conditional
  request for an unchanged answer is a 304, which costs no quota.
- **Each item becomes a snapshot**: its labels, assignees, state,
  `updated_at` and, for a PR, its head SHA.
- **A diff against the last snapshot gives events:** `opened`,
  `closed`, `labeled`, `unlabeled`, `assigned`, `head-changed`,
  `updated`. Rules match events and state, so a rule may say `on: {pr:
  [head-changed]}` or `on: {issue: [labeled]}`, as a GitHub Actions
  workflow does, without webhooks or a GitHub App.
- **Details are fetched on demand,** for the items an event touched and
  a rule needs them for: a PR's checks, mergeability, review decision
  and recent comments in one GraphQL query for many PRs (its own budget,
  a few points for what is 4–6 REST calls per PR today); an issue's
  timeline (is a PR linked) when a fix rule would fire. An item that did
  not change costs its listing row.
- **The cursor, the snapshots and the HTTP cache are persisted.** Today
  the overseer restarts `factory watch` every five minutes and the ETag
  cache (#1961) is in memory, so every first pass after a restart pays
  for everything; persisted, a restart continues where it stopped. The
  cursor and snapshots (labels, assignees, states, SHAs) may go with the
  queue directory, which `run.sh` pushes to a branch of the repository;
  the HTTP cache may not, since it holds response bodies, private
  repositories' included: it goes on the overseer's volume, never to
  git.
- **A full sweep stays, as a safety net,** every so often: what a diff
  missed (a lost snapshot, a skipped pass) is found there, and today's
  split between a fast pass and a sweep becomes this.

Not everything bumps `updated_at`. A reaction does not (a human's 🚀 asking
for a redo), and neither does a change of a PR's check status. Checks
come from the GraphQL rollup of the open PRs each pass, which is cheap;
a signal carried only by a reaction is read by the sweep, or moves to a
label.

On a repository the size of KCC's (some 300 open labelled issues and
PRs, six bots), a quiet pass goes from hundreds of calls, most of them
details for every candidate PR, to a few dozen, mostly 304s, and a busy
pass costs about what changed.

**Risks of the scan:**

- **A lost snapshot must not fire everything.** A missing or reset
  snapshot (a new branch, a corrupt file) would make every item look
  `opened` or `labeled`. A missing snapshot is a baseline, not events:
  the first pass after a loss is a sweep, on state alone.
- **The overseer's own writes are subtracted, not looked up.** A listing
  shows a new label, not who added it, and asking the timeline per item
  would undo the savings. The engine records the changes it makes
  (labels, assignees it expects to see) and takes them out of the diff.
- **`since` has edges:** an item updated during a scan, or clock skew,
  could slip past. The cursor is the last scan's start, less a margin;
  an item seen twice is harmless (dedupe, preconditions).
- **A diff misses what comes and goes between passes** (a label added
  and removed). Rules decide on state, with events as hints, so a missed
  event delays an action until the sweep rather than losing it.
- **GraphQL has its own limits:** no ETags, its own hourly budget, a cap
  on nodes per query. Only the PRs an event touched are batched, beside
  the open PRs' check rollup, and conditional REST is the fallback.
- **Preconditions are calls too** (`head`, `no-linked-pr`): they reuse
  the current pass's snapshot when it is recent, and fetch only when it
  is not.

### Predicates

A closed set, each a named Go function, so a rule cannot reach anything
the engine does not offer. From today's rules:

| on | predicates |
|---|---|
| any item | `labels: {any, all, none}`; `author: pool`; `assignee: pool\|not-pool\|none`; `last-run: failed` (the rule's last run on the item, on its head when deduped by head) |
| issue | `linked-pr: none\|open`; `workflow: none\|any\|missing`; `opened-by: watcher`; `fanout` (a fan-out's parent; a child is an ordinary issue, created only when it is to be worked on) |
| PR | `checks: failing\|pending\|green`; `mergeable`; `feedback: new-trusted\|none`; `closes: {labels}` (the labels of the issues it closes, between them); `reviewed-at-head`; `approved`; `draft`; `inactive-for: <duration>` |
| sandbox | `item: closed`; `age: <duration>`; `idle: <duration>`; `requires-human` |
| any | `any-of: [<predicates>, …]` |

**No `from`.** Which items a rule sees is `on` (the kind) and `when`
(which of them), nothing else. The watch tracks a fixed set, from the
overseer's settings: open items with the trigger label, assigned to a
bot of the pool, or (issues) opened by the watcher's own login; a rule
sees every tracked item of its kind and narrows with `labels`,
`assignee`, `author`, `opened-by`. 2a-2a had a `from: [labelled,
assigned, opened-by-watcher]` naming the listings, but every prebaked
rule named its kind's whole tracked set, so it said nothing, and it was
a second, source-level way of saying what `when` says; it is gone. When
repository rules want items outside the tracked set (Phase 5), the
sources are derived from `on` and `when` (`labels: {any: [x]}` lists
label `x`), and a rule that narrows nothing is refused then. Adoption
is not a rule: an issue the watcher opened is tracked like a labelled
one, and the label and assignee adoption wrote are fix's own (the label
when queued, the coder at the claim).

**`any-of`** holds when one of its predicate sets does, beside the rest:
comments runs on new trusted feedback or a failed last run; ready's
review is satisfied three ways. One level deep.

**Placeholders** are a closed set the engine fills in, by where they
appear: in labels `{{trigger}}` and, writing to a PR,
`{{closing-issue-labels}}`; in assignees `{{watcher}}`, `{{pool}}` and,
on a PR, `{{closing-issue-assignees}}`; in an issue recipe's inputs
`{{pr-labels}}` (the trigger, the configuration's labels and the issue's,
as `--pr-labels`). A duration may name the overseer's
`{{pr-inactivity-timeout}}` or the workflow definition's
`{{workflow-cooldown}}`. Any other name is an error when the rule loads.

**`when` is the item, `policy` the overseer's pacing.** `when` says
what the item is and what has happened to it; how often the overseer
acts and when it stops trying is `policy`: `dedupe` (`head`, or
`update`: once per change to the item; one queued task per rule and item
is every rule's), `cooldown` and `give-up`. A
predicate about the overseer's own runs is in `when` only as a reason to
run (`last-run: failed`), never a limit. 2a had `attempts` in `when`
(which every rule using it duplicated in its give-up, checked first),
`updated-since-run` (now `dedupe: update`), and `stopped` and
`merge-queue` (engine gates already), all gone; and `author: trusted`
and `assignee: owner`, which no scanner recorded, are refused until one
does. 2a's `dedupe: item` (what every rule gets anyway), `identity-pin`
(nothing read it; a task always stays with the bot that owns the item's
sandbox, or a follow-up could not push) and `skip-cooldown` (an event is
a reason to act now: a rule `on: event` is not held back by another
rule's cooldown on the same task) are gone too.

`give-up` replaces counting the bot's own comments: the engine records
each run of a rule on an item (the queue's processed tasks already do)
and counts them since the head changed, a trusted human last spoke or
the watcher last put the item on hold (its comment attaching the stop
label, whatever its wording). The board's auto rules (repo-agent design
generic-board-recipes.md, step 7) use the same vocabulary for their
`when`: `unclaimed`, `assignedToOwner`, `reviewRequestedFromOwner`,
`labels`.

### Actions

- **`run`:** a recipe (built-in or the repository's), its inputs, and
  what is applied when it ends (`apply`, as `task-output.on-finish`
  does for the board). `agent create` becomes the `agent` or `workflow`
  recipe, which it already picks.
- **GitHub writes:** `label`, `unlabel`, `comment` (once, by marker),
  `react`, `assign`, `unassign`. PR adoption, inheritance, readiness,
  inactivity and give-up rules are these alone.
- **`stop`:** the stop label and a comment saying why, which is how
  every give-up ends today.
- **Cluster:** `delete`, `suspend`, `harvest` on sandboxes (GC).

**No `as` on a rule; the recipe says it.** A rule does not name the bot
it runs as: the role follows from what runs. 2a-1 had an `as` per rule
and it was dropped - nothing read it (the scanners' `selectUserForTask`
decided, by task type), two rules running one recipe as different roles
would be a mistake, and the operator, not a repository, decides which bot
does what. Once the watch was made generic the task-type mapping had to
go too, so (October 2026) the recipe declares it: `as:` `author` (the
PR's own bot, whose fork holds its branch: the care recipes), or a pool
of factory's roles, `coder`, `agent`, `reviewer`, pinned to the item (the
bot that owns its sandbox, then a pool bot assigned to it, then any).
Every built-in declares it; a repository's recipe must, and may say only
`author` or `coder` (any, in replace mode, which hands the repository
its recipes). Roles are factory's (its config's `roles:`, an overseer's
`spec.roles`); a runner without pools, like the board, does not read it.
GitHub writes are the watcher's.

### Policies

Every rule has defaults, overridable per rule:

```yaml
policy:
  dedupe: head          # once per head SHA; or update (once per change); one queued task per rule always
  cooldown:
    success: 10m
    failure: 20m
    reset-on: [head]          # a new head ends it early (ci)
    skip-when: {assignee: pool}   # not for a PR assigned to the pool explicitly (ci)
  give-up: {attempts: 3, since: [head, human, pause]}   # the stop label and a comment
  phase: 3              # ordering after the priority label
```

`run: {drop: true}` drops an item's queued tasks; the stop label does it
for every rule, as an engine gate rather than a rule.

The global ones stay global: drain, the stop label, `minNumber`, trust,
`--max-pending`, one task per sandbox.

### The queue

Rules enqueue tasks; the dispatcher starts them, one per sandbox, under
a lease, as now. Two tasks on one PR (feedback and a failing check at
once) do not run together: the second waits for the sandbox. What goes
wrong is staleness: both were decided against head `A`; the first
pushes `B`, which may also fix the check; the second then investigates
`A`'s failure against `B`, a wasted run that also counts as one of its
three attempts. Today `HasActivePRTask` avoids it by enqueuing nothing
for a PR while a task of it is queued or running, and evaluating again
at the new head once it ends.

**Preconditions** (superseded by the decision at start, Phase 4 step 4,
which keeps exactly these checks for a rule the scanner still decides;
`QueueTask.Expect`, `StaleTask` and the `preconditions` setting are gone).
A task records the facts it was decided on, and the
dispatcher checks them before it starts it, as it already checks the
stop label and whether the item is closed (one call, nearly free with
the HTTP cache). Which facts follow from the task, not from the rule
(2a's `expect:` was never read, and its comments value was wrong): the
scanners set them (`prs/task.go` expectFor, the issue scanner):

| task | checked | why |
|---|---|---|
| ci, conflict, review | the head | the failure, conflict or diff was of that commit |
| comments | — | a commit does not address feedback; reactions track that |
| fix | no linked PR | someone opened a PR by hand while it waited |
| workflow, chores | — | |

- **A failed check drops the task,** with the reason in the journal
  (`Dropped: head moved A→B`) and in the overseer's view, as the
  cooldown label is; the task's name is free again, and the next pass
  decides anew at the current state.
- **A dropped task is not an attempt:** it does not count toward give-up
  or start a cooldown.
- **Only before the start:** a task already running when the process
  restarts is followed by its run name, never dropped.
- **An item evaluated again while its task is queued** (dedupe keeps it
  to one) refreshes the task's recorded facts, so it is not dropped for
  a change it already reflects.

**Phase 4** replaced the preconditions with the decision at start and
moved what the scanners decided into the engine; the steps and their PRs
are in the Plan below, the result under "As built".

**Phase 4: decide again at start** (built, #2041, always on since #2049). Preconditions check a few facts; once
the engine can build an item's facts on its own (the shared scan and a
fact builder out of the scanners), the dispatcher asks it instead:
would it still decide this rule on this item now? That catches what the
head check cannot: feedback that arrived while a ci task waited (comments,
earlier in the order, now wins), CI re-run and now pending or green, the
PR approved and ready. It decides as if the task were not there: not
`Queued`, its own run not counted toward give-up or cooldown. A "no" drops
the task as a failed precondition does: not an attempt, no cooldown, the
next scan deciding anew. It costs one decision per task started, not per
item scanned. The preconditions go with it.

Pushes are already safe (the push step leases against the head it last
pushed); preconditions save the wasted run. With them, one task per
item stays as today's guard against queuing work that the
first task's push will obsolete.

### Where rules live

**One rule per file, in `.agents/watch/<name>.yaml`,** beside the
repository's recipes:

```
.agents/
  recipes/<name>.yaml    what to do                         (exists)
  rules/<name>.yaml      when the overseer does it: a trigger, the recipe, its policy, its order
  watch.yaml             optional: the repository's settings, and no rules
```

- **The built-in rules are factory's,** prebaked: one file per rule in
  `factory/pkg/watch/rules/<name>.yaml`, embedded in the binary as the
  built-in recipes are (`factory/pkg/recipe/recipes/`), versioned and
  shipped with the factory image, and tested as code (each one parses,
  names a built-in recipe, and the replay tests run them). They are the
  overseer's behaviour out of the box: an Overseer with no rules of its
  own runs them all, as today's watch does.
- **An Overseer picks among them** by name instead of today's modes:
  `spec.rules.disabled: [review]`, or `enabled: [fix, ci, comments]`;
  `issueMode: disabled` becomes turning off the rules on issues.
- **A repository's rule replaces the built-in of its name**
  (`.agents/watch/ci.yaml` with a different give-up), or adds a rule of
  a new name. Rules are read from the default branch only, as repository
  recipes are.
- **On or off at every source.** A rule is on unless something turns it
  off, and three places can, each overriding the one before:
  1. **factory's file** says the default (`enabled: false` on `inactive`
     and `stop`, as today's watch has them off);
  2. **the repository's file** of the same name, with nothing but its
     name and `enabled`, turns factory's rule off - or on - in code,
     keeping the rest of it; a repository's owner flips a rule in a
     reviewed change, without copying it:
     ```yaml
     # .agents/watch/review.yaml
     name: review
     enabled: false
     ```
  3. **the Overseer** (`spec.rules.disabled`, and the engine's modes
     below), the operator's, has the last word over both.

  `factory watch rules --dir <checkout>` shows the result for a local
  checkout: whose each rule is (SOURCE) and whose its enabled is
  ("false (repo)").
- **A repository's rule can run** only recipes (its own or built-in) and
  the closed set of actions; it cannot add a predicate, an action or a
  source of a new kind.
- **The settings** (the trigger label, the bot pool and its roles,
  `minNumber`, the modes, the concurrency limits, the policy defaults)
  are the Overseer's, as now, and a repository's `watch.yaml` may narrow
  them. A mode that disables issues disables the rules on issues.
- **The whole picture** is a command, not a file: `factory watch rules
  --url <repo>` prints the effective rules (built-ins merged with the
  repository's), where each comes from, in the order the engine walks them, as
  `factory recipe list --url` does for recipes.

**Why not one file.** The alternative is a single `.agents/watch.yaml`
holding the settings, the sources, the order and every rule. The same
five rules, both ways:

```
 single file: .agents/watch.yaml          │ one file per rule: .agents/watch/*.yaml
──────────────────────────────────────────┼──────────────────────────────────────────
 sources:                                 │ # sources: derived from each rule's on/when
   prs-assigned:                          │
     github: "is:pr assignee:{{pool}}"    │
     every: 2m                            │
 groups:                                  │ # order: each rule says its place
   - name: pr-care                        │
     rules: [comments, conflict, ci,      │
             review]                      │
     then: ready                          │
 rules:                                   │ # rules/fix.yaml
   - name: fix                            │ name: fix
     on: issue                            │ on: issue
     when: {labels: {any: [overseer]},    │ when: {labels: {any: [overseer]},
            linked-pr: none}              │        linked-pr: none}
     run: {recipe: fix, apply: open-pr}   │ run: {recipe: fix, apply: open-pr}
   - name: comments                       │ # rules/comments.yaml
     on: pr                               │ name: comments
     from: [prs-assigned]                 │ on: pr
     when: {feedback: new-trusted}        │ order: 10
     run: {recipe: care-comments}         │ when: {feedback: new-trusted}
   - name: ci                             │ run: {recipe: care-comments}
     on: pr                               │ # rules/ci.yaml
     when: {checks: failing}              │ name: ci
     run: {recipe: care-ci}               │ on: pr
     policy: {give-up: {attempts: 3}}     │ order: 30
   - name: review                         │ when: {checks: failing}
     on: pr                               │ run: {recipe: care-ci}
     when: {checks: green,                │ policy: {give-up: {attempts: 3}}
            labels: {any: ["*/review"]}}  │ # rules/review.yaml, rules/ready.yaml: likewise
     run: {recipe: review}                │
──────────────────────────────────────────┼──────────────────────────────────────────
 CI give-up to 5: edit the shared file,   │ add rules/ci.yaml (replaces the built-in)
   restate the ci entry                   │
 a nightly stale summary: edit the file   │ add rules/stale-summary.yaml
 auto-review off: remove review from the  │ rules/review.yaml with enabled: false
   rules and from the group               │
```

| | one file | one file per rule |
|---|---|---|
| familiar from | Kubernetes manifests, CI config | GitHub Actions (`.github/workflows/*`), `.agents/recipes/*` |
| everything at once | by reading it | `factory watch rules --url` |
| order between rules | one list | each rule's `order` |
| sources | declared, tunable | derived; cadence is the engine's |
| add or remove a behaviour | edit a shared file | add or delete a file |
| override one built-in | restate or patch an entry, which drifts | a file of the same name |
| conflicts and ownership | one file for everyone; CODEOWNERS all or nothing | a file each; CODEOWNERS per rule |

One file's strength, everything in one place, a command gives; its cost,
every change an edit to a shared file that drifts from the built-ins, is
structural. One file per rule costs the order spread across files as
numbers: the command prints the rules in order, and loading refuses two
rules on one kind with the same order.

A trigger inside the recipe (an `auto:` beside its `on:`) is rejected:
the same recipe is triggered differently by the overseer, a board's
auto rules and a click, so a trigger is the runner's, not the recipe's.

## What doesn't fit simple rules

- **Fan-out is a controller,** with a ramp: windows and groups that
  double and halve, checkpoints that pause and resume, and its state in
  a GitHub comment. In rules it can only ever be "a labelled child
  becomes a fix". It stays a named plugin with its pure `Decide`.
- **Give-up counters live in GitHub text.** The three-strikes count for
  CI is the number of the bot's "started investigating" comments since
  the last commit, trusted human comment or pause notice; the comments
  rule carries its retry count on the queue's processed task. Reactions
  carry meaning too: the watcher's 👀 means taken, 👍 done, 😕 failed,
  and a human's 🚀 asks for it again. A rules engine needs these as
  named facts backed by a state store (`give-up: {attempts: 3, since:
  human}`, `feedback: new-trusted`), not text matching. The
  reactions can stay as what the engine writes for people to see, but
  not as what it reads to decide.
- **Workflows have no triggering event.** A workflow issue re-runs every
  cooldown, on a timer; it is nudged early when an issue it links
  closes, and that signal comes from the sandbox GC confirming the
  closure before deleting a sandbox, not from a GitHub listing. As
  rules: a timer (`schedule` or `cooldown` with no updated-since gate)
  and a `closed` event source, which the engine does not have yet.
- **GC and warm-up are driven by the cluster,** not by GitHub items.
  They are rules over a different source (`sandboxes`), and some of
  their checks have side effects: reading a sandbox's task state
  corrects its annotation and deletes evicted pods, and resolving a
  PR's sandbox aliases an issue's sandbox to the PR. A pure evaluator
  would not reproduce those. They stay plugins.
- **Restart recovery** is a property of the actions, not a rule: a task
  left in `processing/` is queued again under the same run name, and
  factory follows the run or applies its result (`--run-name`).

## Compared to today: what a naive engine breaks

"List the items, check every rule's `when`, enqueue what matches" loses
what today's watch does by hand, case by case. The engine keeps each as
its own feature, not each rule's:

| risk | today | the engine |
|---|---|---|
| **its own writes trigger it** (labels, the start assignment, comments, reactions bump `updated_at`; inheritance changes a PR mid-evaluation) | a fix's "updated since" counts from the task's completion; PR rules key on the head; feedback is tracked by reactions; the stop label is re-checked after inheritance | events from the diff ignore the overseer's own writes (by actor); PR rules key on the head; an item is evaluated against one snapshot |
| **stale work on one item** (decided at head `A`, run after `B`) | `HasActivePRTask`: nothing enqueued while a task of the PR is queued or running | one task per item (the rules that take it), and preconditions checked at dispatch (The queue) |
| **readiness while work is pending** | readiness checks its own conditions and no active task | `ready` evaluates its conditions, never "no rule matched", and none while a task is queued |
| **counters reset on restart** (the process restarts every 5 minutes) | survives crudely: CI's three strikes are read from the bot's comments, comments' retries and the SHAs from `processed/` | `attempts` from the processed tasks, persisted with the queue; seeded once from the comment counts, so a PR mid-count is not given three more |
| **a failed read decides** | fails closed: no issue scan without the open-PR cache, no GC pass without its caches, no PR evaluation on a history or checks error | predicates are true, false or unknown; unknown skips the item |
| **GitHub cost** | cheap checks first, and the fast pass re-checks only what changed, but the restart resets "what changed", so every pass is in effect a sweep with details for every candidate | predicates ordered by cost; one scan, events from diffs, details on demand, all persisted (One scan); a rule must have a predicate a listing can narrow on |
| **a repository's rule spends on anyone's issue** (`when: {}`), or drops a give-up | not configurable | the trigger label, trusted authors and pool-authored PRs are global and cannot be overridden; floors on attempts and cooldown |
| **per-rule quirks flattened** (workflows skip "updated since"; a failed fix is not stamped; comments' retry path; conflict re-evaluated each pass; CI's "explicitly assigned" bypass) | inline, some implicit | each built-in rule spells its policy out; replay tests prove it |
| **identity** (a PR task as a bot that cannot push to the author's fork; a fix as another bot than the sandbox's) | pinned to the sandbox's user, then the assignee; PR tasks as their author | pinning and author-runs are the engine's |
| **side effects inside checks** (reading task state fixes stale annotations and deletes evicted pods; resolving a PR's sandbox aliases it) | inline, and what keeps it working | explicit reconcile steps, not hidden in predicates |
| **behaviour changing quietly in the move** | — | replay tests (recorded GitHub snapshots through the scanners and the engine, comparing what each enqueues), then shadow mode on staging (the engine logs, the scanners act) |
| **chores and nudges** | their own loops (cron, GC) | a schedule source and a `closed` event; until then, plugins |
| **fairness** | none: strict priority, then phase, then first come | optional, an addition rather than a regression |

Where today's watch is safe and a naive engine is not, the engine must
keep the guarantee. Where it can do better than today is cost and the
counters, which today's restart cycle undoes, and it adds a risk today
does not have: rules a repository writes.

## Moving without regressing

The overseer on KCC must not do less, or worse, at any point of the
move. Both paths stay, and the move is per rule, each step undone by one
setting.

**Why both can run at once.** Task names are deterministic
(`task-issue-N`, `task-pr-N-ci`, …) and a second enqueue of a name is a
no-op. The scanners and the engine write into the same queue, and the
dispatcher, the preconditions, the leases and the identity are shared:
a task both decide runs once.

**Modes,** on the Overseer, during Phases 2 and 3 (all removed by
#2051, once the engine decided every rule; `spec.rules` keeps only
`disabled`):

```yaml
spec:
  rules:
    engine: shadow          # off | shadow | on; off (the default) is today's watch
    use:                    # per rule, once the engine is not off
      conflict: engine
      ci: engine
      comments: legacy
    canaryLabel: overseer/engine   # optional: the engine takes a rule only on items with this label
    canaryPercent: 20              # optional: the watch puts canaryLabel on 20% of the PRs itself
```

- **`off`:** the scanners only, as today; the engine's code ships and
  does nothing.
- **`shadow`:** the scanners act; the engine is given the data the
  scanners fetched (no calls of its own), decides, and acts on nothing.
  Each decision is recorded beside the scanner's, and disagreements per
  rule are shown in the journal and the overseer's view.
- **`use: {<rule>: engine}`:** the rule's tasks come from the engine and
  the scanner skips that task type (as built, the engine decides the
  scanner's step instead; see "How the engine runs inside the scanners"
  below); everything after the queue is
  unchanged. Back is the same setting; the engine's state (snapshots,
  cursor, attempts) only adds to what is there, so nothing migrates.
- **`canaryLabel`:** a rule the engine has taken applies only to items
  with the label, so a few chosen issues and PRs go first while the rest
  stay on the scanners.
- **`canaryPercent`:** the watch picks the canary itself and labels it in
  public, so it can be filtered on GitHub (`is:pr label:<canaryLabel>`):
  while evaluating a pool's PR (not stopped, not in the merge queue), it
  adds `canaryLabel` when the PR's number hashes
  into the percent - the same PRs every pass and after a restart, new ones
  at the same rate - unless the PR's timeline shows a person took the
  label off before, which is final. The label goes on the PR the engine
  is about to see, so it takes the PR that pass. People can still add the
  label by hand. PRs only for now.

**What says it works:** every task records `decidedBy: legacy|engine`;
the overseer's view counts, per rule, tasks queued, dropped by a
precondition, given up, and shadow disagreements; a rule's outcomes (runs,
PRs opened, give-ups) are compared over a week before and after it moves.

**Not partial by loop:** issues on one path and PRs on the other is too
coarse, since PR care is rules of very different risk.

### How the engine runs inside the scanners (Phases 2 and 3)

This is how Phases 2 and 3 ran; Phase 4 replaced it (the shadow, `use`,
the canary and Handed are gone; see "As built"). Built, `use` was
narrower than the bullet above: the engine does not
queue tasks of its own. It runs inside the two scanners and, for a rule
it has taken, answers the scanner's question at the point the scanner
used to answer it from its own condition. The fetching, the handlers
that build and queue the task and the dispatcher stay the scanners'.

**Where the items come from.** With `sharedScan` one Store lists the
tracked set (trigger label ∪ each pool bot's assignments ∪ the
watcher's own issues) in full, then once per pass for what changed
since its cursor, and persists it; without it each scanner lists for
itself. The scanners read their lists out of it on their own tickers:

```
 GitHub
   │  sharedScan: one Store (scan_state.json), full then incremental
   ▼
 ┌───────────────────────────────┐        ┌───────────────────────────────┐
 │ Issue scanner                 │        │ PR scanner                    │
 │  every tick: fast pass        │        │  sweep when due, else fast    │
 │    issues assigned to pool    │        │  sweep: ListOpenPRs → open-PR │
 │  when due: sweep              │◄───────┤    cache, then                │
 │    trigger-labelled issues    │ open-PR│    every labelled/assigned PR │
 │  no open-PR cache → no scan   │ cache  │  fast: assigned PRs that moved│
 └──────────────┬────────────────┘        │    (updated_at, CI active,    │
                │ per issue               │    task started/finished)     │
                ▼                         └──────────────┬────────────────┘
          queueTask()                                    │ per PR (GraphQL
                                                         ▼ details read ahead)
                                                    evaluate()
```

**The record is the engine's input.** Each scanner opens a Recording
when it starts on an item and fills a `rules.Item` as it learns facts.
The engine decides on that Item alone and never calls GitHub:

```
 startRecording(item)          labels, assignees, author, updatedAt
   ├─ rec.Fill(...)            facts as the scanner learns them
   ├─ rec.Gated(why)           a gate outside the rules held it back
   ├─ rec.Decided(rule, run)   what the scanner did
   ▼
 rec.Finish() ─► Recorder ─► decisions.jsonl (rulesRecord; repeats skipped 30m)
                    └──────► Shadow.Observe: Set.Decide(Item) vs the
                             scanner's decisions → /api/v1/queue `shadow`
```

**A pull request** (`prs/scanner.go` evaluate):

```
 stop label? ─► drop its pending tasks, return
 read PR                              Fill: author, head, draft, mergeable
 merge queue? ─► drop pending, return
 author not in the bot pool? ─► return
 closing issues                       Fill: closes
 decides("inherit-labels") ─► label sync from the closing issues
 stop label now? ─► drop pending, return
 queue history                        Fill: runs, queued
 task active? ─► return               Fill: taskActive
 reviews, comments                    Fill: reviewedAtHead, changesRequested,
                                            lastTrustedActivity, humanSpokeAt
 decides("inactive") ─► pause (stop label + comment), return
 legacy conditions                    Fill: newTrustedFeedback, checks, approved
 engine takes any care rule here? ─► engineCare = Set.Decide(Item)
 first that holds:
   picks("comments") → handlePRComments
   picks("conflict") → handlePRIterate
   picks("ci")       → handlePRInvestigate
   picks("review")   → handlePRReview
 picksReadiness → reconcileReadiness (ready / ready-unreview)
```

`picks` and `decides`, per rule and per pull request:

```
 rule in disabled                          → don't act
 Takes(rule, labels): engine on ∧ use[rule]=engine
                      ∧ (no canaryLabel ∨ the item has it)
                                           → did the engine decide it?
 otherwise                                 → the scanner's own condition
```

The handler that follows keeps its own checks and gates (a busy sandbox)
before it queues.

**An issue** (`issues/scanner.go` queueTask). The scanner gathers its
facts cheapest first and returns at the first that says no, so the
engine only confirms; it can hold a fix or a workflow back, never start
one:

```
 stop label? ─► drop pending, return
 fan-out held? ─► return
 an open PR references it? ─► return       Fill: linkedPR
 workflow named? → rule = workflow, else fix   Fill: workflow, runs
 rule disabled? ─► return
 task queued? ─► return                    Fill: queued
 in cooldown? ─► return                    Fill: workflowCooldown
 processed, not updated since (fix)? ─► return
 timeline linked PR (fix)? ─► return
 sandbox needs a person / busy? ─► Gated, return
 trigger label, bot, task (fix: expect no-linked-pr)
 Takes(rule) ∧ the engine does not decide it? ─► return
 rec.Decided(rule, run) → Enqueue
```

**After the queue** nothing changed: the dispatcher drops a stopped
item's tasks (`ShouldCancelTask`) and a task whose preconditions no
longer hold, then runs the recipe and its apply.

**Inside `Set.Decide`:** an item a gate holds (below) decides
nothing; then the enabled rules of the item's kind, walked in `order`:
a rule that takes the item is decided only if none before it was and no
task of the item is active; any other rule decides on its own, its label
writes applied for the rules after it, and a stop it writes (inactive,
or one inherited by inherit-labels) ends the walk. A rule holds on its
predicates and policy (cooldown, dedupe by head); one past its attempts
decides its give-up.

**Gates are code, not rules** (`rules.Gates`, `pkg/watch/rules/gates.go`):
safety, where the rules are policy. A gate holds before any rule, for
every rule including those not written yet, and no rule file (the
factory's or, in Phase 5, a repository's) can turn it off. The scan and
the dispatcher's decision at start (Phase 4) both pass an item through
them; `explain` names the gate that held, `--flow` lists them.

| gate | kinds | the item | queued tasks |
|---|---|---|---|
| `stop` | issue, PR | has `*/stop` | dropped |
| `closed` | issue, PR | is closed (a PR merged or closed): `Item.Closed` | dropped |
| `below-min-number` | issue, PR | is numbered below `minNumber`: `Env.MinNumber` | dropped |
| `merge-queue` | PR | is in the merge queue | held |

Trusted authors and the pool are not gates but how facts are read (only
a trusted human's comment is feedback) and predicates (`author: pool`):
a rule may narrow them, never widen them. `stop.yaml` is gone: it only
named the stop gate. Code for now: the gates may move to rule files later
(a gate kind no repository rule can override), which is why `run: drop`
stays in the rule format with no rule using it.

**What this means for the move:**

- On an item it has taken a rule for, the engine replaces only the
  conditions above. The stop and merge-queue returns, the bot-author
  filter, the active-task skip, the reads, the handlers and the
  dispatcher stay scanner code until Phase 4.
- `engine: on` keeps the shadow, but on an item where the engine has
  taken a rule what the scanner did *is* the engine's decision, so that
  rule's comparison agrees by construction. The scanner's own condition
  is worked out and dropped; until it is recorded beside the engine's
  (the `decidedBy` above is not built yet), the evidence for a taken
  rule is its outcomes.
- An issue rule on the engine can only lose work: its regressions are
  fixes and workflows that did not happen.
- (Phases 2-3) `use`, `canaryLabel`, `canaryPercent` and `disabled` reached the watch as `WATCH_RULES_*`
  environment, so changing them restarts the pod (a safe upgrade on
  KCC); the canary label itself is read every scan, so labelling an item
  moves it with no restart.

## Plan

Each phase ships behind its own setting, defaulting to today's
behaviour, and goes to the smaller overseers (`overseer-repo-agent`,
`overseer-agent-sandbox`) before staging `overseer-kcc`, then
production.

**Phase 0, fixes in today's watch** (done; no decision changes; each a
small PR, each verified on staging `overseer-kcc`; 0e's `attempts`
setting was removed in #2062 once the engine counted give-ups from the
journal):

| PR | what | done when |
|---|---|---|
| 0a | the knobs: remove `--poll-interval`, `--labels`, the Overseer's `repo.reviewMode` (reviews follow the `*/review` label) and `chores.include`/`exclude`; honour the `dryrun` mode; name `pollInterval` the run's lifetime | the Overseer's settings do what they say, or are gone |
| 0b | one definition of a bot review; the dispatcher's comment says its ordering | — |
| 0c | preconditions: PR tasks record their head, issue tasks that no PR is linked; the dispatcher drops a stale task with a reason, not counted as an attempt (setting `preconditions`) | on KCC, drops are visible and no wasted CI run follows a comments run's push |
| 0d | the HTTP cache on the overseer's volume, surviving the restart | the first pass after a restart is mostly 304s |
| 0e | `attempts` from the processed tasks, seeded from the comment counts, replacing the comment counting (setting `attempts`) | the give-ups happen at the same counts as before |

**Phase 1, one scan** (done: `sharedScan`, `graphqlPRDetails`): the scanners read one set
of listings (deduplicated, incremental with `since`), the cursor and
snapshots persisted, PR details from one GraphQL query. The scanners'
decisions are unchanged; only where their data comes from. Done when, on
KCC, calls per pass drop and the tasks enqueued match a week before.

**Phase 2, the engine in shadow** (done, #1996-#2005, #2027; the shadow
became the summary in #2057):

- 2a: the model and the prebaked rules (2a-1, #1996); the model's gaps
  closed and the files exact (2a-2a: `from` (since removed), `any-of`, `closes`,
  `last-run`, `fanout-held` (since folded into `fanout`, once children were created only when due), resets, placeholders; adoption folded into
  fix, #1999); the evaluator (2a-2b: `Set.Decide` over an `Item` of facts
  and an `Env`, no calls, #2000); replay (2a-3: the scanners record each
  decision with its facts behind `rulesRecord`, and `factory watch rules
  replay` runs the rules on the records). In all:
  the engine, the predicates and the order, and the built-in rules as
  prebaked files (`factory/pkg/watch/rules/*.yaml`, embedded), read
  from the start, so there is no rules-in-Go stage to move off later;
  `factory watch rules` lists them; replay tests on recorded KCC
  snapshots (captured read-only from staging), comparing what the
  scanners and the engine enqueue.
- 2b: `engine: shadow` on the smaller overseers, then on KCC staging
  (the Overseer's `spec.rules.engine: shadow`: every recorded decision is
  compared live, the tally served on the queue API as `shadow`; then the
  overseer's view shows it);
  disagreements shown per rule.
- Done when every built-in rule has a week on KCC staging with no
  disagreement that is not understood and fixed.

**Phase 3, rule by rule** (done, #2007, #2025; `use: {<rule>: engine}`,
the canary label first, then all items; staging, then production),
cheapest to get wrong first:

The switches are in (the Overseer's `spec.rules`, all off by default):
`engine: on` with `use: {<rule>: engine}` has the engine decide that rule
for the scanners, on items with `canaryLabel` when one is set;
`disabled: [<rule>]` keeps the watch from acting on a rule at all, through
the scanners or the engine. A rule the engine decides takes the scanner's
step (a pr-care case, the label sync, the inactivity pause, readiness) on
the engine's decision, and the scanner's handler still builds and queues
the task, with its outside gates (a busy sandbox). For issues the engine
confirms: the issue scanner gathers its facts cheapest first and stops at
the first that says no, so the engine can hold a fix or a workflow step
back but not start one the scanner would not; deciding issues outright
waits for the facts gathered up front.

1. `conflict`: rare, and a rebase is easy to check.
2. `ci`, then `review`: replies and pending reviews.
3. `comments`.
4. `ready`, PR adoption (shipped off), label inheritance, inactivity: writes people see.
5. `fix`, `workflow`: they open PRs and spend the most.

Each rule moves when its shadow week is clean, and moves back on any
regression. Done when every rule is on the engine.

**Phase 4, the engine decides, the scanners carry out** (done; on
staging `overseer-kcc` from 2026-10-21, the other overseers at their next
upgrade). Rather than two weeks per rule in production, the rules moved
together once staging's shadow had agreed on every rule:

| step | what | PR |
|---|---|---|
| 1a | the gates in code, one list (`rules.Gates`) | #2039 |
| 1b | `Scanner.Facts`: one item's facts, read-only, with the scan's own functions | #2040 |
| 2 | the decision at start (`Redecide`) | #2041; nudged workflow steps to the gates alone #2046; comments acknowledged at start #2047 |
| 3a | one executor for the engine's writes (`act.ApplyWrites`) | #2042 |
| 3b | tasks stamped with rule, recipe, apply, inputs and `policy.phase` (`act.Task`); the phases checked equal to the handlers' | #2044 |
| 3c | issue rules ask the engine's policy first (`Set.Held`) | #2045 |
| 4 | the decision at start always on, the preconditions gone | #2049 |
| 4 | the engine decides every rule; `engine`, `use`, the canary gone; `spec.rules` keeps `disabled` | #2051 |
| B1, B2 | the PR and issue scanners' own decisions deleted | #2053, #2055 |
| B3 | the shadow becomes `rules.Summary`; Handed gone | #2057 (overseer page #2058) |
| C | the recipe table keeps runner details only | #2059 |
| | workflow-missing posts its rule's comment | #2061 |
| | an issue's costly facts (`connected-pr`, a workflow's definition and cooldown) read on demand, by what the rules that could step use (`Set.Needs`); any recipe rule on issues runs from it | #2094 |
| | a PR's write rules carried out by order and facts, a stage at a time (`Set.Settled`), none by name | #2096 |
| | `author: not-pool`; the author check from the rules (`Set.AdmitsAuthor`); PRs also listed by the rules' namespaced labels, one GraphQL OR query (`Set.ListingLabels`) | #2100 |
| | lifecycle hooks: `run.on-start`, `on-success`, `on-failure`, `on-end`, the watcher's writes at points of a task's life (`rules.Hooks`, stamped on the task, executed in `NotifyTaskStarted`/`Finished`); a label taken off when done makes a rule a command | #2106 |
| | the prebaked rules' `claim` and `start-comment` become `on-start` (`assign: "{{bot}}"`, `unassign: "{{watcher}}"`, `comment`); both fields gone | #2107 |

**Phase 5, the repository's rules** (partly built): the watch reads
`.agents/watch/*.yaml` once when it starts, under `spec.repoRecipe` (one
switch for everything a repository's `.agents/` holds): replace mode
replaces or turns off prebaked rules by name, within the global gates and
floors, and adds rules of new names, which run as rule tasks: a run is
its recipe (factory's, or one in the repository's `.agents/recipes`,
checked when the rules load), in `task-pr-<n>-rule-<name>.yaml` or
`task-issue-<n>-rule-<name>.yaml` (types `pr-rule`, `issue-rule`, the run
named after the rule); writes and give-ups go through the same
`ApplyWrites` as factory's. This repository's own `.agents/watch/` is a
verbatim copy of factory's rules (as its `.agents/recipes/` is of the
built-in recipes), for trying replace mode on an overseer of this
repository; all but workflow-missing, whose stop is still its own handler's
(a copy's writes other than its comment would be ignored). Not built: settings beside the rules, and the board's auto
rules on the same predicates.


## Open questions

- ~~**A repository's rules and the bot pool:**~~ decided: only the
  operator. A rule names no role (No `as`, under Actions); the
  overseer's `roles` maps task types to bots.
- **Workflows on a timer:** a workflow issue re-runs every cooldown. As
  a rule, is that a cooldown without `dedupe: update`, or a
  `schedule` on the rule?
- **Events:** sources are listings. GitHub's events or a webhook would
  let rules fire on `labeled`, `review_requested`, `check_run.completed`,
  which the board's auto rules would want too. Out of scope until a
  GitHub App exists (design/github-app-identity.md).
- **Fairness:** strict priority starves low phases under load. Does the
  engine add a per-rule or per-item share?
