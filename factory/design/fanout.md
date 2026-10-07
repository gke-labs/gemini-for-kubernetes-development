# Fan-out: one task, many items, a slow start

**Status:** Proposed.

Some work is one task applied to many items. [k8s-config-connector#13781](https://github.com/GoogleCloudPlatform/k8s-config-connector/issues/13781) is an example: it has migration steps per resource, a checklist of 21 resources, and a final cleanup once every resource is migrated. Today a person turns an issue like that into child issues by hand and labels them `overseer` a few at a time. Labelling all 21 at once would flood review, and the first few PRs usually show the task's description is wrong somewhere.

This note gives the watch daemon, and so overseer, a fan-out. Everything happens on GitHub: the spec, its changes, the go-ahead and the progress. It does not need repo-agent. The parent issue describes the work in sections. A child issue is created per item ("do the task for item X") and labelled for the coder bots in batches. A batch starts small and grows as children's PRs merge (a slow start). One more child runs the final step once every item is done.

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

#13781 as a spec, as the bot would post it (items shortened):

~~~markdown
<!-- factory:fanout-spec -->
## Fan-out
```yaml
title: "Migrate {item} to kmsv1beta1.KMSCryptoKeyRef"
labels: [direct-migration]
create: all
window: {start: 2, max: 8}
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
- **An agent does the reading.** For issues that do not use the headings, such as #13781 as written, the controller runs the `fanout` recipe. The agent writes the spec in the standard form, and the bot posts it on the parent as a proposal. People change it by replying, and start it with a label.

- **For:** exact, cheap and safe to re-run, and the window is enforced in code. The agent is used where judgement helps, once per parent.
- **Against:** new code: one controller, a markdown parser, a recipe, and a task-output kind with one action.

### C. A fan-out primitive in factory recipes (alternative)

A recipe step or task-output action that creates child issues and labels them in batches (`uses: fanout`, or an action `create-children`).

- **For:** reuses the recipe machinery and its draft/apply flow end to end.
- **Against:**
  - A recipe is one run in one sandbox. A fan-out lasts days, until the last child's PR merges. Ramping the window needs something that wakes up when children close, which is the watch loop's job, not a run's.
  - Task-output actions only touch the run's own issue or PR (`issueTarget`). An action that writes to other issues breaks that rule, and with it the per-action permission model.
  - It would give recipes a second notion of state (the window) that lives outside the sandbox.

C's useful part, an agent writing a spec that a person approves, is kept in B as the `fanout` recipe.

## The design (B)

### The spec: proposed, changed, started

`factory/pkg/fanout` parses a spec from markdown with the headings above: `Parse(markdown) (Spec, error)`. A fan-out goes through three stages, all on the parent issue:

1. **Proposed.** Someone labels the parent `<trigger>/fanout` (`overseer/fanout` by default).
   - If the body already has the headings, the body is the spec, and the controller says so in a comment.
   - Otherwise the controller runs the `fanout` recipe (below), and the bot posts the spec as a comment marked as a proposal.
   - Nothing else happens yet: no children are created.
2. **Changed.** A person replies on the parent with what to change, mentioning the bot ("@bot drop SQLInstance; also update the docs"). The controller acknowledges the reply (👀), runs the recipe again with the reply as its instruction, and the bot **edits its spec comment in place**. The parent only ever has one spec comment. A maintainer with write access can also edit that comment directly.
3. **Started.** A person with triage access or higher adds `<trigger>/fanout-go`. Only then does the controller create and label children. Changes made after that go through step 2 too, with the effects listed under *Changing a started fan-out*.

The spec is read from the bot's comment carrying `<!-- factory:fanout-spec -->`, else from the issue body. Only the bot's comment counts: a spec comment by anyone else is ignored, so nobody can change what is fanned out by posting a comment. The bot posts it as a comment because it often cannot edit another person's issue body (public repos, as in KCC).

A spec that does not parse (in the body, or a hand edit of the comment) gets one comment saying what is missing, and nothing else happens until it is fixed.

### The controller

`watch/fanout` runs on the slow issue sweep, and when the Nudger sees a child close (the Nudger finds a child's parent by the marker, as it finds a workflow by its link). For each open parent labelled `<trigger>/fanout` and without the stop label, it does the following. Until the parent also has `<trigger>/fanout-go`, only steps 1 and 5 run, plus the proposal and its changes.

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

### Changing a started fan-out

The controller re-reads the spec every cycle, so a change takes effect on the next pass:

| Change | Effect |
|---|---|
| Item added | A new child, labelled when its turn in the window comes |
| Item removed | Its child is not labelled. A child that already exists is left as it is; the controller closes nothing |
| Task or title changed | Children **not yet labelled** are rewritten with the new text. Labelled ones, being worked on, are left alone |
| `Finally` changed | Used when the final child is created |
| Window changed | Applies from the next pass. Lowering it labels nothing new and unlabels nothing |

Rewriting children that have not been labelled is what makes the slow start useful. The first few PRs are where a task's description shows its gaps. With `create: all`, the remaining children already exist, so without the rewrite a fixed spec would only reach children created afterwards. Children not yet labelled have not been started, so nothing changes under a bot that is working.

### State on GitHub

The controller keeps no local state. GitHub holds everything, so a restarted daemon (or a second one) carries on where the last one stopped:

- **The children** are the sub-issues, found by the marker.
- **The window** and the PRs already counted against it live in a hidden block in the progress comment: `<!-- factory:fanout-state {"window":3,"counted":[1201,1207]} -->`. Recording the counted PRs is what keeps a cycle from counting the same merge twice. If the block is lost, the window is recomputed as `start + completed`, capped at `max`.
- **The progress comment** has one row per item (child, PR, state) and the current window. It is edited in place and never re-posted, so the parent does not fill up with progress comments.

### The `fanout` recipe

The `fanout` recipe is `on: [issue]` and writes a `FanOut` task output: the spec in the standard markdown form. It has a revise, `change`, whose input is the instruction (a person's reply).

The controller runs it the way the watch runs any task, in a sandbox of its own (`fanout-<repo>-<N>`), queued and capped like everything else. When the run ends, the controller posts or edits the spec comment from the output. A reply runs the `change` revise in the same session, so the agent keeps the context of earlier changes.

The agent only reads and writes text. It never creates or labels issues; the controller does that, and only after `fanout-go`.

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
   - The `watch/fanout` controller: `fanout-go`, children, the window, the final step, the progress comment.
   - The scanner skips parents.
   - The Nudger wakes a parent when one of its children closes.
4. **The proposal:**
   - The `fanout` recipe, its `FanOut` kind, and its `change` revise.
   - The controller runs it on a parent without headings, posts or edits the spec comment, and turns replies that mention the bot into `change` runs.
5. **Verify on KCC:**
   - #13781 as written: label it and check the proposed spec, change it with a reply, then `factory fanout sync --dry-run`.
   - Then `fanout-go`, starting with a window of 2.

Steps 2 and 3 serve parents that already use the headings. Step 4 is only for issues that don't.

## Later: a repo-agent board front end

This design does not need repo-agent. A board could later show a fan-out as a row: the proposal as a draft, with edit and apply, and the children as chips. Applying would add `fanout-go`, and a draft edit would be a `change` revise. That is a thought experiment for now. The controller and the GitHub conventions above are the contract either way.

## Open questions

- **Sub-issues in KCC:** does the bot have triage access there? If not, the parent's checklist (in the progress comment) is the only progress view.
- **Ordering:** is item order (the checklist's) right, or should the window prefer items whose services nobody else is working on, to avoid merge conflicts?
- **How a change is asked for:** a reply mentioning the bot, as above, or a slash command (`/fanout drop SQLInstance`), which is more explicit but another convention to learn?
- **Ramp on review, not merge:** should a PR approved but waiting on a human merge already count as a success? Counting only merges is slower but stricter.
