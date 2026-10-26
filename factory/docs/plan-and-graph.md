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
comments and the code, and writes the plan - Summary, Baseline (what
the default branch has to build on), Approach, PR plan, Risks, Test plan -
posted as **one comment** with a hidden key (`plan`). For one PR, the PR
plan is its numbered steps. When the work is too big for one PR, the PR
plan is a mermaid **dependency graph** of the PRs and a table of them
(what each adds, its files, what it comes after, how to verify it), and
the plan ends with a **Breakdown** (section 4), folded under `<details>`:
it is the overseer's spec, and says the same as the PR plan. On success the watcher adds `<t>/planned`; the command label
comes off when the task ends, whatever happened.

**Questions are asked first, where the answer is needed.** When the plan depends on something only a maintainer can decide, the first version asks before it plans: only a short summary of what it understood and a `## Questions` section - each question a task list, the recommended option first, and "Other: answered in a comment" last. While a question is open the issue is labelled `<t>/needs-human` (`plan-asking`). Tick a box (or tick Other and reply) in each question: once every question is answered, the watcher takes `needs-human` off and applies `<t>/plan` itself (`plan-answered`), and the next version is the plan, with the answers folded in. Until that plan is written - the shape is `asking`, then `replanning` - `<t>/implement` gets a comment saying so (`implement-not-ready`): no sandbox, no graph. You can also apply `<t>/plan` yourself at any point - after answering in comments without ticking, say: the next version folds in whatever has been answered and asks the rest again. Each successful plan run takes `needs-human` off, and `plan-asking` puts it back if the new version still asks, so the label always follows the latest version. A question only one step needs is asked in that step's own sub-issue, not here (section 3).

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
| still asking, or answered but not yet rewritten (`## Questions` present) | `implement-not-ready` | A comment ("answer the questions first"), the label off. |
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
| `ask` | `<t>/needs-human` and `<t>/stop` | the step needs a maintainer's answer first: how big it is (fix it, or plan it first), whether it is needed, or a choice only it depends on. Its body holds the step's work and a `## Questions` checklist; the stop label keeps every bot off it until then. Answer on that issue (tick a box, or reply), then remove `<t>/stop` and label it `<t>` (fix) or `<t>/plan` - the agent reads the answer there - or close it as not planned to skip it. The step's `assignees`, else the parent's humans, else whoever approved the graph, are assigned |

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
`yaml graph` (the plan recipe folds it in `<details>`; it is found
anywhere in the comment):

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
    then: ask           # a person decides; the child is labelled <t>/needs-human and <t>/stop
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

### Validating a graph: `factory graph validate`

Use `factory graph validate` to check a graph before the controller creates
anything. It checks either:

- **A graph template file** (`.agents/graphs/<name>.yaml`): validates the strict
  schema (`apiVersion: factory.gemini.google.com/v1alpha1`, `kind: IssueGraph`),
  unique step IDs, dependency edges (`after`) with no cycles, `then` actions
  (`fix`, `plan`, `ask`), checkpoints (`stop`), param constraints, and step
  template rendering.
- **An issue's plan comment** (`--url <issue>`): finds the latest plan comment
  by a trusted author (a pool bot, owner, member, or collaborator) and checks
  its `yaml graph` Breakdown block (same rules, with `checkpoint: replan`
  allowed).

Runnable command invocations:

```sh
# Validate a graph template file
factory graph validate .agents/graphs/kcc-greenfield.yaml

# Validate an issue's plan comment
factory graph validate --url https://github.com/owner/repo/issues/100
```

Representative terminal output (for the Breakdown above on issue #100):

```
#100: plan v1 (by @coder-bot) is a valid graph
maxActive 1
  api                  after -                        then fix    Add the Foo API
  controller           after api                      then plan   checkpoint replan  Reconcile Foo
  docs                 after api                      then fix    Document Foo
  storage              after -                        then ask    Pick the storage backend
  finally              after every step               then fix    End-to-end test for Foo
```

For a template file, the header reports the file path and template name
(e.g., `.agents/graphs/kcc-greenfield.yaml: graph kcc-greenfield is valid`),
followed by the same `maxActive` limit and step table layout.

### Reconciling a graph: `factory graph reconcile`

Use `factory graph reconcile` to run one reconciliation pass over a graph's
parent issue, exactly as the background watcher does during automated sweeps.

A pass reads the parent's start block, resolves its step definitions (from its
plan comment or a template file), and inspects observed sub-issues and their
merged bot PRs. It then reconciles the graph state (`graph.Sync`):

