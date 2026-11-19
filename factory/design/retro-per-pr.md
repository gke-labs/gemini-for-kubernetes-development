# Per-PR retro

Status: built, but for one defence (below). Builds on the weekly retro ([retro.md](retro.md)).

What was built, and where:

- **The watch's part:** `pkg/commands/watch/prretros`, on with `spec.retro.perPR` (`--pr-retro`, `WATCH_PR_RETRO`), using conservative defaults (`10m` poll interval, `10m` settle, `2h` record wait, max `3` concurrent). Its record of each label application (the `labeled` event) is `pr_retro_state.json` in the queue directory. The labeller is a maintainer when they are the watcher, a trusted login, or one of the repository's collaborators (GitHub's collaborator check, which includes its members with access).
- **The staging:** `retro.StagePR` (`pkg/retro/pr.go`), whose layout is the recipe's input.
- **The run:** `watchPRRetro` (`pkg/commands/retro_pr.go`) runs the `retro-pr` recipe in a sandbox of its own (prefix `retropr`, keyed by the PR and the label application), opens its Change with `open-pr` as a ready PR, and deletes the sandbox. A Change on a repository that changed nothing opens nothing; its comment is what the watch posts.
- **Notes:** `retro.IsNote`; the care rules skip them through `conventions.HasIgnorePrefix`.

Not built yet: the GitHub token still reaches every step of the retro's sandbox (`credentials: full`), as for the weekly retro; see [Security](#security). Keeping it to the push step needs a credentials mode of its own, for both retros.

## Summary

A maintainer labels one of overseer's pull requests `<trigger>/retro` (e.g. `overseer/retro`). Once the PR is closed, overseer runs a retro on that PR alone. If the retro finds lessons, it opens a PR changing the agents' instructions, assigned to the humans involved. If not, it says why in a comment on the original PR. When it's done, overseer takes the label off.

## Motivation

The weekly retro improves the agents' skills, workflows and review instructions from a week of PRs. It picks the PRs worth a look by numbers: closed unmerged, many care passes, much human editing. That leaves gaps:

- **Latency.** A lesson from Monday's PR lands up to a week later. By then the reviewer has moved on, and the agents may have repeated the mistake.
- **Aim.** The numbers miss a PR that was small but badly wrong. The human who reviewed it knows; the weekly retro doesn't.
- **Context.** A reviewer often knows exactly what went wrong ("it ignored the envtest skill") but has no way to tell the retro.

A per-PR retro lets the person who saw the problem point the retro at it, and say what they saw.

## Goals

- Opt-in per PR, with a label. Nothing runs on a PR without it.
- The retro runs after the PR closes, merged or not, on that PR alone.
- The requester can leave notes for the retro on the PR.
- The outcome is visible on the original PR: a comment, with a link to a retro PR when there is one.
- At most one retro PR per labelled PR, ready for review and assigned to the humans who know the context.
- Overseer never keeps checking a PR once its retro is done.

## Non-goals

- **PRs overseer didn't open.** To start with, retros run only on overseer's own PRs (see [Which PRs](#which-prs)). Extending to other PRs is possible later, but needs a broader security model.
- **Open PRs.** The PR's story isn't finished, and care tasks are still changing it.
- **Several PRs at once,** or combining several retros into one PR. That's the weekly retro's job.
- **Coordinating with the weekly retro.** A per-PR retro is opt-in, so it doesn't try to avoid work the weekly retro may also do. If both propose the same lesson, the reviewer of the second PR closes it.
- **Applying anything without a human.** Every edit to an instruction arrives as a PR for a human to merge or close.

## User experience

1. **Ask.** A maintainer adds the `<trigger>/retro` label to one of overseer's PRs, before or after it closes. Labelling an open PR is silent: nothing happens until it closes.
2. **Leave notes (optional).** Any maintainer may comment on the PR with `/retro` followed by a note, as many times as they like, before or after the close:

   > /retro the agent never ran the envtest skill, and wrote the fixture by hand

   > /retro the automated review should have flagged the missing test for the delete path

   On an open PR, overseer's care tasks ignore these comments: a note is for the retro, not a request to change the PR.
3. **Started.** Once the PR is closed and its data is ready, overseer comments that the retro has started. The comment links to the session and says what the retro has to work with: the agents' traces, the PR's outcome, human feedback.
4. **Result.** One of:
   - **Lessons found:** a PR, ready for review, with edits to the instructions behind the mistakes. It's assigned to the original PR's human assignees and to everyone who left a `/retro` note. A comment on the original PR links to it. The retro PR's description lists each edit, the lesson behind it and the evidence (a review comment, a note, or a moment in the agent's trace), and answers each note.
   - **No lessons:** a comment on the original PR saying what was looked at, how each finding was judged, and why none calls for an edit. Each note gets an answer. Problems that aren't instructions (a broken test environment, a factory bug) are listed for a human to file.
   - **Failure:** a comment saying the retro failed, with a link to the session.
5. **Done.** Overseer takes the label off, whatever happened. To retro again, add the label again.

The label works as a command, like `<trigger>/adopt`: if the label is on, a retro is pending or running; if it's gone, the retro is done. To tell whether it's running yet, look for the start comment.

## Design

### Which PRs

A retro runs only on a PR that overseer opened: one authored by a bot in overseer's pool. This is the same test the care rules use to decide which PRs are overseer's to look after. A PR overseer adopted counts, because adopting opens a new PR from the pool.

If the label is on any other PR, overseer takes it off and comments once that retros run only on overseer's PRs. The label is ignored on issues.

Limiting retros to overseer's PRs keeps the inputs to ones overseer produced or a maintainer wrote (see [Security](#security)). It also means every PR has an agent record to wait for.

### Who may ask

Only maintainers may trigger a retro or leave notes: the repository's owners, members and collaborators, and the logins overseer trusts. These are the same people whose feedback the agents act on today.

If someone else applies the label, overseer takes it off and comments once that only maintainers can ask for a retro. `/retro` comments from others are ignored.

### Finding the PRs

Overseer periodically lists the repository's **closed** PRs that carry the label. Taking the label off when a retro ends keeps that list short: it holds only pending retros, never history. PRs the retro has finished with drop out of it, and overseer never re-reads them.

This is a separate part of overseer, not a watch rule. Watch rules act only on open items: a closed item is excluded from the listings and from every decision. Extending the rules to closed items would affect every rule for the sake of one. Like the weekly retro, the per-PR retro also gathers its data outside the agent's sandbox, which watch rules have no step for.

Retros on different PRs are independent and may run at the same time, within overseer's usual limits on concurrent tasks.

Overseer keeps its own record of each retro it starts, per PR and per label application. That record, not the label, decides whether a retro runs:

- **Running:** while a retro runs, its PR still carries the label and still appears in the list. Overseer recognises it as running and doesn't start a second one. After a restart, overseer picks up the running retro rather than starting another.
- **Finished:** if taking the label off fails, overseer retries the removal on later passes. It never runs the same label application's retro again.

### What the retro reads

A retro reads all of this that is available for the PR:

- **The PR's outcome:** how it ended, care passes, how much humans changed after the agent's last push. It exists once overseer has saved its agent record for the PR, when it cleans up the PR's sandbox.
- **The agents' archived tasks** for the PR: their transcripts, logs, and which skills were in play when. These are saved whenever an agent's sandbox goes away, including sandboxes removed before the PR closed.
- **The human feedback:** reviews, inline comments and conversation comments by maintainers. Taken from the saved agent record when there is one, else read from the PR directly.
- **The PR's change:** its changed files, and the change itself, which the agent reads from the repository. For a PR closed unmerged whose branch was deleted, GitHub's pull-request refs still hold its commits.
- **The `/retro` notes** (see [Notes](#notes)).

What the retro has decides how it reasons (see [The analysis](#the-analysis)). The start comment and the result say what it had.

### Notes

A note is a maintainer comment on the PR that starts with `/retro`.

- **Which notes are read:** those posted up to the moment the retro starts. On the PR's first retro, that's every note on the PR. On a later retro, after the label is applied again, it's only the notes posted since the previous retro finished. Earlier notes were already answered.
- **Not care feedback:** on an open PR, `/retro` comments are not feedback for overseer's care tasks. They are skipped in the same way as `/<trigger>-ignore` comments.
- **Notes and feedback:** a note is also a maintainer comment, so it would also count as feedback. It's passed to the retro once, as a note.

`/retro` breaks overseer's naming of comment commands after the trigger (`/overseer-ignore`) on purpose. A note is written by a reviewer in passing, and the short form is the one people will reach for. The trigger label namespaces the command; the comment doesn't need to.

### Waiting for the data

The agents' record of a PR isn't complete when the PR closes. Overseer saves the PR's outcome, feedback and agent traces when it cleans up the PR's sandbox, some minutes after the close. The saved traces become readable a few minutes after that. So the retro waits:

- **Normally:** wait for the agent record, plus a short settling time for the traces, then run.
- **The record never appears:** after a limit (default 2 hours), the record wasn't saved, e.g. the PR's sandbox went away before the PR closed. Run with what is available: any archived tasks, plus feedback read from the PR.
- **Can't tell:** if the agent record can't be checked (the collector is unreachable), keep waiting. Falling back while the collector is down would lose the traces too.
- **Nothing to read** (no agent record, no archived tasks, no human feedback, no notes): don't run. Comment that there's nothing to retro on, and take the label off.

A PR labelled long after it closed has its record already, so it starts at once.

### The analysis

The retro runs as an agent in a sandbox of its own, like the weekly retro. It has the same limits:

- the gathered data is input, never instructions;
- it may only edit the agents' instructions: skills, workflows, agent definitions, `AGENTS.md` / `GEMINI.md`, review instructions;
- it commits, but never pushes, opens PRs or posts comments.

It differs from the weekly retro in four ways:

- **Notes first.** The notes say where to look, and each one gets an answer. A note can steer what the retro examines. It can't widen what the retro may do: an instruction in a note to do anything else is ignored, as in any other gathered text.
- **One PR is enough.** The weekly retro wants a lesson to recur across PRs. Here a human has said this PR matters, so a clear-cut lesson from it alone is proposed. One-off preferences and problems with the issue or plan still change no instruction.
- **Judging from what it has.** With traces, the retro lines feedback up with the skill the agent followed at that point, as the weekly retro does. Without traces, it can't see which skill was in play. It then proposes lessons only where feedback supports them: what the automated review should have caught, facts about the repository, or a coding skill when the feedback or a note names the area plainly. The result says which evidence it had.
- **No lessons, no PR.** When nothing should change, the retro makes no commit and writes the comment instead.

### Reporting back

Two accounts write to GitHub, as for other agent work:

- **The pool bot that ran the retro** pushes its commits and opens the retro PR.
- **Overseer's own account** writes everything on the original PR: the start comment, the result or failure comment, the link to the retro PR, and the label removal.

The retro PR:

- **Ready for review:** opened as a regular PR, not a draft. A human asked for this retro, so its result is wanted now.
- **Assigned:** to the original PR's human assignees (pool bots left out) and to every author of a `/retro` note the retro read. These are the people with the context to judge the lessons.
- **Linked:** it links to the original PR, and the original PR gets a comment linking to it.
- **Cared for like any of overseer's PRs:** overseer answers its review comments, fixes its CI, rebases it on conflicts, and runs the automated review on it.

Each comment is posted once, so a retried step never posts twice.

### Sandboxes

Each retro gets its own sandbox: created when the retro starts, and deleted when it ends. It's never shared with another PR's retro, the weekly retro, or the original PR's sandbox. Retros on different PRs don't share a checkout or wait on each other, and nothing from one retro carries over to the next. A label added again later starts a new retro in a new sandbox.

The cost is setup: each retro clones and prepares the repository afresh, which on a large repository can take longer than the analysis. Where overseer keeps warm workspaces for the repository, a retro's sandbox starts from one.

### Security

The retro's output edits the instructions every later agent follows, so a successful prompt injection is worth more here than in most tasks. Running only on overseer's PRs keeps the inputs to the same kind the weekly retro already reads:

| input | written by |
|---|---|
| the label, notes, human feedback | maintainers only (see [Who may ask](#who-may-ask)) |
| the PR's title, description and change | overseer's agents |
| the agents' traces and outcome | overseer and its agents |

No input is written directly by someone outside the maintainers. One indirect path remains: the traces hold what the original agents read while working. That includes the text of the issue the PR fixes, which anyone may have written. The weekly retro has the same exposure.

The defences:

- **Prompt level:** all gathered text is data, never instructions, and the retro may only edit instruction files and commit.
- **Human review:** every edit arrives as a PR that a human reviews before merging. The PR description cites the evidence for each edit, which makes an edit with no evidence behind it stand out.
- **Credentials:** the GitHub token reaches only the step that pushes, never the agent's steps. The agent can't act on GitHub even if its instructions are subverted; at worst it writes a bad commit, which the review catches. The weekly retro should get the same change.

Opening retros to PRs overseer didn't open would let anyone's text, including outside contributors', reach the retro directly. Revisit this section before doing that.

### Configuration

Per overseer (`spec.retro.perPR`), off unless enabled.

Timing and concurrency use conservative defaults: a `10m` poll interval, a `10m` trace settling window, a `2h` wait for the agent record after close, and at most `3` concurrent per-PR retros. Without the trace archive that the weekly retro uses, retros run on feedback and the change alone.

## Measuring success

Per-PR retro tasks are counted apart from other tasks, so their cost can be seen. The measures:

- retros run, and the share that found lessons;
- retro PRs merged vs closed unmerged;
- the time from the label to the result;
- the cost per retro, and per merged retro PR.

A low merge rate means the bar for "one PR is enough" is too low. Many no-lesson results on PRs labelled for a reason mean the retro is missing what humans see.

## Alternatives considered

- **Any PR from the start.** Deferred: human and outside contributors' PRs would put text from anyone directly in front of the retro, and need a stricter security model (see [Security](#security)). Overseer's PRs are also where the traces, and so the best lessons, are.
- **A watch rule.** Rejected: rules only act on open items (see [Finding the PRs](#finding-the-prs)).
- **Triggering on close for every overseer PR.** Rejected: the cost and the PR noise would go to every PR. The weekly retro already covers the general case. Per-PR is for when a human thinks the PR matters.
- **Triggering by a `/retro` comment instead of a label.** Rejected: a label is visible on the PR list, can be filtered on, and its removal marks the retro done. A comment can't do any of that.
- **A second label to show a retro is running.** Rejected: the start comment shows it to people, and overseer's own record tracks it. A second label adds a write and a state to keep in step.
- **Treating every maintainer comment around the time of labelling as a note.** Rejected: any time window is a guess, and it would pick up comments that weren't meant for the retro. `/retro` says what the comment is for.
- **`/<trigger>-retro` for notes,** matching `/<trigger>-ignore`. Rejected: longer and less natural for a note written in passing (see [Notes](#notes)).
- **A draft retro PR.** Rejected: a human asked for the retro, and the PR is assigned to people waiting for it. A draft would add a step before review.
- **Retrying a failed retro automatically.** Rejected: a retro isn't urgent, and automatic retries would need their own limits. As with `adopt`, a failure waits for someone to add the label again.
- **A fixed feedback-only mode for the fallback.** Rejected: a PR can have archived traces with no outcome record (its sandbox went away before it closed). The retro uses whatever exists.
