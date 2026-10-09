# Warm workspaces: a sandbox's disk from a snapshot

**Status:** proposed.

Every sandbox starts on an empty workspace disk. A KCC fix sandbox first clones 4.9 GB, then downloads 2.2 GB of modules, then compiles 4.8 GB of build cache, all before its own change builds once. On 2026-10-08 the KCC fan-out children took 1h45m to 2h each to open their PRs. About 11 minutes of that was the model; the rest was tools, mostly whole-repo builds from those empty caches (`generate-types-and-mappers` 45m, `validate-generated-files` 29m, `make test` 52m).

On 2026-10-09, `generate-types-and-mappers` was timed in a sandbox of the same size (2 CPUs requested): 1943s from an empty cache, 953s from a full one ([KCC #13869](https://github.com/GoogleCloudPlatform/k8s-config-connector/pull/13869), which also drops the script's `go clean -cache`).

This note gives a sandbox a workspace disk restored from a snapshot of a warmed one: the repository checked out, its modules downloaded, its build cache filled. Each sandbox gets its own copy of the snapshot (copy on write), so no write of one sandbox reaches another.

## Where things are today

- **The disk.** The workspace is a PVC from the Sandbox's `volumeClaimTemplates` (`workspaces-pvc`, `pkg/sandbox/manifests.go`), mounted at `/workspaces`. Its size and class come from `workspaceDiskSize` and `workspaceStorageClass`; KCC uses 40Gi `premium-rwo` (`pd.csi.storage.gke.io`, pd-ssd).
- **Restoring needs no agent-sandbox change.** The Sandbox CRD's claim template already has `dataSource` and `dataSourceRef`, so a sandbox can ask for its disk from a `VolumeSnapshot`.
- **The caches live on the disk.** `lib.sh` puts them there: `GOPATH=/workspaces/.home/go` and `GOCACHE=/workspaces/.home/.cache/go-build`. A restored disk brings both.
- **Config reaches factory through env.** The Overseer controller passes its CR's fields to the overseer sandbox as env vars (`overseer/pkg/overseer/overseer.go`). The sandbox's `run.sh` writes them into `/workspaces/.factory.cfg` and then runs `factory watch` for one `POLL_INTERVAL` (5m), in a loop.
- **Watch passes call into `commands` through hooks.** `factory watch` runs passes as goroutines (`pkg/commands/watch/subcontrollers.go`). A pass that needs a recipe gets a hook from `commands`, as the fan-out controller gets `ProposeFanout`, which calls `runRecipe` (`watch_fanout_propose.go`).
- **A recipe run is resumable.** `runRecipe` with a run name that already exists waits for that run instead of starting another. A cancelled context stops the waiting, not the task.
- **What `run:` steps see.** Inputs arrive as `INPUT_<NAME>` env vars, never as shell source. The GitHub tokens are stripped from a `run:` step's env (`pkg/recipe/sandbox.go`).
- **`credentials: clone` keeps the token off the disk.** `cloneRepo` (`lib.sh`) clones into `/workspaces/<repo>` with a one-shot credential helper, and leaves `origin` as the clone URL with no credentials.
- **The fix recipe is safe on an old checkout; classic `fix_issue.sh` is not.**
  - The recipe resets to upstream's default branch (`checkout-default-branch`, `checkoutDefaultBranch` in `lib.sh`).
  - Classic `fix_issue.sh`, which the overseer's fix tasks run, goes `setupGitRepos` → `checkoutNewBranch`, branching from HEAD. On a restored disk, HEAD is the snapshot's commit.
- **Snapshots aren't set up.** The cluster has the snapshot CRDs (`snapshot.storage.k8s.io/v1`) but no `VolumeSnapshotClass`.

## End to end

```
Overseer CR spec.warmWorkspace {interval, keep, script}
  └─ overseer.go: env WARM_WORKSPACE_INTERVAL, WARM_WORKSPACE_KEEP, WARM_WORKSPACE_SCRIPT
       └─ run.sh writeFactoryConfig: script → /workspaces/warm-workspace.sh;
          .factory.cfg gets warmWorkspace: {interval, keep, script: /workspaces/warm-workspace.sh}
            └─ factory watch: warmworkspace pass (each cycle)
                 └─ hook commands.warmWorkspace(repoURL)
                      ├─ EnsureWarmSandbox warm-<repo> (never restored from a snapshot)
                      ├─ runRecipe("warm", repoURL, run name from the sandbox, inputs {script})
                      │    └─ in the sandbox: clone → run script → clean → credential check
                      ├─ suspend warm-<repo> (replicas 0) → VolumeSnapshot of its PVC
                      └─ readyToUse → keep the newest N → delete warm-<repo>

any later sandbox for the repo (pkg/sandbox manifests)
  └─ newest readyToUse snapshot labelled for the repo, same image → volumeClaimTemplates dataSource
       └─ fix_issue.sh: setupGitRepos → checkoutDefaultBranch → checkoutNewBranch
```

### 1. The Overseer CR

```yaml
spec:
  warmWorkspace:
    interval: 24h      # unset: no warming, as today
    keep: 2
    script: |
      go mod download
      dev/tools/controllerbuilder/generate-proto.sh   # .build/googleapis-<sha>.pb
      go build ./...
      go test -run '^$' ./pkg/... ./apis/...           # compile the tests, run none
```

`overseer_types.go` gets `WarmWorkspace *WarmWorkspaceSpec`. The field is additive, so Overseers without it are unchanged. As with every field, changing it restarts the overseer sandbox.

### 2. Overseer to factory

- `overseer.go` sets `WARM_WORKSPACE_INTERVAL`, `WARM_WORKSPACE_KEEP` and `WARM_WORKSPACE_SCRIPT`. A multi-line env value is fine; the script is a few lines.
- `run.sh`'s `writeFactoryConfig` writes the script to `/workspaces/warm-workspace.sh` and adds this to `.factory.cfg`:
  ```yaml
  warmWorkspace:
    interval: 24h
    keep: 2
    script: /workspaces/warm-workspace.sh
  ```
- `FactoryConfig` gets a matching `WarmWorkspace` struct.
- A later fallback: with no `script`, read `.agents/warm.sh` from the repository's default branch, never from a PR branch.

### 3. The watch pass

`pkg/commands/watch/warmworkspace` is a controller like `fanouts`. It is built only when `warmWorkspace.interval` is set, and paused while the overseer drains. A watch process lives one `POLL_INTERVAL`, so the pass keeps no state in memory: its state is the cluster's.

- A `readyToUse` snapshot labelled for the repository and younger than `interval` exists: nothing to do.
- Otherwise it calls the hook, which picks up a warm already under way: the sandbox `warm-<repo>` and its run name are its state.

The hook `commands.warmWorkspace`, set on the watcher next to `ProposeFanout`, does four things:

1. **Ensure the sandbox.** `EnsureWarmSandbox` (new, `pkg/sandbox`) creates `warm-<repo>`:
   - labelled `factory.gemini.google.com/warm-workspace: <repo>`;
   - the repository's image, resources and storage class;
   - no user secret mounted;
   - **never** a `dataSource`.

   The first time, it records a run name on the sandbox (`factory.gemini.google.com/warm-run: warm-<unix>`).
2. **Run the recipe.** `runRecipe("warm", repoURL, <that run name>, inputs {script: <file contents>})`. It needs a way to be handed the sandbox, since a repository target would otherwise get a research sandbox. A watch cycle that ends mid-run leaves the task running; the next cycle calls again with the same run name and waits on.
3. **Snapshot.** When the run succeeds:
   - suspend the sandbox (`SuspendSandbox`, replicas 0) and wait for the pod to go, so the disk is detached and quiet;
   - create a `VolumeSnapshot` `warm-<repo>-<yyyymmdd-hhmm>` of its PVC, labelled for the repository and annotated with the default branch's SHA, the image digest and the Go version;
   - record it on the sandbox (`warm-snapshot`).

   A later cycle waits for `readyToUse`, deletes all but the newest `keep`, and deletes the sandbox.
4. **On failure.** The sandbox stays, suspended and annotated `warm-failed: <time>`, so its task logs can be read (`factory sandbox task logs warm-<repo>`). The pass tries again one `interval` after the failure, on a new sandbox.

### 4. The recipe

A built-in recipe, `pkg/recipe/recipes/warm.yaml`. Its steps are what make the disk safe to share, so a repository can't edit them; only the script is the repository's.

```yaml
name: warm
on: [repo]
credentials: clone       # the token reaches cloneRepo and nothing else
inputs:
  script: {required: true}
start:
  steps:
    - uses: clone
    - run: |
        cd "/workspaces/$INPUT_REPO_NAME"
        git checkout -q "$(git symbolic-ref --short refs/remotes/origin/HEAD | cut -d/ -f2)"
        printf '%s\n' "$INPUT_SCRIPT" > "$TASK_DIR/warm.sh"
        bash -euo pipefail "$TASK_DIR/warm.sh"
        test -z "$(git status --porcelain)"   # a dirty tree would be in every fix
    - run: |
        rm -rf /workspaces/.tmp /workspaces/spool
        ! test -e /workspaces/.home/.config/gh
        ! git -C "/workspaces/$INPUT_REPO_NAME" config --get-regexp 'credential|url\..*insteadof'
        ! grep -rIlE 'gh[pousr]_[A-Za-z0-9]{20,}|AIza[0-9A-Za-z_-]{20,}' /workspaces/.home --exclude-dir=go --exclude-dir=go-build
```

There is no `ask`, no engine and no task output. The recipe's own task directory, under `/workspaces/tasks`, is deleted by the hook after the run and before the snapshot.

### 5. The restore

- **Which snapshot.** Where `pkg/sandbox` builds the claim template, both builders look in the namespace for the newest `VolumeSnapshot` that:
  - is labelled for the repository;
  - is `readyToUse`;
  - was made with the sandbox's image digest. That avoids a cache full of misses after an image bump.
- **The claim.** If one matches, the claim gets `dataSource: {apiGroup: snapshot.storage.k8s.io, kind: VolumeSnapshot, name: …}`. Its size is the larger of `workspaceDiskSize` and the snapshot's `restoreSize`.
- **No snapshot.** The disk is empty, as today.
- **No changes elsewhere.** No caller of the sandbox builders changes. Neither does repo-agent, nor the overseer's task flow.
- **The checkout.** `fix_issue.sh` calls `checkoutDefaultBranch` between `setupGitRepos` and `checkoutNewBranch`, so a branch starts from upstream's default branch as it is now. That is right with or without a snapshot. The recipes already do it.

### 6. Cluster objects

- **The class.** One `VolumeSnapshotClass` `warm-workspace` (`pd.csi.storage.gke.io`, `deletionPolicy: Delete`). It is cluster-scoped and created once. Step 1 measures `snapshot-type: snapshots` against `images` and records which one to use.
- **RBAC.** The overseer sandbox's role (`overseer/k8s/sandbox-rbac.yaml`) gets `volumesnapshots`: get, list, create, delete.

## What a restored sandbox saves, and what it does not

| | Today | From a snapshot |
|---|---|---|
| Clone | 4.9 GB | a fetch of a day's commits |
| Modules | 2.2 GB download | present |
| Build cache | empty | the default branch as of the snapshot; a change rebuilds what it touches |
| Repository tool output (KCC `.build/`, `bin/`) | built | present (git-ignored, so `git clean -fd` keeps it) |
| Disk provisioning | an empty PD | a PD restored from the snapshot (lazy: first reads are slower) |

Until #13869 merges, KCC's `generate-types-and-mappers` runs `go clean -cache`. That throws the build cache away halfway through, so KCC's warm script leaves it out.

## Not in this design

- **Repository recipes** (`.agents/recipes/`). warm is built in. Recipes defined by a repository need their own design, for trust above all (a recipe can ask for `credentials: full`).
- **One snapshot shared across namespaces.** repo-agent members each have a namespace, and a PVC cannot restore from another namespace's snapshot. A static `VolumeSnapshotContent` per namespace, all pointing at the one GCE snapshot, would share it. Later, if member sandboxes need warm disks.
- **A remote build cache (`GOCACHEPROG`).** Considered and rejected: a cache every sandbox can write to lets one agent poison everyone's builds.

## Risks

- **Restore time.** A PD restored from a snapshot is created in seconds to a minute, but reads blocks lazily at first. Step 1 measures this on a 40Gi pd-ssd.
- **A stale or broken snapshot.**
  - A snapshot older than three `interval`s (warming stopped) is ignored with a warning.
  - A broken snapshot breaks every sandbox restored from it. Deleting it puts sandboxes back on empty disks.
- **Disk size.** A restore must be at least the snapshot's size.
- **Cost.**
  - Storage: about 12–15 GB of snapshot per repository per namespace, `keep` of them.
  - Compute: one warm sandbox per `interval`.

## Steps

1. **Measure by hand on staging** (no code):
   - create the `VolumeSnapshotClass`;
   - warm a KCC disk by hand and snapshot it;
   - restore a sandbox from it;
   - record here the restore time, the time to the first `go build`, and a fix run's tool time against 2026-10-08. Compare snapshots with images.
2. **factory: restore.**
   - the snapshot lookup and `dataSource` in the sandbox builders;
   - `checkoutDefaultBranch` in `fix_issue.sh`.
3. **factory: warm.**
   - the `warm` recipe;
   - `EnsureWarmSandbox`;
   - the hook;
   - the `warmworkspace` pass;
   - `warmWorkspace` in `FactoryConfig`.

   Runnable by hand before the overseer passes anything: `.factory.cfg` with `warmWorkspace`, then `factory watch`.
4. **overseer.**
   - `spec.warmWorkspace`;
   - its env in `overseer.go`;
   - `run.sh`'s config lines;
   - the RBAC;
   - `overseer/examples/kcc.yaml`.
5. **Verify** on the next KCC fan-out: time to PR and tool time against 2026-10-08.