- **Evaluates DAG steps**: determines which steps are ready based on their
  `after` dependencies and the `maxActive` concurrency limit.
- **Creates ready sub-issues**: opens child issues for newly ready steps, setting
  titles, bodies, assignees, and labels according to their `then` action
  (`fix`, `plan`, or `ask`).
- **Repairs linkages**: links existing sub-issues carrying the graph's marker and
  closes duplicate sub-issues.
- **Tracks completion**: closes child issues completed by merged bot PRs.
- **Applies checkpoints**: adds checkpoint labels (such as `<t>/stop` or
  `<t>/plan`) to the parent issue when designated steps finish.
- **Closes completed graphs**: closes the parent issue once every step (and
  `finally`, if present) has completed.
- **Maintains the status comment**: writes or updates the pinned markdown status
  comment on the parent issue.

Runnable command invocations:

```sh
# Preview planned actions and rendered status comment without modifying GitHub
factory graph reconcile --url https://github.com/owner/repo/issues/100 --dry-run

# Run a live reconciliation pass against the parent issue
factory graph reconcile --url https://github.com/owner/repo/issues/100
```

The command takes `--url <parent>` (required), `--dry-run` to preview actions
without modifying GitHub, `--trigger-label` to override the trigger label prefix
(defaults to `triggerLabel` from `.factory.cfg`, e.g., `factory` or `overseer`),
and `--max-depth` for maximum graph nesting depth (default `3`).

