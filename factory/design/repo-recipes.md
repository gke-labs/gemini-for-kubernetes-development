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

## Steps

1. **A run keeps its recipe:** `recipe.source.yaml` in the task, and revise, resume and apply read it from there. Built-ins only change in where they are read from.
2. **Loading from the repository,** with no shadowing of built-ins, plus `recipe list --url` with sources.
3. **Generic `sandbox:`:** `key`, `identity`, `long-lived`, `kind: warm`, `board: false`. The run and warm recipes move to it, and the name checks go.
4. **repo-agent:** the catalog per repository, `long-lived` read from labels, and the board's `repoRecipes.identity` setting.
5. **Later:** the overseer launching repository recipes (on labels or events), and a PR per run (factory/design/run-branches.md).
