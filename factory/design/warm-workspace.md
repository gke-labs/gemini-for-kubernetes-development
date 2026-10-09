# Warm workspaces: a sandbox's disk from a snapshot

**Status:** proposed.

Every sandbox starts on an empty workspace disk. A KCC fix sandbox first clones 4.9 GB, then downloads 2.2 GB of modules, then compiles 4.8 GB of build cache, all before its own change builds once. On 2026-10-08 the KCC fan-out children took 1h45m to 2h each to open their PRs. About 11 minutes of that was the model; the rest was tools, mostly whole-repo builds from those empty caches (`generate-types-and-mappers` 45m, `validate-generated-files` 29m, `make test` 52m).

This note gives a sandbox a workspace disk restored from a snapshot of a warmed one: the repository checked out, its modules downloaded, its build cache filled. Each sandbox gets its own copy of the snapshot (copy on write), so no write of one sandbox reaches another.

## Where things are today

- The workspace is a PVC from the Sandbox's `volumeClaimTemplates` (`workspaces-pvc`, `pkg/sandbox/manifests.go`), mounted at `/workspaces`. Its size and class come from `workspaceDiskSize` and `workspaceStorageClass`; KCC uses 40Gi `premium-rwo` (`pd.csi.storage.gke.io`, pd-ssd).
- The Sandbox CRD's claim template has `dataSource` and `dataSourceRef`, so a sandbox can ask for its disk from a `VolumeSnapshot` today, with no change to agent-sandbox.
- `lib.sh` keeps the Go caches on the disk: `GOPATH=/workspaces/.home/go` and `GOCACHE=/workspaces/.home/.cache/go-build`. A restored disk brings both.
- `setupGitRepos` already handles an existing checkout: it resets it, fetches and forks, instead of cloning.
- `checkoutNewBranch` (`fix_issue.sh`) branches from the checkout's HEAD. On a fresh clone that is the default branch now; on a restored disk it would be the snapshot's commit.
- `setupGit` writes the member's token into `.home/.config/gh/hosts.yml`, on the disk.
- The cluster has the snapshot CRDs (`snapshot.storage.k8s.io/v1`) but no `VolumeSnapshotClass`.

## Decisions

1. **A snapshot per repository, per namespace.** A `VolumeSnapshot` named `warm-<repo>-<yyyymmdd-hhmm>`, labelled with `factory.gemini.google.com/warm-workspace: <repo>`. A PVC's `dataSource` must be in its own namespace, so the snapshot lives where that repository's sandboxes do. That is one namespace (`overseer-kcc`) for an overseer.
2. **factory picks it up by label, no config.** When factory builds a sandbox for a repository, it lists that namespace's `VolumeSnapshot`s labelled for the repository. If one is `readyToUse`, it sets the newest as the claim's `dataSource`; otherwise it creates the empty disk, as today. The disk size is the larger of `workspaceDiskSize` and the snapshot's `restoreSize`. The overseer and repo-agent stay unchanged: they create sandboxes through factory as they do now.
3. **A warm sandbox makes the snapshot.** `factory workspace warm --repo <url>` does the whole run:
   - creates the sandbox `warm-<repo>` (empty disk, the repository's image and resources);
   - runs the warm task in it;
   - scales it to zero, so the disk is detached and quiet;
   - snapshots its PVC;
   - waits for `readyToUse`;
   - keeps the two newest snapshots and deletes the rest;
   - deletes the sandbox.

   It runs from a schedule (a CronJob running the factory image; daily for KCC) and by hand.
4. **The warm task holds no credentials.** It runs with no GitHub token and no engine. It does three things:
   - clones the repository anonymously (public repositories only, for now);
   - checks out the default branch;
   - runs the repository's warm commands: by default `go mod download` and `go build ./...` where there is a `go.mod`, and whatever the repository lists under `warm:` in factory config (KCC: `go vet ./...` and its codegen tools' builds).

   Before the snapshot it deletes `/workspaces/.tmp`, `/workspaces/tasks` and `/workspaces/spool`, and fails if `.home/.config/gh` or any git credential exists on the disk. A snapshot is shared by everyone whose sandbox restores it, so nothing personal may be in it.
5. **A restored checkout is brought up to date before any work.** `setupGitRepos` fetches upstream and resets the default branch to it on an existing checkout; `checkoutNewBranch` branches from upstream's default branch, never from HEAD. A sandbox from a snapshot works on today's code, never on the snapshot's.
6. **The snapshot is one class, created once.** A `VolumeSnapshotClass` named `warm-workspace` (`pd.csi.storage.gke.io`, `deletionPolicy: Delete`). Whether it takes snapshots or disk images (`snapshot-type: images`, which GCE meant for many disks from one source) is measured in step 1 and recorded here.

## What a restored sandbox saves, and what it does not

| | Today | From a snapshot |
|---|---|---|
| Clone | 4.9 GB | a fetch of a day's commits |
| Modules | 2.2 GB download | present |
| Build cache | empty | default branch as of the snapshot; a change rebuilds what it touches |
| Disk provisioning | an empty PD | a PD restored from the snapshot (lazy: first reads are slower) |

KCC's `generate-types-and-mappers` runs `go clean -cache` before generating CRDs, which throws the build cache away halfway through. That is KCC's script, to be raised upstream; until it changes, a KCC run keeps the clone and the modules, but not the build cache, past that point.

## Not in this design

- **Private repositories.** The warm task clones anonymously. A private repository needs a credential that never lands on the disk: a clone with a token passed through the environment and removed from the remote before the snapshot. Later.
- **One snapshot shared across namespaces.** repo-agent members each have a namespace, and a PVC cannot restore from another namespace's snapshot. A static `VolumeSnapshotContent` for each namespace, all pointing at the one GCE snapshot, would share it. Later, if member sandboxes need warm disks.
- **A remote build cache (`GOCACHEPROG`).** Considered and rejected: a cache every sandbox can write to lets one agent poison everyone's builds.

## Risks

- **Restore time.** A PD restored from a snapshot is created in seconds to a minute, but reads blocks lazily at first. A full clone takes minutes. Step 1 measures both on a 40Gi pd-ssd.
- **A stale or broken snapshot.** A snapshot more than three days old (the schedule stopped) is ignored with a warning, and the sandbox gets an empty disk. A sandbox restored from a broken snapshot is deleted and recreated empty.
- **Disk size.** A restore must be at least the snapshot's size. A repository whose `workspaceDiskSize` shrinks below its snapshot keeps the snapshot's size until the next warm.
- **Cost.** About 12 GB of snapshot per repository per namespace, two kept. Small next to the CPU of rebuilding it in every sandbox.

## Steps

1. **Measure by hand on staging** (no code):
   - create the `VolumeSnapshotClass`;
   - warm a KCC disk by hand and snapshot it;
   - restore a sandbox from it;
   - record here the restore time, the time to the first `go build`, and a fix run's tool time against 2026-10-08. Compare snapshot with image.
2. **factory: restore.** The label lookup in sandbox creation, `dataSource`, and `restoreSize`. Plus the up-to-date checkout of decision 5 (`setupGitRepos`, `checkoutNewBranch`), which is right with or without a snapshot.
3. **factory: warm.** `factory workspace warm`, the credential check, retention, `warm:` in factory config.
4. **The schedule.** A CronJob for KCC in `overseer-kcc`, its RBAC (sandboxes, PVCs, volume snapshots), and an example under `overseer/examples`.
5. **Verify** on the next KCC fan-out: time to PR and tool time against 2026-10-08.
