# Plans and issue graphs

From an issue to a reviewed plan, then to one PR or a graph of sub-issues
(each its own PR, or its own plan and graph), steered with labels. The
rules and recipes below ship with factory (`factory watch rules` lists
them); the design is in [issue graphs](../design/issue-graph.md) and
[plans as graphs](../design/issue-graph-plans.md).

`<t>` is the trigger label: `overseer` on an Overseer, `factory` by
default for `factory watch` (`triggerLabel` in `.factory.cfg`).

```
#100 ──<t>/plan──> plan comment (v1, v2...) ──<t>/implement──┬─ one-PR plan ──> fix-with-plan ──> PR "Fixes #100"
                                                             └─ Breakdown ───> <t>/graph ──> sub-issues, each:
                                                                   then: fix  ──> fix ──> PR "Fixes #101"
                                                                   then: plan ──> <t>/plan on the sub-issue (a plan, maybe a graph of its own)
                                                                   then: ask  ──> <t>/needs-human, for a person
```

## 1. Plan: `<t>/plan`

A maintainer labels the issue `<t>/plan`. The `plan` rule runs the `plan`
recipe in the issue's sandbox, as a coder bot: it reads the issue, its
comments and the code, and writes the plan - Summary, Approach, Steps,
Risks, Test plan - posted as **one comment** with a hidden key (`plan`).
When the work is too big for one PR, the plan ends with a **Breakdown**
(section 4). On success the watcher adds `<t>/planned`; the command label
comes off when the task ends, whatever happened.

To refine it, comment on the plan and apply `<t>/plan` again: the next run
revises the same comment (v2, v3...) against the comments posted since,
says what changed, and keeps the ids of the Breakdown's steps.

Only a plan comment by a pool bot or a maintainer (owner, member,
collaborator, or a login the overseer trusts) counts. Anyone else's comment
carrying the marker is ignored, by every rule and recipe that reads the
plan.

`fix` leaves an issue carrying `<t>/plan` or `<t>/planned` alone, even with
the trigger label on: only `implement` turns a planned issue into code.
Take `<t>/planned` off to hand it back to plain `fix`.

## 2. Approve: `<t>/implement`

