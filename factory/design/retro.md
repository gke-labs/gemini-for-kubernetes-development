# A weekly retro that improves the repository's skills and workflows

Status: proposed. Step 1, archiving each task, is **built** (#2031, #2035, #2038: design/token-usage-collection.md §9), not as first proposed here; §1 describes what was built. Steps 2-5 are proposed.

## Goal

**The retro changes the instructions the agents ran on, in the repository they ran on.** Every proposal is a diff to one of:
- a **skill**: `<skills dir>/<name>/SKILL.md` and its files, from the directories the repository lists in `.agents/skills.json` (KCC: `.gemini/skills/`);
- a **workflow**: `.agents/workflows/<name>`;
- an **agent or chore definition**: `.agents/<name>.md`;
- the repository's context files, `AGENTS.md` / `GEMINI.md`, for what no single skill owns;
- the **review instructions**: what the automated reviewer is told to check. They reach it as `{{review-instructions}}`, collected from the `## Review Instructions` sections of the PR and the issues it closes (`watch/prs/handlers.go`). Those sections are written by the workflows and graph steps (`review:`) that file the issues, and by the people filing them. The reviewer also follows the repository's review skills and, when it has one, its own `review` recipe.

Each proposal names the PRs behind it, and the moments in their agent traces where the instruction failed. It arrives as a PR, and a human merges it or doesn't.

The richest signal is what the humans said on the PRs (§4). A reviewer's comment on an agent's PR is a lesson for two sets of instructions at once. The **coding skills** let the agent write it, and the **review instructions** let the automated reviewer pass it.

What isn't an instruction goes elsewhere. A broken test environment, a factory bug, or a recipe problem becomes an issue on the repository that owns it, not a skill edit. The KCC envtest mismatch (#1975, KCC #14022 and #14023) was that kind.

To say which skill to change, we need to know which skills a PR used and what the agent did with them. Both are in the agent's trace, which is now archived before its sandbox goes (§1).

Training a model of our own is out of scope. Most failures are environment and missing repository knowledge, and we have hundreds of PRs, not the thousands of labelled outcomes a model would need.

## Today

- **The trace is archived (built).** acpd writes each task's session as NDJSON, `session/stream.ndjson` in the task's directory: prompts, agent messages, every tool call with its arguments and output, turn ends with usage. The token-usage collector archives every task's directory, that transcript included, to a GCS bucket before the sandbox can lose it (§1). Nothing reads the archive back yet.
- **Which skills ran is in the trace, nowhere else.** A skill reaches the agent in one of two ways:
  - the engine's skill tool (gemini-cli's `activate_skill`), a `tool_call` naming the skill;
  - a file read of a `SKILL.md` or the files beside it.

  A workflow is known up front: the run starts from `.agents/workflows/<name>` and records its name.
- **What a run cost is collected; what it achieved isn't.** The token-usage collector (design/token-usage-collection.md) keeps one record per task. Nothing records whether the PR was merged, how much a human changed, or why it was closed.
- **Scheduled work exists.** The chores scheduler runs `.agents/*` definitions on a `schedule:` (`@weekly`). Repositories can carry their own recipes in `.agents/recipes/`.

## Proposal

### 1. Keep the trace: archive it before the sandbox goes (built)

**Built in #2031, #2035 and #2038**, described in design/token-usage-collection.md §9. It differs from what this section first proposed (an upload by `factory watch` to a `traces/` folder per issue or PR): the archive belongs to the token-usage collector, which already had a bucket.

