# Design Note: Plans as Issue Graphs

| Metadata | Details |
| :--- | :--- |
| **Status** | Proposal; not built. The plan flow it extends is built: `.agents/watch/plan.yaml`, `implement.yaml`, the plan recipe's keyed comment (#2119), the fix guard and fix-with-plan (#2120). |
| **Companion to** | [issue-graph.md](issue-graph.md): the graph schema, the reconciler, `then` routing, nesting and checkpoints are there; this note is how a plan becomes a graph and how it is revised. |
| **Last Updated** | 2026-10-25 |
| **Scope** | `.agents/` rules and recipes (plan, implement), the graph reconciler's plan source |

---

## 1. Summary

An issue is planned on `<trigger>/plan`: the plan recipe writes its plan comment (key `plan`, refined in place, v1, v2…). A maintainer approves it with `<trigger>/implement`, and the plan's shape decides what happens:

* **No Breakdown - one PR:** fix-with-plan follows the plan and opens the PR (#2120).
* **A Breakdown - several PRs:** the Breakdown becomes an [issue graph](issue-graph.md) under the issue: sub-issues, opened as the steps they come `after` are done, each fixed or planned in turn.

The maintainer approves the plan, and the plan says how it is done: one label for both.

As the graph runs, the plan can be **revised** - by a person at any time, or at a checkpoint - and the graph follows the new version for the steps not yet started.

---

## 2. The Breakdown

When the work is bigger than one reviewable PR, the plan ends with a Breakdown: prose for people, and a fenced block in the graph's step schema ([issue-graph.md §4.2](issue-graph.md)), without params:

````markdown
## Breakdown
1. **schema** - the versioned schema package.
2. **recorder** - the event recorder (after schema); big enough to plan on its own.
3. **compare** - threshold compare (after recorder).

```yaml graph
maxActive: 2
steps:
  - id: schema
    title: Add the versioned schema package
    body: |
      ...
  - id: recorder
    after: [schema]
    then: plan
    checkpoint: [replan, stop]
    title: Streaming event recorder and shard merge
    body: |
      ...
  - id: compare
    after: [recorder]
    title: Threshold compare and stats.jsonl
    body: |
      ...
finally:
  title: Wire it into the CLI and document
  body: |
    ...
```
````

* The planner proposes the split, the `after` edges, each step's `then` (`fix`, `plan` or `ask`) and any checkpoints; a maintainer refines them with the rest of the plan.
* A step's body is the child's whole brief: the child's fix sees it, not the parent's discussion.
* A plan without a `graph` block is a one-PR plan.

### 2.1 Reading it (`source: plan`)

The reconciler reads the parent's plan as the `keyed-comment` recipe step does: the newest comment of key `plan` whose author is of a trusted kind - `pool`, `owner`, `member`, `collaborator`. Anyone else's comment carrying the marker is ignored, so an outside author cannot inject steps. Maintainers count, so a maintainer can write or edit the plan by hand.

* No such plan, or a plan without a `graph` block, is `Invalid` as a graph source.
* The block is validated as a template is (`factory graph validate --url <issue>`): ids, `after` edges without cycles, templates.
* The steps are read fresh every pass.

---

## 3. The rules

In this repository's `.agents/watch/` (any repository can adopt them):

| Label | Rule | What happens |
| :--- | :--- | :--- |
| `<trigger>/plan` | `plan` | The plan comment is posted or updated, with a Breakdown when the work needs more than one PR; the watcher adds `<trigger>/planned`. Applied again, it revises the plan (§5). |
| `<trigger>/implement` | `implement` | Approval of the plan. Its recipe first reads the plan (`keyed-comment`, trusted kinds) and looks for a `graph` block: **without one**, it is fix-with-plan and opens one PR (#2120); **with one**, no agent runs: it validates the block (and the depth limit) and posts the start block `source: plan` as a keyed comment (key `graph`) - the bot cannot edit the issue body - and the reconciler takes it from there. |
| `<trigger>` on a planned issue | `fix` | Nothing: fix leaves an issue in the plan flow alone (`labels.none: [<trigger>/plan, <trigger>/planned]`, #2120). |

The implement task's bot may not label, so the parent of a graph started from a plan does not carry `<trigger>/graph` yet: the graph controller sweeps planned issues too, and labels one it finds with a start block, from then on swept and left alone by fix and workflow like any graph parent.

To get one PR from a plan that has a Breakdown, remove the Breakdown (edit the plan comment, or replan asking for one PR) before applying `implement`; to get sub-issues, replan asking for a Breakdown.

---

## 4. Children that are planned: nesting

A step with `then: plan` gives a child labelled `<trigger>/plan`: the same rules plan it, and its maintainer approves with `implement`: one PR, or - if its plan has a Breakdown - its own graph, one level down. A step with `then: ask` waits, labelled `<trigger>/route`, for a person to choose ([issue-graph.md §4.6](issue-graph.md)). The reconciler does not re-apply a `plan` or `route` label to an open child, as it does a fixed child's labels: each is a command its rule takes off when done.

A child that is planned and broken down gets its own start block and becomes a parent. Its graph is reconciled like any other, one level down:

```
#100 --plan, implement--> graph: A, B (after A, then: plan), C (after A)
  |- A  #101 --fix--> PR merged: A done
  |- B  #102 --plan, implement--> graph: B1, B2 (after B1)     <- #102 is a parent too
  |     |- B1 #110 --fix--> PR merged
  |     '- B2 #111 --fix--> PR merged: #102's graph done, #102 closed: B done
  '- C  #103 --fix--> PR merged: C done
#100's graph done: #100 closed
```

* **Completion bubbles up.** A graph that finishes closes its parent as completed ([issue-graph.md](issue-graph.md) §6.1). One level up, that parent is a child, so its step is done. No level reads another level's graph.
* **Depth.** Each child's marker records its depth. A start block on an issue at `maxDepth` (default 3, an overseer setting) is `Invalid`: deeper splits take a person filing a new issue.
* **Dependencies stay within a level.** A step depends only on its siblings; anything else is said one level up, so each graph stays acyclic.

---

## 5. Revising the plan

A plan can change as its steps finish: what one step taught can change the ones still to come. Revising is `<trigger>/plan` on the parent again - the same label and the same `plan` rule that wrote it:

* **A person** applies it at any time.
* **The reconciler** applies it at a `checkpoint: replan` (or `[replan, stop]`) step, once, when the step is done ([issue-graph.md §4.8](issue-graph.md)).

### 5.1 What the planner sees

On a revision the plan recipe reads, besides the plan comment it revises:

* the graph's status comment (`key=graph-status`): each step's state, child issue and PR;
* the done children's PRs (title, description) and what their agents concluded, such as a "nothing should change" explanation;
* the comments on the parent since the plan was last updated, as feedback.

The plan recipe reads the status comment through the same `keyed-comment` step, by the watcher (`pool`).

### 5.2 What may change

* Done and active steps keep their ids and stay as they are: they have happened.
* Steps not yet started can change, be added or be removed; so can their `after` edges among themselves, `then`, checkpoints and `finally`.
* The plan says what changed and why, in its "Changes since the previous version" section ("recorder showed the schema needs a version field; added step `schema-v2`").

A revision that changes or drops a done or active step is not applied: the graph keeps the last version that kept them, and the status says so, for a maintainer to fix.

### 5.3 Who approves a revision

* **`checkpoint: [replan, stop]`** - the graph holds until a maintainer has read the new version and removes the stop label: removing it approves the revision.
* **`checkpoint: replan`** or a person's `<trigger>/plan` - the next pass follows the new version straight away. A maintainer who wants to review first adds the stop label to the parent before replanning.

Either way active children run on, nothing already created is edited, and a step removed after its child exists shows as orphaned.

---

## 6. Plan recipe changes

In this repository's `.agents/recipes/plan.yaml`:

* Ask for a Breakdown (§2) when the work needs more than one reviewable PR, in the graph step schema, with `then`, `after` and checkpoints proposed.
* On a revision of a plan that has a graph: read the status comment and the done children's PRs (§5.1) through `keyed-comment`, and keep done and active steps (§5.2).
* Keep the "Changes since the previous version" section.

---

### 6.1 The implement recipe

`.agents/recipes/fix-with-plan.yaml` (the `implement` rule's) takes both paths in one run:

1. Read the plan through `keyed-comment` (trusted kinds), as today, and note whether it has a `graph` block.
2. **With a `graph` block:** the `graph-start` step (`factory graph start --source plan`) validates it (and the depth limit), posts the start block `source: plan` as a keyed comment (key `graph`), and the run ends: the remaining steps are skipped (`if:`), no agent runs, and the task leaves no Change (an optional result).
3. **Without one:** the fix steps run as they do now, and `open-pr` opens the PR.

So the rule stays one rule (`apply: open-pr`), and the path is decided by the plan, read once, at the moment of approval.

## 7. Edge cases

| Case | Behaviour |
| :--- | :--- |
| `implement` on a plan with an invalid `graph` block | The task fails with the validation error; no PR, no graph. |
| `implement` applied again after the graph started | The start block is already there; nothing changes. |
| `implement` on an issue with no plan comment | The task fails: plan it first. |
| A revision while a child is active | The active child runs on; the revision applies to the steps not yet started. |
| A revision rewrites a done step | Not applied; the status warns (§5.2). |
| A checkpoint reached while a plan run is queued or running | The label is already there: one revision covers it. |
| Someone outside the trusted kinds posts a comment with the plan marker | Ignored. |

---

## 8. Testing

* Decide-scenario tests for the fix guard on planned issues and graph parents.
* The `implement` recipe: a plan without a `graph` block (one PR), with one (the start block posted, no agent run), an invalid block, the depth limit, no plan.
* Reconciler table tests for the plan source: trusted authors only, versions, steps added and removed, a revision that rewrites a done step refused, checkpoints firing once.
* End to end on a scratch repository: plan → implement (a graph) → a child planned and implemented as its own graph → completion bubbling to the top.
