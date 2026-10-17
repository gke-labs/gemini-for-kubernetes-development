# The watch loop as rules

Status: draft, being refined.

`factory watch` (the overseer) decides what to run by code: an issue
scanner, a PR scanner, a fan-out controller, a chores scheduler, a
sandbox reconciler, a dispatcher. Each has its conditions inline, in an
order, with its own dedupe. This note writes what it does as rules: a
GitHub condition and the action taken, the factory recipe it runs and as
whom, plus the policies every rule shares. Then it proposes running the
rules as data, so the loop is one engine and a repository can say what
its overseer does without a Go change, as `.agents/recipes` did for
recipes (design/repo-recipes.md).

## Today

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

### Rules, groups, sources, plugins

The engine has four kinds of thing.

**Rules** are a condition and an action, on one kind of item, one rule
per file (see Where rules live):

```yaml
# .agents/rules/fix.yaml
name: fix
on: issue
when:
  labels: {any: ["{{trigger}}"]}
  linked-pr: none
  workflow: none
  updated-since-run: true
run: {recipe: fix, apply: open-pr, inputs: {draft: "false"}}
as: {role: coder}
policy: {cooldown: {success: 10m, failure: 20m}, phase: 3}
```

**Groups** make rules on one item ordered and exclusive: of the rules
in a group, the first whose condition holds is taken, none while a task
of the item runs, and the rule ordered `last` holds when none other
does. A rule says its place itself, as a GitHub Actions workflow names
its `concurrency` group. PR care is one group:

```yaml
# .agents/rules/ci.yaml
name: ci
on: pr
group: {name: pr-care, order: 30}      # comments 10, conflict 20, ci 30, review 40, ready last
when: {checks: failing}
run: {recipe: care-ci, apply: post-replies}
as: {role: author}
policy: {dedupe: head, give-up: {attempts: 3, since: human, then: stop}}
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
| any item | `labels: {any, all, none}`; `author: pool\|trusted`; `assignee: pool\|not-pool\|none\|owner`; `updated-since-run`; `stopped` (the stop label); `last-run: failed` (the rule's last run on the item, on its head when deduped by head) |
| issue | `linked-pr: none\|open`; `workflow: none\|any`; `opened-by: watcher`; `fanout: none\|parent\|child`; `fanout-held` (a parent, or a child the fan-out has not labelled) |
| PR | `checks: failing\|pending\|green`; `mergeable`; `feedback: new-trusted\|none`; `closes: {labels}` (the labels of the issues it closes, between them); `reviewed-at-head`; `approved`; `draft`; `merge-queue`; `inactive-for: <duration>` |
| sandbox | `item: closed`; `age: <duration>`; `idle: <duration>`; `requires-human` |
| any | `attempts: {rule, since: [head, human, pause], max}`; `any-of: [<predicates>, …]` |

**`from`** names the listings a rule's items come from, any of them:
`labelled` (the trigger label), `assigned` (a bot of the pool) and, on
issues, `opened-by-watcher`. They are the overseer's ways of being
pointed at an item, and what the one scan lists; a rule without `from`
must narrow the listing with a predicate (labels, a pool or owner
assignee, an author). Adoption is not a rule: an issue the watcher
opened is fix's third listing, and the label and assignee adoption wrote
are fix's own (the label when queued, the coder at the claim).

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

`attempts` replaces counting the bot's own comments: the engine records
each run of a rule on an item (the queue's processed tasks already do),
and the predicate counts them since the head changed or a trusted human
last spoke. The board's auto rules (repo-agent design
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

`as` names a role. Pinning (the bot that owns the item's sandbox, then
the assignee, then any) and "a PR's tasks run as its author" are the
engine's, for every rule.

### Policies

Every rule has defaults, overridable per rule:

```yaml
policy:
  dedupe: item          # one task per item and rule; or head (once per head SHA)
  cooldown:
    success: 10m
    failure: 20m
    reset-on: [head]          # a new head ends it early (ci)
    skip-when: {assignee: pool}   # not for a PR assigned to the pool explicitly (ci)
  give-up: {attempts: 3, since: [head, human, pause], then: stop}
  phase: 3              # ordering after the priority label
  identity-pin: true
  before: pr-care       # evaluated ahead of a group, every time (inherit-labels)
  skip-cooldown: true   # queued inside its task's cooldown (nudge)
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

