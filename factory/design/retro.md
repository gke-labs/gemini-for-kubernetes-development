# A weekly retro: outcomes, review memory and skills

Status: proposed.

The agents' pull requests get merged, changed by hand, or closed. Today none of that flows back into the agents. When a fix goes wrong, we find out why by hand, then fix the environment or write the missing repository knowledge down. The KCC envtest mismatch went that way: #1975, KCC #14022 and #14023 all came from one person reading failed runs. This note turns that into a loop: record what happened to every PR, review it every week with a recipe, and deliver the lessons as changes that a human approves. The lessons are lines in `AGENTS.md`, skills, a review checklist, recipe tweaks, and factory or environment bugs.

Training a model of our own is out of scope. We have hundreds of PRs with noisy labels, and most of what goes wrong is environment and missing repository knowledge, not the model's intelligence. If the outcome records below ever reach thousands, that question can be asked again with data.

## Today

- **What a run cost is recorded; what it achieved isn't.** The token-usage collector (`design/token-usage-collection.md`, `factory/pkg/tokenusage`) keeps one record per task, attributed to its issue, PR or workflow, and harvested again before a closed PR's sandbox is deleted. Nothing records whether the PR was merged, how much a human had to change, or why it was closed.
- **The version of what ran is mostly lost.** A run keeps its recipe's source (`recipe.source.yaml`, design/repo-recipes.md), but nothing ties the PR to the engine, the model, or the commit of `AGENTS.md` and the skills the agent read.
- **Review comments are read once.** Care answers a reviewer's comment on the PR in front of it. When the same comment comes up on the next PR ("use `direct.`", "don't touch generated files", "release-note block required"), the agent starts from nothing again.
- **Scheduled work exists.** The chores scheduler runs `.agents/*` definitions with a `schedule:` (`@weekly` for `gemini-model-updates`) as `agent create`. Repositories can carry their own recipes in `.agents/recipes/`.

## Proposal

### 1. An outcome record per PR

When a PR the agents opened is merged or closed, factory writes one **outcome record** to the collector, beside the usage records:

```json
{
  "key": "outcome:owner/name#101",
  "repo": "owner/name", "pr": 101, "issues": [42],
  "state": "merged | closed",
  "openedAt": "...", "closedAt": "...", "closedBy": "login",
  "runs": [{"recipe": "fix", "recipeHash": "sha256:…", "engine": "gemini", "model": "gemini-3-pro",
            "knowledgeCommit": "<default-branch sha of AGENTS.md + skills at task start>"}],
  "carePasses": 3, "reviewComments": 7, "changesRequested": 1,
  "agentHead": "<sha of the agent's last push>",
  "humanEdit": {"files": 2, "added": 14, "deleted": 3},
  "ciRedAfterPush": 1,
  "requiresHuman": false,
  "closeReason": "one line from the closer, if they gave one"
}
```

- **Where:** the closed-PR sweep that already harvests usage before deleting the sandbox (`watch/sandbox/reconciler.go`) also writes the outcome. A PR merged after its sandbox is gone is caught by the overseer's closed-PR scan.
- **`humanEdit`:** how much the PR's change moved between the agent's last push and the final head. The two changes are compared each against its own merge base, so a rebase onto a moved base branch doesn't count. It shows how close the agent got.
- **`runs`:** each task already knows its recipe, engine and model. `knowledgeCommit` is the new field, recorded at task start. Without these fields, a change in acceptance can't be traced to the change that caused it.
- **Cost per merged PR** comes for free: the collector joins outcomes with the PR's usage records. That number is the one to compare models and recipes by, not benchmarks.

The collector stays the only store (JSONL on its PVC, last line wins). `GET /v1/outcomes?repo=&since=` returns the records and `GET /v1/outcomes/rollup?repo=&since=` the aggregates.

### 2. Asking why a PR was closed

When a bot PR is closed unmerged, the overseer comments once:

> Closing? One line on why helps the agents learn, e.g. "wrong approach", "duplicate", "not needed".

The first reply from the closer, or from a member, within 7 days becomes `closeReason` (the outcome record is rewritten). The comment is optional and never asked twice. It is the richest signal we have and today we collect none of it.

### 3. Review memory

