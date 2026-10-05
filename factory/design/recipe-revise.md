# Design Note: Revising a Task's Output from Its Conversation

**Status:** Phases 1 (the recipe shape) and 2 (factory runs revises) built; phases 3–4 not yet.

A recipe task (plan, triage) runs its asks in one agent session. Since #1746 a member can open that session after the task ends and keep talking to the agent: "why this approach?", "make step 3 smaller". This note covers turning that conversation into a new version of the task's output. The recipe declares **revise** parts, each a short list of steps that write the output again. factory runs a revise into the same session. Each revise appears as an action on the task output, so the board shows it as a button, for example **Use as plan**.

---

## Background & Problem Statement

The plan recipe's last two steps produce the plan:

```yaml
- ask: |
    Now write the plan. Respond with ONLY the plan as markdown …
  capture: plan-output.md
- run: |
    test -s "$TASK_DIR/plan-output.md" || { echo "the plan is empty" >&2; exit 1; }
    sed … "$TASK_DIR/plan-output.md" > "/workspaces/plan-issue-${INPUT_ISSUE_NUMBER}.md"
```

The agent never writes the plan to a file. The runner captures the ask's reply into `plan-output.md`, a run step saves it where `factory fix --with-plan` reads it, and the task output is built from the capture.

After a conversation, the agent's view of the plan has changed, but nothing carries that back:

1. **The draft stays as the task left it.** The board's draft, `/workspaces/plan-issue-<n>.md` and the task output still hold the first version.
2. **Refine starts over.** It launches a new plan run in a fresh session, guided by a feedback text, so the conversation is lost.
3. **Copying by hand works but is brittle.** The member copies a reply into the draft editor and has to pick out the plan from the surrounding chat.

What's needed is the same capture, run again at the end of the conversation.

---

## Architecture

### The recipe: `start` and `revise`

A recipe has two kinds of part. `start` is what runs when the task is launched. `revise` is a list of parts that run later, into the started task's session. `context`, `inputs`, `outputs` and `task-output` stay at the top level, and both kinds share them.

