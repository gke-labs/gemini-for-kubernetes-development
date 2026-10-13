# A branch per run

Status: proposed. It follows remove-envd.md step 4 (#1925), which moved a run's phases into the `run-plan`, `run-deploy` and `run-teardown` recipes and run.sh's functions into lib.sh. That step left where a run's files live alone. This note changes it.

## Today

Every run lives in one branch: `docs-exploration/agent-runs/<run>/` on `research/runs`, a branch of the repository on the member's fork. For a repository the member cannot fork, the branch lives only in the run sandbox's checkout (local-only). Most of lib.sh's run section exists because of two things about that layout.

**One shared branch.** Every run pushes to `research/runs`, so:
- runs race each other's pushes, and the finish step replays a rejected push onto the moved branch;
- moving runs between layouts needs an adoption pass (`adoptLegacyInstance`, `LEGACY_RUN_DIRS`) and the board reads every layout;
- removing a run deletes files from a branch that other runs' teardown scripts live on.

**The run's files are in the code checkout.** `research/runs` is a branch of the repository, so the code the run builds and the run's records share one worktree:
- the branch is rebased onto the default branch at every phase;
- to deploy a pull request, its head is laid over the worktree, and cleared again before every commit (`resolveTarget`, `clearTarget`);
- whatever the agent changed outside the run's directory has to be found and thrown away, and reported (`discarded` in result.json).

## Proposal

**One branch per run, `runs/<run>`, on the member's fork, holding only the run's files at its root:** `runbook.md`, `params.env`, `deploy.sh`, `teardown.sh`, the receipts, and `target.env` for a pull request run. There is no `docs-exploration/agent-runs/<run>/` prefix, and none of the repository's code.

**Every run branch starts from a shared root, `runs/root`:** one empty commit, created on the fork the first time a run needs it. A true orphan branch would work for storage, but GitHub refuses a pull request between branches with no common history. A shared root keeps a PR per run possible later (see below) at no cost now.

**In the sandbox, the run's files are a worktree of their own.** The run's branch is checked out with `git worktree add /workspaces/runs/<run> runs/<run>`. It is a worktree of the same clone, so it shares the clone's remotes and its fork setup. The code checkout, `/workspaces/<repo>`, stays where the phase needs it:
- **plan:** the default branch, or, with `--target`, the pull request's head, detached. The plan writes `target.env` (`TARGET_PR`, `TARGET_SHA`) in the run's worktree.
- **deploy, teardown:** `TARGET_SHA` when the run has a `target.env`, else the latest default branch.

The agent works in the code checkout, as now, and writes the run's files under `/workspaces/runs/<run>`. Its shell gets the path as `RUN_DIR` (agent-env), and the prompts name it instead of `docs-exploration/agent-runs/<run>/`.

**The steps.** `runs-remote` (fork or local-only) and `checkout-default-branch` are unchanged.
- **`run-prepare`** fetches `runs/<run>` from origin, or creates it from `runs/root` (creating the root if the fork has none), and adds the worktree. A plan from a runbook copies `.agents/runbooks/<name>` from the code checkout, or another run's files from `runs/<name>` (`git archive` or `git show`), into the worktree. A plan with a target pins it in `target.env`; any phase with a `target.env` checks the code out at `TARGET_SHA`.
- **`run-finish`** commits everything in the run's worktree and pushes `runs/<run>`. A local-only run doesn't push. The push guard stays: origin's push URL is unusable. Nothing outside the run's worktree can reach the commit, so there is nothing to clear or throw away. result.json still lists what the agent changed in the code checkout (`git status`), as a warning: it isn't kept.
- No rebase, and no replaying a rejected push: only this run writes its branch. A rejected push fails the phase loudly.

**What goes:** the shared-branch replay, `ensureRunsBranch`'s rebase onto the default branch, the PR overlay and `clearTarget`, the in-tree discarded-path accounting, and, once the migration below is done, `adoptLegacyInstance` and the legacy layouts.

**The board.**
- **List runs** with one call: the fork's `heads/runs/` refs (`git/matching-refs`), minus `runs/root`.
- **Read a run's files** at `runs/<run>`, at the branch root.
- **Remove a run** by deleting its branch. This keeps today's guard: a run whose receipts say something is deployed is offered teardown, not removal.
- **Local-only runs:** the sandbox's `git show runs/<run>:<file>` instead of `research/runs:docs-exploration/agent-runs/<run>/<file>`.
- `research/notes`, the research path's branch, is unaffected.

## Migration

Existing runs are directories on `research/runs`, some with live deployments whose `teardown.sh` must not be stranded. So this is an exception to iterating without compatibility:
- **Lazily, in `run-prepare`:** a run with no `runs/<run>` branch but a directory on `research/runs` (any layout) is seeded from it. Its files become the first commit on top of `runs/root`. The next phase of an old run, a teardown included, moves it.
- **The board** reads `research/runs` for a run that has no branch yet, and labels it as an old-layout run.
- **Once no run is left on `research/runs`,** the board's legacy read and the seeding go in one PR. `research/runs` itself is left on the forks. Deleting it is the member's call, since it holds the history.

## A PR per run, later

A PR per run is an addition, not part of this. It would open a draft PR in the fork, from `runs/<run>` into `runs/root`, whose diff is exactly the run's files. The plan review would happen on GitHub: inline comments, a re-plan answering them as care-comments does, and an approval as the gate. Receipts would land as commits, and teardown would close the PR. It is worth doing when someone other than the run's owner has to sign off on a plan, or the discussion belongs on the procedure's lines rather than in a free-text intent. Until then, the board is the gate.

## Steps

1. **repo-agent reads both layouts:** runs from `runs/*` branches, and old runs from `research/runs`. Removing a run removes whichever it is. Nothing writes branches yet, so this changes nothing visible.
2. **factory writes branches:** `run-prepare` and `run-finish` as above, the lazy seeding, `RUN_DIR`, and the prompts naming it. lib.sh loses the shared-branch, overlay and discard code.
3. **Later:** once no run is left on `research/runs`, delete the legacy read and the seeding.
