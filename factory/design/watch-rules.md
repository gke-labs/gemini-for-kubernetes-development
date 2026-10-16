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

The overseer runs `factory watch` for `pollInterval` (5m), then commits
the queue directory to a branch and starts it again (`run.sh`). Every
in-memory gate resets each cycle, so the 10-minute sweeps run every
cycle in practice, and correctness rests on the queue files, GitHub
(labels, reactions, the bot's own comments) and the sandboxes'
annotations.

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

### Knobs that do nothing

`--poll-interval` and `--labels` are printed and unused; the Overseer's
`reviewMode` never reaches the watcher (review is gated by labels
alone); `chores.include`/`exclude` are not passed on; a `dryrun` mode is
not honoured (only `disabled` is). The dispatcher calls its ordering
fair share; it is strict priority. Readiness and "already reviewed" use
two different sets of bot accounts.

## Proposal

### Sources, rules, groups, plugins

The engine has four kinds of thing.

**Sources** say what is listed and how often: a GitHub query, its pages,
its order and its cadence, or the cluster's sandboxes, or the
repository's `.agents/`. Today's fast scans and sweeps are two sources
feeding the same rules: the fast one cheap and fresh, the sweep complete.

```yaml
sources:
  issues-assigned: {github: "is:issue is:open assignee:{{pool}}", pages: 1, sort: updated, every: 2m}
  issues-labelled: {github: "is:issue is:open label:{{trigger}}", pages: all, every: 10m}
  prs-assigned:    {github: "is:pr is:open assignee:{{pool}}", pages: 1, every: 2m, changed-only: true}
  prs-candidates:  {github: "is:pr is:open author:{{pool}}", pages: all, every: 10m}
  agents:          {repo: ".agents/*.md", every: 5m}
  sandboxes:       {cluster: sandboxes, every: 5m}
```

**Rules** are a condition and an action, on one kind of item:

```yaml
rules:
  - name: fix
    on: issue
    from: [issues-assigned, issues-labelled]
    when:
      labels: {any: ["{{trigger}}"]}
      linked-pr: none
      workflow: none
      updated-since-run: true
    run: {recipe: fix, apply: open-pr, inputs: {draft: "false"}}
    as: {role: coder}
    policy: {cooldown: {success: 10m, failure: 20m}, phase: 3}
```

**Groups** are ordered, exclusive sets of rules on one item: the first
whose condition holds is taken, none while a task of the item runs, and
a `then` rule when none holds. PR care is one:

```yaml
groups:
  - name: pr-care
    on: pr
    exclusive: true
    rules: [comments, conflict, ci, review]
    then: ready
```

**Plugins** are the controllers that are not condition and action:
fan-out (a ramp of windows and groups, checkpoints, its state in a
comment), the warm workspace, and the sandbox GC. They keep their code;
the rules turn them on and give them their settings.

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

The built-in rules, those above, are embedded in factory as a rule set,
as the built-in recipes are. A repository adds or overrides rules in
`.agents/watch.yaml`, read from its default branch only, as repository
recipes are. A repository's rule can run only recipes (its own or
built-in) and the closed set of actions; it cannot add a predicate, an
action or a source of a new kind. The Overseer's settings (modes,
`minNumber`, roles, the trigger) become the rule set's parameters, and
a mode that disables issues disables the rules on issues.

## What stays code

- **Fan-out's controller**, as a plugin with its pure `Decide`. Only its
  hand-off is a rule: a child labelled with the trigger is a fix.
- **The warm cycle and the GC**, as plugins. GC's deletes are rules over
  the `sandboxes` source, but its checks correct annotations and alias
  sandboxes as they read, which a pure evaluator would not.
- **The nudge**: an event (an issue closed) derived from the GC, not from
  a listing. As a rule it needs a `closed` event source; until then it
  stays with the GC.
- **Recovery after a restart**, which is a property of the actions (a
  run name factory follows), not a rule.

## Steps

0. **Knobs:** wire or remove `--poll-interval`, `--labels`, `reviewMode`,
   `chores.include`/`exclude` and the `dryrun` mode; one definition of a
   bot review; fix the dispatcher's fair-share comment.
1. **Rules in Go:** the issue and PR scanners become rule structs and an
   engine that evaluates them, with today's behaviour exactly; the
   existing scanner tests are the bar.
2. **`attempts` and the give-up policy** from the processed tasks,
   replacing the comment counting.
3. **Rules as data:** the built-in set as an embedded file, the engine
   reading it.
4. **The repository's rules:** `.agents/watch.yaml`, with sources and
   plugins as the engine offers them.
5. **The board** shares the predicates for its auto rules.

## Open questions

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