```yaml
name: plan
context: |
  …
inputs:
  feedback: …
start:
  steps:
    - uses: setup-git
    - uses: setup-repo
    - uses: checkout-default-branch
    - uses: configure-engine
    - ask: |
        Investigate this issue. Do not write the plan yet. …
    - id: write
      ask: |
        Now write the plan. Respond with ONLY the plan as markdown …
      capture: plan-output.md
    - id: save
      run: &save |
        test -s "$TASK_DIR/plan-output.md" || { echo "the plan is empty" >&2; exit 1; }
        sed -e '1{/^```/d;}' -e '${/^```$/d;}' "$TASK_DIR/plan-output.md" > "/workspaces/plan-issue-${INPUT_ISSUE_NUMBER}.md"
revise:
  - id: plan
    label: Use as plan
    steps:
      - ask: |
          Rewrite the plan to reflect our conversation. Respond with ONLY the
          plan as markdown, with the same sections: Summary, Approach, Steps,
          Risks, Test plan.
        capture: plan-output.md
      - run: *save
outputs:
  - plan-output.md
task-output:
  kind: Plan
  from: plan-output.md
  actions: …
```

- **Steps are written in place.** A revise's steps are ordinary steps (`ask`, `run`, `uses`) and need not appear in `start`. A revise can ask its own question; the wording that suits a first draft ("Now write the plan") reads oddly after a conversation.
- **Shared steps use YAML anchors.** `&save` where a step is first written, `*save` where it's reused. No new syntax.
- **`id`** names the revise for the CLI and the action. **`label`** is the button's text.
- **A recipe may have several revises,** and each is its own button. Triage could offer *Rewrite comment* and *Redo labels*. Clicking one runs only its steps.
- **`steps` at the top level is gone.** Recipes move under `start`, the built-in ones included. Sandboxes on older images are recreated, not supported.

**Validation** (when a recipe is loaded):
- A recipe with revises asks at least once in `start`, so there is a session to revise into. A recipe without revises may be all `run` steps.
- Each revise has a unique `id`, a `label`, and at least one ask whose `capture` is a file in `outputs`. A revise that captures nothing would change nothing.
- `{{ .Steps.<id> }}` reaches steps of the same part only.

### Running a revise

```
factory recipe revise <sandbox | issue url> <revise id> [--task ID] [--run-name NAME] [--recipe FILE]
```

`--task` names the task to revise; by default it is the sandbox's newest task whose recipe has that revise. `factory apply --action revise:<id>` runs the same, with the task output's `source.session` (else `source.task`) as `--task`.

A revise is a **new task** in the same sandbox, not the old task re-entered:

- **Inputs** are the started task's `inputs.json`, copied, so `INPUT_ISSUE_NUMBER` and the rest are as before. Setup steps don't run again; the checkout, git and engine configuration are on the sandbox's disk.
- **The recipe** is the one the caller sends, as for any run, with the revise part selected. A fix to a built-in revise reaches tasks started before the fix.
- **Its session is the started task's.** The task records `session: <started task id>` in `task.json`, and its asks prompt that session (session id = started task id) instead of creating their own. `context` isn't sent again; the session already has it.
- **Its result is its own.** It has its own task directory, captures, status and logs, and writes its own `task-output.yaml` with `source.task` set to itself and `source.session` set to the started task. The first version stays readable in the started task's directory.
- **Chains collapse.** Revising a revise resolves `session` to the started task, so every version of a plan comes from one conversation.
- **Task type** is the started task's (`plan`), so `last-task-*` reports a plan and callers that harvest plans need no new case.

### The session while a revise runs

The daemon already gives a running task's session to the task alone (`X-Factory-Task-Token`); the browser watches and its composer is disabled. A revise extends that:

- **While a revise runs, the started task's session is held for the revise.** In the daemon's `Tasks`, `Running(started)` is true while a task whose `session` is `started` runs, and `Token(started)` is that task's token. The browser shows the held state it already has.
- **A busy session refuses the revise.** If a member's turn is in flight, the revise fails at once with 409 and sends nothing; the button is disabled while the session is busy.
- **The revise's ask appears in the conversation as the next turn,** as though the member had typed it, followed by the reply. When the revise ends, the session is the member's again, and they can keep talking and revise again.
- **A session nobody opened since the task ended** is loaded (session/load) by the revise, as the browser would load it, and closed when the revise ends. A session a member has open is used as it is and left open.

### The action

factory adds one action per revise to the task output:

```yaml
actions:
  - {verb: edit, field: spec.markdown, format: markdown}
  - {verb: comment, label: Post plan}
  - {verb: run, run: fix, label: Fix with this plan}
  - {verb: revise, revise: plan, label: Use as plan}
```

- **`revise` is a follow-up verb,** like `run`: factory executes it, by `factory recipe revise <source.session> <revise>`.
- **The recipe declares it; the output only names it.** A revise action whose id the recipe doesn't have is refused, as for any verb.
- **Revising never publishes.** A revise writes a new draft. Posting it stays `comment` (Post plan), applied by the caller as today.

### repo-agent

- **Request verb `revise`** `{number, revise}`. The controller launches it as it launches a plan: `factory recipe revise` with a fresh run name, recorded on the sandbox's run annotation (`plan-run`), harvested into the draft when it ends.
- **The recorded run gains `session`.** The board's `planSession` / `triageSession` use `session`, falling back to `task`, so *Continue session* still opens the one conversation after a revise.
- **Buttons come from the actions.** The task-session view shows each `revise` action of the session's latest output, disabled while the session is busy or held. The draft row shows them too, beside Post plan.

---

## Known limits

- **A revise needs the session's engine.** session/load works only on the engine and checkout the task ran with. If the board's engine changed since, the revise starts a fresh agent, with no conversation, and says so; its output is then a fresh draft from the issue.
- **One conversation per task.** Two members continuing the same task share one session and see each other's turns.
- **Refine and revise overlap.** Refine (a new run guided by feedback) could later become "open the conversation with the feedback, then revise", retiring one of the two.

## Phases

1. **factory, recipe shape (built).** `start` / `revise` in `pkg/recipe`, validation, the built-in recipes moved under `start`, the runner running the part `task.json`'s `revise` names (start when unset).
2. **factory, revise (built).** `session` in `task.json`, the daemon holding a started task's session for a running revise, `factory recipe revise`, revise actions in task outputs, `session` in `source` and in the recorded run annotation. Plan gets **Use as plan**.
3. **repo-agent.** Request verb `revise`, launch and harvest, `session` in the recorded run, the buttons.
4. **Triage revises,** once plan's has been used.
