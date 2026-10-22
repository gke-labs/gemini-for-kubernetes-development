# Recipes from the repository

Status: steps 1-3 built, and repository recipes on the board (each board's catalog is `recipe list --url` for its repository): a task keeps its recipe (#1934); recipes load from the repository's `.agents/recipes/` with `recipe list --url` and sources (#1935); results are files the agent writes and revise goes, factory #1938 and repo-agent #1939, #1940 (design #1933, #1936). Not built: step 4 (generic `sandbox:`; `sandbox: {kind: run}` is still the only kind, and warm is still special-cased by name in `runRecipe` and `recipe list`), step 5 (`long-lived` labels, `repoRecipes.identity`), step 6 (the overseer launching repository recipes; the watch's rules, watch-rules.md, are factory's, and a repository's `.agents/rules/` is read only by `factory watch rules … --dir`). A PR per run is not built. A repository recipe may not declare `sandbox:` yet (#1935).

A repository can define its own recipes in `.agents/recipes/<name>.yaml`. They run like the built-in ones: from the CLI (`factory recipe run --recipe <name> --url <item>`), and from a board's catalog, on the items their `on:` names. Adding one should take no Go change.

## Before

What this note started from. factory was nearly recipe-agnostic already. The recipe-driven board renders from a recipe's metadata: label, `on`, `applicable-when`, task output and actions, revises. `factory recipe` already runs a local file. What still assumes a recipe is built in:

| where | what it assumes |
|---|---|
| `loadRecipe` | a name is a built-in; anything else is a local file path (replaced by `resolveRecipe`, #1935) |
| revise, resume and apply (`recipe_revise.go`, `awaitAndApply`, `apply.go`) | after a run has started, its recipe is found again by name, with `recipe.Builtin(e.Recipe)` (replaced by the task's recipe, #1934; revise is gone, #1938) |
| `recipe list` (the board's catalog, `RepoBoard.status.recipes`) | built-ins only, and the warm recipe left out by name (now with `--url`: the repository's recipes too) |
| `runRecipe` | the warm recipe's sandbox chosen by name (`rec.Name == "warm"`) |
| `sandbox: {kind: run}` | a preset: the `run` input names the sandbox, and it runs as the deployer service account and is long-lived (still so) |

repo-agent's bespoke UI for a few built-ins stays: review's pending review, research sessions, the triage/plan/fix auto-modes, and the overseer's task-type map. A repository recipe renders through the generic path, as any recipe the catalog describes.

## Design

### Loading

`factory recipe run --recipe <name>` (and apply's `run:` follow-up) resolves a name in this order (`resolveRecipe`):
1. a built-in;
2. otherwise `.agents/recipes/<name>.yaml` in the target's repository, at its default branch (the GitHub contents API, with the caller's token);
3. a path with a `/` or a `.yaml` suffix, as now, is a local file.

A repository recipe whose name or session tag is a built-in's is refused, as is one whose name differs from its file's. A repository cannot redefine `fix` or `review`, which the overseer and every board rely on.

Recipes are read from the default branch only, never from a pull request's head. A recipe is code that runs with the caller's credentials, so it is reviewed and merged like code, as `.agents/` chores are today.

### A run keeps its recipe

A task carries the recipe it ran: its full source, as `recipe.source.yaml`, beside the `recipe.yaml` the sandbox runs (`ForSandbox` strips factory's fields from that one). Revise, resume and apply read the recipe from the task, through the task server, instead of looking it up by name. So:
- a run of a repository recipe can be revised and applied after the recipe has changed or gone;
- a built-in's run uses the recipe it started with, not the binary's current one.

### The catalog

`factory recipe list --url <repo>` lists the built-ins and the repository's recipes, each with its source (`builtin` or the file's path and commit) (#1935). The board's catalog is that list: the controller runs `recipe list --url` for each board's repository with the board owner's token, every 10 minutes, and publishes it on `RepoBoard.status.recipes` with each recipe's `source`, so a repository recipe gets its row buttons, its chips and its session view like any other; a click on one runs `factory recipe run --recipe <name>` (a repository recipe takes no `--instruction`). A file that is not a recipe is left out and logged.

Not built: a recipe with nothing for a board to launch (the warm recipe) says so with `board: false`, instead of being left out by name.

### Sandboxes (not built)

`sandbox:` becomes generic. A recipe that needs a sandbox of its own declares:

```yaml
sandbox:
  kind: run          # the name's prefix and the type label: run-<repo>-<key>
  key: run           # the input whose value names it; default: kind
  identity: deployer # optional: the GCP deployer service account
  long-lived: true   # optional: not idle-paused, not a task slot
  disk: 30Gi
  ephemeral-storage: 10Gi
```

The run recipes declare `identity: deployer` and `long-lived: true` explicitly. The warm recipe declares `kind: warm` (no key: one per repository), and `runRecipe`'s name check goes. A recipe without `sandbox:` gets its target's sandbox, as now: the item's, a PR review's own (`credentials: clone`), or a repository session.

The board reads `long-lived` from the sandbox's labels, not from its type, to exempt it from idle-pause and slot counting.

### Trust

Only the closed `uses:` set and the known verbs hold today; `identity:` and its board setting wait on step 4.

A repository recipe gets nothing a built-in does not, and less by default:
- the token-holding steps are still the closed `uses:` set (`NamedSteps`): a recipe can choose among them but cannot add one;
- `identity: deployer` in a repository recipe needs the board to allow it (`spec.repoRecipes.identity: true`), because it reaches the member's GCP project;
- `credentials:` defaults to `full`, as for built-ins (the engine GitHub write-access note applies to every recipe alike);
- task-output actions are the known verbs; nothing in a recipe adds one.

## Results are files the agent writes

A recipe's `revise:` parts exist because a result is captured from one ask's reply (`capture:`): only a step the runner sends can produce one, so changing a result after the run takes a recipe step of its own (Update plan, Update review, Save notes, Iterate, Update summary). A repository's recipes cannot declare `revise:` (see Loading), and the mechanism is one more thing to reason about for every recipe.

Instead, the agent writes the result to a file, as the prompt tells it to. The file belongs to the conversation, not to an ask: in Continue session a member can add instructions and say "save the plan", and the agent rewrites it. No step, recipe or button is needed for that, so `revise:` goes, with no follow-up recipes in its place.

**The recipe.** A result is a file in the run's task directory, named by `task-output.from` and listed in `outputs`, as now. An ask names its path with a template function, `{{ output "plan.md" }}`, which renders the absolute path in the run's task directory: the agent writes there, and the instruction stays in the conversation's history for later turns. `capture:` goes from the result-producing asks: their prompts say "write … to <path>" instead of "respond with ONLY …", so a reply's prose, fences and a restarted generation (#1845) no longer reach the result.

**The runner.** Built differently (#1938): an ask declares `expects: <file>`; after it the file must exist and not be empty, and an agent that forgot is asked once more before the step fails. The runner does not clear output files at a run's start. The task server re-wraps a result file newer than `task-output.yaml` (`taskoutput.Refresh`) before it lists or serves a task's files, so every reader sees the rewritten result. `capture:` stays in the recipe model; no built-in uses it. As designed:
- Before the first step, it removes the run's output files, so a file the agent did not write is never a stale result.
- After the last step, a declared output that is missing or empty fails the run, unless the recipe marks it `optional`. research's notes are optional: they exist once someone asks for them.
- `run:` steps that read a result (plan's copy to `/workspaces/plan-issue-<n>.md`) read the file, as they read a capture now.

**The board.** Built as #1940: the stored output carries the hash of the document it was harvested from (`OutputSourceAnnotation`), and the session view's draft read replaces it only when the document changed. It stores a run's output on its sandbox (`OutputAnnotation`) when the run ends. It reads the run's output files again, through the task server, whenever a turn in the session ends, the member's included, and a file whose hash changed replaces the stored output: a new draft, which is unapplied again if it had been posted, and which replaces any edit made on the board since. A file that does not parse (a review.yaml) shows its error on the draft, and the stored draft stays.

**What changes per recipe.**
- **plan:** writes `plan.md`. Update plan goes: say so in the session.
- **review:** writes `review.yaml`. Update review goes: say so in the session, then post the new draft. Review with instructions is a new run: review takes its `instructions` input from the board's click too (below). Posting replaces the pending review factory posted before, as GitHub keeps one pending review per person.
- **research:** its notes are an optional output, `notes.md`, which the agent writes when asked. The prompt says how a note is written (the text of today's Save notes ask), so "save notes" in the conversation is enough. The Save notes button is kept: it sends "Save notes." into the conversation (#1940).
- **summarize:** writes `summary.md`. Update summary goes.
- **fix:** writes `change.yaml` before the push step. Iterate goes: a fix's Change opens a draft PR by default, and the PR is changed with care-iterate. Before then, its Change can be edited, rejected, or the fix run again with `--instruction`. A fix changed in its session afterwards is not pushed: nothing runs the push step outside a run.
- **triage, care's recipes:** write their results the same way.

**The board's launch.** A board click on review, plan or triage passes the click's inputs to the recipe, as fix's does (the controller reads a Request's inputs for fix only today), and the launch asks for instructions. A second click with an instruction is review (plan, triage) with instructions; a click already reviews again (the re-review marker).

**What goes.** `revise:`, revise inputs (`revise: true`) and capture of results in the recipe model; `recipe_revise.go` with its push lookup (a PR's `pushed_*` come from the PR, as now); `RecordedRevise`; the `revise` action verb; repo-agent's revise Requests, the revise route and `markSandboxRevises`. The session view's button row (`Research.js`) keeps its recipe entries (care's) and loses its revise entries, with Iterate's prompt for an input.

## Steps

1. **A run keeps its recipe** (#1934).
2. **Loading from the repository,** with no shadowing of built-ins, plus `recipe list --url` with sources (#1935).
3. **Results are files the agent writes** (factory #1938; repo-agent #1939 inputs with instructions, #1940 re-read results and revise removed):
   - factory: the `output` template function, output files cleared at a run's start and required at its end (`optional`), every built-in's prompts writing their result, `revise:` and result capture removed;
   - repo-agent: outputs read again when a turn ends, review, plan and triage passing a Request's inputs with instructions asked on launch, and the revise route, verb, Requests and buttons removed.
4. **Generic `sandbox:`** (not built): `key`, `identity`, `long-lived`, `kind: warm`, `board: false`. The run and warm recipes move to it, and the name checks go.
5. **repo-agent** (not built): the catalog per repository, `long-lived` read from labels, and the board's `repoRecipes.identity` setting.
6. **Later:** the overseer launching repository recipes (on labels or events), and a PR per run (factory/design/run-branches.md).
