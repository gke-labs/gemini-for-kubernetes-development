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

> **Status.** The rules describe what today's watch does and can be
> evaluated and replayed against it (`factory watch rules replay`), but the
> watch still acts through its scanners. A rule takes over only once its
> replay and shadow-mode comparisons agree with today's code (the design's
> Phase 3).

---

## Where rules live

| source | where | what it can do |
|---|---|---|
| factory | `factory/pkg/watch/rules/<name>.yaml`, embedded in the binary | the prebaked rules: today's watch, out of the box |
| a repository | `.agents/rules/<name>.yaml` on its default branch (Phase 5) | replace a factory rule of the same name, add a rule of a new name, or turn a factory rule on or off |
| the overseer | the Overseer's `spec.rules.disabled` (with the engine) | the operator's emergency switch: turn rules off |

A rule's file is named after it: `fix.yaml` holds the rule named `fix`.

List the rules, with a checkout's `.agents/rules/` laid over factory's:

```sh
factory watch rules                 # table: NAME ON FROM GROUP RUN AS POLICY SOURCE ENABLED
factory watch rules -o yaml         # every rule, whole
factory watch rules --dir .         # with this checkout's .agents/rules/ laid over
```

---

## Anatomy of a rule

`fix.yaml`, annotated:

```yaml
name: fix                    # unique; names the file
description: Fix an issue the overseer is pointed at and open the PR
on: issue                    # the kind of item the rule looks at
from: [labelled, assigned, opened-by-watcher]   # the listings its items come from: any of them
when:                        # the condition: every predicate set must hold
  fanout-held: false         #   no fan-out holds it back
  linked-pr: none            #   no open PR closes it or is linked to it
  workflow: none             #   its body names no workflow (the workflow rule's)
  updated-since-run: true    #   changed since this rule's last run that succeeded
run:                         # the action
  recipe: fix                #   run the fix recipe in the issue's sandbox
  apply: open-pr             #   and apply its result with open-pr when it ends
  inputs:
    draft: "false"
    labels: "{{pr-labels}}"  #   the trigger, the configuration's labels and the issue's
as: coder                    # the bot role it runs as
policy:
  dedupe: item               # one queued task per item
  cooldown: {success: 10m, failure: 20m}   # how long after a run, by its outcome
  phase: 3                   # its tasks' order in the queue, after the priority label
expect: [open, no-linked-pr] # checked again when the task is about to start
```

Read aloud: *for an issue that is labelled, assigned to the bot pool, or
opened by the overseer's own login; not held back by a fan-out; with no open
PR; naming no workflow; and changed since the last fix that succeeded - once
the last fix is 10 minutes old (20 after a failure), queue the fix recipe as
a coder bot, open the PR with the issue's labels when it ends, and drop the
task if the issue closes or a PR appears before it starts.*

---

## Top-level fields

