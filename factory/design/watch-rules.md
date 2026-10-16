# The watch loop as rules

Status: draft, being refined.

`factory watch` (the overseer) decides what to run by code: an issue
scanner, a PR scanner, a fan-out controller, a chores scheduler, a
sandbox reconciler, a dispatcher. Each has its conditions inline, in an
order, with its own dedupe. This note writes what it does as rules: a
GitHub condition and the action taken, the factory recipe it runs and as
whom, plus the policies every rule shares. Then it proposes running the
rules as data, so the loop is one engine and a repository can say what
its overseer does without a Go change, as `.agents/recipes` did for
recipes (design/repo-recipes.md).

## Today

### The loops

| loop | every | lists | decides |
|---|---|---|---|
| issue scanner | 2m fast, 10m sweep | fast: open issues assigned to the bot pool (one page, newest updated), and those the watcher's login opened; sweep: every open issue with the trigger label | fix, workflow, adoption |
| PR scanner | 2m fast or 10m sweep | fast: open PRs assigned to the pool, those changed since last evaluated; sweep: assigned and trigger-labelled, all pages | label inheritance, comments, conflict, CI, review, readiness, inactivity |
| fan-out | 5m, and woken when a child closes | open `*/fanout` issues | children, checkpoints, the spec proposal |
| chores | 30s (definitions every 5m) | `.agents/*.md` with `schedule:` | scheduled agents |
| sandbox reconciler | 30s, GC 5m | the namespace's sandboxes | task state, evicted pods, closed items, age, idle; the linked-issue nudge |
| warm workspace | 5m | the warm snapshot | the warm cycle |
| dispatcher | 30s | the queue | what starts, as whom |

Correctness rests on the queue files, GitHub (labels, reactions, the
bot's own comments) and the sandboxes' annotations, since the process is
restarted every few minutes (below).

### What it does, as rules

| rule | when | runs | as | dedupe, give-up |
|---|---|---|---|---|
| fix | issue with the trigger label; no open PR closes or links it; its body names no workflow; updated since its last fix | `recipe fix --apply --action open-pr --input draft=false`, in `fix-<repo>-N` | coder, pinned to the sandbox's user, then the assignee | task `task-issue-N`; 10m cooldown, 20m after a failure |
| workflow | as fix, but the body names a workflow (`.agents/…`, `mode: workflow`) | `agent create --agent <path> --session-id issue-N`, in `wf-issue-N` | agent, pinned likewise | the workflow's `cooldown:` (default 10m), 20m after a failure; no updated-since gate, so it re-runs each cooldown |
| adopt issue | issue the watcher's login opened, without the trigger label or a pool assignee | add the label; assign the target assignee | watcher | — |
| adopt PR | bot-authored PR without the trigger label or a pool assignee, whose referenced issue has it | copy the issue's labels | watcher | — |
| inherit labels | candidate PR | copy the labels of the issues it closes, but `ready-for-human` | watcher | each evaluation |
| comments | PR with trusted feedback after its last commit and last addressing, not yet 👀'd (or 🚀'd to redo); or the last comments run failed on this head | `recipe care-comments --apply --action post-replies`; 👀 on each comment, 👍 or 😕 after | the PR's author | task `task-pr-N-comments`; three tries per head, then the stop label and a pause comment |
| conflict | PR not mergeable | `recipe care-rebase --apply --action post-replies` | the PR's author | once per head |
| CI | PR with a failing check or status | `recipe care-ci --apply --action post-replies` | the PR's author | a new head, a failed last run, or 2h; three tries (counted in the bot's "started investigating" comments), then the stop label |
| review | PR green, no unresolved feedback, not approved, `*/review` on it (or on a closing issue), its head not reviewed by a pool or allowlisted bot | `recipe review --apply --action submit-review [--instruction …]`, in `review-<repo>-N` | reviewer | once per head |
| ready | PR open, not draft, mergeable, green, no feedback pending, no task, the reviewer bot not asking for changes | add `*/ready-for-human`, drop `*/review`, assign the closing issues' human assignees, unassign the bot | watcher | idempotent |
| inactive | PR with no trusted human activity for `prInactivityTimeout` | comment once; stop label | watcher | — |
| fan-out | `*/fanout` issue with a spec | create, rewrite or label children within the window; stop at a checkpoint; the final child; close | watcher | state in its progress comment |
| fan-out spec | `*/fanout` issue without a spec | `recipe fanout --apply` (outside the queue) | coder | one at a time per parent; 1h after a failure |
| chore | `.agents/*.md` whose `schedule:` is due | `agent create --agent <path>` on the repository | agent | `chores_state.json` |
| nudge | an issue's closure confirmed by the GC; an open issue it links runs a workflow and has its sandbox | the linked issue's workflow, past its cooldown | agent | the task's name only |
| stop | issue or PR with the stop label | drop its queued tasks; at dispatch, drop a task whose item closed or stopped | — | — |
| requires human | sandbox annotated `requires-human` | never enqueue; drop at dispatch | — | until a human clears it |
| GC | sandbox of a closed issue or PR; older than the eviction age; idle | harvest usage; delete, or scale to zero | — | the sandbox's lease |
| warm | no warm snapshot younger than the interval | step the warm cycle | coder | one at a time |

