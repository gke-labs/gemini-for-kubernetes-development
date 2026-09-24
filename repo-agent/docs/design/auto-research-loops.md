# Auto-research loops: Study and Trial

Status: proposal · extends [platform-v2.md](platform-v2.md)

## The ask

"Set a goal, let a loop optimize something, get back to me." Reduce
p99 latency 20%. Get the test suite under five minutes. Cut this
deployment's cost by a third. Find the memory leak. Shrink the image.

## Why this is not a Recipe

A Run answers *do this once and report*. Research is *keep trying
until the number moves or the budget runs out* — which needs state
**between** runs and a policy that chooses what to try next. Two
things a single long agent session cannot give us, as this codebase
has already learned the hard way:

- **Longevity**: `ax/instance1` ran four hours, outlived its watcher's
  60-minute leash, exhausted context, and kept all its reasoning in an
  ephemeral session. A loop of that shape is worse, not better.
- **Steerability**: nobody could inspect, budget, or redirect it
  between iterations, and its cost was invisible until the receipt.

So: a **loop over workflows**, with the loop's memory in git.

## The model

```
Study — the goal, the budget, the ledger
  ├─ objective     minimize p99_ms                (machine-readable)
  ├─ measurement   recipe: bench-gcp              (read-only to trials)
  ├─ environment   recipe: deploy-gcp, instance reused across trials
  ├─ space         what a trial may change (paths, params, knobs)
  ├─ budget        12 trials | $40 | 6h | Δ<2% for 3 → stop
  └─ Trials — each a Run (or a short chain of Runs)
       t1  hypothesis → patch → deploy → measure → 480 ms
       t2  hypothesis → patch → deploy → measure → 391 ms   ← best
       t3  hypothesis → patch → deploy → measure → 520 ms   (reverted)
```

`Study` is the fifth noun beside Repo / Target / Recipe / Run — and
it is *composed of* recipes rather than parallel to them. Defining
one is naming an objective, an environment recipe, a measurement
recipe, and a budget.

### Four ingredients a Recipe deliberately lacks

1. **Objective** — a number with a direction, extracted by
   deterministic code from the measurement's output. Never a figure
   the trial agent reports about itself.
2. **Suggestion policy** — what to try next. Numeric knobs can use
   grid/random/Bayesian; the interesting case is the **LLM proposing
   the next hypothesis from the ledger** — a search space made of
   code changes rather than hyperparameters. This is the part no
   existing tool does.
3. **Budget and stopping rule** — trials, dollars, wall-clock, or
   diminishing returns. A study without a hard stop is a bill
   generator.
4. **Ledger** — the research log, in git, human-readable.

## The ledger

```
docs-exploration/studies/<study>/
├── study.md                  goal, objective, budget, environment, space
├── trials/
│   ├── t1.md                 hypothesis · diff · metric · verdict · cost
│   ├── t2.md
│   └── …
├── best.md                   winning trial, its diff, confirmation run
└── summary.md                what was learned (including dead ends)
```

Same contract as receipts: git is the record, the artifact is
reviewable prose plus evidence, and the dead ends are as valuable as
the winner — they are what stops the next person re-running them.

## How it lands on what exists

- **Environment**: one instance reused across trials (cheaper, faster,
  and it keeps caches warm), torn down at study end. One cluster per
  trial is how you wake up to a cleanup day.
- **Receipts** gain a machine-readable `metrics:` block; everything
  else about them is unchanged.
- **Naming**: trial resources extend the ownership prefix —
  `ax-study1-t3` — so a stray resource still names its owner.
- **The gate moves up a level**: you approve the *study* (goal +
  budget), not each trial. Per-trial approval would defeat the point;
  study-level approval is where the money decision actually lives.
- **The output is a PR**: the winning diff, with the ledger as its
  justification. That is the artifact a maintainer wants — a change
  and the evidence that it worked.

## Parallel arms and isolation

Three optimizations at once is the case that most clearly earns this
noun — not because a session cannot fan out (it can), but because
nine VMs and four hours need accounting, naming discipline, and a
guarantee that everything dies when the study does.

```yaml
study: reduce-p99
parallelism: 3                  # controller admits at most 3 concurrent trials
isolation: cluster              # namespace | cluster | project
budget:
  trials: 12
  usd: 60
  hours: 6
  concurrent_envs: 3            # admission control, not a suggestion
```

**`isolation` decides the bill**, and it is the study author's most
consequential choice:

| Level | When it is right | Cost shape |
|---|---|---|
| `namespace` | the change is app-level: code, config, replica counts | one cluster for the whole study — by far the cheapest |
| `cluster` | the change is cluster-level: node type, GKE version, CSI driver, kernel flags | one cluster per concurrent arm |
| `project` | quota or IAM isolation is the thing under test | rare, expensive, occasionally unavoidable |

Mechanics that fall out of the existing design:

- **Each arm is an ordinary chain of Runs** — `deploy → measure →
  teardown` — in its own sandbox, so one-task-per-sandbox holds and a
  failing arm does not take the others down. The study records the
  failure and continues with the survivors.
- **Names cannot collide.** Trials extend the ownership prefix
  (`ax-study1-t1`, `-t2`, `-t3`), so three simultaneous deploys into
  one project are separable by construction — the cross-wiring
  failure mode this codebase has already lived through, made
  impossible rather than forbidden.
