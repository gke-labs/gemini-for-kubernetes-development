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

- It posts a **spec comment**: your issue rewritten under the standard headings, with the task written as a template for one child (`{{.item.name}}`) and settings filled in (see *The spec* below).
- It adds **`overseer/stop`**: nothing more happens until a maintainer has read the spec.
- It posts a **progress comment**, a table of items, which it keeps up to date from then on.

If your issue already uses the standard headings, the body is the spec. No agent runs, and the bot only adds `overseer/stop` and the progress comment.

### 3. Review and edit the spec

Edit the bot's spec comment directly on GitHub (*Edit* in the comment's ⋯ menu). Editing a bot's comment needs write access, so maintainers do it; anyone else asks a maintainer. You can fix the task text, drop or add items, change the child title or labels, and change the window and the checkpoints.

If the proposal is too far off, delete the spec comment and the bot writes a new one. You can also first edit the parent issue to make it clearer.

### 4. Remove `overseer/stop` to start

The bot creates the children and labels the first batch (`window.start`, 2 by default) `overseer`. From there each child is an ordinary overseer issue: a coder bot picks it up and opens a PR, and the PR goes through review as usual.

The progress comment shows each child's PR and state. (Listing children as GitHub sub-issues is left for later.)

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
| `## Task` | What to do for one child: one item, or a group of them. A template (see *Templates*). | "Migration Steps per Resource" |
| `## Items` | A checklist; each line is an item. A line already checked is not fanned out. Not needed when the items come from a file (see *Items from a JSON file*). | "Affected Resources & Controllers Checklist" |
| `## Finally` (optional) | One more child, created when every item is done. | "Final Deprecation & Cleanup" |
| `## Fan-out` (optional) | Settings, as a YAML block (below). Nobody has to write it. | — |

An item's name is its bold text if it has any, otherwise the line up to the first ` (` or ` - `. The whole line, file paths included, goes into the child's body.

```yaml
# ## Fan-out
title: "{{.item.name}}: {{.parent.title}}"  # child issue title, a template
labels: [direct-migration]    # labels for every child, besides the trigger label
create: lazy                  # the default; all: create every child at the start (group 1 only)
group: 1                      # items per child; {start: 1, max: 5} grows (see *Groups*)
window: {start: 2, max: 8}    # children in flight: the slow start (below)
checkpoints: [2]              # stop when this many items are done; any number of them; [] never
items:                        # optional: the items from a JSON file instead of ## Items
  from: config/kms-resources.json
  select: .resources
  where: '{{ne .status "done"}}'
  name: "{{.kind}}"
```

Where each setting comes from when the bot writes the spec:

| Setting | Source |
|---|---|
| `title` | Deduced by the agent from the issue |
| `labels` | Deduced: the parent's own labels, or labels the issue's text asks children to carry |
| `create`, `window`, `checkpoints` | Defaults, not deduced. The agent writes them out so that maintainers see them and can edit them |

In a spec written by hand in the body, the whole section can be left out: every setting takes its default (title `{{.item.name}}: {{.parent.title}}`, or `{{range .items}}…` names when `group` is above 1, no extra labels, one item per child).

#13781 as a spec, as the bot would post it (items shortened):

~~~markdown
<!-- factory:fanout-spec -->
## Fan-out
```yaml
title: "Migrate {{.item.name}} to kmsv1beta1.KMSCryptoKeyRef"
labels: []
group: 1
window: {start: 2, max: 8}
checkpoints: [2]
```

## Task
For `{{.item.name}}`, switch its KMS reference from `refs.KMSCryptoKeyRef` to
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

The agent's part was mapping the issue's own headings to these, rewriting the steps for one `{{.item.name}}`, and picking a title. #13781 has no labels and asks for none, so `labels` is empty; the rest are the defaults. It copies the items exactly as written, because their file paths go into each child.

A child issue looks like this:

```markdown
<the Task section, rendered for this child>

### Item
- **ApigeeInstance** (`apis/apigee/v1alpha1/instance_types.go`, …)

Part of #13781.
<!-- factory:fanout parent=13781 items=apigeeinstance -->
```

The marker comment makes children findable: the controller finds a parent's children by it, never by title. "Part of #N" puts each child on the parent's timeline, where the controller looks for markers; the state (below) also records every child it created, since a timeline can lag a creation. Linking children as GitHub sub-issues, so GitHub shows a progress bar, is left for later.