On a PR the rules are not independent: one is taken, comments before
conflict before CI before review, and none while a task of the PR runs;
readiness is what holds when none applies.

### The policies they share

- **Gates:** drain (an env var or a marker file); the stop label
  (`<trigger>/stop`), checked at scan and again at dispatch; `minNumber`;
  the issue, PR and chore modes; feedback from trusted authors only, and
  `/<trigger>-ignore` to opt out; PR work only on PRs the pool authored.
- **Dedupe**, outermost first: a task name per item and rule (a second
  enqueue is a no-op); one task per PR; the head SHA last handled, per
  rule; state on GitHub (reactions, labels, the bot's comments); state on
  the sandbox (task state, `requires-human`, the user who owns it).
- **Scheduling:** strict order by the `priority/*` label, then a phase
  per rule (comments and review 1, conflict 2, fix and CI 3, workflows
  and chores 4), then first come; one task per sandbox, under a lease;
  `--max-pending` and `--max-actions` (40 each). No fairness between
  items, users or rules.
- **Identity:** a role per rule (coder, agent, reviewer); a PR's tasks
  run as its author, who must be in the pool; an item is pinned to the
  bot whose sandbox it already has.

### Worth fixing whether or not this goes anywhere

- **Settings that do nothing:** `--poll-interval` and `--labels` are only
  printed; the Overseer's `reviewMode` never reaches the watcher, so
  review is gated by labels alone; `chores.include`/`exclude` are not
  passed on; a `dryrun` mode is not honoured (only `disabled` is).
- **`pollInterval` is the process's lifetime,** not a poll interval:
  `run.sh` runs `factory watch` for it (5m), pushes the queue to git and
  starts it again. Every in-memory gate resets each time (the PR fast
  pass's verdicts, the issue scanner's processed stamps, fan-out's
  proposal backoff, sandbox leases), so the 10-minute sweeps run every
  cycle.
- **No fairness:** the dispatcher calls its ordering fair share; it is
  strict priority, then phase, then first come.
- **Two definitions of "a bot reviewed it":** whether to review counts a
  review by any pool, allowlisted or ignored bot after the last commit;
  readiness counts only the reviewer role's (`IsBotReply` against
  `IsReviewerBot`).

## Proposal

### Rules, groups, sources, plugins

The engine has four kinds of thing.

**Rules** are a condition and an action, on one kind of item, one rule
per file (see Where rules live):

```yaml
# .agents/rules/fix.yaml
name: fix
on: issue
when:
  labels: {any: ["{{trigger}}"]}
  linked-pr: none
  workflow: none
  updated-since-run: true
run: {recipe: fix, apply: open-pr, inputs: {draft: "false"}}
as: {role: coder}
policy: {cooldown: {success: 10m, failure: 20m}, phase: 3}
```

