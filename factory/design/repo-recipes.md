# Recipes from the repository

Status: proposed.

A repository can define its own recipes in `.agents/recipes/<name>.yaml`. They run like the built-in ones: from the CLI (`factory recipe <name> --url <item>`), and from a board's catalog, on the items their `on:` names. Adding one should take no Go change.

## Today

factory is nearly recipe-agnostic already. The recipe-driven board renders from a recipe's metadata: label, `on`, `applicable-when`, task output and actions, revises. `factory recipe` already runs a local file. What still assumes a recipe is built in:

| where | what it assumes |
|---|---|
| `loadRecipe` | a name is a built-in; anything else is a local file path |
| revise, resume and apply (`recipe_revise.go`, `awaitAndApply`, `apply.go`) | after a run has started, its recipe is found again by name, with `recipe.Builtin(e.Recipe)` |
| `recipe list` (the board's catalog, `RepoBoard.status.recipes`) | built-ins only, and the warm recipe left out by name |
| `runRecipe` | the warm recipe's sandbox chosen by name (`rec.Name == "warm"`) |
| `sandbox: {kind: run}` | a preset: the `run` input names the sandbox, and it runs as the deployer service account and is long-lived |

repo-agent's bespoke UI for a few built-ins stays: review's pending review, research sessions, the triage/plan/fix auto-modes, and the overseer's task-type map. A repository recipe renders through the generic path, as any recipe the catalog describes.

## Proposal

### Loading

`factory recipe <name>` resolves a name in this order:
1. a built-in;
2. otherwise `.agents/recipes/<name>.yaml` in the target's repository, at its default branch (the GitHub contents API, with the caller's token);
3. a path with a `/` or a `.yaml` suffix, as now, is a local file.

A repository recipe whose name is a built-in's is refused. A repository cannot redefine `fix` or `review`, which the overseer and every board rely on.

Recipes are read from the default branch only, never from a pull request's head. A recipe is code that runs with the caller's credentials, so it is reviewed and merged like code, as `.agents/` chores are today.

### A run keeps its recipe

A task carries the recipe it ran: its full source, as `recipe.source.yaml`, beside the `recipe.yaml` the sandbox runs (`ForSandbox` strips factory's fields from that one). Revise, resume and apply read the recipe from the task, through the task server, instead of looking it up by name. So:
- a run of a repository recipe can be revised and applied after the recipe has changed or gone;
- a built-in's run uses the recipe it started with, not the binary's current one.

### The catalog

`factory recipe list --url <repo>` lists the built-ins and the repository's recipes, each with its source (`builtin` or the file's path and commit). The board's catalog is that list, so a repository recipe gets its row buttons, its chips and its session view like any other.

A recipe with nothing for a board to launch (the warm recipe) says so with `board: false`, instead of being left out by name.

### Sandboxes

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

A repository recipe gets nothing a built-in does not, and less by default:
- the token-holding steps are still the closed `uses:` set (`NamedSteps`): a recipe can choose among them but cannot add one;
- `identity: deployer` in a repository recipe needs the board to allow it (`spec.repoRecipes.identity: true`), because it reaches the member's GCP project;
- `credentials:` defaults to `full`, as for built-ins (the engine GitHub write-access note applies to every recipe alike);
- task-output actions are the known verbs; nothing in a recipe adds one.

## Follow-ups instead of revises

A recipe's `revise:` parts and a session of recipes (`session:`, as care's) do the same job by two mechanisms: both continue a conversation in its sandbox and capture a new result. One mechanism is simpler for anyone writing a recipe, and a repository's recipes (which cannot declare `revise:`, see Loading) need only that one.

Two revises are removed rather than converted:
- **fix's `iterate`.** It duplicates `care-iterate`. A fix's Change opens a draft PR by default, and the PR is then changed with care-iterate. Before the PR is open, its Change can be edited, rejected, or the fix run again with `--instruction`.
- **summarize's `summary`.** Run summarize again instead.
- **review's `review`.** Run review again with `--instruction` (its `instructions` input) saying what to change. Posting replaces the pending review factory posted before: GitHub keeps one pending review per person.

The other two become recipes of their own, in their start recipe's session:

| revise | follow-up recipe | session |
|---|---|---|
| plan `plan` (Update plan) | `plan-update` | `plan` |
| research `notes` (Save notes) | `research-notes` | `research` |

`plan-update` stays beside plan's `--feedback`: a re-plan with feedback is a new conversation reading the last plan, while plan-update writes the plan from the conversation the member had in the session. research's start has no result: `research-notes` is the only recipe that writes Notes, so research loses its `task-output` and `StartOutputs` its revise-only case.

What a revise did that a follow-up recipe needs:
- **The same sandbox.** A recipe in a session already runs where its group's last run did: an issue's or a PR's sandbox, or with `--session` a repository's.
- **The same conversation.** A run in a session continues the conversation its group's last run recorded (`sessionToContinue`), including the member's turns from the board's Continue session. plan declares its session tag, as `research` does.
- **Only after a start.** `applicable-when: {session: started}`: the recipe runs only where its session has a run. factory refuses it otherwise, and a board offers it only on an item whose group has one.
- **The same result.** A follow-up declares its start recipe's `outputs` and `task-output`, copied: YAML anchors do not cross files. plan-update copies plan's save step the same way. A test holds each copy equal to its original.
- **A result's actions.** An output offers `run: <follow-up>` instead of `revise: <id>`. The `run:` verb starts a recipe on the result's target, and passes the result as the recipe's input only when the recipe takes one from that kind.

What goes: `revise:` and revise inputs (`revise: true`) in the recipe model, `recipe_revise.go` with its push lookup (`pushed_*` from a session's tasks; a PR's come from the PR, as now), `RecordedRevise`, the `revise` action verb, and repo-agent's revise Requests. The board's Update plan and Save notes become the follow-up recipes' buttons in their session group; Iterate, Update review and Update summary go.

## Steps

1. **A run keeps its recipe** (#1934).
2. **Loading from the repository,** with no shadowing of built-ins, plus `recipe list --url` with sources (#1935).
3. **Follow-ups instead of revises:**
   - factory: the two follow-up recipes, `applicable-when: {session: started}`, and `run:` without an input; fix's iterate, review's and summarize's updates removed; then `revise` removed;
   - repo-agent: the buttons become the follow-ups' recipe Requests, and the revise verb goes.
4. **Generic `sandbox:`:** `key`, `identity`, `long-lived`, `kind: warm`, `board: false`. The run and warm recipes move to it, and the name checks go.
5. **repo-agent:** the catalog per repository, `long-lived` read from labels, and the board's `repoRecipes.identity` setting.
6. **Later:** the overseer launching repository recipes (on labels or events), and a PR per run (factory/design/run-branches.md).
