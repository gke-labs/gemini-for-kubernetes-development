# Review loop: review and fix inside one task

Status: phase 1 in progress (steps 1-3 built).

## Why

A grouped issue graph (design/issue-graph.md) starts its next step only when the group's draft PR would pass the `ready` rule: checks green, the automated review at its head asking for no changes, no feedback unanswered, no task of the PR running (`groupStepReady`). The automated review and its answer go through GitHub and the watch, one task at a time:

```
fix ──push──▶ PR ··scan·· review task (its own sandbox) ──review──▶ GitHub ··scan··
care-comments task (the fix sandbox) ──push──▶ PR ──CI──▶ ··scan·· review task …
```

Every hand-off waits for a scan, a queue slot and a sandbox to wake: 40-50 minutes each on KCC staging, and up to three rounds per step. A batched step took 8-9 hours from its draft PR to the next step (KCC #13107 and #15028, 2026-11-06).

## What

Two tasks instead of that chain:

1. **`fix`**, unchanged: it pushes the step's work to the draft PR.
2. **`review-loop`**, once, on that PR, in the same sandbox. In one task it goes back and forth between two conversations:
   - **a reviewer**: a step session of its own (`ask: … session: review-N`), fresh each round, so it reads the change as someone who did not write it. It sees the change against the PR's base, the PR's text, the review instructions and, after the first round, the earlier findings. It has no way to GitHub (gh logged out, git without credentials).
   - **the fixer**: the PR's own conversation (`session: care`, shared with the care recipes), which remembers why it wrote what it wrote. It answers each finding: it changes the code where the reviewer is right, or says why not.

   Each round's fix is pushed, so CI runs on that head while the next round reviews it. The loop stops at the first clean review, or after the third round's fix. Between rounds nothing touches GitHub or the watch, and while the task runs it holds the PR, so `review` and `comments` wait.

Recipes have no loops, so the rounds are written out: each round is a review ask, an ask for the findings file (`verdict: clean|changes`), a `run:` that greps the verdict (`continue-on-error`), then the fix ask and the push, gated on that step's exit code (`if: '{{ ne .Steps.cleanN.ExitCode 0 }}'`). A skipped step's exit code is 0, so a clean round skips every round after it. After each push the lease moves to the head it pushed (`push.json`).

## Phase 1: one external review at the end

The automated review that counts (`reviewed-at-head`) is one from a reviewer-role login (`roles.reviewer.users`, or a login with "reviewbot" in it), and a task posts with the token of the account it runs as. A loop that pushes runs as the PR's author, so a review it posted would not count. So in phase 1 the loop posts nothing (its reviews are task files, `review-N.yaml`, kept in the task archive), and the `review` rule reviews the head it leaves, as now. That review should pass the first time: about three rounds through GitHub become one.

Steps:
1. An `ask:` can name a session of its own in the task (`session:`): #2226.
2. A step session has no way to GitHub: #2227.
3. The `review-loop` recipe.
4. A watch rule that runs it on a graph group's draft PR (`author: pool`, `draft: true`, `closes: {labels: {all: ["*/review"]}}`), before `review`, once per push of a step: `dedupe: head` records the head a task started on, and the loop's own pushes move it, so the rule needs the head the task ended on (or a marker the loop leaves).
5. Measure (review rounds per step, time from draft PR to next step, against the 2026-11-06 baselines), then turn it on for staging's KCC overseer.

## Phase 2

- **The loop's last review as the review of record:** apply posts the loop's final review with a reviewer identity's token (a second identity for the task), so `reviewed-at-head` holds when the loop ends and no external round is left.
- **A reviewer on another model or engine**, for a second opinion that does not share the fixer's blind spots; another engine needs its API key passed beside the run's.
- **Every `*/review` PR of the pool**, not only grouped steps, if phase 1's numbers say so.
