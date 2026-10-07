# Fan-out: one task, many items, a slow start

**Status:** Proposed.

Some work is one task applied to many items. [k8s-config-connector#13781](https://github.com/GoogleCloudPlatform/k8s-config-connector/issues/13781) is an example: it has migration steps per resource, a checklist of 21 resources, and a final cleanup once every resource is migrated. Today a person turns an issue like that into child issues by hand and labels them `overseer` a few at a time. Labelling all 21 at once would flood review, and the first few PRs usually show the task's description is wrong somewhere.

This note gives the watch daemon a fan-out. The parent issue describes the work in sections. A child issue is created per item ("do the task for item X") and labelled for the coder bots in batches. A batch starts small and grows as children's PRs merge (a slow start). One more child runs the final step once every item is done.

## The issue format

The parent issue has three sections, under headings that are the same for every fan-out:

| Heading | Meaning | In #13781 |
|---|---|---|
| `## Task` | What to do for one item. `{item}` is replaced by the item's name. | "Migration Steps per Resource" |
| `## Items` | A checklist; each line is an item. A line already checked is not fanned out. | "Affected Resources & Controllers Checklist" |
| `## Finally` (optional) | One more child, created when every item is done. | "Final Deprecation & Cleanup" |
| `## Fan-out` (optional) | Settings, as a YAML block (below). | — |

An item's name is its bold text if it has any, otherwise the line up to the first ` (` or ` - `. The whole line, file paths included, goes into the child's body.

```yaml
# ## Fan-out
title: "{item}: {parent}"     # child issue title; {parent} is the parent's title
labels: [direct-migration]    # labels for every child, besides the trigger label
create: all                   # all: create every child up front; batch: as it is labelled
window: {start: 2, max: 8}    # the slow start (below)
```

A child issue looks like this:

```markdown
<the Task section, with {item} replaced>

### Item
- **ApigeeInstance** (`apis/apigee/v1alpha1/instance_types.go`, …)

Part of #13781.
<!-- factory:fanout parent=13781 item=apigeeinstance -->
```

The marker comment makes children findable: the controller finds a parent's children by it, never by title. Children are also linked to the parent as GitHub sub-issues, so GitHub shows the parent's progress bar.

A child is an ordinary issue, so the existing workflow features apply to it. If the Task section links a workflow file (for example `.agents/workflows/kcc-example.txt` with `{item}` as its kind), each child becomes a workflow issue: a fan-out of multi-step workflows, with no extra design needed.

## Options

### A. A workflow prompt only (alternative)

A generic `.agents/workflows/fanout.md` that the parent links. The agent reads the sections, creates the children, labels the next batch, and keeps a progress table. The workflow's cooldown and the Nudger (which re-runs a workflow when an issue it links closes) drive it. `kcc-example.txt` already works this way for steps.

- **For:** no code. It could run on #13781 now, and it copes with any headings.
- **Against:**
  - The part that has to be exact is the bookkeeping: how many children are open, the window size, not creating a child twice. An LLM does it again on every run, and `kcc-example.txt` dedupes by searching titles, which is fragile.
  - Every cooldown costs a full agent run, a sandbox and a model call, just to count issues.
  - Nothing enforces the window. One bad run can label every child.

### B. A deterministic fan-out in `factory watch`, an agent only to write the spec (chosen)

- **A controller does the counting.** A new `watch/fanout` controller reconciles every parent labelled `<trigger>/fanout` (`overseer/fanout` by default). It is deterministic, idempotent and cheap: a few GitHub reads per parent per cycle, and no sandbox.
- **An agent does the reading.** The `fanout` recipe is for issues that do not use the headings, such as #13781 as written. The agent reads the issue and writes the spec in the standard form as a draft. Applying the draft posts the spec as a comment and labels the parent. On the board this is the same flow as a Plan.

- **For:** exact, cheap and safe to re-run, and the window is enforced in code. The agent is used where judgement helps, once per parent.
- **Against:** new code: one controller, a markdown parser, a recipe, and a task-output kind with one action.

### C. A fan-out primitive in factory recipes (alternative)

A recipe step or task-output action that creates child issues and labels them in batches (`uses: fanout`, or an action `create-children`).

- **For:** reuses the recipe machinery and the board's draft/apply flow end to end.
- **Against:**
  - A recipe is one run in one sandbox. A fan-out lasts days, until the last child's PR merges. Ramping the window needs something that wakes up when children close, which is the watch loop's job, not a run's.
  - Task-output actions only touch the run's own issue or PR (`issueTarget`). An action that writes to other issues breaks that rule, and with it the per-action permission model.
  - It would give recipes a second notion of state (the window) that lives outside the sandbox.

C's useful part, the agent writing a draft that a person approves, is kept in B as the `fanout` recipe.

## The design (B)

### The spec, and where it is read from

`factory/pkg/fanout` parses a spec from markdown with the headings above: `Parse(markdown) (Spec, error)`. The controller reads it from the parent, in this order:

1. The newest comment carrying `<!-- factory:fanout-spec -->`, which is what applying the recipe's draft posts. A comment is used because the bot often cannot edit another person's issue body (public repos, as in KCC).
2. Otherwise the issue body.

Someone who writes the headings in the body by hand needs no agent: they add the label, and that's all.

A parent whose spec does not parse gets one comment saying what is missing, and nothing else happens until it is edited.

### The controller

`watch/fanout` runs on the slow issue sweep, and when the Nudger sees a child close (the Nudger finds a child's parent by the marker, as it finds a workflow by its link). For each open parent labelled `<trigger>/fanout` and without the stop label, it does the following:

1. **Read.** It reads the spec, the children (the parent's sub-issues, plus a search for the marker as a fallback), and the state in the progress comment (below).
2. **Create.** With `create: all`, it creates every missing child, unlabelled. With `create: batch`, it creates children only as they are labelled. Creation is idempotent by the marker's `item=` key.
3. **Label.** `active` is the children that are open and carry the trigger label. It labels the next `window − active` children, in item order, with the trigger label plus the spec's `labels`. From then on the existing scanner owns them, as it owns any labelled issue. The daemon's `--max-pending` still caps how many run at once, so the window decides which children are eligible, not how fast they run.
4. **Final.** Once every item's child is closed as completed, it creates the `Finally` child (labelled at once). When that one closes as completed, or straight away if there is no `Finally` section, it closes the parent. If it cannot close the parent, it comments instead.
5. **Report.** It updates the progress comment.

The issue scanner skips a parent labelled `<trigger>/fanout`. Otherwise a coder bot would pick up the whole parent as one fix task.

### The slow start

It works the way TCP ramps up: a window that grows on success and shrinks on failure.

- It starts at `window.start` (default 2).
- A child closed as completed (its PR merged) adds 1, up to `window.max` (default 8). One step per success is what doubles the window each round, as TCP slow start does per round trip.
- A child whose linked PR closed without merging halves the window, with a minimum of 1. The child stays open: what happens to it is the existing watch's business (a person relabels it, closes it, or the bot retries).
- A child closed as not planned is a person skipping the item. The window is unchanged, and the item counts as done for the `Finally` step.

### State on GitHub

The controller keeps no local state. GitHub holds everything, so a restarted daemon (or a second one) carries on where the last one stopped:

- **The children** are the sub-issues, found by the marker.
- **The window** and the PRs already counted against it live in a hidden block in the progress comment: `<!-- factory:fanout-state {"window":3,"counted":[1201,1207]} -->`. Recording the counted PRs is what keeps a cycle from counting the same merge twice. If the block is lost, the window is recomputed as `start + completed`, capped at `max`.
- **The progress comment** has one row per item (child, PR, state) and the current window. It is edited in place and never re-posted, so the parent does not fill up with progress comments.

### The `fanout` recipe

The `fanout` recipe is `on: [issue]` and writes a `FanOut` task output: the spec in the standard markdown form, as a draft. It has one action, `start-fanout`. Applying it posts the spec comment and adds the `<trigger>/fanout` label, both on the run's own issue, so it fits the existing rule that an action touches only its run's target. The board's draft editing (edit, reject, apply) works on it unchanged. The board needs no fan-out code; it gets the recipe from the catalog, like any other recipe.

The agent only reads and writes text. It never creates or labels issues.

### Permissions

- **Creating issues** needs only read access on a public repo.
- **Labelling and linking sub-issues** need triage access. Without it, the controller still creates children with the marker and "Part of #N", and says once, on the parent, that labelling needs a person (or a bot with triage).
- **Closing the parent** needs the same access as editing it; without it, the controller comments.

## Steps

1. **This note.**
2. **factory `pkg/fanout`:**
   - The parser: `Spec`, items, settings.
   - `Plan(spec, children, state) []Change`, a pure function returning the issues to create, the labels to add and the new window. All the rules above live here, tested by table.
   - `factory fanout sync --issue N [--dry-run]` runs it once by hand, so it can be tried on #13781 before any daemon runs it.
3. **watch:**
   - The `watch/fanout` controller.
   - The scanner skips parents.
   - The Nudger wakes a parent when one of its children closes.
4. **The `fanout` recipe:** the `FanOut` kind and the `start-fanout` action.
5. **Verify on KCC:**
   - A dry run on #13781, as written: the recipe writes the spec, then `sync --dry-run`.
   - Then the real thing, starting with a window of 2.

Steps 2 and 3 serve parents that already use the headings. Step 4 is only for issues that don't.

## Open questions

- **Sub-issues in KCC:** does the bot have triage access there? If not, the parent's checklist (in the progress comment) is the only progress view.
- **Ordering:** is item order (the checklist's) right, or should the window prefer items whose services nobody else is working on, to avoid merge conflicts?
- **Ramp on review, not merge:** should a PR approved but waiting on a human merge already count as a success? Counting only merges is slower but stricter.