**Groups** make rules on one item ordered and exclusive: of the rules
in a group, the first whose condition holds is taken, none while a task
of the item runs, and the rule ordered `last` holds when none other
does. A rule says its place itself, as a GitHub Actions workflow names
its `concurrency` group. PR care is one group:

```yaml
# .agents/rules/ci.yaml
name: ci
on: pr
group: {name: pr-care, order: 30}      # comments 10, conflict 20, ci 30, review 40, ready last
when: {checks: failing}
run: {recipe: care-ci, apply: post-replies}
as: {role: author}
policy: {dedupe: head, give-up: {attempts: 3, since: human, then: stop}}
```

**Sources** are what is listed and how often. A rule does not name one:
the engine derives the listings from the rules' `on` and `when` (a rule
on issues with the trigger label lists those; one on PRs assigned to
the pool lists those), and keeps today's split between a cheap, fresh
fast pass (one page, newest updated, only what changed) and a complete
sweep as an engine setting. The cluster's sandboxes and the
repository's `.agents/` are sources of their own, for the plugins and
the chores.

**Plugins** are the controllers that are not condition and action:
fan-out (a ramp of windows and groups, checkpoints, its state in a
comment), the warm workspace, and the sandbox GC. They keep their code;
the settings turn them on and configure them.

### Predicates

A closed set, each a named Go function, so a rule cannot reach anything
the engine does not offer. From today's rules:

| on | predicates |
|---|---|
| any item | `labels: {any, all, none}`; `number: {min}`; `author: {in: pool, trusted}`; `assignee: {in: pool, none, owner}`; `updated-since-run`; `stopped` (the stop label) |
| issue | `linked-pr: none\|open`; `workflow: none\|any\|<name>`; `opened-by: watcher`; `fanout: parent\|child` |
| PR | `checks: failing\|pending\|green`; `mergeable: false\|true`; `feedback: new-trusted`; `reviewed-at-head`; `approved`; `draft`; `merge-queue`; `inactive-for: <duration>` |
| sandbox | `item: closed`; `age: <duration>`; `idle: <duration>`; `requires-human` |
| any | `attempts: {rule, since: head\|human, max}` |

`attempts` replaces counting the bot's own comments: the engine records
each run of a rule on an item (the queue's processed tasks already do),
and the predicate counts them since the head changed or a trusted human
last spoke. The board's auto rules (repo-agent design
generic-board-recipes.md, step 7) use the same vocabulary for their
`when`: `unclaimed`, `assignedToOwner`, `reviewRequestedFromOwner`,
`labels`.

### Actions

