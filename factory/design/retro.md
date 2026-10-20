# A weekly retro that improves the repository's skills and workflows

Status: proposed.

## Goal

**The retro changes the instructions the agents ran on, in the repository they ran on.** Every proposal is a diff to one of:
- a **skill**: `<skills dir>/<name>/SKILL.md` and its files, from the directories the repository lists in `.agents/skills.json` (KCC: `.gemini/skills/`);
- a **workflow**: `.agents/workflows/<name>`;
- an **agent or chore definition**: `.agents/<name>.md`;
- the repository's context files, `AGENTS.md` / `GEMINI.md`, for what no single skill owns.

Each proposal names the PRs behind it, and the moments in their agent traces where the instruction failed. It arrives as a PR, and a human merges it or doesn't.

What isn't an instruction goes elsewhere. A broken test environment, a factory bug, or a recipe problem becomes an issue on the repository that owns it, not a skill edit. The KCC envtest mismatch (#1975, KCC #14022 and #14023) was that kind.

To say which skill to change, we need to know which skills a PR used and what the agent did with them. Both are in the agent's trace, and today the trace is deleted with the sandbox.

Training a model of our own is out of scope. Most failures are environment and missing repository knowledge, and we have hundreds of PRs, not the thousands of labelled outcomes a model would need.

## Today

- **The trace exists, then disappears.** acpd writes every session as NDJSON in `/workspaces/.acpd/sessions/<id>/stream.ndjson`: prompts, agent messages, every tool call with its arguments and output, turn ends with usage. "A transcript does not outlive its sandbox, by design" (`acpd/transcript.go`). The closed-PR and idle sweeps in `watch/sandbox/reconciler.go` harvest token usage and then delete the sandbox, transcripts included.
- **Which skills ran is in the trace, nowhere else.** A skill reaches the agent in one of two ways:
  - the engine's skill tool (gemini-cli's `activate_skill`), a `tool_call` naming the skill;
  - a file read of a `SKILL.md` or the files beside it.

  A workflow is known up front: the run starts from `.agents/workflows/<name>` and records its name.
- **What a run cost is collected; what it achieved isn't.** The token-usage collector (design/token-usage-collection.md) keeps one record per task. Nothing records whether the PR was merged, how much a human changed, or why it was closed.
- **Scheduled work exists.** The chores scheduler runs `.agents/*` definitions on a `schedule:` (`@weekly`). Repositories can carry their own recipes in `.agents/recipes/`.

## Proposal

### 1. Keep the trace: archive it before the sandbox goes

Each task's trace is copied to a **GCS bucket per overseer**, written by `factory watch`:

```
gs://<bucket>/traces/<owner>/<repo>/<issue-N|pr-N>/<sandbox>/<task>/
  stream.ndjson.gz      the acpd transcript of each of the task's sessions
  record.json           acpd's session record (engine, model, mode)
  task/                 recipe.source.yaml, the prompts, task-output.yaml, result.json
  manifest.json         the instructions the task used (§2)
```