| field | values | meaning |
|---|---|---|
| `name` | string | Unique in the set; the file is `<name>.yaml`. |
| `description` | string | What the rule is for, in a line. |
| `on` | `issue`, `pr`, `schedule`, `event` | The kind of item. `schedule` is an `.agents/` agent definition whose schedule is due; `event` is something the watch derives rather than lists (today `linked-issue-closed`). |
| `enabled` | `true`, `false`; unset is on | A rule shipped off says `enabled: false`. See [Turning rules on and off](#turning-rules-on-and-off). |
| `from` | `labelled`, `assigned`; on issues also `opened-by-watcher` | The listings the rule's items come from, any of them. See [`from`](#from-the-listings). |
| `group` | `{name: <group>, order: <n>}` or `{name: <group>, last: true}` | Makes the rule one of an ordered, exclusive set on an item. See [Groups](#groups). |
| `when` | predicates | The condition. See [`when`](#when-the-predicates). |
| `run` | a recipe, or GitHub writes | The action. See [`run`](#run-the-action). |
| `as` | `coder`, `agent`, `reviewer`, `author`, `watcher` | The role the action runs as. A recipe runs as `coder`, `agent`, `reviewer` or `author` (the PR's own bot, from the pool); GitHub writes are the `watcher`'s (its own token), and `as` may be left out for them. |
| `policy` | see below | Dedupe, cooldown, give-up and ordering. See [`policy`](#policy). |
| `expect` | `open`; `head` (PRs); `no-linked-pr` (issues) | Facts the queued task is checked against when the dispatcher is about to start it: the item still open, the PR's head unchanged, still no linked PR. A task whose facts no longer hold is dropped, and a drop is not an attempt. |

---

## `from`: the listings

| listing | items |
|---|---|
| `labelled` | open items with the trigger label (`overseer`, or the overseer's own) |
| `assigned` | open items assigned to a bot of the pool |
| `opened-by-watcher` | open issues the watcher's own login opened (a workflow's, a chore's, a fan-out's children) |

An item is looked at only if it came from one of the rule's listings. The
listings are what the watch's one scan lists, so they are also what keeps a
rule from looking at every open item. A rule without `from` must narrow the
listing with a predicate instead: `labels` (`any` or `all`), `assignee:
pool|owner`, `author` or `opened-by`; in every `any-of` set, if not beside
them. A rule that narrows nothing is an error.

---

## `when`: the predicates

Every predicate set must hold. A predicate left out is not checked.

### Any item

| predicate | values | holds when |
|---|---|---|
| `labels` | `{any: [...], all: [...], none: [...]}` | the item has any of `any`, all of `all` and none of `none`. `{{trigger}}` is the trigger label; `*/name` is `name` under any of the overseer's prefixes (`overseer/review`, `kcc/review`). Case-insensitive. |
| `assignee` | `pool`, `not-pool`, `none`, `owner` | a bot of the pool is assigned; none is; nobody is; the owner is |
| `author` | `pool`, `trusted` | a bot of the pool opened it; a trusted account did (a member, or a trusted login) |
| `stopped` | `true`, `false` | it has the stop label (`*/stop`) |
| `updated-since-run` | `true`, `false` | it was updated after this rule's last run that succeeded; a failed run is left out, so a failure retries after its cooldown |
| `attempts` | `{max: <n>, since: <events>, rule: <name>}` | the rule's runs on the item (or `rule`'s) since the latest of `since` are fewer than `max` (1-10) |
| `last-run` | `failed` | the rule's last run on the item failed; on a PR, on its current head |
| `any-of` | a list of predicate sets | at least one set holds (see [`any-of`](#any-of)) |

`since` is one event or a list, and the count starts at the latest of them:

| event | when |
|---|---|
| `head` | the PR's head last changed |
| `human` | a trusted human last commented |
| `pause` | the stop label was last taken off |

### Issues

| predicate | values | holds when |
|---|---|---|
| `linked-pr` | `none`, `open` | no open PR closes or is linked to the issue; one does (a closing reference such as "Fixes #N", or GitHub's sidebar link) |
| `workflow` | `none`, `any` | the issue's body names no workflow definition; it names one |
| `opened-by` | `watcher` | the watcher's own login opened it |
| `fanout` | `none`, `parent`, `child` | it is not in a fan-out; it is a fan-out's parent; it is a child |
| `fanout-held` | `true`, `false` | a fan-out holds it back: it is a parent, or a child the fan-out has not labelled yet |

### Pull requests

| predicate | values | holds when |
|---|---|---|
| `checks` | `failing`, `pending`, `green` | its head's checks |
| `mergeable` | `true`, `false` | GitHub says it merges (unknown while GitHub computes it: neither) |
| `draft` | `true`, `false` | it is a draft |
| `merge-queue` | `true`, `false` | it is in the merge queue |
| `feedback` | `new-trusted`, `none` | a trusted human's feedback is waiting for an answer; none is |
| `closes` | `{labels: {any, all, none}}` | the labels of the issues it closes, between them, match |
| `reviewed-at-head` | `true`, `false` | the automated reviewer reviewed its head |
| `changes-requested` | `true`, `false` | that review asked for changes |
| `approved` | `true`, `false` | it is approved (or LGTM'd) |
| `inactive-for` | a duration, or `{{pr-inactivity-timeout}}` | no trusted human has acted on it for that long (the overseer's `prInactivityTimeout`; 0 is never) |

### Schedules and events

| on | predicate | holds when |
|---|---|---|
| `schedule` | `due: true` | the agent definition's schedule is due |
| `event` | `event: linked-issue-closed` | the sandbox GC confirmed an issue closed (an issue linking it gets the event) |

A rule on `event` needs `when.event`.

### `any-of`

`any-of` holds when one of its sets does, besides every other predicate:

```yaml
when:
  author: pool
  any-of:
    - feedback: new-trusted        # new trusted feedback,
    - last-run: failed             # or the last run on this head failed
  attempts: {since: head, max: 3}
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
| `inputs` | the recipe's inputs, by name; values may use `{{trigger}}`, and on issues `{{pr-labels}}` |

The task is queued, and runs in the item's sandbox as the role `as` names.

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
| `{{pr-inactivity-timeout}}` | a duration | the overseer's `prInactivityTimeout` |
| `{{workflow-cooldown}}` | a duration | the cooldown the workflow definition declares (10m when it sets none) |

---

## `policy`

| field | values | meaning |
|---|---|---|
| `dedupe` | `item`, `head` | `item`: one task per item and rule at a time. `head`: once per PR head; a run on the head that failed may run again. |
| `cooldown` | see below | how long after its last run the rule waits before it runs on the item again |
| `give-up` | see below | when the rule stops trying and stops the item |
| `phase` | a number | its tasks' order in the queue, after the priority label: lower first |
| `identity-pin` | `true`, `false`; unset is on | run as the bot that owns the item's sandbox |
| `before` | a group's name | evaluated ahead of that group, every time; the group's rules see its label writes (`inherit-labels` before `pr-care`) |
| `skip-cooldown` | `true` | its task is queued inside the cooldown of the rule whose task it is (`nudge` stepping a workflow at once) |

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
  then: stop                      # the stop label
  comment: Pausing after 3 attempts to fix the failing checks.
```

When the rule's other conditions hold but it has run `attempts` times since
`since`, it gives up instead of running: the stop label and the comment.
The stop label then keeps every rule off the item until a person takes it
off (which is a `pause` event, and resets the counts that name it).

---

## Groups

A group is an ordered, exclusive set of rules on one item: the first rule of
the group whose condition and policy hold is taken, and the rest are not
looked at. While a task of the item is queued or running, a group takes
nothing.

The prebaked `pr-care` group, in order:

| order | rule | does |
|---|---|---|
| 10 | `comments` | answers new trusted feedback |
| 20 | `conflict` | rebases a PR that conflicts |
| 30 | `ci` | fixes failing checks |
| 40 | `review` | reviews a green PR that wants one |
| 50 | `ready` | hands a ready PR to the humans |
| 60 | `ready-unreview` | takes `*/review` off a PR already handed over |

A group's rules look at one kind of item; their orders are unique, and at
most one is `last` (taken when no other holds). A rule ordered `before` a
group (`inherit-labels`) is not in it.

---

## How a decision is made

For one item, the engine (`rules.Set.Decide`) decides from facts already
gathered - it makes no GitHub calls:

1. **Gates on every rule.** An issue or PR with the stop label gets nothing,
   and neither does a PR in the merge queue.
2. **The rules that apply.** The enabled rules of the item's kind, each only
   if the item came from one of its `from` listings.
3. **Before the groups.** The rules ordered `before` a group decide first;
   their label writes are what the rest see. A stop label written here ends
   the decision.
4. **Rules of no group** decide each on its own. One that stops the item
   (`inactive`) ends the decision.
5. **Each group** takes its first rule that holds, unless a task of the item
   is active.

A single rule decides:

- nothing while its own task for the item is queued or running;
- nothing unless `when` holds;
- to **give up**, when `give-up`'s count is reached;
- nothing while `attempts` is used up, the head is already done (`dedupe:
  head`), or the cooldown holds (unless `reset-on` or `skip-when` lifts it);
- otherwise to **run**: queue the recipe, or make the writes (if they change
  something).

---

## Turning rules on and off

A rule is on unless something turns it off. Each source overrides the one
before it:

1. **factory's file:** `enabled: false` ships a rule off (`inactive`: no
   timeout by default; `stop`: the stop label is a gate, not a rule;
   `adopt-pr`: a safety net held in reserve).
2. **a repository's file** of the same name with nothing but its name and
   `enabled` turns factory's rule off - or on - keeping the rest of it:

   ```yaml
   # .agents/rules/review.yaml
   name: review
   enabled: false
   ```

   A repository's file that is a whole rule replaces factory's rule of that
   name, or adds a rule of a new name.
3. **the overseer** (`spec.rules.disabled: [review]`): the operator's
   emergency switch, which only turns rules off.

`factory watch rules --dir <checkout>` shows whose each rule is (SOURCE) and
whose its enabled is (`false (repo)`).

---

## Writing a rule

A repository rule that asks for a review of every green bot PR labelled
`needs-review`, once per head:

```yaml
# .agents/rules/review-on-label.yaml
name: review-on-label
description: Review a green bot PR labelled needs-review
on: pr
from: [labelled, assigned]
when:
  author: pool
  labels: {all: [needs-review]}
  checks: green
  reviewed-at-head: false
run:
  recipe: review
  apply: submit-review
as: reviewer
policy:
  dedupe: head
expect: [open, head]
```

Rules are checked strictly when they load:

- an unknown field, a predicate of another kind of item (`checks` on an
  issue rule), or a value outside a predicate's (`checks: red`) is an error;
- `run` is a built-in recipe or GitHub writes, not both, and not neither;
- a recipe runs as `coder`, `agent`, `reviewer` or `author`; writes are the
  watcher's;
- a rule on issues or PRs narrows its listing (`from`, or a narrowing
  predicate);
- the floors: `attempts.max` and `give-up.attempts` at most 10, a cooldown
  at least 1m - a rule cannot loop on an item;
- a placeholder the engine does not fill in where it is used is an error;
- in a set: unique names; a group's rules on one kind, unique orders, at
  most one `last`; `before` names a group of the same kind.

---

## Comparing the rules with today's watch

The rules take over from today's scanners one at a time, and only once they
decide the same. To compare them:

1. **Record.** With the Overseer's `spec.rulesRecord: true` (`factory watch
   --rules-record <file>`), the issue and PR scanners record every decision
   with the facts it was made on, in
   `/workspaces/.cache/factory-watch/decisions.jsonl` on the overseer's
   volume. Recording reads what the scanners already fetched - no extra
   GitHub calls - and changes nothing they decide. A decision a gate outside
   the rules held back (a busy sandbox, one needing a person) is recorded as
   such.
2. **Replay.**

   ```sh
   kubectl exec -n overseer-kcc overseer-kcc -- \
     cat /workspaces/.cache/factory-watch/decisions.jsonl > decisions.jsonl
   factory watch rules replay decisions.jsonl          # summary, by rule, first differences
   factory watch rules replay decisions.jsonl -o json  # every difference, with its record
   factory watch rules replay decisions.jsonl --dir .  # with a checkout's rules laid over
   ```

   Every record where the rules decide something else than the scanner did
   is a difference to explain: a rule file to fix, or a fact recorded wrong.

Next, shadow mode runs the same comparison live, and the overseer's view
shows the disagreements per rule (the design's Phase 2b).

---

## The prebaked rules

| rule | on | does |
|---|---|---|
| `fix` | issue | fixes an issue the overseer is pointed at and opens the PR |
| `workflow` | issue | takes the next step of the workflow an issue names |
| `stop` | issue | (off: the stop label is an engine gate) drops an item's queued tasks |
| `inherit-labels` | pr | copies the closing issues' labels onto the PR, before `pr-care` |
| `adopt-pr` | pr | (off) labels a bot PR no listing finds whose closing issue is the overseer's |
| `inactive` | pr | (off: no timeout by default) pauses a PR nobody looked at |
| `comments` ... `ready-unreview` | pr | the `pr-care` group, above |
| `chore` | schedule | runs a scheduled agent on the repository |
| `nudge` | event | steps a workflow when an issue it links closes |

Each file starts with a `# Mirrors` comment naming the code of today's watch
it describes.
