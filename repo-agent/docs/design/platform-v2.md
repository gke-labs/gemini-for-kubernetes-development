# Platform v2: recipes, runs, and a UI that materializes

Status: **explored, not being implemented** (2026-09-25) · companion to
[repoboard.md](repoboard.md) and [factory-cli-migration.md](factory-cli-migration.md)

> **Why this was set down.** The order was wrong. Every generic surface
> here depends on recipes being data, and recipes cannot be data until
> factory grows one entry point — `factory run <recipe> --target <t>
> --input k=v` — that takes its prompt from the repo instead of its own
> binary. Without it, `executors` can only be a table of Go functions,
> and a page has nothing to materialize *from*. So the work moves to
> the factory command first, and the UI question is reopened after.
>
> Two findings from the survey below are worth acting on regardless,
> and neither needs this design:
>
> - The **GitHub-reactive inbox** is the most differentiated thing in
>   the product. No tool surveyed offers a groomable cross-source queue
>   of issues + review requests + CI failures before a session exists.
> - **Typed workflows** — declared inputs, a rendered prompt, a
>   structured output, and typed actions — are a real gap, not a
>   reinvention. The nearest shipping thing is GitHub Agentic Workflows'
>   `import-schema` plus `safe-outputs`, whose security model (agent
>   runs read-only, emits structured requests, a separate privileged
>   job validates and applies them) is the pattern to copy.
>
> What was built and then removed: #1590 (shadow read) landed and is
> reverted in #1601; #1592 (the Run object, one reconciler, retention,
> durable task observation) was closed unmerged and survives on the
> `v2-run-explore` branch. Retained here for the analysis — especially
> *Two loops*, *What the Run object holds*, and *The second axis: the
> worker*, which describe the system as it actually is rather than as
> this proposal wished it were.

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

These four describe the *work*. The **worker** — sandbox, task,
terminal, session — is a second axis with its own surface and its own
endpoints; see *The second axis: the worker*. The two meet only at a
Run, which records which worker ran it.

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

## Worked example: issue → triage → plan → fix → PR

The model has to express what already exists before it earns the
right to reach further. The classic flow, in four nouns:

```
Target: issue:42 "reconciler flakes under load"

Run 1  recipe:triage   → TRIAGED      writes: labels, triage comment
                          actions: [plan, fix, dismiss]
Run 2  recipe:plan     → PLANNED      writes: plans/42.md (+ issue comment)
                          actions: [approve→fix, revise, reject]   ← the human gate
Run 3  recipe:fix      → PR-OPENED    writes: branch, draft PR      (never main)
                          actions: [promote, iterate, review]

Target flips to pr:1324

Run 4  recipe:review   → REVIEWED     writes: a pending review on GitHub (you submit)
Run 5  recipe:address  → PUSHED       writes: commits on the PR branch
```

Each stage is an ordinary Run over a Target. There is no "issue
pipeline" machinery — the same objects as a deployment, different
recipes. Stages chain through `actions` (loosely coupled, each output
durable and reviewable on its own) rather than through phases, which
are for steps too tightly coupled to separate — plan/apply of one
deployment being the canonical case.

```yaml
name: fix
targets: [issue]
inputs:
  - {name: instruction, type: text, optional: true}
phases:
  - id: implement
    requires_verdict: PLANNED       # the approval gate, declared not coded
    writes:   [branch:fix-{{issue}}, github:pr.create]
    readonly: [github:main, github:issue.close]
verdicts: [PR-OPENED, NO-CHANGE-NEEDED, FAILED, BLOCKED]
actions:
  PR-OPENED:  [promote, iterate, review]
  BLOCKED:    [retry, dismiss]
```

Two details carry the argument. `requires_verdict: PLANNED` is the
entire approve-before-code gate — the same field that holds a deploy
behind its plan, doing identical work in an unrelated domain. And
**write scopes extend past git files to upstream objects**: the
mechanism that stops a deploy run editing a runbook stops a fix run
force-pushing your default branch.

The row materializes from that data:

```
#42  reconciler flakes under load   📋 planned · 2h ago   [ Approve ]  ⌄
                                                            plan ↗ · revise · reject
```