Labelling takes triage access, so the label is the maintainer's approval.
What happens depends on the plan's shape, a fact the watcher reads from the
plan comment when the label is there (`plan: none | pr | graph` in the
[rules guide](watch-rules.md#issues)):

| Plan | Rule | What happens |
|---|---|---|
| one PR (no Breakdown) | `implement` | `fix-with-plan` implements the plan - not the issue's text - and opens the PR, ready, saying which plan version it implements and `Fixes #<issue>`. |
| a Breakdown | `implement-graph` | No task. The watcher labels the issue `<t>/graph`, takes `<t>/implement` off and comments; the graph controller starts the graph (section 3). |
| none | `implement-unplanned` | A comment ("plan it first"), the label off. |

The plan as it is when the label goes on is the one implemented. Apply
`<t>/implement` again after a failure, or after revising a one-PR plan.

## 3. The graph

The graph controller (`factory watch --graphs`, or `WATCH_GRAPHS=true`;
on an Overseer, `spec.graphs: true`)
keeps every graph parent moving. A parent is an issue labelled `<t>/graph`;
one with no start block whose plan has a Breakdown is started from its
plan - the label is the approval, the watcher's on `implement` or a
maintainer's.

Each pass, on a slow sweep and as soon as a child closes:

1. Reads the steps - from the plan comment, every pass, so a revised plan
   changes the steps not yet started - and validates them: ids, `after`
   edges, no cycles, the nesting depth. An invalid Breakdown creates
   nothing and says why on the status comment.
2. Creates a sub-issue for each step that is ready (every step in its
   `after` done), in the order written, up to `maxActive` open at once
   (default 1). The child carries the step's title and body, a hidden
   marker (parent, step, depth) and is linked as a GitHub sub-issue.
3. Keeps one **status comment** on the parent: phase, a row per step
   (after, then, issue, state), the plan version.
4. When every step is done (and `finally`, if any), closes the parent.

The parent never opens a PR. `fix` and `workflow` leave it alone
(`graph: true`).

### What a sub-issue does (`then`)

| `then` | The child is labelled | What runs |
|---|---|---|
| `fix` (default) | `<t>`, the graph's default labels, the step's labels, `<t>/review` with review instructions | the `fix` rule: a coder bot fixes the child from its body and opens a PR `Fixes #<child>`; PR care follows (review, CI, comments, ready-for-human) |
| `plan` | `<t>/plan` | the `plan` rule plans the child; a maintainer applies `<t>/implement` - one PR, or a graph of its own (section 5) |
| `ask` | `<t>/needs-human` | waits for a person: no bot works on it. Its body says, under the step's text, what the person can do - label it `<t>` (fix) or `<t>/plan`, do it and close it, or close it as not planned to skip the step. It is assigned, so GitHub notifies them: to the step's `assignees` if the plan names any, else the parent's human assignees, else whoever approved the graph (the last person to label the parent `<t>/implement` or `<t>/graph`) |

### Progress, pauses and checkpoints

- A step is done when its child is closed: completed (its PR merged and
  closed it, or the controller closes it when a pool bot's PR naming it
  merged) is Succeeded; closed as not planned is Skipped, which counts as
  done for `after`.
- A child closing wakes its parent at once: the steps after it are created
  on that pass.
- `<t>/stop` on the parent pauses the graph (status Paused); take it off to
  resume.
- A step's `checkpoint`, once, when the step is done: `stop` puts
  `<t>/stop` on the parent, for a maintainer to look; `replan` puts
  `<t>/plan` on it, so the plan is revised (v+1) and the next pass reads
  the new Breakdown. A step whose child exists keeps it, by its id.
- A replan - a checkpoint's, or a maintainer's `<t>/plan` on the parent -
  reads the graph's status comment, the finished sub-issues and the PRs
  that closed them, and revises only the steps not yet started: done and
  active steps stay as they are, and "Changes since the previous version"
  says what the finished steps showed.
- A parent closed by a person cancels the graph; one closed by a commit
  ("Fixes #100" in a child's PR) is reopened until the graph is done.

## 4. The Breakdown

The plan's Breakdown is one fenced block whose info string is exactly
`yaml graph`:

````markdown
## Breakdown

```yaml graph
maxActive: 1            # how many steps may be open at once (default 1)
defaults:
  labels: [area/foo]    # on every fix child
steps:
  - id: api             # lowercase letters, digits, dashes; unique; names its sub-issue
    title: Add the Foo API
    body: |
      What this step changes and how to verify it, in full: the child is
      fixed from this body alone.
  - id: controller
    title: Reconcile Foo
    after: [api]        # steps done before this one starts (one or more)
    then: plan          # fix (default) | plan | ask
    checkpoint: replan  # stop | replan | [stop, replan]
    body: |
      ...
  - id: docs
    title: Document Foo
    after: [api]
    review: Check the examples run.   # review instructions; adds <t>/review
  - id: storage
    title: Pick the storage backend
    then: ask           # a person decides; the child is labelled <t>/needs-human
    assignees: [alice]  # optional, any step: assigned when its child is created
                        # (an ask step without: the parent's humans, else the approver)
    body: |
      Spanner or Bigtable? ...
    body: |
      ...
finally:                # optional: created once every step is done
  title: End-to-end test for Foo
  body: |
    ...
```
````

`finally` is one more sub-issue, created last - not the parent running.
`factory graph validate --url <issue>` checks an issue's plan as a graph,
and `factory graph reconcile --url <issue> --dry-run` shows what a pass
would do.

## 5. Nesting

A step with `then: plan` gets a child that is planned like any issue; if
its plan has a Breakdown, `implement` makes it a parent one level down.
When its own graph is done the controller closes it, which completes its
step in the graph above.

```
#100  plan → implement → graph: api, controller (after api, then: plan), docs (after api)
 ├─ #101 api         fix → PR → merged ✅
 ├─ #102 controller  plan v1 with a Breakdown → implement → <t>/graph (depth 1)
 │   ├─ #104 c1      fix → PR ✅
 │   └─ #105 c2      (after c1) fix → PR ✅
 │   → #102 closed by the controller → wakes #100
 ├─ #103 docs        fix → PR ✅
 └─ #106 finally     fix → PR ✅
→ #100 closed: Succeeded
```

Graphs nest at most 3 deep; a deeper Breakdown is invalid.

## Labels

Create them in the repository before using the flow:

| Label | Put on by | Meaning |
|---|---|---|
| `<t>/plan` | a maintainer; a `replan` checkpoint | plan (or revise the plan of) this issue |
| `<t>/planned` | the watcher | the issue has a plan; `fix` leaves it alone |
| `<t>/implement` | a maintainer | the plan is approved: implement it |
| `<t>/graph` | the watcher on `implement`, or a maintainer | the issue is a graph's parent |
| `<t>/needs-human` | the graph controller | a `then: ask` step waits for a person to decide |
| `<t>/stop` | a maintainer; a `stop` checkpoint | pause the graph (or anything on the item) |

## Not yet

- Nothing but a person acts on `<t>/needs-human`: no rule proposes fix or
  plan for a `then: ask` step yet.
