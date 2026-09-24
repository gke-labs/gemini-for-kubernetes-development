# Platform v2: recipes, runs, and a UI that materializes

Status: proposal · supersedes nothing yet · companion to
[repoboard.md](repoboard.md) and [factory-cli-migration.md](factory-cli-migration.md)

## Why

repo-agent works, and each feature earned its place. But the product's
opinions live in the *wrong layer*: they are encoded in UI structure
(six tabs, each with its own grammar) and in code paths (a factory
subcommand, an options struct, a claim key, a controller pass, and a
React panel per verb). Two symptoms follow:

- **Adding a workflow is a week.** Deploy needed: `factory runbook`
  subcommand + prompts + task script + `RunbookOptions` + claim
  parsing + a reconciler pass + an API handler + a table + badges.
  Nothing about a *deployment* is harder than a *fix*; the tax is
  structural.
- **Nothing is discoverable.** A verb is findable only if you know
  which tab we happened to put its button on. The vocabulary a new
  user must learn — board, sandbox, claim, instance, runbook,
  receipt, plan, task-type, engine — is our implementation history,
  not their mental model.

The fix is not fewer opinions. It is moving the opinions **out of
code and into content**: a small generic core plus curated recipes
that ship as data, which users can extend without touching the
product.

## What an SWE actually cares about

Strip the vocabulary down to what a developer would name unprompted:

| Primitive | The question it answers |
|---|---|
| **Repo** | where I work |
| **Work item** (issue / PR / review request) | what needs doing |
| **Understanding** (overview, architecture, questions) | what is this thing |
| **Change** (branch, PR, diff) | my output |
| **Environment** (pod, cluster, cloud) | does it actually run |
| **Evidence** (CI, receipts, verification) | can I prove it |
| **Run** | the agent labor that moves any of the above |

Every feature we have is the same five-step shape: *pick a target,
pick a recipe, an agent runs in a sandbox with the member's
credentials, artifacts land in git, a verdict comes back with one
obvious next action.* Fix, review, triage, plan, explore,
draft-runbook, deploy, teardown — no exceptions.

## The model: four nouns

**Repo** — the unit of subscription and identity: source URL, member
token, engine, limits, settings. (Today's RepoBoard, renamed for what
users call it.)

**Target** — a typed pointer a recipe acts on: `issue:42`, `pr:1324`,
`repo`, `environment:ax-deploy-1`. Targets are rows in lists; they
are not stored objects except where they already exist upstream.

**Recipe** — a named, versioned workflow definition: which targets it
accepts, what inputs it needs, what phases it has, what it may write,
what verdicts it can end in, and which actions each verdict offers.
Curated recipes ship in-tree; user recipes live in the repo.

**Run** — one execution of (recipe, target, inputs). It owns status,
phase, cost, logs, artifacts, and a verdict. **A Run is the single
source of truth for "what is happening"** — replacing today's
scattering across board annotations, in-memory runner results, and
the pod's filesystem.

Everything else demotes to an attribute: a *sandbox* is where a Run
executes; an *instance* is a Target of kind environment; a *runbook*
is a document one recipe writes and another consumes; *claims* become
queue plumbing with no user-facing vocabulary.

## Recipes are the extension point

```yaml
name: deploy
version: 3
summary: Stand up this repo in a cloud environment and verify it
targets: [environment]
inputs:
  - name: instance
    default: "{{repo.short}}-{{next}}"      # ax-3
  - name: guidance
    type: text
    optional: true
    hint: region overrides, flags, 'skip step 4'
phases:
  - id: plan                                 # gate: stops here
    writes: deployments/{{instance}}/**
    readonly: [runbooks/**]
  - id: apply
    requires_verdict: PLANNED                # the plan/apply gate, declared
    writes: deployments/{{instance}}/**
    readonly: [runbooks/**]
verdicts: [PLANNED, VERIFIED, DEPLOYED-UNVERIFIED, FAILED, BLOCKED, TORN-DOWN]
actions:
  PLANNED:            [apply, discard]
  VERIFIED:           [reapply, teardown]
  FAILED | BLOCKED:   [retry, teardown, remove-records]
  TORN-DOWN:          [reapply, remove-records]
prompts:
  plan:  ./prompts/deploy-plan.md
  apply: ./prompts/deploy-apply.md
script: ./scripts/deploy.sh
```

Three properties matter more than the exact syntax:

1. **Governance is data.** `readonly: [runbooks/**]` is how
   "a deploy run may not edit the runbook" stops being three hundred
   words of prompt plus a `git checkout -f` in a shell script. The
   executor enforces write scopes; the prompt merely explains intent.
2. **Gates are declared.** `requires_verdict: PLANNED` is the entire
   plan/apply gate. Any recipe can opt into review-before-spend.