`verdicts` renders the badge, `actions[PLANNED]` renders the buttons,
`inputs` renders the form behind *revise*. Today that row is bespoke
React with hard-coded "Plan ready" handling.

### Three mechanisms, three questions

They are easy to conflate, so state them apart:

| Question | Mechanism | Nature |
|---|---|---|
| What **can** run on this item? | `recipe.targets` + preconditions | static: recipe × target kind |
| What **should** I do next? | latest Run's verdict → `actions` | stateful |
| What runs **without me**? | `triggers` on the Repo | event-driven |

Availability is not granted by triggers; triggers only press buttons
a human could have pressed. The UI falls out of the same split: the
**primary button** comes from the verdict's `actions` (or from
attention, when upstream state dominates — a requested review beats
everything), the **overflow menu** is every recipe whose `targets`
match this item, and the **palette** is every recipe across every
target. Today's Agent ▾ drawer is a hard-coded list of four verbs;
there it is layer one, rendered.

**A trigger cannot bypass a gate.** Auto means *start the run*, never
*approve the plan*: an auto-fix trigger runs triage, then plan, and
stops at `requires_verdict: PLANNED` waiting for a human — unless the
repo separately opts into auto-approval, which must be a loud, named
setting rather than an emergent property of having automation
enabled. Otherwise propose/dispose collapses the first time someone
turns a trigger on.

### Automation is a trigger, not a code path

Today's auto tiers (`assigned` / `labeled`), PR watch, and
auto-iterate collapse into declarative triggers on the Repo:

```yaml
triggers:
  - {on: issue.labeled(agent-fix), run: triage}
  - {on: review_requested(me),     run: review}
  - {on: pr.ci_failed,             run: fix-ci, if: pr.auto_iterate}
```

A user adds their own reflex without touching the controller.

### What this deletes

- `ensureTriage`, `ensurePlan`, `ensureFix`, `ensureReview`,
  `ensurePRTaskClicks/Claims` → one generic reconciler
- `fix-*`, `plan-*`, `triage-*`, `review-*`, `iterate-*` claim keys →
  Runs
- verdict-panel special cases, "Plan ready"/"Review ready" chip
  logic, the Agent ▾ drawer's hard-coded verbs → `verdicts` +
  `actions`
- `FixOptions` / `PlanOptions` / `ReviewOptions` / `PRTaskOptions` →
  one `RunSpec` with inputs

### What does not collapse

The **attention computation** — *does this need me?* — stays real
code, because it fuses upstream state (review requested, CI red,
draft versus ready) with the latest Run's verdict, and no declarative
table expresses that well. That is the inbox's intelligence and the
product's actual differentiator; everything else in this flow becomes
data.

## Backend shape

- **One `Run` CRD** with real status (phase, verdict, cost, artifact
  paths, sandbox ref). `ensureFix`, `ensureReview`, `ensurePlan`,
  `ensureExploreClaims`, `ensureRunbookClaims` collapse into a single
  reconciler — see *Two loops* below for what that reconciler does and,
  as importantly, what it must not absorb.
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

### Two loops

"One generic reconciler" is true of *execution* and false of the
controller as a whole. Today's `Reconciler` also polls GitHub, and
folding that into the Run controller would rebuild the god object
under a new name. The steady state is two loops with a hard boundary:

**The Run controller** owns work items and never talks to GitHub:
admission (is this repo v2's to drive, does the recipe accept this
target kind, are its gates satisfied — this is where *a trigger cannot
bypass a gate* is enforced); dedup and concurrency, at most one live
Run per `(repo, recipe, target)`, which is a label selector rather than
v1's served-ness predicates; credential resolution; placement, honouring
`isolation: namespace | cluster | project` when a Study fans out arms;
launch; observation; and terminal handling, where a terminal failure
does not retry.

**The discovery loop** owns targets and never executes: it polls
GitHub for open PRs, requested reviews, assigned issues and triage
candidates, writes them to the Repo's status, and evaluates declared
triggers — which create Runs. `discoverAllPRs`, `discoverRequestedReviews`,
`discoverAssigned`, `discoverTriage`, `followUpPRs`, `filterOnboarded`
and `loadSandboxes` move here. It is repo-scoped and continuous; a Run
is one execution.

