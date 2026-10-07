# A board driven by recipes

**Status:** Proposed.

Triage, plan, research, review and fix all run as recipes now. The board still knows each by name. Only the task-session view is driven by the recipe: its revise buttons come from the run factory records, and its draft buttons from the task output. Everything else is written per recipe:

| Where | What is per recipe today | Evidence |
|---|---|---|
| Launch | the row buttons (Triage, Plan, Fix, Review), the button→verb map, the Request verbs, one `Start*` per recipe naming it | `Work.js:402-446`; `handlers_board.go:1316`; `request_types.go:75-81`; `factorycli.go:457/529/617/658`, `research.go:133` |
| Finding a run | five annotation keys, each tied to a kind | `factorycli/recorded_run.go:76-82` |
| Revises | enablement and dispatch branch on the kind; the controller routes by sandbox shape (review → fix → otherwise Notes); `reviseInputs = {"iterate": ["instruction"]}` | `handlers_task_session_actions.go:100-188, 290-305`; `revise.go:86-97, 182-190` |
| Drafts | one stored-output annotation per kind; a panel only for Plan and Notes; verbs filtered by Go `boardVerbs[kind]` and UI `DRAFT_VERBS[kind]`; apply knows Triage, Plan and Notes | `actions.go:43-48`; `board_actions.go:47-111`; `apply.go:67-80` |
| PR row | Address comments, Fix CI, Iterate as fixed buttons; the fix recipe's **Rebase** is never shown | `Work.js:818-858` |
| Stages | one stage per issue (`untriaged` … `pr-open`), computed on every request from `last-task-type`/`last-task-state`, plan and triage annotations, the PR alias and the claim; it drives the chips, which buttons show, and Up Next's attention | `handlers_board.go:794-808, 902-935`; `Work.js:42-67, 397-417` |

A new recipe, or a new revise of an existing one, needs Go and UI changes in all of these before anyone can click it. This note moves what the board shows and files to the recipe, and keeps per-recipe code only where the board does something only that recipe needs.

## Decisions

1. **The recipe says where it runs.** A recipe declares the items it runs on (`on: [issue]`, `[pr]`, `[my-pr]`, `[repo]`). The board shows a launch button for it on those rows. `pr` is any PR, which a recipe only reads; `my-pr` is a PR the recipe may push to, which opens up more.
2. **The recorded run says what it is.** The run annotation carries the recipe's name, its task-output kind, and each revise's inputs. The board finds a sandbox's runs by scanning, not by a list of keys.
3. **One revise path.** A revise Request names a sandbox and a revise id. The controller runs `factory recipe revise` and stores the task output. Plan, review and fix keep their extra steps as hooks on that path, not as separate paths.
4. **One draft and one apply path.** The draft is the run's stored task output, for any kind. An apply Request names the sandbox, the run and the action, and the controller runs `factory apply --action`. Verbs carry permissions, not kinds.
5. **No stages.** A row shows its runs, each with its own state, read from the sandbox. Buttons, chips and attention follow from the runs by one set of rules, the same for every recipe.
6. **Per-recipe board code stays only where it is board policy:** settings (auto-triage, auto-fix, auto-review), Review's Finalize link, research's landing pane. A recipe without such code still gets a button, a session, revises and a draft.
7. **The overseer is unchanged**, as for every recipe step so far.

## The design

### factory: recipes declare it, runs record it

- **`on:`**, a list of `issue`, `pr`, `my-pr`, `repo`. `factory recipe <name> --url` already refuses a URL of the wrong shape. This makes that rule data the board can read.
  - **`pr`**: any pull request. The recipe reads it and writes only through applies with the caller's token: a review, a comment.
  - **`my-pr`**: a pull request the caller may change. It was **authored by the caller, and its head is a branch on the caller's fork**, the only place `push` pushes to.
    - The board's "mine" is "authored by you" alone, so a PR you authored whose head is on the repository or on someone else's fork is not `my-pr`. The button shows there, disabled: "the PR's head is not on your fork".
    - factory checks the same rule when the recipe runs, since the board's view can be stale. That is fix's rule for a PR it did not open: "adopt it first" (`factory pr adopt`).
  - The built-ins: triage, plan and fix `[issue]`; review `[pr]`; research `[repo]`. care ([care-recipe.md](care-recipe.md)) is `[my-pr]`.
  - `my-pr` rows also get `pr` recipes: Review shows on your own PR, as it does today.
  - **Revises are not filtered by `on`.** They follow their run: the fix's revises show on the PR its run opened, which is `my-pr` by construction.
