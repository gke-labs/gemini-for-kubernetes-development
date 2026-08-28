# Factory Watch: Subcontroller Architecture & Asynchronous Orchestration

| Metadata | Details |
| :--- | :--- |
| **Status** | Implementable |
| **Author(s)** | Sam Dowell (`sdowell@google.com`) |
| **Created** | 2026-08-31 |
| **Last Updated** | 2026-08-31 |

This document proposes a decoupled, asynchronous subcontroller architecture for the `factory watch` command. It details the motivation, structural design, shared memory model, concurrency controls, lifecycle management, and a gradual implementation plan.

---

## 1. Overview & Goals

The `factory watch` daemon serves as the central background orchestration engine for the AI Factory. It continuously monitors GitHub repository activity (issues, pull requests, review comments, CI check failures) and scheduled chores, translating them into sandbox execution tasks executed via child `factory` CLI processes.

### Primary Objectives
* **Lower Pickup & Dispatch Latency**:
  * Reduce newly assigned/created issue pickup latency from **O(minutes-hours) down to O(seconds)**.
  * Reduce task scheduling latency from **O(minutes) down to sub-second** via in-memory dispatching and immediate enqueue signaling.
* **Decoupled Separation of Concerns**:
  * Break down the monolithic loop into **5 dedicated, autonomous subcontrollers** running in isolated goroutines coordinated by a shared context.
  * Enforce strict, uni-directional dependencies to prevent circular cross-controller calls.
* **Shared In-Memory State with Disk Write-Through**:
  * Maintain queue state, sandbox leases, and repository metadata in thread-safe memory primitives (`TaskQueueManager`, `SandboxLockRegistry`, `EntityStateCache`).
  * Ensure disk write-through persistence (`incoming/`, `processing/`, `processed/`, `journal.jsonl`) for crash recovery, CLI inspection, and observability.
* **Fault Isolation & Non-Blocking Execution**:
  * Prevent slow GitHub API endpoints (e.g., paginating PR comments or rate limit pauses) or slow Kubernetes API calls (sandbox pod reconciliation, cleanup) from blocking issue scanning or task execution.

---

## 2. Problem Statement & Motivation