Each repository keeps a checklist of what its reviewers keep asking for: **`.agents/review-checklist.md`**, short bullet points, each with a link to a PR where it came up.

- **fix** and **care** put it in the agent's context, like `AGENTS.md`.
- **fix** gets a **self-review step before push**: the agent checks its diff against the checklist, then fixes what it finds. One ask, on the session it already has.
- **review** gets the checklist as instructions, so the automated review asks what the humans would have asked.

The checklist is plain text in the repository, reviewed and merged like code. Only the retro (below) proposes additions, and a human approves them.

### 4. The retro recipe

A built-in recipe, **`retro`**, on a repository (`on: [repo]`), run weekly per overseer:

- **Inputs:** a window (`since`, default 7 days), and the outcome records and rollup for it.
- **The agent reads**, for every PR closed in the window that was closed unmerged, needed 3 or more care passes, or had human edits above a threshold:
  - the PR, its review threads and its `closeReason`;
  - the human edits (`git diff agentHead..<final head>`);
  - the session transcript, when the sandbox is still there (`spec.sessionURL`, #1971).
- **It classifies** each PR: wrong approach, ignored a repository convention, missing tests, scope creep, environment or tooling, bad issue, or fine.
- **It proposes concrete changes**, at most a handful, each tied to the PRs behind it:
  - a line in `AGENTS.md` / `GEMINI.md`, or a skill;
  - an entry in `.agents/review-checklist.md`;
  - a recipe tweak, as a factory issue;
  - an environment or factory bug, as an issue.

It writes two results:
- **a `Digest`** (a new task-output kind): the week's metrics (merged, closed, cost per merged PR, care passes, top failure classes, compared with the week before) and the proposals. The new `open-issue` action posts it as an issue on the repository, labelled `<trigger>/retro`.
- **a `Change`**: the proposed edits to `AGENTS.md`, skills and the checklist, on a branch. The existing `open-pr` action opens it as a PR that links the digest.

Nothing is applied automatically. A human merges the PR or doesn't, and closes the issue. The next retro reads the previous digest and reports which of its proposals were merged. A rejected proposal is not made again unless new PRs back it.

The retro runs with `credentials: clone`, like review: it reads, and the actions post. Its model can be a cheap one.

**Scheduling:** the chores scheduler learns to run a recipe, not just an agent: a chore definition with `recipe: retro` (and `inputs:`) and `schedule: "@weekly"`. Until watch-rules (design/watch-rules.md) has a schedule source, that is the one place schedules live. A repository can carry its own retro in `.agents/recipes/retro.yaml` if the built-in doesn't suit it.

### 5. Evaluating a change before making it (later)

Once proposals start changing prompts and skills, we need to know whether they helped:
- **A replay set:** about 30 past KCC issues with known merged fixes. A candidate (model, recipe, skills) runs on them in sandboxes. It is scored on build and tests passing, CI, closeness to the merged fix, and an LLM judge calibrated against the outcome records.
- **Live A/B:** engine and model are already per board and per overseer. Route a slice of issues to a variant and compare acceptance and cost per merged PR from the outcome records.

This is a separate design when we get there. Steps 1–4 don't depend on it.

## Steps

1. **Outcome records:** the record, `knowledgeCommit` on tasks, writing it from the closed-PR sweep, and the collector's endpoints. factory only.
2. **Why closed:** the overseer's comment and the reply harvest.
3. **Review memory:** fix, care and review read `.agents/review-checklist.md`; fix's self-review step. Seed KCC's checklist by hand from its recent reviews.
4. **The retro recipe:** the `Digest` kind and the `open-issue` action, chores that run a recipe, and the recipe. Run it by hand on KCC first (`factory recipe retro --url <repo>`), then weekly on overseer-kcc.
5. **Later:** the replay set and A/B (§5), and the Sessions/Overseer pages showing the rollup.

## Open questions

- **Which PRs count as the agents'?** Proposed: the PRs the overseer or a board opened (author is a bot identity, or the PR body has the factory session link). PRs a member opened and the agent only cared for are recorded with `runs` but left out of acceptance rates.
- **Who answers "why closed?"** Only the closer and members, so a drive-by reply can't steer the retro.
- **Where the retro's PR goes** for a repository the bot can't push to: the digest issue carries the diffs inline instead.