- **Revise inputs.** A revise lists the inputs it asks for (`inputs: [instruction]` on fix's `iterate`). Today an input is marked `revise: true` for every revise.
- **The recorded run gains** `recipe`, `kind` (the `task-output` kind), per-revise `inputs`, and its own **`state`**:

  ```json
  {"name": "…", "task": "…", "startedAt": "…",
   "recipe": "fix", "kind": "Change",
   "state": "Completed", "endedAt": "…",
   "revises": [{"id": "iterate", "label": "Iterate", "inputs": ["instruction"]},
               {"id": "rebase", "label": "Rebase"}]}
  ```

  - `state` is `Running`, `Completed` or `Failed`. factory writes it in the same update that writes the task's `last-task-state` or side `<type>-task-state` today. A revise writes it too: the run is `Running` while a revise of it runs.
  - Today only side tasks (triage) have a state of their own. The issue sandbox's main task, plan then fix, shares `last-task-type`/`last-task-state`, so the plan's state is gone once the fix starts. With `state` in every run, each run reads the same way. `last-task-*` stay, for the overseer and `factory sandbox` only; the board stops reading them.

- **`factory recipe list -o json`** prints the built-in recipes, each with its name, label, `on`, inputs, kind and revises. The board reads it from its factory binary, so a new built-in shows up with the image that ships it.

### repo-agent: runs, revises and drafts by data

- **Runs.** `SessionRun` reads every `sandbox.gemini.google.com/*-run` annotation and takes the kind from the record. `recordedRunKinds` and `reviseInputs` go.
- **Revises.**
  - The session view enables a revise unless its run's task is running, or a hook says otherwise. Plan's hook (disabled once approved, or when the draft came from another session) is the only one today.
  - `ensureRevises` and `settleRevise` take one path for a sandbox-keyed revise:
    1. wake the sandbox;
    2. run `factory recipe revise <sandbox> <id> --input …`;
    3. store the task output (below).
  - Review (replace the pending review), fix (post-replies) and plan (store as the draft, on the issue row) keep what they do after the revise as hooks. Notes stops being the fallback; it becomes one more kind with no hook.
- **Drafts.**
  - The task output is stored on the sandbox under one key per run (`board.gemini.google.com/<task-type>-output`). This replaces the `triage-output`, `plan-output` and `notes-output` keys. No migration: sandboxes are recreated, as for every recipe change so far.
  - The task-session `GET` returns it as `draft` for any kind, with its actions.
- **Apply.**
  - A `VerbApply` Request names `{sandbox, run, action}`. The controller runs `factory apply --action <action>` on the stored output with the executor's token, and stamps the click on the sandbox, in one annotation per run (`board.gemini.google.com/<task-type>-applied`, action → time).
  - This replaces `triage-published-at` and `plan-approved-at`: "posted" and "approved" are an applied action on that run's draft.
  - `boardVerbs[kind]` becomes a permission per verb:
    - `label` needs triage;
    - `comment`, `post-*`, `open-pr`, `push-notes` need what they need today;
    - an unknown verb needs write.
  - UI `DRAFT_VERBS` keeps only fallback labels and confirm texts, and filters nothing.

### repo-agent: rows from runs, not stages

The work item's `stage`, its `switch` in the API and the UI's stage table go. The API returns, per row, its runs (below, `sessions[]`), and the UI applies these rules:

| A run is | Its chip | Its button | Attention (Up Next) |
|---|---|---|---|
| none yet | — | *‹label›* | — |
| `Running` (start or revise) | *‹label›: running* | none; *Continue session ↗* | working |
| `Completed`, with a draft nothing has been applied to (beyond `edit`) | *‹label›: ready* | the draft's actions; *Continue session ↗* | needs you |
| `Completed`, draft applied or none | *‹label›: done* | *‹label› again* | — |
| `Failed` | *‹label›: failed* | *‹label› again* | needs you |

- **An issue with an open PR from its fix** offers no more recipes on its row. The PR row carries the work from there, and the issue row links to it.
- **Order** is the order of `factory recipe list`. The built-ins are listed triage, plan, fix, review, research.
- **"Again"** starts a new run with a new run name, as *Fix again* does today. The separate `rerun` path goes.
- **Unclaimed** stays a fact on the row (who claimed it), not a stage. Up Next keeps its own ordering of issues nobody has touched.
- **Promote PR** moves off the issue row to the PR row, where it acts.

### repo-agent: launch by data

- **One launch Request**, `{verb: recipe, recipe: <name>, number, inputs}`, and one `StartRecipe(name, inputs)` in factorycli. It runs `factory recipe <name> --url … --run-name …` and records the run like the others.
- **The rows list sessions, not four fields.** A work item carries `sessions[]` (recipe, label, sandbox, task, state, draft and applied actions). Each becomes a chip, a button and a *Continue session ↗* by the rules above, and its revises.
- **Launch buttons** come from `factory recipe list`, filtered by `on` and by the row. The work item says which it is: `issue`, `pr`, or `my-pr` (authored by the member and headed on their fork, which the API already has from the PR's `head.repo`). Past a few, the rest go under a `Run ▾` menu on the row.
- **One PRs tab.** *Review* and *My PRs* become one *PRs* tab: the same rows, whose actions change with what the row is.
  - Any PR: the `pr` recipes (Review, Review again; Finalize and Abandon on a pending review) and Deploy ▾.
  - Your PR: those, plus the `my-pr` recipes, the fix session's revises, Auto, and Promote on a draft.
  - Filter chips replace the split: *mine*, *review requested*, *drafts*. The tab opens on what *Up Next* would show first. *Up Next* is unchanged: it already mixes the two.
- **The PR row's follow-ups** are the fix session's revises: Iterate (with its input box, from `inputs`), Address comments, Fix CI, and Rebase.
- **What stays per recipe in the controller:**
  - `ensureFix`'s apply of `open-pr` when the run ends, and the PR alias;
  - `ensureReview`'s pending review;
  - plan approval;
  - the auto-* settings that start fix, triage and review on their own.
  These are board policy for those recipes. A recipe gains such a hook in Go when it needs one, not to appear on the board.

## The board after this

| Page | Element | Source |
|---|---|---|
| Board | Tabs: Up Next, Issues, **PRs**, Research | Hardcoded: board features |
| Issues row | Launch buttons (Triage, Plan, Fix, any new recipe) | **Recipes**: `factory recipe list`, filtered by `on: issue`; label and order from the list |
| Issues row | *‹label› again* | **Run state** on the sandbox, by the row rules |
| Issues row | Chips (‹label›: running / ready / done / failed) | **Run state** on the sandbox, by the row rules; no stages |
| Issues row | Button → Request | One launch Request `{recipe, number, inputs}` |
| Issue and PR rows | Draft actions (Add labels, Post, Fix with this plan, Edit, Reject…) | **Task output's actions**; filtered only by the verb's permission. `DRAFT_VERBS` keeps fallback labels and confirm texts |
| Issue and PR rows | *Continue session ↗* | **Recorded runs**: `sessions[]` from every `*-run` annotation |
| PR row | Review, Review again | **Recipes** (`on: pr`) and run state |
| PR row (yours) | Iterate (with its input box), Address comments, Fix CI, **Rebase** | **Fix run's revises**, inputs from the run record |
| PR row (yours) | iterating / addressing / … chips | **Run state** and the newest revise Request's revise label |
| PR row (yours) | Promote, Auto | Hardcoded: GitHub draft → ready, and a board setting; not recipes |
| PR row | Finalize ↗ | Hardcoded: review's link to GitHub's pending review |
| PR row | Abandon | **Review output's `reject`**, with review's hook deleting the pending review |
| PR row | Deploy ▾ | Unchanged: runbooks from the repository, modes hardcoded |
| Task session view | Revise buttons | **Recorded run's revises**; enabled unless the run is running, plus plan's hook |
| Task session view | Draft panel + actions | **Task output**, for any kind; triage's form and review's inline comments stay written for them |
| Research launcher | overview / recent changes | Hardcoded |
| Settings | auto triage / fix / review | Hardcoded: board policy |
| Controller | Recipe names | Only in the hooks: fix (open-pr, PR alias), review (pending review), plan (approval), triage and the auto-* settings. Launch, revise and apply take any recipe |

## Steps

1. **repo-agent: one PRs tab.** Review and My PRs merge, with the row's actions from whether it is `my-pr`; the API computes that from the PR's author and `head.repo`. The follow-ups come from the fix run's revises, which shows Rebase. This needs nothing from factory: the run already records ids and labels, and `iterate`'s input stays in `reviseInputs` until step 3.
2. **factory:** `on:`, revise `inputs`, `recipe`/`kind`/`inputs`/`state` in the recorded run, and `factory recipe list -o json`.
3. **repo-agent:** runs found by scanning; one revise path with hooks; `reviseInputs` and `recordedRunKinds` deleted.
4. **repo-agent:** one stored-output key, `draft` for any kind, `VerbApply` by `{sandbox, run, action}`, permissions per verb, the per-run applied stamp.
5. **repo-agent:** the launch Request, `StartRecipe`, `sessions[]` on work items, launch buttons from `factory recipe list`, and the stages removed: chips, buttons and attention by the run rules. The five `Start*` functions become `StartRecipe` plus their hooks.
6. **Verify in-cluster:** a new built-in recipe, added to factory with no repo-agent change, shows on its rows, runs, opens a session, revises and applies.

## Not planned here

- **The board's other tabs, settings, Deploy ▾ and research's landing pane.** They are board features, not recipe surfaces.
- **A recipe's own UI.** Triage's label form and review's inline-comment view stay written for them. A new kind's draft is shown as its `markdown`.
- **Recipes from the watched repository** (e.g. `.agents/recipes/`). factory reads built-ins and local files only. Once it reads a repository's recipes, `factory recipe list` lists them and the board needs nothing more.
- **Who may launch a recipe.** Launching is a board member's, as today; per-recipe permissions can come later from the recipe.