**Preconditions.** A task records the facts it was decided on, and the
dispatcher checks them before it starts it, as it already checks the
stop label and whether the item is closed (one call, nearly free with
the HTTP cache):

```yaml
# rules/ci.yaml
expect: [head]          # recorded when queued, checked before start
```

| rule | expect | why |
|---|---|---|
| ci, conflict, review | `head` | the failure, conflict or diff was of that commit |
| comments | `open` | a commit does not address feedback; reactions track that |
| fix, workflow | `open`, `no-linked-pr` | someone opened a PR by hand while it waited |
| chores | — | no item |

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

Pushes are already safe (the push step leases against the head it last
pushed); preconditions save the wasted run. With them, one task per
group per item stays as today's guard against queuing work that the
first task's push will obsolete.

### Where rules live

**One rule per file, in `.agents/rules/<name>.yaml`,** beside the
repository's recipes:

```
.agents/
  recipes/<name>.yaml    what to do                         (exists)
  rules/<name>.yaml      when the overseer does it: a trigger, the recipe, its policy, its group
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
  (`.agents/rules/ci.yaml` with a different give-up), or adds a rule of
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
     # .agents/rules/review.yaml
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
  repository's), where each comes from, and each group in order, as
  `factory recipe list --url` does for recipes.

**Why not one file.** The alternative is a single `.agents/watch.yaml`
holding the settings, the sources, the groups and every rule. The same
five rules, both ways:

```
 single file: .agents/watch.yaml          │ one file per rule: .agents/rules/*.yaml
──────────────────────────────────────────┼──────────────────────────────────────────
 sources:                                 │ # sources: derived from each rule's on/when
   prs-assigned:                          │
     github: "is:pr assignee:{{pool}}"    │
     every: 2m                            │
 groups:                                  │ # groups: each rule says its place
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
     when: {feedback: new-trusted}        │ group: {name: pr-care, order: 10}
     run: {recipe: care-comments}         │ when: {feedback: new-trusted}
   - name: ci                             │ run: {recipe: care-comments}
     on: pr                               │ # rules/ci.yaml
     when: {checks: failing}              │ name: ci
     run: {recipe: care-ci}               │ on: pr
     policy: {give-up: {attempts: 3}}     │ group: {name: pr-care, order: 30}
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
| order between rules | one list per group | each rule's `group: {name, order}` |
| sources | declared, tunable | derived; cadence is the engine's |
| add or remove a behaviour | edit a shared file | add or delete a file |
| override one built-in | restate or patch an entry, which drifts | a file of the same name |
| conflicts and ownership | one file for everyone; CODEOWNERS all or nothing | a file each; CODEOWNERS per rule |

One file's strength, everything in one place, a command gives; its cost,
every change an edit to a shared file that drifts from the built-ins, is
structural. One file per rule costs the order of a group spread across
files as numbers, and only groups have an order (PR care, today): the
command prints each group in order, and loading refuses two rules of a
group with the same order.

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
  named predicates backed by a state store (`attempts: {rule: ci, since:
  human, max: 3}`, `feedback: new-trusted`), not text matching. The
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
| **stale work on one item** (decided at head `A`, run after `B`) | `HasActivePRTask`: nothing enqueued while a task of the PR is queued or running | one task per group per item, and preconditions checked at dispatch (The queue) |
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

**Modes,** on the Overseer:

```yaml
spec:
  rules:
    engine: shadow          # off | shadow | on; off (the default) is today's watch
    use:                    # per rule, once the engine is not off
      conflict: engine
      ci: engine
      comments: legacy
    canaryLabel: overseer/engine   # optional: the engine takes a rule only on items with this label
```

- **`off`:** the scanners only, as today; the engine's code ships and
  does nothing.
- **`shadow`:** the scanners act; the engine is given the data the
  scanners fetched (no calls of its own), decides, and acts on nothing.
  Each decision is recorded beside the scanner's, and disagreements per
  rule are shown in the journal and the overseer's view.
- **`use: {<rule>: engine}`:** the rule's tasks come from the engine and
  the scanner skips that task type; everything after the queue is
  unchanged. Back is the same setting; the engine's state (snapshots,
  cursor, attempts) only adds to what is there, so nothing migrates.
- **`canaryLabel`:** a rule the engine has taken applies only to items
  with the label, so a few chosen issues and PRs go first while the rest
  stay on the scanners.

**What says it works:** every task records `decidedBy: legacy|engine`;
the overseer's view counts, per rule, tasks queued, dropped by a
precondition, given up, and shadow disagreements; a rule's outcomes (runs,
PRs opened, give-ups) are compared over a week before and after it moves.

**Not partial by loop:** issues on one path and PRs on the other is too
coarse, since PR care is rules of very different risk.

## Plan

Each phase ships behind its own setting, defaulting to today's
behaviour, and goes to the smaller overseers (`overseer-repo-agent`,
`overseer-agent-sandbox`) before staging `overseer-kcc`, then
production.

**Phase 0, fixes in today's watch** (no decision changes; each a small
PR, each verified on staging `overseer-kcc`):

| PR | what | done when |
|---|---|---|
| 0a | the knobs: remove `--poll-interval`, `--labels`, the Overseer's `repo.reviewMode` (reviews follow the `*/review` label) and `chores.include`/`exclude`; honour the `dryrun` mode; name `pollInterval` the run's lifetime | the Overseer's settings do what they say, or are gone |
| 0b | one definition of a bot review; the dispatcher's comment says its ordering | — |
| 0c | preconditions: PR tasks record their head, issue tasks that no PR is linked; the dispatcher drops a stale task with a reason, not counted as an attempt (setting `preconditions`) | on KCC, drops are visible and no wasted CI run follows a comments run's push |
| 0d | the HTTP cache on the overseer's volume, surviving the restart | the first pass after a restart is mostly 304s |
| 0e | `attempts` from the processed tasks, seeded from the comment counts, replacing the comment counting (setting `attempts`) | the give-ups happen at the same counts as before |

**Phase 1, one scan** (behind `scan: shared`): the scanners read one set
of listings (deduplicated, incremental with `since`), the cursor and
snapshots persisted, PR details from one GraphQL query. The scanners'
decisions are unchanged; only where their data comes from. Done when, on
KCC, calls per pass drop and the tasks enqueued match a week before.

**Phase 2, the engine in shadow:**

- 2a: the model and the prebaked rules (2a-1, #1996); the model's gaps
  closed and the files exact (2a-2a: `from`, `any-of`, `closes`,
  `last-run`, `fanout-held`, resets, placeholders; adoption folded into
  fix, #1999); the evaluator (2a-2b: `Set.Decide` over an `Item` of facts
  and an `Env`, no calls, #2000); replay (2a-3: the scanners record each
  decision with its facts behind `rulesRecord`, and `factory watch rules
  replay` runs the rules on the records). In all:
  the engine, the predicates and groups, and the built-in rules as
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

**Phase 3, rule by rule** (`use: {<rule>: engine}`, the canary label
first, then all items; staging, then production), cheapest to get wrong
first:

1. `conflict`: rare, and a rebase is easy to check.
2. `ci`, then `review`: replies and pending reviews.
3. `comments`.
4. `ready`, PR adoption (shipped off), label inheritance, inactivity: writes people see.
5. `fix`, `workflow`: they open PRs and spend the most.

Each rule moves when its shadow week is clean, and moves back on any
regression. Done when every rule is on the engine.

**Phase 4, delete:** a rule's scanner code goes once the rule has run on
the engine in production for two weeks without moving back; the
scanners go with their last rule.

**Phase 5, the repository's rules:** `.agents/rules/*.yaml` replacing,
turning off or adding to the prebaked rules, within the global gates and
floors, and `.agents/watch.yaml` for its settings; then the board's auto
rules on the same predicates.

Phase 0 is worth doing whether or not the rest follows: it fixes what
the read turned up, saves quota and wasted runs, and makes today's
give-ups sturdier.

## Open questions

- **A repository's rules and the bot pool:** a rule runs as a role of
  the overseer's bots. Does a repository get to say which, or only the
  overseer's operator?
- **Workflows on a timer:** a workflow issue re-runs every cooldown. As
  a rule, is that `updated-since-run: false` plus a cooldown, or a
  `schedule` on the rule?
- **Events:** sources are listings. GitHub's events or a webhook would
  let rules fire on `labeled`, `review_requested`, `check_run.completed`,
  which the board's auto rules would want too. Out of scope until a
  GitHub App exists (design/github-app-identity.md).
- **Fairness:** strict priority starves low phases under load. Does the
  engine add a per-rule or per-item share?