- **When:** at task end, and again in the sweeps, right where usage is harvested today, just before the sandbox is deleted. A session can grow after its task ends (Continue session, care's shared conversation), so the sweep's copy is the final one. Writes overwrite: one object per task, last copy wins.
- **How:** read through the sandbox's task server, as the usage harvest does, and streamed to GCS, gzipped. Nothing new runs in the sandbox, and the engine never holds the bucket's credentials. The watch uses Workload Identity.
- **Redaction:** before upload, known token shapes are masked (`ghp_`, `github_pat_`, `ghs_`, `AIza…`, `ya29.`). Tokens are kept out of the engine's environment already (#1695, #1696), so this is a backstop.
- **Retention:** a bucket lifecycle rule, 90 days. Access is the overseer's namespace and the project's owners, as for the cluster itself.
- **A pointer** in the collector: the usage record gets `traceURL`, so the dashboard and the session link can find an archived trace once the sandbox is gone.

Archiving is off unless the overseer sets `traceBucket`. A failed upload logs a warning and never blocks a deletion: the sweep that deletes doesn't wait on GCS.

A GCS bucket rather than the collector's PVC because of size: KCC care conversations reach megabytes per task, and a bucket has lifecycle rules without our writing any.

### 2. Map a run to its instructions: the manifest

From the trace and the run, factory writes `manifest.json` per task:

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

The manifest is small, so a copy also goes in the PR's outcome record (§3). That makes per-skill rollups cheap without reading the bucket.

### 3. An outcome record per PR

When a PR the agents opened is merged or closed, factory writes one **outcome record** to the collector:

```json
{
  "key": "outcome:owner/name#101",
  "repo": "owner/name", "pr": 101, "issues": [42],
  "state": "merged | closed", "openedAt": "...", "closedAt": "...", "closedBy": "login",
  "tasks": [{"sandbox": "...", "task": "...", "traceURL": "gs://…", "manifest": {…}}],
  "carePasses": 3, "reviewComments": 7, "changesRequested": 1,
  "agentHead": "<sha of the agent's last push>",
  "humanEdit": {"files": 2, "added": 14, "deleted": 3, "paths": ["pkg/controller/foo.go"]},
  "ciRedAfterPush": 1, "requiresHuman": false,
  "closeReason": "one line from the closer, if they gave one"
}
```

- **Where:** the closed-PR sweep writes it, after archiving the traces. A PR merged after its sandbox is gone is caught by the overseer's closed-PR scan.
- **`humanEdit`:** how much the PR's change moved between the agent's last push and the final head. Each is compared against its own merge base, so a rebase onto a moved base branch doesn't count. `paths` lets the retro line a human's edit up with the stretch of the trace that wrote that file.
- **Rollups per instruction:** `GET /v1/outcomes/rollup?repo=&since=&by=skill|workflow` gives, for each skill and workflow, the PRs that used it, how many merged, care passes, human edits and cost per merged PR. That is the scoreboard the KCC plan asks for (docs/kcc-code-emergency-scaling-plan.md, 6.1: which step fails most).

### 4. Asking why a PR was closed

When a bot PR is closed unmerged by someone other than the bot, the watch comments once, mentioning the closer:

> @alice closing? One line on why helps the agents learn, e.g. "wrong approach", "duplicate", "not needed", "superseded by #123". Optional.

- The PR is remembered as asked in the watch's state, and the comment carries a hidden marker, so it is never posted twice.
- For 7 days, the watch reads new comments on the PRs it asked about, with the cached GitHub client. The first reply from the closer or a member becomes `closeReason`. A `<trigger>/closed-<reason>` label counts as an answer too.
- A PR the bot closed itself gets no comment: factory records its own reason.

### 5. The retro recipe

A built-in recipe, **`retro`**, on a repository (`on: [repo]`), run weekly per overseer from the chores scheduler. The scheduler learns to run a recipe: a chore definition with `recipe: retro`, `inputs:` and `schedule: "@weekly"`.

**Inputs, staged by the watch before the task starts:**
- the week's outcome records and the per-skill rollup;
- for every PR worth a look, its traces from the bucket: closed unmerged, 3 or more care passes, or human edits above a threshold;
- the PR's review threads and `closeReason`.

The staged files go into the task's directory, so the agent needs no GCP credentials. The recipe runs with `credentials: clone`.

**The agent:**
1. For each PR, finds what went wrong. It lines up the review comments and the human edits (by path) with the trace, and with the skill in play at that point (the manifest's `firstSeq`–`lastSeq`).
2. Classifies it: the skill was wrong, the skill was missing a step, the skill was ignored, there was no skill for this, the workflow's order was wrong, environment or tooling, bad issue, or fine.
3. Groups across PRs: one lesson that three PRs show beats three anecdotes.
4. Proposes edits to the skill or workflow responsible, at the default branch's current version, citing PRs and trace positions. A lesson with no skill to own it becomes a new skill or a line in `AGENTS.md`. Environment and factory problems become issues instead.

**Results:**
- **a `Change`**: the edits to skills and workflows, opened as one PR by the existing `open-pr` action. Its body is a table of `skill → lesson → PRs (trace links)`.
- **a `Digest`** (a new task-output kind): the week's scoreboard (per skill and per workflow, compared with last week), the classified PRs, and the issues to file. The new `open-issue` action posts it, labelled `<trigger>/retro`.

Nothing is applied automatically. The next retro reads the previous digest and reports, for each skill edited last week, whether its numbers moved. A proposal the humans rejected isn't made again unless new PRs back it.

A repository can carry its own retro in `.agents/recipes/retro.yaml` if the built-in doesn't suit it.

### 6. Evaluating a change before making it (later)

Archived traces and outcome records are also what an offline evaluation needs. The idea: a replay set of about 30 past KCC issues with known merged fixes, run against a candidate skill version, and live A/B by engine. That's a separate design. Steps 1–5 don't depend on it.

## Steps

1. **Archive traces:** the bucket layout, upload at task end and in the sweeps, redaction, `traceURL` on usage records, the overseer's `traceBucket`. factory and overseer manifests.
2. **Manifests:** skill detection from the trace (skill-tool calls, reads under `.agents/skills.json` dirs), the workflow and the instruction commit; written beside the trace.
3. **Outcome records and rollups:** the record from the closed-PR sweep, `humanEdit`, the collector's endpoints including `by=skill|workflow`.
4. **Why closed:** the comment and the reply harvest.
5. **The retro recipe:** chores that run a recipe, staging traces as inputs, the `Digest` kind and `open-issue` action, the recipe. Run it by hand on KCC first (`factory recipe retro --url <repo>`), then weekly on overseer-kcc.

Steps 1 and 2 are worth having on their own: an archived trace answers "what did the agent do?" for any PR after its sandbox is gone.

## Open questions

- **Which PRs count as the agents'?** Proposed: the PRs the overseer or a board opened (a bot author, or the factory session link in the body). PRs a member opened and the agent only cared for are archived and recorded, but left out of acceptance rates.
- **Skills outside the repository** (user-level or engine-bundled): recorded in the manifest by name, but the retro proposes no edits to them.
- **Trace access:** whether members who can see the board should read archived traces through repo-agent (a signed URL), or only through the bucket.
- **Repositories the bot can't push to:** the digest carries the diffs inline instead of a PR.