### Current Monolithic Architecture
Currently, [`Watcher.Run()`](file:///usr/local/google/home/sdowell/code/gemini-for-kubernetes-development/factory/pkg/commands/watch/watch.go) executes a single sequential loop governed by a 10-second sleep timer:

```mermaid
flowchart TD
    Sleep["Sleep 10s"] --> PodCleanup["1. Delete Evicted Sandbox Pods (K8s API)"]
    PodCleanup --> Reconcile["2. Reconcile Running Sandboxes (K8s API / envd)"]
    Reconcile --> DrainCheck["3. Check Drain Mode ('isDoNotProcess')"]
    DrainCheck --> SlowPR{"4. Slow PR Cycle?<br/>(every 5m)"}
    
    SlowPR -->|Yes| FullPR["List All Open PRs<br/>Scan Slow Issues<br/>Process PRs (commits, CI, comments, reviews)<br/>Scan Chores<br/>Clean Closed PR/Issue Sandboxes<br/>Suspend Idle Sandboxes"]
    SlowPR -->|No| FastIssue{"5. Fast Issue Cycle?<br/>(every 30s)"}
    
    FullPR --> FastIssue
    FastIssue -->|Yes| IssueScan["Scan Bot & User Issues<br/>Process Fast PRs"]
    FastIssue -->|No| RunnerCheck{"6. Runner Cycle?<br/>(every 30s)"}
    
    IssueScan --> RunnerCheck
    RunnerCheck -->|Yes| RunTasks["Read incoming/ YAMLs from disk<br/>Sort tasks fairly<br/>Call K8s API per candidate (isSandboxRunning)<br/>Claim task & spawn subprocess"]
    RunnerCheck -->|No| Sleep
    RunTasks --> Sleep
```

### Core Bottlenecks
1. **Head-of-Line Blocking**:
   Evaluating pull requests requires numerous sequential GitHub REST calls (`ListAllCommits`, `ListAllIssueComments`, `ListAllReviews`, `ListAllReviewComments`, `ListAllCheckRuns`, `ListAllStatuses`). If a repository has several active PRs or GitHub experiences latency, PR processing can block the entire thread for 30–60 seconds, halting issue discovery and task dispatching.
2. **Two-Stage Dispatch Lag**:
   An issue created immediately after a fast scan cycle must wait up to 30s for the next scan cycle to be written to `incoming/`, and then another 30s for the runner loop to pick it up, resulting in **60s to 90s+ total delay**.
3. **Repetitive Disk I/O and YAML Deserialization**:
   Every 30-second runner cycle and every HTTP query to `/api/v1/queue` re-reads all files across `incoming/`, `processing/`, and `processed/` directories on disk, parsing each YAML file individually.
4. **Synchronous Kubernetes Status Probing**:
   Candidate queue tasks are verified for sandbox availability via synchronous Kubernetes API queries (`isSandboxTaskRunning`) inside the critical runner loop, causing dispatch delay to scale with queue size and cluster latency.

---

## 3. Proposed Architecture

The daemon will be rearchitected into **5 decoupled subcontroller goroutines** plus an HTTP server, running concurrently under a shared root context. The subcontrollers coordinate entirely through thread-safe in-memory primitives:

```mermaid
flowchart TB
    subgraph External["External Systems"]
        GH["GitHub REST API"]
        K8S["Kubernetes API / Envd"]
    end

    subgraph Controllers["Autonomous Subcontroller Goroutines"]
        ISC["Issue Scanner<br/>(Single Cycle: 30s-60s)"]
        PSC["PR Scanner<br/>(Single Cycle: 1m-2m<br/>+ Worker Pool)"]
        CSC["Chore Scheduler<br/>(Dedicated Cron Timers)"]
        TQD["Task Dispatcher<br/>(500ms Ticker + Event Wakeup)"]
        SGC["Sandbox Reconciler & GC<br/>(Ticker: 30s-60s)"]
        SRV["Queue HTTP Server<br/>(:13338 /api/v1/queue)"]
    end

    subgraph SharedMemory["Shared Thread-Safe In-Memory State"]
        TQM["TaskQueueManager<br/>- incoming / processing / processed maps<br/>- fair-share sorter, RWMutex<br/>- enqueue wakeup channel"]
        ESC["EntityStateCache<br/>- open PRs, referenced issues<br/>- processed state (SHAs, timestamps)<br/>- RWMutex"]
        SLR["SandboxLockRegistry<br/>- in-flight sandbox lease table<br/>- Mutex"]
    end

    subgraph Storage["Durable Disk Storage (Write-Through)"]
        FS_INC["queue/incoming/*.yaml"]
        FS_PROC["queue/processing/*.yaml"]
        FS_DONE["queue/processed/*.yaml"]
        FS_JRNL["queue/journal.jsonl"]
    end

    %% Ingestion Flow
    ISC -->|"1. Fetch open issues"| GH
    ISC -->|"2. Check referenced PRs O(1)"| ESC
    ISC -->|"3. Enqueue issue-fix"| TQM

    PSC -->|"1. Fetch PRs, reviews, CI"| GH
    PSC -->|"2. Update open PRs & metadata"| ESC
    PSC -->|"3. Enqueue pr-tasks"| TQM

    CSC -->|"1. Read .agents/ cron"| GH
    CSC -->|"2. Enqueue agent-chore"| TQM

    %% Persistence
    TQM -->|"Atomic write"| FS_INC

    %% Dispatcher Flow
    TQM -.->|"Instant Wakeup Signal"| TQD
    TQD -->|"1. Poll / wake on ready tasks"| TQM
    TQD -->|"2. Acquire sandbox lease"| SLR
    TQD -->|"3. Claim task (incoming to processing)"| TQM
    TQM -->|"Atomic rename"| FS_PROC

    TQD -->|"4. Spawn worker goroutine"| WKR["Worker Goroutine<br/>(factory CLI)"]
    WKR -->|"Executes in cluster sandbox"| K8S
    WKR -->|"5. Release lease"| SLR
    WKR -->|"6. Mark completed / failed"| TQM
    TQM -->|"Atomic rename"| FS_DONE
    TQM -->|"Append event"| FS_JRNL

    %% Reconciliation Flow
    SGC -->|"1. Check pod states & envd status"| K8S
    SGC -->|"2. Fast-path check open status"| ESC
    SGC -->|"3. Clean up closed sandboxes"| K8S

    %% HTTP API
    SRV -->|"In-memory RLock query"| TQM
```

---

## 4. Subcontroller Design & Separation of Concerns

To guarantee maintainability and eliminate circular dependencies, subcontrollers must have strictly defined boundaries and communicate only via shared memory primitives and Go channels.

### 1. `IssueScanner`
* **Cadence**: Single-ticker model running periodically (every **30–60 seconds**; the fast cycle is eliminated).
* **Responsibilities**:
  * Scans open issues assigned to bot accounts or created by the user login (automatically labeling and assigning newly created user issues) and issues carrying the trigger label (e.g., `factory`).
  * Filters out items that are pull requests (`item.PullRequestLinks != nil`).
  * Consults `EntityStateCache.IsIssueReferenced(num)` for instant O(1) in-memory checks rather than calling the GitHub Timeline API.
  * Consults `SandboxLockRegistry.IsBusy(sandboxName)` to avoid duplicate work if a sandbox is currently active.
  * Uses `TaskQueueManager.TaskExists(filename)` as the authoritative in-memory check before falling back to disk.
  * Enqueues `issue-fix` (or `agent-chore` if the issue specifies an `.agents/` workflow) directly into `TaskQueueManager`.
* **Decoupling Guarantee**: Does **not** invoke `PRScanner.ProcessPRs`. Pull requests returned in GitHub API issue queries are either passed to `PRScanner` via a non-blocking channel or skipped, allowing `PRScanner` to discover them independently.

### 2. `PRScanner`
* **Cadence**: Single-ticker model running periodically (every **1–2 minutes**; the fast cycle is eliminated).
* **Concurrency**: Evaluates candidate PRs concurrently using a bounded worker pool (3–5 workers) so that a slow PR does not block others.
* **Responsibilities**:
  * Performs a repository sweep across open PRs (evaluating PRs assigned to the bot pool, updated recently, or carrying the trigger label).
  * Updates `EntityStateCache` with the latest open PR list and referenced parent issue mappings.
  * Checks `SandboxLockRegistry.IsBusy(sandboxName)` to avoid queueing redundant work for active sandboxes.
  * Uses `TaskQueueManager.HasActivePRTask(num)` and `TaskQueueManager.TaskExists(filename)` as authoritative in-memory checks to prevent racy disk directory scans during task renames.
  * Evaluates PR state across four ordered phases:
    1. **Phase 1: Conflicts / Rebase**: Gated by mergeability (`pr-iterate`).
    2. **Phase 2: CI Gating & Investigation**: Evaluates check runs and commit statuses using `common.ListAllCheckRuns` and `common.ListAllStatuses` (`pr-investigate`), respecting retry circuit breakers.
    3. **Phase 3: Review Feedback & Comments**: Detects unaddressed review comments and human comments (`pr-comments`), managing acknowledgement reactions (`eyes`).
    4. **Phase 4: Automated Code Review**: Verifies reviewer bot eligibility (`pr-review`), strictly gated on passing and completed CI checks (`!hasPending` and `!hasFailure`).
  * **Deterministic Ready-for-Human Reconciler**: Gated on clean CI (`!hasFailure && !hasPending`), addressed comments, completed reviews, and no active tasks (`!hasActiveTask` via in-memory `TaskQueueManager.HasActivePRTask`). Unassigns bot accounts upon qualification.
  * Enqueues tasks directly into `TaskQueueManager`.
* **Decoupling Guarantee**: Does **not** scan issues or chores.

### 3. `ChoreScheduler`
* **Cadence**: Evaluates `.agents/` workflows on independent cron triggers or a dedicated 30-second interval.
* **Responsibilities**:
  * Parses schedules in `.agents/` workflow definitions from repository contents.
  * Tracks execution history in `chores_state.json` using atomic write-and-rename semantics.
  * Uses `TaskQueueManager.TaskExists(filename)` as primary in-memory deduplication.
  * Computes the next scheduled execution using `cron.Parse(schedule).Next(lastRun)`.
  * Directly enqueues `agent-chore` tasks into `TaskQueueManager` when triggers fire.
* **Decoupling Guarantee**: Completely independent of PR and issue scanning cycles.

### 4. `TaskDispatcher` (Core Execution Engine)
* **Cadence**: Dual trigger: **500ms ticker** OR **immediate wakeup** via `TaskQueueManager.notifyChan`.
* **Responsibilities**:
  * Evaluates drain state (`isDoNotProcess`) and active execution bounds (`MaxPending`, `MaxActions`).
  * Consults `TaskQueueManager.ClaimNextEligibleTask(predicate)`:
    ```go
    // Predicate MUST be a pure, non-blocking in-memory check.
    // NEVER perform network calls (GitHub/K8s) or call mutating TaskQueueManager methods inside predicate!
    isAvailable := func(filename string, task *QueueTask) bool {
        sandboxName := resolveSandboxNameFast(task.Type, task.Number)
        return !sandboxLocks.IsBusy(sandboxName)
    }
    ```
  * Task claim and lease acquisition flow:
    1. Atomically claims candidate task in memory (`incoming` $\rightarrow$ `processing`) and moves disk file (`incoming/` $\rightarrow$ `processing/`).
    2. Atomically acquires lease in `SandboxLockRegistry` via `sandboxLocks.TryAcquire(sandboxName, filename)`. If acquisition fails, reverts task back to `incoming` via `RequeueTask`.
    3. Performs post-claim validation outside the queue lock: checks stop labels (`overseer/stop`), closed state, and completed recovery status. If invalidated, marks completed/cancelled outside the lock without deadlocking.
  * Spawns an asynchronous worker goroutine executing the child `factory` CLI command.
  * Worker completion handling (success, failure, or timeout):
    1. Calls `TaskQueueManager.CompleteTask` or `TaskQueueManager.FailTask`.
    2. Atomically moves disk file from `processing/` $\rightarrow$ `processed/`.
    3. Appends structured entry to `journal.jsonl`.
    4. Releases lease in `SandboxLockRegistry` verifying task ownership: `sandboxLocks.Release(sandboxName, filename)`.

### 5. `SandboxReconciler`
* **Cadence**: Background ticker running every **30–60 seconds**.
* **Responsibilities**:
  * Proactively deletes evicted sandbox pods (`Phase == Failed`, `Reason == Evicted`) and increments eviction counts.
  * Reconciles running sandbox pods (checks container exit codes and `envd` status, updates annotations).
  * Cleans up sandboxes belonging to closed/merged PRs and closed issues (using `EntityStateCache` to fast-path open entities without GitHub API calls).
  * **Lease-Protected Operations**: Strictly verifies `!sandboxLocks.IsBusy(sandboxName)` before deleting closed PR/issue sandboxes, suspending idle sandboxes, or evicting stale sandboxes, preventing destruction of active worker environments.
  * Suspends idle sandboxes (`factorysandbox.SuspendIdleSandboxes`).
  * Harvests token usage via `usagereport.HarvestSandbox`.
* **Decoupling Benefit**: Removes all heavy cluster operations from the critical scanning and task dispatching paths.

### 6. `QueueServer` (HTTP API)
* **Port**: `:13338`
* **Responsibilities**:
  * Serves `GET /api/v1/queue` directly from in-memory state via `TaskQueueManager.GetQueueResponse()` under an `RLock()` in **< 1ms**.
  * Handles queue mutation endpoints (`DELETE /queue/:task`, `POST /queue/:task/priority`) by updating in-memory state and persisting to disk.

---

## 5. Shared Memory Model & Concurrency Primitives

### A. `TaskQueueManager` (In-Memory Queue with Write-Through Disk Persistence)

```go
type TaskQueueManager struct {
    mu          sync.RWMutex
    incoming    map[string]*QueueTask // Key: filename
    processing  map[string]*QueueTask
    processed   map[string]*QueueTask

    notifyChan  chan struct{} // Non-blocking notification to wakeup dispatcher immediately

    incomingDir      string
    processingDir    string
    processedDir     string
    processingLogDir string
    processedLogDir  string
    queueDir         string
    dryRun           bool
}
```

* **Thread-Safety**: All reads and writes to `incoming`, `processing`, and `processed` maps are guarded by `mu`.
* **In-Memory Authority**: Subcontrollers (`PRScanner`, `IssueScanner`, `ChoreScheduler`) MUST consult `queueMgr.TaskExists(filename)` and `queueMgr.HasActivePRTask(num)` rather than reading disk directories, eliminating race conditions during atomic file renames.
* **Atomic Write-Through**:
  * `Enqueue()`:
    1. Verifies deduplication in memory (`incoming` and `processing` maps).
    2. Writes task file atomically to `incomingDir` using temporary file + rename (`writeTaskAtomically`).
    3. Adds task to `incoming` map.
    4. Appends `Created` event to `journal.jsonl`.
    5. Non-blocking send to `notifyChan` to wake the dispatcher immediately.
  * `ClaimNextEligibleTask()`:
    1. Sorts `incoming` map in memory using fair-share prioritization (`sortTasksFairly`).
    2. Iterates over candidates and tests caller's availability predicate (`isAvailable(task)`).
    3. Predicate execution MUST NOT make network calls or call mutating `TaskQueueManager` methods.
    4. Moves file on disk from `incomingDir` $\rightarrow$ `processingDir`.
    5. Sets `task.Status = "Running"`, moves from `incoming` to `processing` map, records `Started` in journal.
  * `CompleteTask()` / `FailTask()`:
    1. Updates task status and completion timestamp in memory.
    2. Renames disk file from `processingDir` $\rightarrow$ `processedDir`.
    3. Moves corresponding `.log` file from `processingLogDir` $\rightarrow$ `processedLogDir`.
    4. Records `Completed` or `Failed` in `journal.jsonl`.
* **Recovery on Startup**: `LoadFromDisk()` populates in-memory maps from disk on daemon initialization.

### B. `SandboxLockRegistry` (In-Memory Leases)

```go
type SandboxLockRegistry struct {
    mu     sync.Mutex
    leases map[string]string // sandboxName -> taskFilename
}
```

* **Operations**:
  * `TryAcquire(sandboxName, taskFilename string) bool`: Atomically acquires lease if not already held. Returns false if already leased.
  * `Release(sandboxName, taskFilename string) bool`: Scoped release that only clears the lease if held by `taskFilename`, preventing lease hijacking if another worker exits.
  * `IsBusy(sandboxName string) bool`: O(1) check used by the dispatcher predicate and reconciler guards.

### C. `EntityStateCache` (Thread-Safe Metadata Cache)

```go
type EntityStateCache struct {
    mu               sync.RWMutex
    openPRs          []*githubv39.PullRequest
    referencedIssues map[int]bool
    processedIssues  map[int]time.Time
    processedPRs     map[int]prWatchState
    lastPRScan       time.Time
    lastIssueScan    time.Time
}
```

* **Purpose**:
  * Serves as the single authoritative thread-safe cache for PR and issue state across subcontrollers.
  * Eliminates unsynchronized local map writes in `PRScanner` and `IssueScanner`.
  * Allows `IssueScanner` to check `IsIssueReferenced(num)` in O(1) time without API calls.
  * Allows `SandboxReconciler` to fast-path open PR and open issue checks during sandbox GC.
  * Safely stores per-PR task gating state (`lastReviewedSHA`, `lastCommentAddressedSHA`, `lastInvestigatedSHA`).

---

## 6. Lifecycle, Concurrency & Shutdown

### Startup & Initialization Flow
1. **Load Config & Secret**: Initialize GitHub and Kubernetes clients, resolve target bot identities.
2. **Directory Verification**: Create `incoming`, `processing`, `processed`, and `logs` directories if they do not exist.
3. **Queue Rehydration**: `TaskQueueManager.LoadFromDisk()` seeds in-memory maps from existing YAMLs.
4. **Crash Recovery & Task Adoption**: Run `recoverStuckTasks(ctx)` to reconcile tasks found in `processing/`. Active pod runs are adopted by supervisor goroutines.
5. **Start HTTP Server**: Launch queue server listening on `:13338`.

### Startup Crash Recovery & Running Task Adoption

Because task execution inside cluster sandboxes is detached (`nohup ... &` via `envd`), a task may still be actively running inside a sandbox container even if the `factory watch` host process crashed or restarted. In the previous architecture, tasks found running during startup were simply left in `processing/` without any supervisor, causing them to become permanently orphaned.

The new architecture handles tasks in `processing/` across restarts using a deterministic three-way recovery flow:

```mermaid
sequenceDiagram
    participant Watcher as Watcher Startup (Recovery)
    participant TQM as TaskQueueManager
    participant SLR as SandboxLockRegistry
    participant Mon as Adoption Monitor Goroutine
    participant Pod as Cluster Sandbox (envd)

    Note over Watcher: Watcher restarts, finds task-*.yaml in processing/
    Watcher->>TQM: Load task into in-memory processing map
    Watcher->>SLR: TryAcquire(sandboxName, filename) (Reserve Lease)
    Watcher->>Pod: Probe isSandboxTaskRunning()
    
    alt State A: Pod already completed before restart
        Watcher->>TQM: CompleteTask() (move to processed/)
        Watcher->>SLR: Release(sandboxName)
    else State B: Pod failed / evicted / missing
        Watcher->>TQM: Re-queue to incoming/ with Recovered=true
        Watcher->>SLR: Release(sandboxName)
    else State C: Pod is still actively executing
        Watcher->>Mon: Spawn monitorAdoptedTask(ctx, task, sandboxName)
        Note over Watcher: Watcher continues starting subcontrollers...
        
        loop Poll Status (every 5-10s)
            Mon->>Pod: Check envd exit_code file / pod status
            Pod-->>Mon: Still running
        end
        
        Pod-->>Mon: Task finished! (Exit code 0 or != 0)
        Mon->>TQM: CompleteTask() / FailTask()
        Note over TQM: Moves processing/ -> processed/ on disk & memory
        Mon->>SLR: Release(sandboxName)
        Mon->>Mon: Resolve GitHub comment reactions & write journal
    end
```

#### Detailed Recovery States:
1. **Lease Re-Acquisition**: For every task found in `TaskQueueManager.processing`, the watcher immediately acquires the sandbox lease in `SandboxLockRegistry` (`sandboxLocks.TryAcquire(sandboxName, filename)`). This guarantees that neither the `TaskDispatcher` nor scanners can dispatch a conflicting task to that sandbox.
2. **Tri-State Evaluation**:
   * **State A (Already Finished)**: If `isSandboxTaskCompleted(sandboxName)` is true, the watcher immediately calls `TaskQueueManager.CompleteTask(filename, task)`, atomically moving the task file and logs to `processed/`, writing a journal event, and releasing the sandbox lease.
   * **State B (Terminated / Evicted)**: If the pod failed, was evicted, or does not exist, the pod is cleaned up, `task.Recovered = true` is set, and the task is moved back to `incoming/` via `TaskQueueManager.Enqueue` so the `TaskDispatcher` can re-schedule it. The lease is released.
   * **State C (Still Actively Running — Adoption)**: The task remains in `TaskQueueManager.processing`, the lease remains held in `SandboxLockRegistry`, and the daemon spawns an **Adoption Monitor Goroutine** (`go w.monitorAdoptedTask(ctx, filename, task, sandboxName)`).

#### Adoption Monitor Goroutine (`monitorAdoptedTask`):
* **Budget Tracking**: Computes remaining timeout from `task.EnqueuedAt + taskTimeout - time.Now()`.
* **Exit Code Polling**: Periodically (every 5–10 seconds) connects to `envd` and checks for `{taskDir}/exit_code`.
* **Post-Completion Execution**:
  * On exit code `0`: Updates PR comment reactions (`+1` for `pr-comments`) and calls `TaskQueueManager.CompleteTask()`.
  * On exit code `!= 0`: Updates PR comment reactions (`confused` for `pr-comments`) and calls `TaskQueueManager.FailTask()`.
  * Atomically renames YAML and `.log` files to `processed/`.
  * Appends structured entry to `journal.jsonl`.
  * Releases sandbox lease via `defer sandboxLocks.Release(sandboxName, filename)`.
* **Reconciliation Safety Net**: As a secondary safeguard, `SandboxReconciler` periodically checks all active tasks in `TaskQueueManager.processing`. If a sandbox task has completed according to annotations or `envd` but has no active monitor, `SandboxReconciler` automatically transitions the task to `processed/`.

### Subcontroller Coordination & Lifecycle Management
Subcontrollers run under a shared cancelable context (`daemonCtx`) with decoupled synchronization primitives to prevent `sync.WaitGroup` misuse:

* **Dual WaitGroup Model**:
  * `controllerWg sync.WaitGroup`: Tracks the 5 background subcontroller polling loops (`taskDispatcher`, `prScanner`, `issueScanner`, `sandboxReconciler`, `choreScheduler`).
  * `workerWg sync.WaitGroup`: Dedicated strictly to active child worker executions and adopted tasks.
  * *Rationale*: Mixing long-running controller loops and short-running tasks on a single WaitGroup causes `Wait()` to block indefinitely or triggers runtime panics if `Add()` is invoked concurrently with `Wait()`.

```go
func (w *Watcher) Run(ctx context.Context) error {
    if err := w.init(ctx); err != nil {
        return err
    }

    if w.Once {
        w.checkRepoOnce(ctx)
        w.workerWg.Wait()
        return nil
    }

    daemonCtx, daemonCancel := context.WithCancel(ctx)
    defer daemonCancel()

    // Launch subcontrollers under controllerWg
    w.controllerWg.Add(5)
    go func() { defer w.controllerWg.Done(); _ = w.taskDispatcher.Run(daemonCtx) }()
    go func() { defer w.controllerWg.Done(); _ = w.issueScanner.Run(daemonCtx) }()
    go func() { defer w.controllerWg.Done(); _ = w.prScanner.Run(daemonCtx) }()
    go func() { defer w.controllerWg.Done(); _ = w.choreScheduler.Run(daemonCtx) }()
    go func() { defer w.controllerWg.Done(); _ = w.sandboxReconciler.Run(daemonCtx) }()

    // Handle watch timeout
    if w.WatchTimeout > 0 {
        time.AfterFunc(w.WatchTimeout, func() {
            klog.Infof("Watch timeout of %s reached, initiating graceful shutdown", w.WatchTimeout)
            daemonCancel()
        })
    }

    // Wait for shutdown trigger
    <-daemonCtx.Done()

    // Stop subcontrollers and drain workers
    w.controllerWg.Wait()
    return w.waitForActiveWorkers(5 * time.Minute)
}
```

### Graceful Shutdown & Drain Handling
* When `ctx.Done()` is received or `WatchTimeout` expires:
  1. `daemonCancel()` cancels `daemonCtx`, signaling all 5 subcontrollers to exit their polling `select` loops cleanly.
  2. `controllerWg.Wait()` ensures all subcontrollers have stopped scheduling or claiming new tasks.
  3. `TaskDispatcher` stops claiming new tasks from `incoming`.
  4. Active worker goroutines tracked by `workerWg` are allowed to finish, with a 5-minute maximum drain timeout.
* When drain mode (`isDoNotProcess`) is active:
  1. The `TaskDispatcher` stops claiming new tasks.
  2. Active in-flight tasks continue running until completion.

### Concurrency Invariants & Safeguards

To prevent race conditions, deadlocks, and state divergence in this asynchronous design:

1. **Non-Reentrant, Non-Blocking Predicates**:
   `TaskQueueManager.ClaimNextEligibleTask(predicate)` holds the queue write lock (`m.mu.Lock()`). The `predicate` function MUST be a pure in-memory test (e.g. `!sandboxLocks.IsBusy(sbName)`). It MUST NEVER make network calls (GitHub, Kubernetes) and MUST NEVER call mutating `TaskQueueManager` methods (`RemoveTask`, `CompleteTask`) to avoid fatal self-deadlocks and thread starvation.
2. **Scoped Sandbox Leases with Task Ownership**:
   `SandboxLockRegistry.Release(sandboxName, taskFilename)` only releases a lease if `leases[sandboxName] == taskFilename`. This prevents a failing or timed-out worker from inadvertently wiping out a lease owned by another active worker.
3. **Atomic Claim & Lease Acquisition with Fallback Requeue**:
   When `TaskDispatcher` claims a task, it immediately calls `sandboxLocks.TryAcquire(sandboxName, filename)`. If `TryAcquire` fails (e.g., due to a concurrent lease acquisition), the task is immediately reverted back to `incoming` via `RequeueTask`, preventing dual execution on the same sandbox. Post-claim checks (stop labels, closed status) execute outside the lock.
4. **In-Memory Cache Authority**:
   Subcontrollers MUST consult in-memory state (`queueMgr.TaskExists`, `queueMgr.HasActivePRTask`, `entityCache`) as the primary authority before falling back to disk reads. This eliminates the race window where active tasks temporarily "disappear" during atomic disk renames (`incoming` $\rightarrow$ `processing`), preventing flapping `ready-for-human` labels and premature bot unassignment.
5. **Lease-Protected Cluster Reconciliation**:
   `SandboxReconciler` MUST consult `sandboxLocks.IsBusy(sandboxName)` before deleting closed PR/issue sandboxes, suspending idle sandboxes, or evicting stale sandboxes. This prevents destructive garbage collection while an active worker is executing against the sandbox.

---

## 7. Latency & Performance Comparison

| Metric / Scenario | Current Monolithic Architecture | Proposed Subcontroller Architecture |
| :--- | :--- | :--- |
| **New Issue Pickup Latency** | minutes - hours (sequential loop blocking) | **Up to 30–60 seconds** (dedicated single-cycle issue ticker) |
| **Task Dispatch Latency** | 0s – 30s after enqueue (waits for runner) | **< 500 milliseconds** (500ms ticker + channel wakeup) |
| **PR Evaluation Concurrency** | Serial loop (all PRs block one another) | **Concurrent (3–5 workers in PR scanner pool)** |
| **Chore Trigger Precision** | Delayed by up to 5 minutes | **Sub-second precision** |
| **HTTP Queue API Latency** | 50ms – 500ms+ (disk scans & YAML parsing) | **< 1 millisecond** (in-memory `RLock` read) |
| **Sandbox Concurrency Checks** | Repetitive K8s API calls per candidate | **O(1) in-memory lookups** (`SandboxLockRegistry`) |
| **Error Isolation** | Single API timeout pauses entire watch loop | **Isolated to offending subcontroller** |

---

## 8. Clean Code Principles & Design Structure

To avoid the code sprawl and tight coupling seen in initial refactoring attempts:

1. **No Circular Dependencies**:
   * `IssueScanner` never invokes `PRScanner`.
   * `PRScanner` never invokes `IssueScanner` or `ChoreScheduler`.
   * All coordination occurs via `TaskQueueManager` (enqueue), `EntityStateCache` (metadata queries), or Go channels.
2. **Configuration Structs instead of Constructor Parameter Explosion**:
   * Instead of passing 25+ positional parameters, define clean option structs:
     ```go
     type DispatcherConfig struct {
         MaxPending   int
         MaxActions   int
         TaskTimeout  time.Duration
         Image        string
         DiskSize     string
         CPURequest   string
         CPULimit     string
         MemoryRequest string
         MemoryLimit  string
         DryRun       bool
     }
     ```
3. **Preserve Existing Fixes on `main`**:
   * CI pending status gating (`!hasPending`) for `ready-for-human` and automated bot reviews.
   * State-driven bot unassignment upon `ready-for-human` qualification.
   * Active PR task check (`hasActivePRTask`).

---

## 9. Gradual 4-Phase Implementation Plan

### Phase 1: In-Memory Primitives & Infrastructure
* **Status**: Completed ([`queue_manager.go`](file:///usr/local/google/home/sdowell/code/gemini-for-kubernetes-development/factory/pkg/commands/watch/queue_manager.go), [`sandbox_lock_registry.go`](file:///usr/local/google/home/sdowell/code/gemini-for-kubernetes-development/factory/pkg/commands/watch/sandbox_lock_registry.go), [`entity_state_cache.go`](file:///usr/local/google/home/sdowell/code/gemini-for-kubernetes-development/factory/pkg/commands/watch/entity_state_cache.go))
* **Scope**:
  * Implement `TaskQueueManager` with atomic write-through, disk rehydration (`LoadFromDisk`), and fair-share in-memory sorting.
  * Implement `SandboxLockRegistry` for tracking in-flight sandbox leases.
  * Implement `EntityStateCache` for cached open PRs, referenced issues, and per-entity processed SHAs/timestamps.
* **Verification Gate**:
  * Comprehensive unit test suites ([`queue_manager_test.go`](file:///usr/local/google/home/sdowell/code/gemini-for-kubernetes-development/factory/pkg/commands/watch/queue_manager_test.go), [`sandbox_lock_registry_test.go`](file:///usr/local/google/home/sdowell/code/gemini-for-kubernetes-development/factory/pkg/commands/watch/sandbox_lock_registry_test.go), [`entity_state_cache_test.go`](file:///usr/local/google/home/sdowell/code/gemini-for-kubernetes-development/factory/pkg/commands/watch/entity_state_cache_test.go)) covering concurrent access, race detection (`go test -race`), write-through consistency, and crash recovery.

### Phase 2: In-Memory Task Dispatcher & HTTP Server Refactor
* **Status**: Completed ([`runner.go`](file:///usr/local/google/home/sdowell/code/gemini-for-kubernetes-development/factory/pkg/commands/watch/runner.go), [`server.go`](file:///usr/local/google/home/sdowell/code/gemini-for-kubernetes-development/factory/pkg/commands/watch/server.go))
* **Scope**:
  * Implement `TaskDispatcher` running a 500ms ticker + wakeup channel listener.
  * Refactor `server.go` to serve `/api/v1/queue` directly from `TaskQueueManager.GetQueueResponse()`.
  * Wire `TaskDispatcher` into `Watcher.Run()` under an asynchronous goroutine while keeping scanners temporarily in `checkRepo()`.
* **Verification Gate**:
  * Verify that enqueued tasks execute in <500ms.
  * Verify `server_test.go` and `queue_test.go` pass.

### Phase 3: Decouple Autonomous Subcontrollers
* **Status**: Completed ([`scan_issue.go`](file:///usr/local/google/home/sdowell/code/gemini-for-kubernetes-development/factory/pkg/commands/watch/scan_issue.go), [`scan_pr.go`](file:///usr/local/google/home/sdowell/code/gemini-for-kubernetes-development/factory/pkg/commands/watch/scan_pr.go), [`chores.go`](file:///usr/local/google/home/sdowell/code/gemini-for-kubernetes-development/factory/pkg/commands/watch/chores.go), [`sandbox.go`](file:///usr/local/google/home/sdowell/code/gemini-for-kubernetes-development/factory/pkg/commands/watch/sandbox.go))
* **Scope**:
  * Extract `ChoreScheduler` into `chores.go` as an independent goroutine.
  * Extract `SandboxReconciler` into `sandbox.go` as a background GC/reconciler goroutine (30s–60s).
  * Extract `IssueScanner` into `scan_issue.go` with a single ticker (30s–60s).
  * Extract `PRScanner` into `scan_pr.go` with a single ticker (1m–2m) and worker pool concurrency.
  * Wire all subcontrollers into `Watcher.Run()` via `errgroup.WithContext(ctx)`.
* **Verification Gate**:
  * Run all existing test suites: `scan_pr_test.go`, `scan_issue_test.go`, `github_helpers_test.go`, `sandbox_test.go`. Ensure recent CI gating and unassignment tests pass without regressions.

### Phase 4: Lifecycle, Recovery, and End-to-End Verification
* **Status**: Completed ([`watch.go`](file:///usr/local/google/home/sdowell/code/gemini-for-kubernetes-development/factory/pkg/commands/watch/watch.go), [`adoption_test.go`](file:///usr/local/google/home/sdowell/code/gemini-for-kubernetes-development/factory/pkg/commands/watch/adoption_test.go), [`concurrency_test.go`](file:///usr/local/google/home/sdowell/code/gemini-for-kubernetes-development/factory/pkg/commands/watch/concurrency_test.go))
* **Scope**:
  * Finalize graceful shutdown on context cancellation or `WatchTimeout`.
  * Validate drain mode (`isDoNotProcess`) behavior.
  * Verify startup recovery of interrupted tasks in `processing/`.
* **Verification Gate**:
  * Full repository test pass: `go test -race ./factory/pkg/commands/watch/...`.
  * End-to-end integration test verifying concurrent task enqueue and dispatch.
