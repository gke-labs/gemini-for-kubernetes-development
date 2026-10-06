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
| Stages | chips from task types and revise ids by name | `handlers_board.go:794-808, 903-935` |

A new recipe, or a new revise of an existing one, needs Go and UI changes in all of these before anyone can click it. This note moves what the board shows and files to the recipe, and keeps per-recipe code only where the board does something only that recipe needs.

## Decisions

1. **The recipe says where it runs.** A recipe declares the items it runs on (`on: [issue]`, `[pr]`, `[my-pr]`, `[repo]`). The board shows a launch button for it on those rows. `pr` is any PR, which a recipe only reads; `my-pr` is a PR the recipe may push to, which opens up more.
2. **The recorded run says what it is.** The run annotation carries the recipe's name, its task-output kind, and each revise's inputs. The board finds a sandbox's runs by scanning, not by a list of keys.
3. **One revise path.** A revise Request names a sandbox and a revise id. The controller runs `factory recipe revise` and stores the task output. Plan, review and fix keep their extra steps as hooks on that path, not as separate paths.
4. **One draft and one apply path.** The draft is the run's stored task output, for any kind. An apply Request names the sandbox, the run and the action, and the controller runs `factory apply --action`. Verbs carry permissions, not kinds.
5. **Per-recipe board code stays only where it is board policy:** settings (auto-triage, auto-fix, auto-review), the stage chips the five recipes have today, Review's Finalize link, research's landing pane. A recipe without such code still gets a button, a session, revises and a draft.
6. **The overseer is unchanged**, as for every recipe step so far.

## The design

### factory: recipes declare it, runs record it

- **`on:`**, a list of `issue`, `pr`, `my-pr`, `repo`. `factory recipe <name> --url` already refuses a URL of the wrong shape. This makes that rule data the board can read.
  - **`pr`**: any pull request. The recipe reads it and writes only through applies with the caller's token: a review, a comment.
  - **`my-pr`**: a pull request the caller may change. It was **authored by the caller, and its head is a branch on the caller's fork**, the only place `push` pushes to.
    - The board's "mine" is "authored by you" alone, so a PR you authored whose head is on the repository or on someone else's fork is not `my-pr`. The button shows there, disabled: "the PR's head is not on your fork".
    - factory checks the same rule when the recipe runs, since the board's view can be stale. That is fix's rule for a PR it did not open: "adopt it first" (`factory pr adopt`).
  - The built-ins: triage, plan and fix `[issue]`; review `[pr]`; research `[repo]`. Fix's PR mode, deferred in [fix-recipe.md](fix-recipe.md), is `[issue, my-pr]` once it is built.
  - `my-pr` rows also get `pr` recipes: Review shows on your own PR, as it does today.
  - **Revises are not filtered by `on`.** They follow their run: the fix's revises show on the PR its run opened, which is `my-pr` by construction.
- **Revise inputs.** A revise lists the inputs it asks for (`inputs: [instruction]` on fix's `iterate`). Today an input is marked `revise: true` for every revise.
- **The recorded run gains** `recipe`, `kind` (the `task-output` kind) and per-revise `inputs`:

  ```json
  {"name": "…", "task": "…", "startedAt": "…",
   "recipe": "fix", "kind": "Change",
   "revises": [{"id": "iterate", "label": "Iterate", "inputs": ["instruction"]},
               {"id": "rebase", "label": "Rebase"}]}
  ```

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
  - A `VerbApply` Request names `{sandbox, run, action}`. The controller runs `factory apply --action <action>` on the stored output with the executor's token, and stamps the click on the sandbox.
  - `boardVerbs[kind]` becomes a permission per verb:
    - `label` needs triage;
    - `comment`, `post-*`, `open-pr`, `push-notes` need what they need today;
    - an unknown verb needs write.
  - UI `DRAFT_VERBS` keeps only fallback labels and confirm texts, and filters nothing.

### repo-agent: launch by data

- **One launch Request**, `{verb: recipe, recipe: <name>, number, inputs}`, and one `StartRecipe(name, inputs)` in factorycli. It runs `factory recipe <name> --url … --run-name …` and records the run like the others.
- **The rows list sessions, not four fields.** A work item carries `sessions[]` (recipe, label, sandbox, task, state). Each becomes a *Continue session ↗*, a state chip (`<label>: running / ready / failed`), and its revises.
- **Launch buttons** come from `factory recipe list`, filtered by `on` and by the row. The work item says which it is: `issue`, `pr`, or `my-pr` (authored by the member and headed on their fork, which the API already has from the PR's `head.repo`). The five recipes that have buttons today keep their place and their chips. Others go under a `Run ▾` menu on the row.
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

## Steps

1. **repo-agent: one PRs tab.** Review and My PRs merge, with the row's actions from whether it is `my-pr`; the API computes that from the PR's author and `head.repo`. The follow-ups come from the fix run's revises, which shows Rebase. This needs nothing from factory: the run already records ids and labels, and `iterate`'s input stays in `reviseInputs` until step 3.
2. **factory:** `on:`, revise `inputs`, `recipe`/`kind`/`inputs` in the recorded run, and `factory recipe list -o json`.
3. **repo-agent:** runs found by scanning; one revise path with hooks; `reviseInputs` and `recordedRunKinds` deleted.
4. **repo-agent:** one stored-output key, `draft` for any kind, `VerbApply` by `{sandbox, run, action}`, permissions per verb.
5. **repo-agent:** the launch Request, `StartRecipe`, `sessions[]` on work items, launch buttons from `factory recipe list`. The five `Start*` functions become `StartRecipe` plus their hooks.
6. **Verify in-cluster:** a new built-in recipe, added to factory with no repo-agent change, shows on its rows, runs, opens a session, revises and applies.

## Not planned here

- **The board's other tabs, settings, Deploy ▾ and research's landing pane.** They are board features, not recipe surfaces.
- **Stage chips for the five recipes.** They keep their wording; only new recipes get the generic chip.
- **A recipe's own UI.** Triage's label form and review's inline-comment view stay written for them. A new kind's draft is shown as its `markdown`.
- **Recipes from the watched repository** (e.g. `.agents/recipes/`). factory reads built-ins and local files only. Once it reads a repository's recipes, `factory recipe list` lists them and the board needs nothing more.
- **Who may launch a recipe.** Launching is a board member's, as today; per-recipe permissions can come later from the recipe.