- **`run`:** a recipe (built-in or the repository's), its inputs, and
  what is applied when it ends (`apply`, as `task-output.on-finish`
  does for the board). `agent create` becomes the `agent` or `workflow`
  recipe, which it already picks.
- **GitHub writes:** `label`, `unlabel`, `comment` (once, by marker),
  `react`, `assign`, `unassign`. The adoption, inheritance, readiness,
  inactivity and give-up rules are these alone.
- **`stop`:** the stop label and a comment saying why, which is how
  every give-up ends today.
- **Cluster:** `delete`, `suspend`, `harvest` on sandboxes (GC).

`as` names a role. Pinning (the bot that owns the item's sandbox, then
the assignee, then any) and "a PR's tasks run as its author" are the
engine's, for every rule.

### Policies

Every rule has defaults, overridable per rule:

```yaml
policy:
  dedupe: item          # one task per item and rule; or head (once per head SHA)
  cooldown: {success: 10m, failure: 20m}
  give-up: {attempts: 3, since: head, then: stop}
  phase: 3              # ordering after the priority label
  identity-pin: true
```

The global ones stay global: drain, the stop label, `minNumber`, trust,
`--max-pending`, one task per sandbox.

### Where rules live

**One rule per file, in `.agents/rules/<name>.yaml`,** beside the
repository's recipes:

```
.agents/
  recipes/<name>.yaml    what to do                         (exists)
  rules/<name>.yaml      when the overseer does it: a trigger, the recipe, its policy, its group
  watch.yaml             optional: the repository's settings, and no rules
```

- **The built-in rules** ship the same way, embedded in factory one file
  per rule, as the built-in recipes are.
- **A repository's rule replaces the built-in of its name**
  (`.agents/rules/ci.yaml` with a different give-up), or turns it off
  (`enabled: false`), or adds a rule of a new name. Rules are read from
  the default branch only, as repository recipes are.
- **A repository's rule can run** only recipes (its own or built-in) and
  the closed set of actions; it cannot add a predicate, an action or a
  source of a new kind.
- **The settings** (the trigger label, the bot pool and its roles,
  `minNumber`, the modes, the concurrency limits, the policy defaults)
  are the Overseer's, as now, and a repository's `watch.yaml` may narrow
  them. A mode that disables issues disables the rules on issues.
- **The whole picture** is a command, not a file: `factory watch rules
  --url <repo>` prints the effective rules (built-ins merged with the
  repository's), where each comes from, and each group in order, as
  `factory recipe list --url` does for recipes.

**Why not one file.** The alternative is a single `.agents/watch.yaml`
holding the settings, the sources, the groups and every rule. The same
five rules, both ways:

```
 single file: .agents/watch.yaml          │ one file per rule: .agents/rules/*.yaml
──────────────────────────────────────────┼──────────────────────────────────────────
 sources:                                 │ # sources: derived from each rule's on/when
   prs-assigned:                          │
     github: "is:pr assignee:{{pool}}"    │
     every: 2m                            │
 groups:                                  │ # groups: each rule says its place
   - name: pr-care                        │
     rules: [comments, conflict, ci,      │
             review]                      │
     then: ready                          │
 rules:                                   │ # rules/fix.yaml
   - name: fix                            │ name: fix
     on: issue                            │ on: issue
     when: {labels: {any: [overseer]},    │ when: {labels: {any: [overseer]},
            linked-pr: none}              │        linked-pr: none}
     run: {recipe: fix, apply: open-pr}   │ run: {recipe: fix, apply: open-pr}
   - name: comments                       │ # rules/comments.yaml
     on: pr                               │ name: comments
     from: [prs-assigned]                 │ on: pr
     when: {feedback: new-trusted}        │ group: {name: pr-care, order: 10}
     run: {recipe: care-comments}         │ when: {feedback: new-trusted}
   - name: ci                             │ run: {recipe: care-comments}
     on: pr                               │ # rules/ci.yaml
     when: {checks: failing}              │ name: ci
     run: {recipe: care-ci}               │ on: pr
     policy: {give-up: {attempts: 3}}     │ group: {name: pr-care, order: 30}
   - name: review                         │ when: {checks: failing}
     on: pr                               │ run: {recipe: care-ci}
     when: {checks: green,                │ policy: {give-up: {attempts: 3}}
            labels: {any: ["*/review"]}}  │ # rules/review.yaml, rules/ready.yaml: likewise
     run: {recipe: review}                │
──────────────────────────────────────────┼──────────────────────────────────────────
 CI give-up to 5: edit the shared file,   │ add rules/ci.yaml (replaces the built-in)
   restate the ci entry                   │
 a nightly stale summary: edit the file   │ add rules/stale-summary.yaml
 auto-review off: remove review from the  │ rules/review.yaml with enabled: false
   rules and from the group               │
```

| | one file | one file per rule |
|---|---|---|
| familiar from | Kubernetes manifests, CI config | GitHub Actions (`.github/workflows/*`), `.agents/recipes/*` |
| everything at once | by reading it | `factory watch rules --url` |
| order between rules | one list per group | each rule's `group: {name, order}` |
| sources | declared, tunable | derived; cadence is the engine's |
| add or remove a behaviour | edit a shared file | add or delete a file |
| override one built-in | restate or patch an entry, which drifts | a file of the same name |
| conflicts and ownership | one file for everyone; CODEOWNERS all or nothing | a file each; CODEOWNERS per rule |

One file's strength, everything in one place, a command gives; its cost,
every change an edit to a shared file that drifts from the built-ins, is
structural. One file per rule costs the order of a group spread across
files as numbers, and only groups have an order (PR care, today): the
command prints each group in order, and loading refuses two rules of a
group with the same order.

A trigger inside the recipe (an `auto:` beside its `on:`) is rejected:
the same recipe is triggered differently by the overseer, a board's
auto rules and a click, so a trigger is the runner's, not the recipe's.

## What doesn't fit simple rules

- **Fan-out is a controller,** with a ramp: windows and groups that
  double and halve, checkpoints that pause and resume, and its state in
  a GitHub comment. In rules it can only ever be "a labelled child
  becomes a fix". It stays a named plugin with its pure `Decide`.
- **Give-up counters live in GitHub text.** The three-strikes count for
  CI is the number of the bot's "started investigating" comments since
  the last commit, trusted human comment or pause notice; the comments
  rule carries its retry count on the queue's processed task. Reactions
  carry meaning too: the watcher's 👀 means taken, 👍 done, 😕 failed,
  and a human's 🚀 asks for it again. A rules engine needs these as
  named predicates backed by a state store (`attempts: {rule: ci, since:
  human, max: 3}`, `feedback: new-trusted`), not text matching. The
  reactions can stay as what the engine writes for people to see, but
  not as what it reads to decide.
- **Workflows have no triggering event.** A workflow issue re-runs every
  cooldown, on a timer; it is nudged early when an issue it links
  closes, and that signal comes from the sandbox GC confirming the
  closure before deleting a sandbox, not from a GitHub listing. As
  rules: a timer (`schedule` or `cooldown` with no updated-since gate)
  and a `closed` event source, which the engine does not have yet.
- **GC and warm-up are driven by the cluster,** not by GitHub items.
  They are rules over a different source (`sandboxes`), and some of
  their checks have side effects: reading a sandbox's task state
  corrects its annotation and deletes evicted pods, and resolving a
  PR's sandbox aliases an issue's sandbox to the PR. A pure evaluator
  would not reproduce those. They stay plugins.
- **Restart recovery** is a property of the actions, not a rule: a task
  left in `processing/` is queued again under the same run name, and
  factory follows the run or applies its result (`--run-name`).

## Steps

0. **Fix what the read turned up:** wire or remove `--poll-interval`,
   `--labels`, `reviewMode`, `chores.include`/`exclude` and the `dryrun`
   mode; name `pollInterval` for what it is (the run's lifetime) or make
   it the poll interval it claims to be; one definition of a bot review;
   the dispatcher's comment says what its ordering is.
1. **Rules in Go:** the issue and PR scanners become rule structs and an
   engine that evaluates them, with today's behaviour exactly; the
   existing scanner tests are the bar.
2. **`attempts` and the give-up policy** from the processed tasks,
   replacing the comment counting.
3. **Rules as data:** the built-in rules as embedded files, one per rule,
   the engine reading them, and `factory watch rules`.
4. **The repository's rules:** `.agents/rules/*.yaml` replacing, turning
   off or adding to the built-ins, and `.agents/watch.yaml` for its
   settings.
5. **The board** shares the predicates for its auto rules.

## Open questions

- **Replacing a built-in:** a repository's recipe may not take a
  built-in's name; a repository's rule may, above, because changing the
  overseer's behaviour per repository is the point. Should replacing be
  allowed outright, or only `enabled: false` plus a rule of a new name?

- **A repository's rules and the bot pool:** a rule runs as a role of
  the overseer's bots. Does a repository get to say which, or only the
  overseer's operator?
- **Workflows on a timer:** a workflow issue re-runs every cooldown. As
  a rule, is that `updated-since-run: false` plus a cooldown, or a
  `schedule` on the rule?
- **Events:** sources are listings. GitHub's events or a webhook would
  let rules fire on `labeled`, `review_requested`, `check_run.completed`,
  which the board's auto rules would want too. Out of scope until a
  GitHub App exists (design/github-app-identity.md).
- **Fairness:** strict priority starves low phases under load. Does the
  engine add a per-rule or per-item share?