Sorting today's ~1800-line reconciler by these two: roughly 17 methods
collapse into the Run controller, 8 move to discovery, and 7 —
`mailboxPlans`, `trimMailbox`, `activeCount`, `pauseFinished`,
`updateCounts`, `stampUnpaused`, `stampEngine` — simply vanish, because
they exist only to manage state encoded in annotations. The clearest
symptom is `mailboxPlans`, whose signature returns **seven** slices,
one per claim family, because v1 has no shared representation of "a
piece of work."

### What the Run object holds, and for how long

A Run is a **work item**, not an archive. Three layers, each holding
what it is good at:

| Layer | Holds | Lifetime |
|---|---|---|
| `Run` | intent, inputs, phase, verdict, pointers | the work, plus a retention window |
| sandbox task dir | pid, exit code, logs, prompts | the sandbox's |
| git | receipts, notes, scripts, ledgers | forever |

Two rules follow, and they are what keep the object honest:

1. **A recipe that succeeds must leave a durable external artifact** —
   notes on a branch, a receipt, a PR. A recipe that can succeed
   without leaving one is a bug in the recipe.
2. Therefore a **succeeded Run is retired** shortly after completion
   (it duplicates something git already holds), while a Run that
   **failed without producing an artifact is kept**, because nothing
   else records that it happened.

Rule 2 is not hypothetical. On 2026-09-24 the `granule` board carried
one standing claim, `{"explore-onboard":"barney-s|2026-09-23T23:04:52Z"}`,
while its sandbox accumulated 56 explore task directories — twelve that
day, every one exiting 128 after the agent succeeded and the push
returned `403: write access to repository not granted`. A permanently
terminal failure, retried indefinitely, invisible from the cluster,
legible only by `exec`-ing into the pod. Those are precisely the runs
worth keeping, and precisely the retry the Run controller's terminal
handling must refuse.

The corollary is that the object must stay small: **logs and receipts
never live in it**, only pointers to where they do.

### The second axis: the worker

Four nouns describe *work*. They do not describe the **worker**, and a
third of what people actually do with this system is inspect one.

| Axis | Nouns | The question it answers |
|---|---|---|
| work | Repo, Target, Recipe, Run | what should happen, and what did |
| worker | Sandbox, task, terminal, session | where it ran, and what is going on in there *now* |

The worker axis already exists in v1 and has its own endpoints, which
notably mention none of the four nouns: `/api/sandbox-card/:name/...`,
`/api/terminal/:namespace/:name`. It carries the web terminal, the task
history with log tails, wake/pause, delete-for-wedge-recovery, and
Continue session — resuming the agent's own conversation.

Leaving it unnamed had two consequences worth recording, because both
showed up the first time v2 was used in anger:

- The v2 page has no worker surface at all, so the terminal, the task
  history and Continue session were simply unreachable — not removed by
  a decision, just never given a home.
- A run row's link to its logs was written as a route,
  `/sandboxes/<name>`, which does not exist. The sandbox card is a
  component opened by state, and there is no URL for a worker anywhere
  in the product.

Two things follow.

**The axes join at the Run.** `status.sandbox` and `status.taskDir` are
the only bridge: a work item says which worker ran it and where that
worker wrote the record. That join is what makes "show me the logs for
this run" answerable, and it is the reason those two fields exist.

**The worker surface is code, not page data.** A terminal is a dense,
stateful, bidirectional component; an enum of view types will never
describe one. This is the escape hatch the declarative model needs and
should state plainly: *a section may name a rich component that code
owns entirely.* The rule that keeps it from becoming a CMS is unchanged
— data chooses among code-owned components, and never describes layout
— and the cost of each such component is a named code change, which is
an honest signal. Two exist today: the sandbox card and (still to come)
the exploration composer. A third should prompt the question of whether
the declarative model is carrying its weight.

The concrete gap: a **worker section** in the page spec, so Continue
session and wedge recovery have somewhere to live that is not bolted
onto the activity log.

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
- **When does `Run` outgrow etcd?** Settled below for now: a CRD, kept
  small and retired quickly. It becomes a database question at
  thousands of runs a day, or as soon as someone wants an aggregate git
  cannot answer cheaply ("p95 cost of the fix recipe over 90 days").