- **Comparability is enforced, not hoped for**: every arm uses the
  same measurement recipe, pinned and read-only, on the same declared
  node shape. Three improvising agents measuring three different ways
  produce noise with a decimal point.
- **Admission control**: the controller starts a trial only while
  concurrent environments and remaining budget both allow it. Quota
  errors are a study-level concern, not something a prompt can be
  asked to respect.
- **Teardown is per-arm, plus a sweep.** Each trial tears down its own
  environment when it completes; at study end the controller sweeps
  for anything labeled `repo-agent-instance=<study>-*` and reports
  stragglers. That sweep is the cleanup this project has done by hand
  more than once, made automatic.
- **The ledger gains an arm column**, so the summary can say which
  hypothesis won and what each one cost.

Rough economics, for calibration: three arms of three
`e2-standard-4` nodes run about $1.10/hour, plus roughly $0.10/hour
per control plane; a six-hour study lands near $9. Cheap enough to be
worth running, expensive enough that a forgotten arm is a real
mistake — which is exactly the asymmetry the budget and the sweep
exist to manage.

## The hard problem: Goodhart

An agent optimizing a metric will eventually optimize the
*measurement*: disable the slow test, shrink the workload, cache the
benchmark, special-case the input. The answer is structural, and it
mirrors the separation this codebase already built between deploy
runs and runbooks — **the optimizer may not touch the measurer**:

- the measurement recipe and its harness are **read-only** to trials
  (the same `readonly:` write-scope mechanism);
- the metric is extracted by deterministic code from the measurement
  output, never asserted by the trial agent;
- the best trial is **confirmed by a clean-room re-run** from a fresh
  checkout before the study reports success;
- every trial's diff is recorded in the ledger, so gaming is visible
  on review rather than buried in a summary.

A study that cannot satisfy those four is not a study; it is an agent
marking its own homework.

## Governance and safety

- **Budget is a hard stop**, enforced by the controller, not the
  agent's judgement: trials, dollars (usage reporting already tracks
  cost), wall-clock.
- **Mandatory teardown** at study end, and a study cannot start
  without a teardown path for its environment.
- **Checkpoints**: after N trials, or whenever the policy wants to
  change direction (widen the space, change the approach), the study
  pauses and reports — the attempt-budget lesson, applied one level
  up.
- **Blast radius**: a trial may only write inside the study's declared
  space; the repo's main branch is never a trial target — trials work
  on their own branch and the study's output is a PR.

## The second search space: optimizing the recipe itself

A Study optimizes *the artifact under test* — code, config, a
deployment. The workflow-optimization literature (Yue et al.,
arXiv:2603.22386; see platform-v2 §structure-aware design) optimizes
*the workflow that produced it*. Both are the same loop with a
different search space, so the same machinery should host both:

```yaml
study: improve-fix-recipe
space: recipe                  # instead of: code
  edit: [prompts/**, phases]   # node-level and graph-level
objective: maximize success_rate, then minimize cost
signal: execution traces from the last 200 runs of recipe:fix
validation: replay on 20 held-out targets
output: a PR against the recipe
```

This is GEPA/DSPy-shaped optimization — reflective evolution of
prompts, and topology edits such as inserting a critic phase — with
two properties the literature mostly lacks: the search space is
**diffable data in git**, and the winner arrives as a **pull request
a human reviews** rather than a silently updated artifact. The
propose/dispose rule already built for runbooks applies unchanged:
the optimizer proposes a recipe change, the owner merges it.

Sequencing matters. Node-level first (prompts, per-phase model
choice — cheap, measurable, low blast radius), topology second
(adding or reordering phases), joint last if ever. And the Goodhart
separation below applies with extra force here: a recipe optimizer
scored on *its own* runs' verdicts is grading its own homework unless
the verdict comes from a measurement it cannot edit.

## Prior art

Optuna (Study/Trial), Kubeflow **Katib** (Experiment → Suggestion →
Trial → metrics collector), Google Vizier: the object model is
settled and we should copy its naming rather than invent synonyms.
What none of them have is a search space made of **code**, a
**language model as the suggestion service**, and a **human-readable
research log** as the primary artifact. That is the part worth
building.

## UI surface

A Study is a row on Runs that expands into its trial table:

```
▸ study: p99-latency        running · 4/12 trials · $11 · best 391ms (−19%)
    t1  480 ms   patch ↗   receipt ↗
    t2  391 ms   patch ↗   receipt ↗   ← best
    t3  520 ms   patch ↗   receipt ↗   reverted
    t4  running · 6m
  [ pause ] [ stop & report ] [ open PR from best ]
```

Starting one is the same palette as any recipe — pick *study*, pick
an objective and budget, go — because a Study is just the one noun
that owns other runs.

## Open questions

- **Who writes the objective extractor?** A tiny script in the repo
  (reviewable, versioned) is the honest answer; an agent-written
  extractor is a Goodhart hole.
- **Resumability across controller restarts**: the ledger is durable,
  the suggestion policy's state should be derivable from it — which
  argues for keeping the policy stateless over the ledger.
- **When is a study the wrong tool?** Open-ended investigation ("why
  does this flake?") has no metric; that is a long Run with
  checkpoints, not a Study. Keep the boundary sharp.
