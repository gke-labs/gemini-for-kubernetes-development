# Watch rules guide

The overseer's watch (`factory watch`) decides what to work on: which issue to
fix, which PR needs its comments answered, its CI fixed, a rebase, a review,
or handing to a human. The watch rules write those decisions down as data:
one YAML file per rule, each a condition on an item and the action taken on
it.

This guide is the reference for a rule file: every field, what it means, and
how a rule is evaluated. Why the watch is moving to rules, and the plan for
getting there without changing what it does, is in
[design/watch-rules.md](../design/watch-rules.md).

> **Status.** The rules engine decides every rule; the scanners list the
> items, read their facts and carry out what the engine decides. The
> overseer's only say is turning rules off (`spec.rules.disabled`).

---

## Where rules live

| source | where | what it can do |
|---|---|---|
| factory | `factory/pkg/watch/rules/<name>.yaml`, embedded in the binary | the prebaked rules: today's watch, out of the box |
| a repository | `.agents/watch/<name>.yaml`, read once when the watch starts, at the Overseer's `spec.repoRecipe.gitCommit`, when its `spec.repoRecipe.mode` is replace or augment (`factory watch rules --dir` lays a checkout's over factory's for listing, explain and replay) | replace a factory rule of the same name, turn one on or off, or add a rule of a new name (augment: new names only), whose recipe may be the repository's own |
| the overseer | the Overseer's `spec.rules.disabled` | the operator's emergency switch: turn rules off |

A rule's file is named after it: `fix.yaml` holds the rule named `fix`.

List the rules, with a checkout's `.agents/watch/` laid over factory's:

```sh
factory watch rules                 # table: NAME ON ORDER RUN AS POLICY SOURCE ENABLED
factory watch rules -o yaml         # every rule, whole
factory watch rules --dir .         # with this checkout's .agents/watch/ laid over
factory watch rules --flow          # how the PR rules are applied, top to bottom (or: --flow issue)
factory watch rules explain <URL>   # one PR or issue through the rules, rule by rule
factory watch rules decide -f <dir> # items written by hand (scenarios) through the rules
```

---

## Anatomy of a rule

`fix.yaml`, annotated:

```yaml
name: fix                    # unique; names the file
description: Fix an issue the overseer is pointed at and open the PR
on: issue                    # the kind of item the rule looks at
order: 20                    # its place among the issue rules; a recipe rule takes the item
when:                        # the condition: every predicate set must hold
  fanout: false              #   not a fan-out's parent (the fan-out controller's)
  linked-pr: none            #   no open PR says it closes it
  connected-pr: none         #   nor is one linked to it (its timeline, read only if needed)
  workflow: none             #   its body names no workflow (the workflow rule's)
run:                         # the action
  recipe: fix                #   run the fix recipe in the issue's sandbox
  apply: open-pr             #   and apply its result with open-pr when it ends
  inputs:
    draft: "false"
    labels: "{{pr-labels}}"  #   the trigger, the configuration's labels and the issue's
policy:
  dedupe: update             # once per change: not again until the issue changes after a fix that succeeded
  cooldown: {success: 10m, failure: 20m}   # how long after a run, by its outcome
  phase: 3                   # its tasks' order in the queue, after the priority label
```