- **What:** every file of the task's directory and one directory down, as the task server lists them (`GET /v1/tasks/{id}/files?subdirs=1`): `task.json`, `status.json`, `inputs.json`, `push.json`, `recipe.source.yaml`, `task-output.yaml`, `change.yaml`, `comments.jsonl`, `execution.log`, `steps/<NN-step>.log`, `session/stream.ndjson` (the transcript, always tried) and `session/session.json` (the session record). Left out: `recipe.yaml` and `steps/uses.sh` (generated from `recipe.source.yaml`), `pid`, `lease`, empty files, and the env and secrets files, which the task server never serves.
- **Who and how:** the harvest (`usagereport.HarvestTask` / `HarvestSandbox`, in the recipe runner and the watch's sweeps) reads the files through the sandbox's task server and sends them with the task's usage, as one tar stream, to the collector (`PUT /v1/tasks/{sandbox}/{task}?repo=&issue=&pr=&taskType=`). The collector spools the task on its PVC and its flusher uploads it every `FLUSH_INTERVAL` (5m). Nothing runs in the sandbox for it, the engine never holds bucket credentials, and the collector writes with Workload Identity (its own KSA, `roles/storage.objectAdmin` on the bucket).
- **When:** at task end; before an idle sandbox is suspended (bounded to 3 minutes, so a later delete need not wake it); and before each delete (the closed-PR, closed-issue and stale sweeps). A pass that got everything in sets `sandbox.gemini.google.com/harvested-at`, and a suspended sandbox harvested since its last activity is not woken again. A later harvest of the same task replaces its record, so a session that grew after its task ended (Continue session) ends up archived whole.
- **Layout:**

  ```
  gs://<bucket>/<prefix>/tasks/<owner>/<repo>/<sandbox>/<task>/
    manifest.json                  the index record, written last (a folder without it did not finish)
    <file>.gz                      one gzipped object per file, named as in the sandbox
    steps/<NN-step>.log.gz
    session/stream.ndjson.gz  session/session.json.gz
  gs://<bucket>/<prefix>/task-index/   the index log (segments and snapshots)
  ```

  By sandbox, not by issue or PR: a fix sandbox serves its issue and then its PR, and its tasks stay together. The index answers by issue or PR: `GET /v1/tasks?repo=&issue=&pr=&sandbox=` on the collector lists each task's record - `folder`, `recipe`, `runName`, `url`, `state`, `exitCode`, `startedAt`/`endedAt`, `pushedBranch`/`pushedHead`, `applied`, and `files` with size and sha256. A file whose content the archive already holds is not uploaded again.
- **Redaction:** the collector masks credential shapes line by line on the way in (`gh[pousr]_…`, `github_pat_…`, `AIza…`, `ya29.…`, `sk-ant-…`), a backstop to keeping tokens out of the engine (#1695, #1696).
- **Limits:** 256MiB per task, 512MiB for the spool (the harvest tries again on its next pass).
- **Configuration:** the collector's `GCS_BUCKET` / `GCS_PREFIX`, from the `token-usage` ConfigMap (`gcs-bucket`, `gcs-prefix`). Without a bucket the collector answers 501 and keeps nothing, and the harvest moves on: off unless set. There is no per-overseer setting.
- **Retention:** no lifecycle rule. The collector's prefix is the only full copy of its records, so none was added; a rule for `tasks/` alone is an open question (below).

What changed from the first proposal, and what the retro does about it:
- **No `traceURL`:** a task's place is its index record's `folder`. Outcome records (§3) carry it.
- **The collector never serves a task's files**; readers go to the bucket. The retro's staging (§6) needs a reader.
- **The engine's own chat history** (gemini-cli's `.gemini/tmp/*/chats/*.jsonl`, outside the task directory) is not archived. acpd's `stream.ndjson` already has every prompt, message and tool call of the task, which is what the retro reads. Those chat files are the engine's resumable state, and one shared across many runs grows without bound: a KCC care conversation reached 745MB and looped (PR 14181, 2026-10-26).

### 2. Map a run to its instructions

The archive's `manifest.json` is the task's index record (§1): what ran and how it ended, but not which instructions it ran on. This step adds an `instructions` object to that record, computed by the collector when it spools the task, from the files it already has (`session/stream.ndjson`, `session/session.json`, `task.json`, `inputs.json`):

```json
{
  "repo": "GoogleCloudPlatform/k8s-config-connector",
  "commit": "<default-branch sha the instructions were read at>",
  "workflow": {"name": "kcc-greenfield", "path": ".agents/workflows/kcc-greenfield.txt"},
  "recipe": {"name": "fix", "hash": "sha256:…"},
  "engine": "gemini", "model": "gemini-3-pro",
  "context": ["AGENTS.md", "GEMINI.md"],
  "skills": [
    {"name": "kcc-direct-controller-implementer", "path": ".gemini/skills/kcc-direct-controller-implementer",
     "how": "activated", "firstSeq": 412, "lastSeq": 1980},
    {"name": "record-real-gcp", "path": ".gemini/skills/record-real-gcp", "how": "read", "firstSeq": 2210, "lastSeq": 2211}
  ]
}
```

- **Skills** come from the trace: a skill-tool call names the skill (`how: activated`). A file-read tool call of a path under a skills directory from `.agents/skills.json` is `how: read`. Engines without a skill tool are covered by the reads.
- **`firstSeq` / `lastSeq`** place the skill in the transcript: the stretch of the conversation where it was in play. The retro uses them to find what the agent did while following that skill.
- **`commit`** pins the version of every instruction, so a lesson is checked against the skill as it was, and a later edit can be compared before and after.

- **`engine` and `model`** come from `session/session.json`; the workflow and the recipe from `task.json` and `inputs.json`.

The `instructions` object is small and is part of the index record, so `GET /v1/tasks` returns it, and the PR's outcome record (§3) carries a copy. That makes per-skill rollups cheap without reading the bucket.

### 3. An outcome record per PR

When a PR the agents opened is merged or closed, factory writes one **outcome record** to the collector:

```json
{
  "key": "outcome:owner/name#101",
  "repo": "owner/name", "pr": 101, "issues": [42],
  "state": "merged | closed", "openedAt": "...", "closedAt": "...", "closedBy": "login",
  "tasks": [{"sandbox": "...", "task": "...", "folder": "tasks/owner/name/<sandbox>/<task>", "instructions": {…}}],
  "carePasses": 3, "reviewComments": 7, "changesRequested": 1,
  "feedback": {"items": 9, "humans": 2, "afterBotReview": 3, "folder": "prs/owner/name/101"},
  "agentHead": "<sha of the agent's last push>",
  "humanEdit": {"files": 2, "added": 14, "deleted": 3, "paths": ["pkg/controller/foo.go"]},
  "ciRedAfterPush": 1, "requiresHuman": false,
  "closeReason": "one line from the closer, if they gave one"
}
```

- **Where:** the closed-PR sweep writes it, after the harvest that archives the PR's tasks; it lists them from the collector's index by PR. A PR merged after its sandbox is gone is caught by the overseer's closed-PR scan.
- **`humanEdit`:** how much the PR's change moved between the agent's last push and the final head. Each is compared against its own merge base, so a rebase onto a moved base branch doesn't count. `paths` lets the retro line a human's edit up with the stretch of the trace that wrote that file.
- **Rollups per instruction:** `GET /v1/outcomes/rollup?repo=&since=&by=skill|workflow` gives, for each skill and workflow, the PRs that used it, how many merged, care passes, human edits and cost per merged PR. That is the scoreboard the KCC plan asks for (docs/kcc-code-emergency-scaling-plan.md, 6.1: which step fails most).

### 4. Human feedback on the PR

What the humans said on an agent's PR is captured whole, not just counted. It's the most direct statement of what the agent got wrong, and of what the automated review should have caught.

**What is captured**, for each PR the agents opened, from the PR's reviews, inline review comments and conversation comments:
- comments by a **human of a trusted kind** (owner, member, collaborator, or a login the overseer trusts). Pool bots, the watcher and apps are left out, as the care rules leave them out of `feedback`.
- each item records:
  - `author`, `kind` (`review`, `inline`, `comment`), the review `state` (`CHANGES_REQUESTED`, `APPROVED`, `COMMENTED`), `path` and `line` for an inline comment, `at`, `inReplyTo`, and the `body`;
  - the PR `head` it was made on;
  - `afterBotReview`: true when the automated reviewer had reviewed that head without raising the point. That's a review miss candidate.
  - `answeredBy`: the care task whose `comments.jsonl` (archived, §1) holds the item, and what came of it - a push after it (`fixed`), a reply only (`replied`), or nothing.
- the automated reviewer's own comments that a human **dismissed or disagreed with**: resolved without a change, or answered with "not needed" or "won't fix". These are review false positives.

**When and where:** the closed-PR sweep reads them with the cached GitHub client when the PR closes, after the harvest of its tasks. It writes them to the archive through the collector as one file per PR, `<prefix>/prs/<owner>/<repo>/<n>/feedback.jsonl`, redacted like task files (`PUT /v1/prs/{owner}/{repo}/{n}/feedback`). The outcome record (§3) keeps the counts and the folder. Feedback on a PR that is still open is already in its care tasks' `comments.jsonl`, so nothing more is read while it's open.

**What the retro does with it** (§6): it puts every item in one of these classes:

| Class | When | What it proposes |
|---|---|---|
| **coding** | the agent's change was wrong or incomplete | an edit to the coding skill in play where the agent wrote that `path`: the manifest's skill stretch (§2) around the transcript position that edited the file, as for `humanEdit` |
| **review miss** | `afterBotReview`: the automated review saw that head and didn't raise it | an edit to the review instructions: the review skill, or the `## Review Instructions` source (the workflow or graph step that writes it), or the repository's `review` recipe |
| **review noise** | a human dismissed the bot reviewer's point | the same targets, to drop or narrow an instruction |
| **repository knowledge** | a fact about the code base no skill owns | a line in `AGENTS.md` / `GEMINI.md`, or a new skill |
| **scope or plan** | the issue or the plan was the problem | nothing to the instructions; noted in the journal |
| **taste** | a one-off preference | nothing, unless several PRs show it |

A point that recurs across PRs, or across reviewers, is a lesson; a single comment is evidence. Coding and review lessons from the same feedback item are proposed together: fix the skill that let the agent write it, and the review instruction that let it through.

### 5. Asking why a PR was closed

When a bot PR is closed unmerged by someone other than the bot, the watch comments once, mentioning the closer:

> @alice closing? One line on why helps the agents learn, e.g. "wrong approach", "duplicate", "not needed", "superseded by #123". Optional.

- The PR is remembered as asked in the watch's state, and the comment carries a hidden marker, so it is never posted twice.
- For 7 days, the watch reads new comments on the PRs it asked about, with the cached GitHub client. The first reply from the closer or a member becomes `closeReason`. A `<trigger>/closed-<reason>` label counts as an answer too.
- A PR the bot closed itself gets no comment: factory records its own reason.

### 6. The retro recipe

A built-in recipe, **`retro`**, on a repository (`on: [repo]`), run weekly per overseer from the chores scheduler. The scheduler learns to run a recipe: a chore definition with `recipe: retro`, `inputs:` and `schedule: "@weekly"`.

**Inputs, staged by the watch before the task starts:**
- the week's outcome records and the per-skill rollup;
- for every PR worth a look, its archived tasks' transcripts and logs: closed unmerged, 3 or more care passes, human feedback (§4), or human edits above a threshold;
- the PR's human feedback (`feedback.jsonl`, §4) and `closeReason`.

The staged files go into the task's directory, so the agent needs no GCP credentials. The recipe runs with `credentials: clone`.

Reading the archive is the one new piece: today nothing reads `tasks/` back, and the collector never serves a task's files. The collector already holds the bucket's credentials, so it gets a read endpoint, `GET /v1/tasks/{sandbox}/{task}/files/{name}`, that streams one archived file (unzipped) from the bucket; the watch stages through it and needs no GCS access of its own.

**The agent:**
1. For each PR, finds what went wrong. It lines up the human feedback (§4) and the human edits (by path) with the trace, and with the skill in play at that point (the manifest's `firstSeq`–`lastSeq`). It also lines them up with the automated review of the same head.
2. Classifies each finding, using §4's classes for feedback: the coding skill was wrong, missed a step, was ignored, or there was none; the review instructions missed it or were noise; the workflow's order was wrong; environment or tooling; bad issue; or fine.
3. Groups across PRs: one lesson that three PRs show beats three anecdotes.
4. Proposes edits to the skill, workflow or review instructions responsible, at the default branch's current version, citing PRs, feedback items and trace positions. A lesson with no skill to own it becomes a new skill or a line in `AGENTS.md`. Environment and factory problems change no instruction: they go in the week's journal (below), for a human to file.

**Result: one `Change`**, as `fix` and the agent chores produce, opened as one PR by the existing `open-pr` action. No new task-output kind and no new action. It holds:
- the edits to skills, workflows and review instructions;
- **the week's journal**, `.agents/journals/retro/<YYYY-MM-DD>.md`: the scoreboard (per skill and per workflow, compared with last week, plus the automated review's misses and noise), the classified PRs, what was proposed and why, and the environment and factory problems to file.

The PR body is a table of `instruction → lesson → PRs (feedback and trace links)`, and links the journal.

A journal is a file in the repository, as `.gemini/journals/` is on KCC, not an issue: it is reviewed with the edits it explains, needs nothing beyond the PR to produce, and the history of retros is the folder.

Nothing is applied automatically. The next retro reads the journals on the default branch and last week's retro PR: for each skill edited and merged, whether its numbers moved; for a PR closed unmerged, or edits a reviewer removed, what was rejected. A rejected proposal isn't made again unless new PRs back it.

A repository can carry its own retro in `.agents/recipes/retro.yaml` if the built-in doesn't suit it.

### 7. Evaluating a change before making it (later)

Archived traces and outcome records are also what an offline evaluation needs. The idea: a replay set of about 30 past KCC issues with known merged fixes, run against a candidate skill version, and live A/B by engine. That's a separate design. Steps 1–6 don't depend on it.

## Steps

1. **Archive traces - done** (#2031, #2035, #2038): the collector archives every task's directory, transcript included, at task end, before an idle suspend and before each delete, redacted, indexed in `task-index` and listed by `GET /v1/tasks`. Not done, by decision: a lifecycle rule, `traceURL` (the index's `folder` instead), a per-overseer bucket setting.
2. **Instructions:** skill detection from the archived transcript (skill-tool calls, reads under `.agents/skills.json` dirs), the workflow, recipe, engine, model and the instruction commit - an `instructions` object in the index record, computed by the collector at spool time. Plus the collector's read endpoint for archived files (§5).
3. **Outcome records and rollups:** the record from the closed-PR sweep, `humanEdit`, the collector's endpoints including `by=skill|workflow`.
4. **Human feedback:** the closed-PR sweep captures the PR's human reviews and comments (with `afterBotReview` and `answeredBy`) into `prs/.../feedback.jsonl` through the collector, with counts on the outcome record.
5. **Why closed:** the comment and the reply harvest.
6. **The retro recipe:** chores that run a recipe, staging traces as inputs, the recipe (one `Change`: the edits and the week's journal). Run it by hand on KCC first (`factory recipe retro --url <repo>`), then weekly on overseer-kcc.

Steps 1, 2 and 4 are worth having on their own: an archived trace already answers "what did the agent do?" for any PR after its sandbox is gone - today by reading the bucket directly.

## Open questions

- **Which PRs count as the agents'?** Proposed: the PRs the overseer or a board opened (a bot author, or the factory session link in the body). PRs a member opened and the agent only cared for are archived and recorded, but left out of acceptance rates.
- **Skills outside the repository** (user-level or engine-bundled): recorded in the manifest by name, but the retro proposes no edits to them.
- **Trace access:** whether members who can see the board should read archived traces through repo-agent (through the collector's read endpoint, or a signed URL), or only through the bucket.
- **Retention of `tasks/`:** the collector's prefix has no lifecycle rule, as it is the only copy of the usage records. Task folders are the bulk (KCC transcripts reach megabytes per task); a rule on `<prefix>/tasks/` alone (e.g. 180 days, after the retro has read them) would bound the cost without touching the records.
- **Repositories the bot can't push to:** no PR, so the journal and the proposed diffs stay in the task's output, for a human to apply.