3. **The UI materializes.** `targets` decides which lists the recipe
   appears on; `inputs` generates the form; `verdicts` and `actions`
   generate badges and buttons. **Adding a workflow is a YAML file** —
   no React, no controller change, no new claim key.

**Curated vs. user recipes.** The curated set ships in-tree and
encodes our opinions: triage, plan, fix, review, address-comments,
iterate, explore (onboard/activity/topic), draft-runbook, deploy,
teardown. Users add their own at `.repo-agent/recipes/*.yaml` **in
their repo** — which makes workflows code-reviewed, diffable,
per-repo, and forkable. That is the platform.

**Deliberate limit:** recipes stay declarative — inputs, phases,
write scopes, verdicts, actions. No conditionals, no loops, no
expressions beyond simple templating. A DSL with control flow is a
programming language you will regret maintaining; the intelligence
belongs in the prompt, not the schema.

## Three surfaces instead of six tabs

**Work** — the cross-repo attention inbox (today's Up Next / All).
One list of targets that need *you*, each row carrying what the agent
found and one primary action.

**Repo** — everything about one repo on one page: understanding docs
(rendered), environments, its available recipes, settings. Explore
and Runs stop being separate tabs and become sections, because they
answer the same question: *what is this repo and what is running in
it?*

**Runs** — the activity log: every run with status, phase, elapsed,
cost, logs, receipt. The "what are my agents doing" view that today
is scattered across chips, agent cards, and the usage page.

Plus one global door: **"Run a recipe…"** — a command palette. Pick a
recipe, pick a target, fill the generated form, go. This is the
discoverability fix; a verb no longer needs a home to be findable.

## Backend shape

- **One `Run` CRD** with real status (phase, verdict, cost, artifact
  paths, sandbox ref). The controller becomes a single generic
  reconciler: *pending Run → ensure sandbox → execute recipe phase →
  record status*. `ensureFix`, `ensureReview`, `ensurePlan`,
  `ensureExploreClaims`, `ensureRunbookClaims` collapse into it.
- **Factory becomes an executor library** with one entry point —
  `factory run <recipe> --target <t> --input k=v` — instead of a
  subcommand family. Its CLI remains useful standalone; repo-agent
  stops needing a bespoke integration per verb.
- **Receipts stay in git.** Git as the record is the system's best
  property; runs reference artifacts, they do not replace them.
- **Write scopes enforced by the harness**, from recipe data.

Most of this week's bug class — stale `Running` annotations, zombie
liveness probes, duplicate runs after a watcher timeout, claims that
re-fire — exists because run state was reconstructed from three
unreliable places. A Run whose status is written by its own executor
makes those bugs unrepresentable.

## What we keep (hard-won invariants)

Each is expressible as recipe data or executor behavior, not bespoke
code:

- git is the record; receipts are evidence.
- Plan/apply gates anything that spends money.
- Resource names carry ownership (`<repo-short>-<instance>`), and an
  instance never adopts infrastructure it did not create.
- Humans submit reviews; agents propose. Owners dispose.
- Failure is terminal for deploy-class work — retry is a click.
- One task per sandbox.

## What we delete

- Per-verb factory subcommands, options structs, and claim keys.
- Per-verb reconciler passes and API handlers.
- Per-surface React panels with private grammars.
- The annotation zoo (`*-requested-at`, `*-kind`, task-state probes).
- The vocabulary that leaked into the UI: board, claim, task-type,
  sandbox-type.

## Migration (no big bang)

1. **Model first.** Define `Recipe` + `Run`; implement the generic
   executor; port **two** recipes onto it — `explore` and `deploy` —
   while every existing path keeps running untouched.
2. **Generic surfaces.** Ship the Runs list and the recipe palette,
   both driven by `Run` status. Two features, one UI.
3. **Port the rest, one verb per PR**, deleting bespoke code as each
   lands. Fix/review/triage are the easy ones — they are already
   uniform.
4. **Open it up.** User recipes in `.repo-agent/recipes/` once the
   schema has survived contact with all ten curated ones.

Steps 1–2 are the real work; step 3 is mechanical; step 4 is the
payoff — the point at which repo-agent stops being *our workflows
with a UI* and becomes *a platform for running agents against a
codebase*.

## Prior art, and what we borrow

The core is deliberately unoriginal — the novelty should sit in the
agent layer, not in reinvented plumbing:

| Our concept | Already exists as |
|---|---|
| Recipe / Run | Tekton Task/TaskRun, Argo WorkflowTemplate/Workflow |
| Inputs → generated form | GitHub Actions `workflow_dispatch`, Rundeck job options, AWX surveys |
| Template → UI, entity catalog | Backstage Scaffolder + Software Catalog |
| Plan/apply gate | Terraform, and Atlantis for the human-clicks-apply loop |
| Curated + user jobs with run history | Rundeck |

So: borrow **JSON Schema** for inputs (render with
react-jsonschema-form, the Backstage-proven path) rather than
inventing an input DSL; borrow GitHub Actions' input vocabulary so
the mental model transfers; borrow Tekton's status conventions
(phases, conditions, completion times) for `Run`.

What none of them have, and what therefore justifies building rather
than adopting: **the unit of work has judgement**. A Tekton step
cannot deviate, explain why, repair its own script, or return
`DEPLOYED-UNVERIFIED` instead of exit 0. The consequences — receipts
as evidence in git, deviation reporting, propose/dispose governance,
write scopes, and an attention inbox fusing upstream state with agent
state — are the actual product. Everything else is borrowed
scaffolding.

Related: [auto-research-loops.md](auto-research-loops.md) adds
`Study` (a loop over runs with an objective and a budget) as the
fifth noun, composed of recipes rather than parallel to them.

## Structure-aware design (what the workflow-optimization literature says)

*Yue et al., "From Static Templates to Dynamic Runtime Graphs: A
Survey of Workflow Optimization for LLM Agents" (arXiv:2603.22386).*

The survey treats **workflow structure as the primary optimization
object** and gives a vocabulary worth adopting wholesale, because it
names three things we currently conflate:

| Survey term | Ours |
|---|---|
| **Workflow template** — the reusable executable specification | **Recipe** |
| **Realized graph** (`G_run`) — the structure actually used for one run | what the agent actually did |
| **Execution trace** — states, actions, observations, **and costs** | receipt attempt log + usage report |

Its taxonomy is organized by *when structure is determined* and how
plastic it is: (1) static template optimization, (2) pre-execution
generation or selection, (3) in-execution editing — cross-cut by
*what* is optimized: **node** (prompts), **graph** (topology), or
**joint**. Named approaches span DSPy (a compiler that synthesizes
prompts/demonstrations for a module pipeline), CAPO/GEPA and OPRO
(prompt-level optimization, LLM-as-optimizer), and routing or
architecture search — DyLAN, MasRouter, SkillOrchestra (select teams,
collaboration modes, models) and MaAS (a query-conditioned
distribution over architectures via an agentic supernet).

Five consequences for this design:

1. **We are deliberately at the static-template end, and that is a
   choice, not an oversight.** Recipes have no control flow on
   purpose (see above). The plasticity the survey argues for lives
   *inside* the agent — an LLM phase decides what to do — so our
   realized graph already differs from the template on most runs.
2. **Make the realized graph an artifact.** We are one small step
   from execution traces as a dataset: receipts already carry attempt
   logs, deviations, and cost. Emitting them as a structured block
   (phases actually run, tools used, retries, cost per phase) turns
   every run into optimizer-grade signal — the raw material every
   method in the survey consumes, and something the survey says the
   field lacks in realistic form.
3. **Bounded in-execution plasticity beats a DSL.** Rather than
   declaring conditionals, permit the agent to skip or repeat
   *declared* phases and require it to log the edit as a deviation.
   That is in-execution editing **with an audit trail** — the survey
   notes realized structure is often unlogged, and our propose/dispose
   governance turns that gap into a feature.
4. **Node-level routing is the cheapest win available.** Per-phase
   model policy (draft with a cheap model, critique with a strong one)
   is exactly the routing axis DyLAN/MasRouter occupy, and we already
   have multi-engine plumbing plus per-run cost data to tune it.
5. **Structure-aware evaluation.** The survey recommends reporting
   graph-level properties alongside task metrics — size, depth,
   critical path, edit count, fraction of steps spent editing,
   execution cost, robustness. For us those are per-recipe statistics
   we can emit for free, and they are what makes two recipe versions
   comparable. The survey's stated open problems — benchmark
   standardization and the cost/quality interplay under dynamic
   optimization — are both things a fleet of real SWE runs with real
   verdicts and real dollar costs is unusually well placed to answer.

## Open questions

- **Recipe versioning across a fleet**: if a repo pins recipe v2 and
  the product ships v3, who upgrades? (Leaning: recipes are data in
  the repo; curated ones are copied in on first use and updated by an
  explicit action, like a dependency bump.)
- **Trust for user recipes**: a recipe is instructions executed by an
  agent holding the member's token. Same prompt-injection surface as
  the parked skillpack discussion — likely resolution: user recipes
  run only with the credentials of the member who owns the repo, and
  a recipe change is a diff a human reviewed (because it lives in the
  repo).
- **Targets without upstream identity** (environments) need a home;
  the instance directory on the notes branch is the current answer
  and probably stays.
- **If the org runs Backstage, is this a plugin rather than a
  product?** The catalog, entity model, and template UI would come
  free; we would contribute the agent-run layer. A shortcut or a
  straitjacket depending on how much the inbox experience matters.
- **Does `Run` belong in etcd or a database?** CRD gives free
  watch/RBAC/kubectl; volume (hundreds/day/member) is fine for CRDs,
  but receipts and logs must not live in the object.