Read aloud: *for an issue the overseer tracks (see [Which items a rule
sees](#which-items-a-rule-sees)); not held back by a fan-out; with no open
PR; naming no workflow; and changed since the last fix that succeeded - once
the last fix is 10 minutes old (20 after a failure), queue the fix recipe (a coder
bot runs it) and open the PR with the issue's labels when it ends.*

A queued task is checked again just before it starts, and dropped (not
counted as an attempt) if the item closed or was stopped, or the facts it
was decided on moved: a PR's head for ci, conflict and review, a PR
linked to the issue for fix. A rule does not say which; see
[Before a task starts](#before-a-task-starts).

---

## Top-level fields

| field | values | meaning |
|---|---|---|
| `name` | string | Unique in the set; the file is `<name>.yaml`. |
| `description` | string | What the rule is for, in a line. |
| `on` | `issue`, `pr` | The kind of item. Scheduled chores and the linked-issue nudge are not rules (below). |
| `enabled` | `true`, `false`; unset is on | A rule shipped off says `enabled: false`. See [Turning rules on and off](#turning-rules-on-and-off). |
| `order` | a number above 0 | The rule's place among the rules on its kind of item, lower first. A rule without one goes after those with one; a rule that takes the item needs one, and no two rules on a kind share one. See [Order](#order). |
| `exclusive` | `true`, `false`; unset: a recipe rule `true`, GitHub writes `false` | Whether the rule takes the item: of the rules that do, only the first that holds is decided. See [Order](#order). |
| `when` | predicates | The condition. See [`when`](#when-the-predicates). |
| `run` | a recipe, or GitHub writes | The action. See [`run`](#run-the-action). |
| `policy` | see below | Dedupe, cooldown, give-up and ordering. See [`policy`](#policy). |

---

## Which items a rule sees

The watch tracks a fixed set of open items, from the overseer's settings,
not from the rules:

| tracked because | items |
|---|---|
| labelled | open issues and PRs with the trigger label (`overseer`, or the overseer's own) |
| assigned | open issues and PRs assigned to a bot of the pool |
| opened by the watcher | open issues the watcher's own login opened (a workflow's, a chore's, a fan-out's children) |

A rule sees every tracked item of its `on` kind, and `when` narrows that:
`labels`, `assignee`, `author`, `opened-by`, in `any-of` if need be. A rule
cannot widen the set; an item the overseer should look at gets the trigger
label or a pool assignee.

---

## `when`: the predicates

Every predicate set must hold. A predicate left out is not checked.

### Any item

| predicate | values | holds when |
|---|---|---|
| `labels` | `{any: [...], all: [...], none: [...]}` | the item has any of `any`, all of `all` and none of `none`. `{{trigger}}` is the trigger label; `*/name` is `name` under any of the overseer's prefixes (`overseer/review`, `kcc/review`). Case-insensitive. |
| `assignee` | `pool`, `not-pool`, `none` | a bot of the pool is assigned; none is; nobody is |
| `author` | `pool` | a bot of the pool opened it |
| `last-run` | `failed` | the rule's last run on the item failed; on a PR, on its current head |
| `any-of` | a list of predicate sets | at least one set holds (see [`any-of`](#any-of)) |

`when` is about the item: what it is and what has happened to it. How
often the overseer acts on it, and when it stops trying, is `policy`'s
(dedupe, cooldown, give-up). A predicate about the overseer's own runs is
in `when` only when it is a reason to run (`last-run: failed`), never a
limit. There is no `stopped` or `merge-queue`: a stopped item and a PR in
the merge queue get nothing from any rule.

### Issues

| predicate | values | holds when |
|---|---|---|
| `linked-pr` | `none`, `open` | no open PR closes the issue; one does (a closing reference such as "Fixes #N", from the PR listing the scan already has) |
| `connected-pr` | `none`, `open` | no open PR is linked to the issue, by a closing reference or GitHub's sidebar link; one is. Read from the issue's timeline, one call per issue, so only when a rule that could otherwise step asks for it |
| `workflow` | `none`, `any`, `missing` | the issue's body names no workflow definition; it names one it can load; it names one that cannot be loaded (missing, or not a workflow it can run) |
| `opened-by` | `watcher` | the watcher's own login opened it |
| `fanout` | `true`, `false` | it is a fan-out's parent (the fanout label): the fan-out controller's, never fixed itself. A child is an ordinary issue, created when it is to be worked on |

### Pull requests

| predicate | values | holds when |
|---|---|---|
| `checks` | `failing`, `pending`, `green` | its head's checks |
| `mergeable` | `true`, `false` | GitHub says it merges (unknown while GitHub computes it: neither) |
| `draft` | `true`, `false` | it is a draft |
| `feedback` | `new-trusted`, `none` | a trusted human's feedback is waiting for an answer; none is |
| `closes` | `{labels: {any, all, none}}` | the labels of the issues it closes, between them, match |
| `reviewed-at-head` | `true`, `false` | the automated reviewer reviewed its head |
| `changes-requested` | `true`, `false` | that review asked for changes |
| `approved` | `true`, `false` | it is approved (or LGTM'd) |
| `inactive-for` | a duration, or `{{pr-inactivity-timeout}}` | no trusted human has acted on it for that long (the overseer's `prInactivityTimeout`; 0 is never) |

### Chores and the nudge are not rules

- **Scheduled chores:** the chore scheduler runs an `.agents/<name>.md`
  whose `schedule:` is due, as the `agent` recipe. The Overseer's
  `chores.mode` (enabled, disabled, dryrun) switches them.
- **The linked-issue nudge:** when the sandbox GC confirms an issue closed,
  a workflow on an open issue that links it takes its next step at once,
  inside the `workflow` rule's cooldown. The step is a workflow task: just
  before it starts it is held to the gates, and turning the `workflow`
  rule off drops it.

### `any-of`

`any-of` holds when one of its sets does, besides every other predicate:

```yaml
when:
  author: pool
  any-of:
    - feedback: new-trusted        # new trusted feedback,
    - last-run: failed             # or the last run on this head failed
```

A set may hold several predicates, all of which must hold for it:

```yaml
  any-of:
    - reviewed-at-head: true
      changes-requested: false
    - labels: {none: ["*/review"]}
      closes: {labels: {none: ["*/review"]}}
```

It is one level deep (a set has no `any-of` of its own) and needs two sets
or more.

---

## `run`: the action

A rule runs a recipe or makes GitHub writes, not both.

### A recipe

| field | meaning |
|---|---|
| `recipe` | a built-in recipe (`fix`, `workflow`, `agent`, `care-comments`, `care-rebase`, `care-ci`, `review`, ...) |
| `apply` | the action its task output is applied with when the run ends (`open-pr`, `post-replies`, `submit-review`); none leaves the output for a person |
| `inputs` | the recipe's inputs, by name; values may use `{{trigger}}`, on issues `{{pr-labels}}`, and on PRs `{{review-instructions}}` |
| `claim` | issues: `true` assigns the issue to the task's bot when it starts, in the watcher's place (the prebaked fix and workflow) |
| `acknowledge` | PRs: `feedback` marks the feedback the task answers as seen (👀) when it starts, and resolved (👍) or failed (😕) when it ends (the prebaked comments) |
| `start-comment` | a comment the watcher posts on the item when the task starts |

`claim`, `acknowledge` and `start-comment` are read from the rule again
just before the task starts, so a task queued earlier does what the rule
says now. The task is queued and runs in the item's sandbox. A rule does not say
which bot runs it: that is the recipe's and the overseer's (see [Who runs
it](#who-runs-it)).

### Who runs it

The bot a task runs as follows from what it runs, not from the rule: the
recipe says, `as:`.

| recipe `as` | who |
|---|---|
| `author` | the PR's own bot: the pool bot assigned to it, else its author (its branch is in that bot's fork). The care recipes |
| `coder`, `agent`, `reviewer` | a bot of that pool of factory's roles (an overseer's `spec.roles`): the one pinned to the item's sandbox, then a pool bot assigned to it, else any; `agent` falls back to coder when there is no agent pool. fix and the other issue recipes coder, review reviewer, workflow agent |

A repository's recipe must say it, and may say only `author` or `coder`
(any in replace mode). A task with no recipe (a scheduled chore) runs as
its type's role. GitHub writes are always the watcher's own.

### GitHub writes

Made by the watcher, and only what changes something: a label the item
already has is not added again, and a write rule whose writes would change
nothing does not fire.

| field | values |
|---|---|
| `label`, `unlabel` | label names; `{{trigger}}`; `*/name` (added in the overseer's own spelling, removed in either); on a PR `{{closing-issue-labels}}` (the closing issues' labels, but ready-for-human) |
| `assign`, `unassign` | a login; `{{watcher}}`; `{{pool}}` (the pool's bots assigned to it); on a PR `{{closing-issue-assignees}}` (the closing issues' human assignees) |
| `comment` | a comment to post |
| `stop` | `{comment: ...}`: the stop label, and the comment saying why |
| `drop` | `true`: drop the item's queued tasks |

### Placeholders

Placeholders are a closed set, checked where they are used: a name the
engine does not fill in there is an error when the rule loads.

| placeholder | where | is |
|---|---|---|
| `{{trigger}}` | labels, inputs | the trigger label |
| `{{closing-issue-labels}}` | PR label writes | the labels of the issues the PR closes, but ready-for-human |
| `{{closing-issue-assignees}}` | PR assign | the human assignees of the issues the PR closes |
| `{{pool}}` | assign, unassign | the pool's bots assigned to the item |
| `{{watcher}}` | assign, unassign | the watcher's own login |
| `{{pr-labels}}` | issue recipe inputs | the trigger, the configuration's additional labels and the issue's labels (what `--pr-labels` resolves) |
| `{{review-instructions}}` | PR recipe inputs, whole | the review instructions in the PR's body, else in the issues it closes (a `Review instructions` heading), given to the recipe as its `--instruction` flags; the prebaked review asks for them |
| `{{pr-inactivity-timeout}}` | a duration | the overseer's `prInactivityTimeout` |
| `{{workflow-cooldown}}` | a duration | the cooldown the workflow definition declares (10m when it sets none) |

---

## `policy`

| field | values | meaning |
|---|---|---|
| `dedupe` | `head`, `update` | Every rule decides nothing while its task for the item is queued or running; `dedupe` narrows that. `head`: once per PR head. `update`: once per change to the item, not again until it is updated after the run. A failed run does not count, so it retries after its cooldown. |
| `cooldown` | see below | how long after its last run the rule waits before it runs on the item again |
| `give-up` | see below | when the rule stops trying and stops the item |
| `phase` | a number | its tasks' order in the queue, after the priority label: lower first |
| `revisit` | `every-scan` | look at the item again every scan once the rule is decided, rather than skipping it until it changes: what the rule waits on need not change the item (the prebaked conflict: resolving it upstream does not touch the PR) |

### `cooldown`

```yaml
cooldown:
  success: 2h                     # after a run that succeeded
  failure: 20m                    # after one that failed (unset: none)
  reset-on: [head]                # a PR's new head ends the wait
  skip-when: {assignee: pool}     # no wait for a PR assigned to the pool explicitly
```

A duration is Go's (`10m`, `2h`), at least `1m`, or a named one
(`{{workflow-cooldown}}`). `skip-when` says `assignee`, and only that.

### `give-up`

```yaml
give-up:
  attempts: 3                     # 1-10
  since: [head, human, pause]     # counted since the latest of these
  failed: true                    # optional: count only the runs that failed (comments: new feedback is always answered)
  comment: Pausing after 3 attempts to fix the failing checks.
```

`since` is one event or a list, and the count starts at the latest of them:

| event | when |
|---|---|
| `head` | the PR's head last changed |
| `human` | a trusted human last commented |
| `pause` | the watcher last put the item on hold (its comment attaching the stop label) |

When the rule's `when` holds but it has run `attempts` times since `since`,
it gives up instead of running: the stop label and the comment. The stop
label then keeps every rule off the item until a person takes it off; no
run happens while it is on, so a count since `pause` starts again from
zero.

---

## Order

The rules on one kind of item are one list, walked in `order`. A rule
**takes the item** (`exclusive`; a recipe rule does unless it says
otherwise) or does not:

- of the rules that take the item, only the first that holds is decided,
  and none is while a task of the item is queued or running: one task at
  a time per item, in priority order;
- a rule that does not (label writes) decides on its own, and its label
  writes are what the rules after it see; one that writes the stop label
  ends the walk.

The prebaked PR rules, in order:

| order | rule | takes the item | does |
|---|---|---|---|
| 5 | `inherit-labels` | no | copies the closing issues' labels, so the rules after it see them |
| 7 | `inactive` | no | pauses an idle PR (the stop label ends the walk) |
| 10 | `comments` | yes | answers new trusted feedback |
| 20 | `conflict` | yes | rebases a PR that conflicts |
| 30 | `ci` | yes | fixes failing checks |
| 40 | `review` | yes | reviews a green PR that wants one |
| 50 | `ready` | yes (`exclusive: true`) | hands a ready PR to the humans |
| 60 | `ready-unreview` | yes (`exclusive: true`) | takes `*/review` off a PR already handed over |

On issues, `workflow-missing` (5) stops an issue whose workflow cannot be
loaded, which ends the walk; `workflow` (10) and `fix` (20) both take the item; their
predicates (`workflow: any` and `workflow: none`) keep them apart. Like a PR, an issue gets
one task at a time: while its fix or a workflow step is queued or running,
neither rule decides (an edited body naming a workflow waits for the fix). `ready`
writes labels but takes the item: it must not hold beside a care rule, nor
while a task runs.

Some facts cost a GitHub call per issue: `connected-pr` (the timeline),
and, for an issue naming a workflow, its definition (`workflow: any` and
`missing`) and its cooldown (`{{workflow-cooldown}}`). The scan reads
them only when a rule that would step on every other fact uses them, so a
rule of your own may use them as freely as factory's do. A fact that
cannot be read holds the issue for that pass.

---

## How a decision is made

For one item, the engine (`rules.Set.Decide`) decides from facts already
gathered - it makes no GitHub calls:

1. **Gates on every rule.** An issue or PR with the stop label gets nothing,
   and neither does a PR in the merge queue.
2. **The rules that apply.** The enabled rules of the item's kind (the item
   is one the watch tracks).
3. **In order.** The rules are walked in `order`. A rule that takes the
   item is decided only if none before it was and no task of the item is
   active; any other rule decides on its own, and its label writes are what
   the rules after it see. A stop label written by one (`inactive`, or
   inherited from a closing issue) ends the decision.

A single rule decides:

- nothing while its own task for the item is queued or running;
- nothing unless `when` holds;
- to **give up**, when `give-up`'s count is reached;
- nothing while the head is already done (`dedupe: head`), the item has not
  changed since (`dedupe: update`), or the cooldown holds (unless `reset-on` or `skip-when` lifts it);
- otherwise to **run**: queue the recipe, or make the writes (if they change
  something).

---

## Before a task starts

A decision is made at scan time; the task may wait in the queue behind
the concurrency limits. Just before it starts, the dispatcher has the
engine decide the task's rule again, on the item's facts read then, as
if the task were not there (not queued, its own run not counted). It
drops the task if:

- a gate now holds: the item closed, has the stop label, or is in the
  merge queue;
- a PR task's head moved since it was decided;
- the rule no longer decides it: feedback answered or gone, checks green,
  an earlier rule now takes the item, a give-up due, a fix whose issue
  got a linked PR;
- the rule is disabled (`spec.rules.disabled`).

A drop is not an attempt and starts no cooldown; the next scan decides
again. The reason is in the log and in the rule's `lastDrop` on the queue
API. A workflow step the linked-issue nudge queued is held to the gates
alone (the nudge steps inside the workflow's cooldown). A comments task
acknowledges its feedback (👀) only when it starts, so the feedback is
still new at this check. A task already running when the watch restarts
is followed by its run name, never dropped.

---

## Turning rules on and off

A rule is on unless something turns it off. Each source overrides the one
before it:

1. **factory's file:** `enabled: false` ships a rule off (no prebaked rule
   does today). `inactive` ships on but holds only with a timeout above 0
   (`--pr-inactivity-timeout`).
2. **a repository's file** of the same name with nothing but its name and
   `enabled` turns factory's rule off - or on - keeping the rest of it:

   ```yaml
   # .agents/watch/review.yaml
   name: review
   enabled: false
   ```

   A repository's file that is a whole rule replaces factory's rule of that
   name, or adds a rule of a new name, which runs as a rule task
   (`task-pr-<n>-rule-<name>.yaml`, `task-issue-<n>-rule-<name>.yaml`): its
   recipe, factory's or one in the repository's `.agents/recipes`, or its
   writes. `--dir` shows what a checkout's files do.
3. **the overseer** (`spec.rules.disabled: [review]`): the operator's
   emergency switch, which only turns rules off: nothing decides a disabled
   rule, and its queued tasks are dropped before they start.

`factory watch rules --dir <checkout>` shows whose each rule is (SOURCE) and
whose its enabled is (`false (repo)`).

---

## Writing a rule

A repository rule that asks for a review of every green bot PR labelled
`needs-review`, once per head:

```yaml
# .agents/watch/review-on-label.yaml
name: review-on-label
description: Review a green bot PR labelled needs-review
on: pr
order: 45                    # after review (40), before ready (50)
when:
  author: pool
  labels: {all: [needs-review]}
  checks: green
  reviewed-at-head: false
run:
  recipe: review
  apply: submit-review
policy:
  dedupe: head
```

Rules are checked strictly when they load:

- an unknown field, a predicate of another kind of item (`checks` on an
  issue rule), or a value outside a predicate's (`checks: red`) is an error;
- `run` is a built-in recipe or GitHub writes, not both, and not neither;
- the floors: `give-up.attempts` at most 10, a cooldown
  at least 1m - a rule cannot loop on an item;
- a placeholder the engine does not fill in where it is used is an error;
- a rule that takes the item has an `order`; `order` and `exclusive` are
  only for rules on issues and PRs;
- in a set: unique names; no two rules on one kind share an `order`.

---

## Seeing how the rules apply

`factory watch rules --flow [pr|issue]` draws a kind's rules
in the order the engine walks them: the gates, then each rule with its
`when`, what holds it back (`held`: dedupe and cooldown), its give-up, and
what it does. `●` marks the rules that take the item.

`factory watch rules explain <PR or issue URL>` walks one item through the
rules and says, for each, what it decides or why not:

```
PR o/r#7  (facts the overseer's record of 2026-10-19T13:00:00Z)
facts   author ada-coder-bot · labels overseer · head abc1234 · checks failing · mergeable true · …

  5   inherit-labels  ⏸  held: its writes would change nothing
  7   inactive        ✗  inactive-for: '{{pr-inactivity-timeout}}' (has no inactivity timeout set)
 10 ● comments        ✗  any-of: [{feedback: new-trusted}, {last-run: failed}]
 20 ● conflict        ✗  mergeable: false (has mergeable true)
 30 ● ci              ✓  run care-ci → post-replies
 40 ● review          –  skipped: ci took the item

decides ci run
```

`✓` decided, `✗` a predicate of its `when` failed (with what the item has),
`⏸` its policy held it back, `–` skipped (off, a task active, an earlier
rule took the item). By default the facts are read live, as today's scanner
reads them, with a GitHub client that refuses every write and the
overseer's settings from the flags (`--pool`, `--watcher`, `--trigger`,
`--inactivity`); the run history behind cooldowns and give-ups is the
overseer's, so `--record decisions.jsonl` explains the overseer's own last
record of the item instead, history and settings included. `-o json` gives
the record and the explanation.

`factory watch rules decide -f <scenario.yaml or directory>` does the same
for items written by hand: no GitHub, just facts. A scenario is a YAML
document with the item's facts (the record's `item` fields, in any case),
optionally the overseer's side (`env: {trigger, pool, watcher, inactivity,
now}`) and what the rules should decide (`expect: [ready run]`, `[]` for
nothing); one that decides otherwise fails the command. With `--dir`, a
checkout's rules are decided with: try a rule change on the cases that
matter before anything runs it. `factory/pkg/watch/rules/scenarios/` is
the suite CI runs.

---

## The record, and replaying it

1. **Record.** With the Overseer's `spec.rulesRecord: true` (`factory watch
   --rules-record <file>`), the issue and PR scanners record every decision
   with the facts it was made on, in
   `/workspaces/.cache/factory-watch/decisions.jsonl` on the overseer's
   volume, rotated daily into `decisions-<date>.jsonl` and kept 30 days (at
   most 5 GiB). Recording reads what the scanners already fetched - no extra
   GitHub calls. A decision a gate outside the rules held back (a busy
   sandbox, one needing a person) is recorded as such. The watch's queue
   API serves a summary beside the queue (`rules`): each rule's runs,
   give-ups and tasks dropped at start (with the last reason), and how many
   recorded decisions the engine decides the same on again (`consistent`;
   one that does not was decided on facts other than those recorded),
   which the overseer's view shows.
2. **Replay** - the rules, or a change to them, on what was recorded:

   ```sh
   kubectl exec -n overseer-kcc overseer-kcc -- \
     cat /workspaces/.cache/factory-watch/decisions.jsonl > decisions.jsonl
   factory watch rules replay decisions.jsonl          # summary, by rule, first differences
   factory watch rules replay decisions.jsonl -o json  # every difference, with its record
   factory watch rules replay decisions.jsonl --dir .  # with a checkout's rules laid over
   kubectl cp overseer-kcc/overseer-kcc:/workspaces/.cache/factory-watch ./record
   factory watch rules replay ./record                 # every day kept, in order
   factory watch rules explain <URL> --record decisions.jsonl
   ```

3. **Decided again at start.** Just before a queued task starts, the watch
   reads the item's facts again (what `factory watch rules explain <URL>`
   reads live) and asks the engine whether it still decides the task's
   rule, its own task taken out. A task it no longer decides is dropped:
   feedback that came while a ci task waited (comments, earlier, takes the
   PR), checks re-run and green, a head that moved, a PR linked to the
   issue, the trigger label taken off, the item closed or stopped, the rule
   disabled. A dropped task is journalled `Dropped` with the reason and
   served with the queue's drops; it is not an attempt and starts no
   cooldown, and the next scan decides anew. It costs one read of the item
   per task started. A workflow step the nudge queued (a linked issue
   closed) is held to the gates alone (stopped, closed): it steps the
   workflow inside its cooldown on purpose. A comments task acknowledges
   the feedback it answers (the 👀 reaction, the addressed time) when it
   starts, not when it is queued, so one dropped before it starts leaves
   the feedback for the next scan.

---

## The prebaked rules

| rule | on | does |
|---|---|---|
| `fix` | issue | fixes an issue the overseer is pointed at and opens the PR |
| `workflow-missing` | issue | stops an issue whose workflow definition cannot be loaded, with a comment saying so |
| `workflow` | issue | takes the next step of the workflow an issue names (held while an open PR closes the issue) |
| `inherit-labels` | pr | copies the closing issues' labels onto the PR, ahead of the rules that take it |
| `inactive` | pr | (no timeout by default, so it never holds) pauses a PR nobody looked at |
| `comments` ... `ready-unreview` | pr | PR care, in [order](#order) |

Each file starts with a `# Mirrors` comment naming the code of today's watch
it describes.