Representative terminal output for `--dry-run` (for the Breakdown above on issue #100):

```
would create step api's child "Add the Foo API", labelled factory, area/foo
would write the status comment on #100 (Running)

### Issue graph - Running · 0/5 done

| Step | After | Then | Issue | State |
|---|---|---|---|---|
| api | - | fix | - | 🔄 Active |
| controller | api | plan | - | ⏳ Pending |
| docs | api | fix | - | ⏳ Pending |
| storage | - | ask | - | ▶️ Ready |
| finally | every step | fix | - | ⏳ Pending |

<sub>steps: plan v1 · maxActive 1</sub>

<!-- factory:graph-state {"params":"e3b0c44298fc"} -->
<!-- factory:comment key=status v=1 -->
```

In `--dry-run` mode:
- Planned actions are printed line-by-line with the `would ` prefix (e.g.,
  `would create step api's child...`, `would write the status comment on #100 (Running)`).
- Because `maxActive` is 1, only `api` is activated (`🔄 Active`); `storage` is
  also ready but waits in `▶️ Ready` until an active step finishes.
- The full rendered status comment is printed below the planned actions, showing
  the overall progress, the step status table, and hidden state metadata blocks.
- On subsequent passes, `--dry-run` details other planned actions as the graph
  progresses (e.g., `would close #101 (step api) as completed by #105`,
  `would link #101 (step api) as a sub-issue`,
  `would label the parent #100 factory/plan (checkpoint controller)`, or
  `would close the parent #100 as completed`).

In live execution (without `--dry-run`), actions are written to GitHub and the
resulting phase is output:

```
create step api's child "Add the Foo API", labelled factory, area/foo
#100: Running
```

### Adopting an issue: `factory graph adopt`

Use `factory graph adopt` to bind an existing GitHub issue to a step of a parent
issue's graph.

Instead of the reconciler opening a new child issue when a step becomes ready,
adopting an issue injects the child metadata marker into the existing issue's
body and links it as a GitHub sub-issue of the parent. The next reconciliation
pass counts the adopted issue as that step's child.

The operation verifies that:
- The parent issue is an active graph parent with a valid start block and graph spec.
- The specified `--step` exists in the parent's graph.
- The target `--issue` is an issue (not a pull request) and does not already carry
  a child marker from another step or parent.

Runnable command invocations:

```sh
# Preview adopting an issue without modifying GitHub
factory graph adopt --url https://github.com/owner/repo/issues/100 --step storage --issue 104 --dry-run

# Bind an existing issue to a step of the parent graph
factory graph adopt --url https://github.com/owner/repo/issues/100 --step storage --issue 104
```

The command requires `--url <parent>`, `--step <id>`, and `--issue <n>`, and supports
`--dry-run` to preview the action without writing changes to GitHub.

Representative terminal output:

For `--dry-run`:

```
would adopt #104 as step storage of #100
```

For live execution:

```
adopt #104 as step storage of #100
```

In live execution:
- The child issue's body is updated to append the child metadata marker
  (e.g., `<!-- factory:graph-child parent=100 source=plan step=storage depth=1 params=e3b0c44298fc -->`).
- The issue is linked as a GitHub sub-issue under the parent (#100).
- On the next reconciliation pass, `factory graph reconcile` (or the background watcher)
  recognizes #104 as step `storage`'s child and tracks its status in the graph.

### Starting a graph: `factory graph start`

Use `factory graph start` to initialize an issue as a graph's parent.

It checks the steps that define the graph—either from an approved plan comment
(`--source plan`) or from a repository template and its parameters
(`--graph <name> [--param <k>=<v>]...`)—and posts the start block as a keyed
comment (`key=graph`). Because a bot cannot edit another account's issue body
without write access, posting a keyed comment allows rules or maintainers to
start a graph on an issue.

The operation verifies that:
- The issue does not already have a start block in its body or in an existing
  comment. An issue that already has a start block is left as it is.
- The graph nesting depth does not exceed `--max-depth` (default `3`).
- The steps are valid: for `--source plan`, the issue must have an approved plan
  comment containing a valid `yaml graph` Breakdown block; for `--graph`, the
  template (`.agents/graphs/<name>.yaml`) must exist and validate with the given
  parameters.

Runnable command invocations:

```sh
# Preview starting a graph from an issue's approved plan
factory graph start --url https://github.com/owner/repo/issues/100 --source plan --dry-run

# Start a graph from an issue's approved plan
factory graph start --url https://github.com/owner/repo/issues/100 --source plan

# Preview starting a graph from a repository template
factory graph start --url https://github.com/owner/repo/issues/124 --graph kcc-greenfield --param kind=SpannerInstanceConfig --dry-run

# Start a graph from a repository template
factory graph start --url https://github.com/owner/repo/issues/124 --graph kcc-greenfield --param kind=SpannerInstanceConfig
```

The command requires `--url <parent>` and either `--source plan` or `--graph <template>`
(with repeatable `--param name=value` for template parameters). It supports `--dry-run`
to preview the action without writing changes to GitHub, `--trigger-label` to override
the trigger label prefix, and `--max-depth` to set the maximum allowed graph nesting depth.

Representative terminal output:

For `--dry-run`:

```
would start #100's graph from its plan
```

Or for a template:

```
would start #124's graph from its template `kcc-greenfield`
```

For live execution:

```
start #100's graph from its plan
```

Or for a template:

```
start #124's graph from its template `kcc-greenfield`
```

If the issue already has a start block:

```
#100 already has a start block (plan): nothing to do
```

In live execution:
- Posts a keyed comment (`<!-- factory:comment key=graph v=1 -->`) containing the
  `factory-graph` start block on the parent issue.
- When the parent is labelled `<t>/graph` (or detected during watcher sweeps),
  `factory graph reconcile` (or the background watcher) recognizes the start block,
  evaluates dependencies, and begins creating sub-issues for ready steps.

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
| `<t>/needs-human` | the graph controller (with `<t>/stop`) | a `then: ask` step waits for a person to decide |
| `<t>/stop` | a maintainer; a `stop` checkpoint | pause the graph (or anything on the item) |

## Not yet

- Nothing but a person acts on `<t>/needs-human`: no rule proposes fix or
  plan for a `then: ask` step yet.