A child is an ordinary issue, so the existing workflow features apply to it. If the Task section links a workflow file (for example `.agents/workflows/kcc-example.txt` with `{{.item.name}}` as its kind), each child becomes a workflow issue: a fan-out of multi-step workflows, with no extra design needed.

### Templates

The Task, the title, `items.name` and `items.where` are Go [`text/template`](https://pkg.go.dev/text/template)s. Each is rendered with:

| Name | Value |
|---|---|
| `.item` | The child's first item; its only one when `group` is 1 |
| `.items` | Every item of the child, in order |
| `.parent` | The parent issue: `.number`, `.title` |

An item is an object. From `## Items` it has `name` (the bold text, or the line up to ` (` or ` - `) and `line` (the whole line without its checkbox). From a JSON file it is the element itself, plus `name`, so `{{.item.kind}}` reads the element's own field. Conditions and loops work as in any Go template:

```markdown
## Task
Migrate {{range .items}}`{{.kind}}` ({{.file}}), {{end}}to `kmsv1beta1.KMSCryptoKeyRef`.
{{if .item.beta}}This is a beta resource: keep the old field, deprecated.{{end}}
```

Templates are rendered with `missingkey=error`, and every item is rendered once when the spec is parsed, so a typo such as `{{.kidn}}` is a spec error in the progress comment, never a broken child. Only the built-in functions exist, and a template cannot run commands or read files. They are enough for most conditions:

| Built-in | Use |
|---|---|
| `and a b c`, `or a b c`, `not a` | Combine conditions; `and` and `or` take any number of arguments and stop early |
| `eq x a b c` | `x` equals any of them, so it doubles as "in a list" |
| `ne`, `lt`, `le`, `gt`, `ge` | Compare strings or numbers |
| `len` | `{{gt (len .files) 3}}` |
| `index . "beta"` | A field that may be missing: empty instead of a `missingkey` error |
| `if` / `else if` / `else`, `range`, `( … )` | Branches, loops, grouping |

```yaml
where: '{{and (ne .status "done") (or (index . "beta") (eq .service "kms" "kmsautokey"))}}'
```

There is no string matching (contains, prefix, regex). Helper functions are added when a fan-out needs one, as pure functions only, so a template still cannot reach outside the item: `contains`, `hasPrefix`, `hasSuffix`, `lower` and `join`, from Go's `strings`, are the likely first ones.

CEL was considered for `where`. It is the better condition language (`has()`, `in`, string methods, list macros), but it cannot render text, so a spec would mix two languages, and it adds a dependency factory does not have (`cel-go`, ANTLR). Go templates do conditions well enough. If filters outgrow them, `where` alone can move to CEL later.

The child's body is the rendered Task, then the items' lines under `### Item` (or `### Items`), then "Part of #N." and the marker, whose `items=` lists the keys of every item in the child.

### Items from a JSON file

The items can come from a JSON file in the repository instead of a checklist:

```yaml
items:
  from: config/kms-resources.json   # read from the default branch, every pass
  select: .resources                # a dotted path to the array; left out, the file is the array
  where: '{{ne .status "done"}}'    # optional: only the elements for which this renders true
  name: "{{.kind}}"                 # an item's name; its key is the name, lowercased, folded to '-'
```

- `select` is a plain dotted path (`.a.b`), not jq.
- An element can be an object or a string; a string is its own name.
- **Leaving items out** is `where`, a template rendered for each element: `true` keeps it, `false` leaves it out, and anything else is a spec error. The example fans out only the resources not yet done. Without `where`, every element is an item.
- A name that renders empty is a spec error, so a typo in `name` is caught rather than silently dropping items.
- Two items with the same key, a missing file, or a file that is not JSON are spec errors.
- The file is re-read on every pass, and the commit it was read at is shown in the progress comment. Changing it is like editing the checklist (see *Changing the spec*): items are matched across passes by key.
- A spec has `## Items` or `items.from`, not both.

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

1. **The newest spec comment**, carrying `<!-- factory:fanout-spec -->`, written by the bot or by a maintainer (author association OWNER, MEMBER or COLLABORATOR). A spec comment by anyone else is ignored, so nobody else can change what is fanned out by posting a comment. Only people with write access can edit the bot's comment, so editing the spec is a maintainer's action; a maintainer may also post a spec comment of their own.
2. **Otherwise the issue body**, if it has the standard headings.
3. **Otherwise there is no spec.** The controller runs the `fanout` recipe, posts its output as the spec comment, and adds the stop label. This also happens again if the spec comment is deleted.

The spec comment is separate from the progress comment so that the bot never edits a comment a maintainer is editing.

A spec that does not parse (a hand edit gone wrong) is reported in the progress comment, edited in place, saying what is missing. The controller then does nothing more until it is fixed.

### The stop label is the gate

A fan-out uses the stop label the watch already has (`<trigger>/stop`, and `overseer/stop` always): it means "leave this issue alone". On a parent, it means no new children and no new labels. It does not reach children that are already labelled. To pause them too, a person labels them.

- **The bot adds it** when it proposes a spec, and when a checkpoint is reached. Each time it comments to say why.
- **A person removes it** to start or resume. That is the only go-ahead, every time.
- **A person may add it** at any time, as on any issue.

Checkpoints reached are recorded in the progress comment's state (below), so removing the stop label does not trip the same checkpoint again.

### The controller

`watch/fanouts` is a subcontroller of its own, one goroutine with no workers. It sweeps every open parent labelled `<trigger>/fanout` (either spelling) every 5 minutes, one after another. When the sandbox reconciler collects a closed issue's sandbox, it tells the fan-out controller as well as the Nudger, and a closed child (found by its marker) wakes its parent straight away. Nothing wakes a parent when a PR closes unmerged or a stop label comes off; the sweep catches those. While the queue drains, no pass runs. For each parent:

1. **Read** the spec (above), the children (issues with the marker on the parent's timeline, plus the ones the state records), and the state in the progress comment.
2. **Propose**, if there is no spec: run the recipe, post the spec comment, add the stop label. Nothing else happens on this pass.
3. **Report:** update the progress comment. This runs on every pass, stopped or not.
4. If the parent has the stop label, stop here.
5. **Checkpoint:** if the number of items done has reached a checkpoint not yet passed, add the stop label, comment, record the checkpoint, and stop here.
6. **Create.** With `create: lazy` (the default), create children only as they are labelled, each for the next `group` items without a child. With `create: all`, create every missing child at once, unlabelled. Creation is idempotent by the marker's `items=` keys. Children not yet labelled whose text is out of date with the spec are rewritten (see *Changing the spec*).
7. **Label.** `active` is the children that are open and carry the trigger label. Label the next `window − active` children, in item order (existing children not yet started, and new ones), with the trigger label plus the spec's `labels`. From then on the existing scanner owns them, as it owns any labelled issue. The daemon's `--max-pending` still caps how many run at once, so the window decides which children are eligible, not how fast they run.
8. **Final.** Once every item's child is closed, create the `Finally` child (labelled at once). When that one closes as completed, or straight away if there is no `Finally` section, close the parent. If the bot cannot close it, it comments instead.

The issue scanner skips a parent labelled `<trigger>/fanout`. Otherwise a coder bot would pick up the whole parent as one fix task. It also leaves alone a child that has the marker but not the trigger label, both when it adopts issues filed by the bot's login and when it queues. The fan-out files its children as that same login, so adoption would otherwise label every child at once and defeat the window. Once the fan-out labels a child, the scanner handles it like any other labelled issue.

### The slow start

It works the way TCP ramps up: a window that grows on success and shrinks on failure.

- It starts at `window.start` (default 2).
- A child closed as completed (its PR merged) adds 1, up to `window.max` (default 8). One step per success is what doubles the window each round, as TCP slow start does per round trip.
- A child whose linked PR closed without merging halves the window, with a minimum of 1. The child stays open: what happens to it is the existing watch's business (a person relabels it, closes it, or the bot retries).
- A child closed as not planned is a person skipping the item. The window is unchanged, and the item counts as done, for checkpoints and for the `Finally` step.

### Groups

A child can cover several items, so one PR does a batch of them. `group` is how many, and like the window it can grow:

```yaml
group:  {start: 1, max: 5}   # items per child; `group: 5` is {start: 5, max: 5}; default 1
window: {start: 1, max: 3}   # children in flight
checkpoints: [3, 15]         # items done
```

The ramp has two phases. The group grows first, then the window: first find out how big a PR reviewers take, then how many at once.

1. **Grow the group.** While the group is below `group.max`, the window stays at `window.start`, and each child closed as completed doubles the group, up to `group.max`.
2. **Grow the window.** Once the group is at `group.max`, each child completed adds 1 to the window, as above.

A PR closed unmerged backs off in the reverse order: it halves the window, and only once the window is 1 does it halve the group, down to 1.

With the settings above:

| Child | Items | Children in flight | Items done after |
|---|---|---|---|
| 1 | 1 | 1 | 1 |
| 2 | 2 | 1 | 3 (checkpoint 3) |
| 3 | 4 | 1 | 7 |
| 4 | 5 | 1 | 12 |
| 5, 6 | 5 each | 2 | 22 (checkpoint 15) |
| … | 5 each | 3 | |

- **Units.** The window counts children (PRs in flight). Everything else counts items: checkpoints, the progress comment, and what `Finally` waits for. A child closed as completed counts all its items done; closed as not planned, all of them skipped.
- **A group is made when its child is labelled**: the next `group` items without a child, in order. A child keeps the items it was made with; a group that grows only changes the next child. So `group.max` above 1 needs `create: lazy` (the default, and a spec with it and `create: all` is an error), and children not yet labelled do not exist. The progress comment still lists every item from the start.
- **Default checkpoint**: the items of the first batch, `group.start × window.start`.
- `group: 1`, the default, is the one-item-per-child fan-out described above.

`create: lazy` is the value called `batch` before groups; renamed so "batch" does not mean two things.

### Changing the spec

The controller re-reads the spec every pass, so a change takes effect on the next one:

| Change | Effect |
|---|---|
| Item added | A new child, labelled when its turn in the window comes |
| Item removed | Its child is not labelled. A child that already exists is left as it is; the controller closes nothing. With `create: lazy`, an item without a child just drops out |
| Task or title changed | Children **not yet labelled** are rewritten with the new text. Labelled ones, being worked on, are left alone |
| `Finally` changed | Used when the final child is created |
| Window, group or checkpoints changed | Apply from the next pass. Lowering the window labels nothing new and unlabels nothing; a new group size applies to the next child made |

Rewriting children that have not been labelled is what makes the slow start useful. The first few PRs are where a task's description shows its gaps. With `create: all`, the remaining children already exist, so without the rewrite a fixed spec would only reach children created afterwards. Children not yet labelled have not been started, so nothing changes under a bot that is working.

### State on GitHub

The controller keeps no local state. GitHub holds everything, so a restarted daemon (or a second one) carries on where the last one stopped:

- **The spec** is the bot's spec comment, or the body.
- **The children** are found by the marker, on the parent's timeline and in the state.
- **The window, what was already counted against it, the checkpoints passed, the children created and the children labelled** live in a hidden block in the progress comment: `<!-- factory:fanout-state {"window":3,"group":1,"counted":[1201,1207],"checkpoints":[2],"children":{"apigeeinstance":1201},"started":[1201]} -->`. `counted` holds the children closed and the PRs closed unmerged that moved the window, so a pass never counts one twice. A child in `started` is never labelled again, so a person who unlabels a child has the last word. If the block is lost, the group is recomputed as the largest completed child's size, doubled and capped at `group.max` (just the largest when nothing completed), and the window as `start + completed` once the group is at its max, capped at `max`, and the checkpoints at or below the number done count as passed.
- **The progress comment** has one row per item (child, PR, state) and the current window. It is edited in place and never re-posted, so the parent does not fill up with progress comments.

### The `fanout` recipe

The `fanout` recipe is `on: [issue]` and writes a `FanOut` task output: the spec in the standard markdown form. It has no revises. Changes are made by maintainers, in the comment.

It has one action, `post-spec`. Applying it adds the stop label, then posts the bot's spec comment (or edits it, if there is one), so the fan-out cannot start before the spec is read. The stop label is `<prefix>/stop` for the issue's `<prefix>/fanout` label, else `overseer/stop`, which the daemon always honours. Once a task's spec is posted, applying it again does nothing: a maintainer may have edited the comment or removed the stop label since. Both touch only the run's own issue, so it fits the rule that an action never writes to another issue.

It runs as any recipe does, from the command line:

```sh
# Write the spec: task-output.yaml (kind FanOut). Nothing is posted.
factory recipe fanout --url https://github.com/GoogleCloudPlatform/k8s-config-connector/issues/13781

# Show what applying it would post.
factory recipe fanout --url …/issues/13781 --apply --dry-run

# Post it: the spec comment and the stop label.
factory recipe fanout --url …/issues/13781 --apply
```

The controller makes the same call in-process, as `factory pr watch` already runs `care` (`runRecipe`, `pr_watch_care.go`), with `--apply` and the run name `fanout-<N>-<time>`. `--apply` makes it idempotent: a daemon restarted mid-run that calls it again picks up the recipe's last run on the parent, following it if it is still running or applying its result if it has not been applied, instead of starting another. Once that run is applied, as when the spec comment has been deleted, the next call starts a new one. `runRecipe` lives in `pkg/commands`, which the `watch` package cannot import, so `watch_cmd.go` passes it in as a function (`Watcher.ProposeFanout`). The run uses the issue's sandbox (`fix-<repo>-<N>`), as `factory recipe fanout` by hand does; a parent is never fixed itself, so nothing else runs there. It starts only below `--max-pending`, runs in a goroutine of its own so that the sweep goes on, one at a time per parent, and a failed one waits an hour before the next. The controller syncs the parent again as soon as it lands.

The agent only reads and writes text. It never creates or labels issues; the controller does that.

### Permissions

- **Creating issues** needs only read access on a public repo.
- **Labelling, and unlabelling `overseer/stop`,** need triage access. Without it, the controller still creates children with the marker and "Part of #N", and says once, on the parent, that labelling needs a person (or a bot with triage).
- **Closing the parent** needs the same access as editing it; without it, the controller comments.
- **Editing the spec comment** needs write access, which is what makes it a maintainer's action.

## Steps

1. **This note.**
2. **factory `pkg/fanout`:**
   - The parser: `Spec`, items, settings.
   - `Decide(Input) Plan`, a pure function returning the issues to create or rewrite, the labels to add, the checkpoint to stop at, and the new state. All the rules above live here, tested by table.
   - `Sync`, one pass on GitHub: read, `Decide`, write, the progress comment. The watch controller (step 3) calls it.
   - `factory watch fanout --url <issue> [--dry-run]` runs it once by hand, so it can be tried on #13781 before any daemon runs it.
3. **watch:**
   - The `watch/fanouts` controller: a sweep calling `Sync` on each parent.
   - The scanner skips parents and children the fan-out has not labelled (adoption too).
   - The reconciler's closed-issue hook wakes a parent when one of its children closes.
4. **Templates, item files and groups** (two PRs):
   - Templates (`text/template`, `.item` / `.items` / `.parent`, `missingkey=error`) replacing `{item}` / `{parent}`, and `items.from` / `select` / `where` / `name` read through the contents API.
   - `group` with the two-phase ramp, `create: lazy`, `items=` markers and the state's `group`.
5. **The proposal** (two PRs):
   - The `fanout` recipe, its `FanOut` kind and its `post-spec` action (`factory recipe fanout`).
   - The controller runs it in-process (`runRecipe` with `--apply`, run name `fanout-<N>-<time>`) on a parent without a spec.
6. **Verify on KCC:**
   - #13781 as written, by hand first: `factory recipe fanout --url … --apply`, edit the spec comment, then `factory watch fanout --url …/issues/13781 --dry-run`.
   - Then label it `overseer/fanout` and let the daemon carry on.
   - Then remove `overseer/stop`, with a window of 2 and the default checkpoint.

Steps 2 to 4 serve parents that already use the headings or an item file. Step 5 is only for issues that don't.

## Later: a repo-agent board front end

This design does not need repo-agent. A board could later show a fan-out as a row: the spec as a draft to edit, the children as chips, and removing the stop label as its button. That is a thought experiment for now. The controller and the GitHub conventions above are the contract either way.

## Later: detecting a fan-out from the issue

For now a fan-out is asked for with the `overseer/fanout` label. Later, an issue labelled only `overseer` could be recognised as a fan-out: by the standard headings (deterministic), or, for a body with a task list of several items, by running the `fanout` recipe first and letting it answer "not a fan-out" when it isn't one. A false positive would cost one proposal comment, since the stop label holds it. The label would remain as the way to force a fan-out, and removing it as the way to refuse one.

## Open questions

- **Triage access in KCC:** does the bot have it? Without it, it cannot label children; and sub-issue linking, if added, needs it too.
- **Label name:** `overseer/fanout`, or something that says "parent" more plainly, such as `overseer/fanout-parent`?
- **Ordering:** is item order (the checklist's) right, or should the window prefer items whose services nobody else is working on, to avoid merge conflicts?
- **Ramp on review, not merge:** should a PR approved but waiting on a human merge already count as a success? Counting only merges is slower but stricter.
