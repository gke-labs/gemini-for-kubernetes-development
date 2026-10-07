# Fan-out: one task, many items, a slow start

**Status:** Proposed.

Some work is one task applied to many items. [k8s-config-connector#13781](https://github.com/GoogleCloudPlatform/k8s-config-connector/issues/13781) is an example: it has migration steps per resource, a checklist of 21 resources, and a final cleanup once every resource is migrated. Today a person turns an issue like that into child issues by hand and labels them `overseer` a few at a time. Labelling all 21 at once would flood review, and the first few PRs usually show the task's description is wrong somewhere.

This note gives the watch daemon, and so overseer, a fan-out. The parent issue describes the work in sections, and the bot keeps it as a spec comment that maintainers edit. A child issue is created per item ("do the task for item X") and labelled for the coder bots in batches. A batch starts small and grows as children's PRs merge (a slow start). The fan-out pauses at checkpoints so the spec can be corrected, and one more child runs the final step once every item is done.

Everything happens on GitHub, with two labels: `overseer/fanout` marks a parent, and the existing `overseer/stop` holds it. It does not need repo-agent.

## How to use it

### 1. Write the parent issue

Describe the task, the items and, optionally, a final step. The headings can be your own: #13781's "Migration Steps per Resource" and "Affected Resources & Controllers Checklist" are fine. Items are a checklist, one item per line. A line you check now is left out.

### 2. Label it `overseer/fanout`

Within a sweep, the bot does three things:

- It posts a **spec comment**: your issue rewritten under the standard headings, with the task written for one `{item}` and settings filled in (see *The spec* below).
- It adds **`overseer/stop`**: nothing more happens until a maintainer has read the spec.
- It posts a **progress comment**, a table of items, which it keeps up to date from then on.

If your issue already uses the standard headings, the body is the spec. No agent runs, and the bot only adds `overseer/stop` and the progress comment.

### 3. Review and edit the spec

Edit the bot's spec comment directly on GitHub (*Edit* in the comment's ⋯ menu). Editing a bot's comment needs write access, so maintainers do it; anyone else asks a maintainer. You can fix the task text, drop or add items, change the child title or labels, and change the window and the checkpoints.

If the proposal is too far off, delete the spec comment and the bot writes a new one. You can also first edit the parent issue to make it clearer.

### 4. Remove `overseer/stop` to start

The bot creates the children and labels the first batch (`window.start`, 2 by default) `overseer`. From there each child is an ordinary overseer issue: a coder bot picks it up and opens a PR, and the PR goes through review as usual.

Each child is listed as a sub-issue of the parent, and the progress comment shows its PR and state.

### 5. Checkpoint: the bot stops after the first batch

When the first batch is done (`checkpoints`, by default `[window.start]`), the bot adds `overseer/stop` again and comments with what finished: "2 done: #a → PR #x, #b → PR #y. Edit the spec if needed, then remove `overseer/stop` to continue."

This is the point of a slow start. Read the first PRs, put what they taught into the spec, and remove `overseer/stop`. Children not yet started are **rewritten from the edited spec**, so the fix reaches every remaining item. The window keeps the size it reached and grows by one with each merged PR, up to `window.max`.

### 6. Along the way

- **Pause:** add `overseer/stop` at any time. No new children are labelled. Children already labelled keep going: they are their own issues, with their own labels. To stop one of them too, label that child `overseer/stop`.
- **Skip an item:** close its child as *not planned*. It counts as done and does not slow the fan-out down.
- **A child's PR closed unmerged:** the window halves, down to 1. The child stays open, for a person (or the bot's next attempt) to deal with.
- **Edit the spec while running:** allowed at any time. It applies to the next children labelled. Checkpoints are just where you are expected to look.

### 7. Done

When every item's child is closed, the bot creates the `Finally` child and labels it straight away. When that one closes, or at once if there is no `Finally` section, the bot closes the parent.

### At a glance

| Parent has | What is happening | Your move |
|---|---|---|
| `overseer/fanout`, `overseer/stop`, no children | The spec is proposed | Edit it, then remove `overseer/stop` |
| `overseer/fanout` | Running: children are labelled within the window | Review the children's PRs |
| `overseer/fanout`, `overseer/stop`, a checkpoint comment | Paused at a checkpoint | Read the PRs, edit the spec, remove `overseer/stop` |
| `overseer/fanout`, `overseer/stop`, no checkpoint comment | Paused by a person | Remove `overseer/stop` when ready |
| closed | Done | — |

## The spec

The bot writes the spec in four sections, under headings that are the same for every fan-out:

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
create: all                   # all: create every child at the start; batch: as each is labelled
window: {start: 2, max: 8}    # the slow start (below)
checkpoints: [2]              # stop when this many children are done; default [window.start]; [] never
```

#13781 as a spec, as the bot would post it (items shortened):

~~~markdown
<!-- factory:fanout-spec -->
## Fan-out
```yaml
title: "Migrate {item} to kmsv1beta1.KMSCryptoKeyRef"
labels: [direct-migration]
create: all
window: {start: 2, max: 8}
checkpoints: [2]
```

## Task
For `{item}`, switch its KMS reference from `refs.KMSCryptoKeyRef` to
`kmsv1beta1.KMSCryptoKeyRef`. The files to change are listed under Item below.

1. **Update API types.** In the item's `*_types.go`, import `kmsv1beta1` and change the
   KMS reference field(s) from `*refs.KMSCryptoKeyRef` to `*kmsv1beta1.KMSCryptoKeyRef`.
2. **Update the direct controller.** Remove the manual calls to `refs.ResolveKMSCryptoKeyRef`,
   and make sure `common.NormalizeReferences(ctx, reader, obj, nil)` is called in `AdapterForObject`.
3. **Regenerate and verify the schema.** Run `./dev/tasks/generate-types-and-mappers` and
   `make fmt`, then `./dev/tasks/diff-crds`: zero CRD schema drift.
4. **Test.** Unit, mock and E2E tests for the resource pass.

## Items
- [ ] **ApigeeInstance** (`apis/apigee/v1alpha1/instance_types.go`, …, `pkg/controller/direct/apigee/instance_resolverefs.go`)
- [ ] **BigQueryDataset** (`apis/bigquery/v1beta1/bigquerydataset_types.go`, …)
- … (21 lines, as written in the issue)

## Finally
Every caller in `pkg/controller/direct/` and `apis/` now uses `kmsv1beta1.KMSCryptoKeyRef`.
Remove `refs.KMSCryptoKeyRef` and `refs.ResolveKMSCryptoKeyRef` from `apis/refs/v1beta1/kmsrefs.go`.
~~~

The agent's part was mapping the issue's own headings to these, rewriting the steps for one `{item}`, and picking the title and labels. It copies the items exactly as written, because their file paths go into each child.

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
- **An agent does the reading.** For an issue that does not use the standard headings, the controller runs the `fanout` recipe once, to write the spec. Maintainers edit the spec themselves after that.

- **For:** exact, cheap and safe to re-run, and the window is enforced in code. The agent is used where judgement helps, once per parent.
- **Against:** new code: one controller, a markdown parser and a recipe.

### C. A fan-out primitive in factory recipes (alternative)

A recipe step or task-output action that creates child issues and labels them in batches (`uses: fanout`, or an action `create-children`).

- **For:** reuses the recipe machinery and its draft/apply flow end to end.
- **Against:**
  - A recipe is one run in one sandbox. A fan-out lasts days, until the last child's PR merges. Ramping the window needs something that wakes up when children close, which is the watch loop's job, not a run's.
  - Task-output actions only touch the run's own issue or PR (`issueTarget`). An action that writes to other issues breaks that rule, and with it the per-action permission model.
  - It would give recipes a second notion of state (the window) that lives outside the sandbox.

C's useful part, an agent writing a spec that a person approves, is kept in B as the `fanout` recipe.

## The design (B)

### Where the spec is read from

`factory/pkg/fanout` parses a spec from markdown with the standard headings: `Parse(markdown) (Spec, error)`. The controller reads it, every pass, from:

1. **The bot's comment** carrying `<!-- factory:fanout-spec -->`. Only a comment written by the bot counts: a spec comment by anyone else is ignored, so nobody can change what is fanned out by posting a comment. Only people with write access can edit the bot's comment, so editing the spec is a maintainer's action.
2. **Otherwise the issue body**, if it has the standard headings.
3. **Otherwise there is no spec.** The controller runs the `fanout` recipe, posts its output as the spec comment, and adds the stop label. This also happens again if the spec comment is deleted.

The spec comment is separate from the progress comment so that the bot never edits a comment a maintainer is editing.

A spec that does not parse (a hand edit gone wrong) gets one comment saying what is missing. The controller then does nothing more until it is fixed.

### The stop label is the gate

A fan-out uses the stop label the watch already has (`<trigger>/stop`, and `overseer/stop` always): it means "leave this issue alone". On a parent, it means no new children and no new labels. It does not reach children that are already labelled. To pause them too, a person labels them.

- **The bot adds it** when it proposes a spec, and when a checkpoint is reached. Each time it comments to say why.
- **A person removes it** to start or resume. That is the only go-ahead, every time.
- **A person may add it** at any time, as on any issue.

Checkpoints reached are recorded in the progress comment's state (below), so removing the stop label does not trip the same checkpoint again.

### The controller

`watch/fanout` runs on the slow issue sweep, and when the Nudger sees a child close (the Nudger finds a child's parent by the marker, as it finds a workflow by its link). For each open parent labelled `<trigger>/fanout`:

1. **Read** the spec (above), the children (the parent's sub-issues, plus a search for the marker as a fallback), and the state in the progress comment.
2. **Propose**, if there is no spec: run the recipe, post the spec comment, add the stop label. Nothing else happens on this pass.
3. **Report:** update the progress comment. This runs on every pass, stopped or not.
4. If the parent has the stop label, stop here.
5. **Checkpoint:** if the number of children done has reached a checkpoint not yet passed, add the stop label, comment, record the checkpoint, and stop here.
6. **Create.** With `create: all`, create every missing child, unlabelled. With `create: batch`, create children only as they are labelled. Creation is idempotent by the marker's `item=` key. Children not yet labelled whose text is out of date with the spec are rewritten (see *Changing the spec*).
7. **Label.** `active` is the children that are open and carry the trigger label. Label the next `window − active` children, in item order, with the trigger label plus the spec's `labels`. From then on the existing scanner owns them, as it owns any labelled issue. The daemon's `--max-pending` still caps how many run at once, so the window decides which children are eligible, not how fast they run.
8. **Final.** Once every item's child is closed, create the `Finally` child (labelled at once). When that one closes as completed, or straight away if there is no `Finally` section, close the parent. If the bot cannot close it, it comments instead.

The issue scanner skips a parent labelled `<trigger>/fanout`. Otherwise a coder bot would pick up the whole parent as one fix task.

### The slow start

It works the way TCP ramps up: a window that grows on success and shrinks on failure.

- It starts at `window.start` (default 2).
- A child closed as completed (its PR merged) adds 1, up to `window.max` (default 8). One step per success is what doubles the window each round, as TCP slow start does per round trip.
- A child whose linked PR closed without merging halves the window, with a minimum of 1. The child stays open: what happens to it is the existing watch's business (a person relabels it, closes it, or the bot retries).
- A child closed as not planned is a person skipping the item. The window is unchanged, and the item counts as done, for checkpoints and for the `Finally` step.

### Changing the spec

The controller re-reads the spec every pass, so a change takes effect on the next one:

| Change | Effect |
|---|---|
| Item added | A new child, labelled when its turn in the window comes |
| Item removed | Its child is not labelled. A child that already exists is left as it is; the controller closes nothing |
| Task or title changed | Children **not yet labelled** are rewritten with the new text. Labelled ones, being worked on, are left alone |
| `Finally` changed | Used when the final child is created |
| Window or checkpoints changed | Apply from the next pass. Lowering the window labels nothing new and unlabels nothing |

Rewriting children that have not been labelled is what makes the slow start useful. The first few PRs are where a task's description shows its gaps. With `create: all`, the remaining children already exist, so without the rewrite a fixed spec would only reach children created afterwards. Children not yet labelled have not been started, so nothing changes under a bot that is working.

### State on GitHub

The controller keeps no local state. GitHub holds everything, so a restarted daemon (or a second one) carries on where the last one stopped:

- **The spec** is the bot's spec comment, or the body.
- **The children** are the sub-issues, found by the marker.
- **The window, the PRs already counted against it, and the checkpoints passed** live in a hidden block in the progress comment: `<!-- factory:fanout-state {"window":3,"counted":[1201,1207],"checkpoints":[2]} -->`. Recording the counted PRs keeps a pass from counting the same merge twice. If the block is lost, the window is recomputed as `start + completed`, capped at `max`, and the checkpoints at or below the number done count as passed.
- **The progress comment** has one row per item (child, PR, state) and the current window. It is edited in place and never re-posted, so the parent does not fill up with progress comments.

### The `fanout` recipe

The `fanout` recipe is `on: [issue]` and writes a `FanOut` task output: the spec in the standard markdown form. It has no revises. Changes are made by maintainers, in the comment.

The controller runs it the way the watch runs any task, in a sandbox of its own (`fanout-<repo>-<N>`), queued and capped like everything else. When the run ends, the controller posts the spec comment from the output and adds the stop label.

The agent only reads and writes text. It never creates or labels issues; the controller does that.

### Permissions

- **Creating issues** needs only read access on a public repo.
- **Labelling, unlabelling `overseer/stop`, and linking sub-issues** need triage access. Without it, the controller still creates children with the marker and "Part of #N", and says once, on the parent, that labelling needs a person (or a bot with triage).
- **Closing the parent** needs the same access as editing it; without it, the controller comments.
- **Editing the spec comment** needs write access, which is what makes it a maintainer's action.

## Steps

1. **This note.**
2. **factory `pkg/fanout`:**
   - The parser: `Spec`, items, settings.
   - `Plan(spec, children, state, stopped) []Change`, a pure function returning the issues to create or rewrite, the labels to add, the checkpoint to stop at, and the new state. All the rules above live here, tested by table.
   - `factory fanout sync --issue N [--dry-run]` runs it once by hand, so it can be tried on #13781 before any daemon runs it.
3. **watch:**
   - The `watch/fanout` controller: children, the window, checkpoints, the final step, the progress comment.
   - The scanner skips parents.
   - The Nudger wakes a parent when one of its children closes.
4. **The proposal:** the `fanout` recipe and its `FanOut` kind. The controller runs it on a parent without a spec and posts the spec comment.
5. **Verify on KCC:**
   - #13781 as written: label it, check and edit the proposed spec, then `factory fanout sync --dry-run`.
   - Then remove `overseer/stop`, with a window of 2 and the default checkpoint.

Steps 2 and 3 serve parents that already use the headings. Step 4 is only for issues that don't.

## Later: a repo-agent board front end

This design does not need repo-agent. A board could later show a fan-out as a row: the spec as a draft to edit, the children as chips, and removing the stop label as its button. That is a thought experiment for now. The controller and the GitHub conventions above are the contract either way.

## Open questions

- **Sub-issues in KCC:** does the bot have triage access there? If not, the parent's checklist (in the progress comment) is the only progress view.
- **Label name:** `overseer/fanout`, or something that says "parent" more plainly, such as `overseer/fanout-parent`?
- **Ordering:** is item order (the checklist's) right, or should the window prefer items whose services nobody else is working on, to avoid merge conflicts?
- **Ramp on review, not merge:** should a PR approved but waiting on a human merge already count as a success? Counting only merges is slower but stricter.
