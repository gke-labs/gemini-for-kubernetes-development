# Watch Scanning & Task Orchestration Design

This document details the design of the `factory watch` background loop, which serves as the core scanning and orchestration daemon of the AI Factory.

---

## 1. Overview & Goals

The watch loop provides continuous monitoring of a target GitHub repository, transforming external events (issues, comments, and CI failures) into isolated coding tasks running inside cluster sandboxes.

The key goals of the watch loop design are:
* **Decoupled Scanning and Execution**: Separating event discovery (scanning) from task runs using a filesystem-based queue.
* **Smart Filtering**: Minimizing unnecessary tasks and API rate limits by skipping duplicate work (e.g., issues that already have open PRs).
* **Multi-Identity Coordination**: Automatically routing task execution to specific bot user accounts based on role mappings and ownership requirements.

---

## 2. Watch Loop Architecture

The daemon executes periodically (default: every 2 minutes) and follows a three-stage pipeline:

```
+------------------+         +------------------+         +------------------+
|                  |         |                  |         |                  |
|  1. GitHub Scan  |-------->|  2. Queue Tasks  |-------->|  3. Run Sandbox  |
|                  |         |                  |         |                  |
+------------------+         +------------------+         +------------------+
 - List open issues           - Write to incoming/         - Move to processing/
 - List open PRs              - POSIX atomic move          - Exec child CLI cmd
 - Fetch CI status                                         - Move to processed/
```

---

## 3. Work Item Discovery Rules

The watcher uses the GitHub API to discover open issues and pull requests, applying specific filters based on labels, creators, and assignees.

### A. Issue Discovery (Bugs & Chores)
The watcher fetches open issues matching any of the following criteria:
1. **Assigned to Watcher**: Issues assigned to the watcher bot's login username.
2. **Labeled with Trigger Label**: Issues that have the trigger label (e.g., `factory`, `overseer`).
3. **Created by Watcher**: Open issues created by the watcher bot account itself.

*Deduplication Filters*:
* **PR Filter**: The watcher ignores issues that are actually Pull Requests (where `PullRequestLinks != nil`).
* **Linked PR Check**: The watcher searches all open PRs in the repository. If an open PR references the issue number in its title, description, branch name, or via the GitHub Timeline API, the issue is skipped to prevent duplicate work.

### B. Pull Request Discovery
The watcher fetches open Pull Requests matching:
1. **Assigned to Watcher**: Pull Requests assigned to the watcher bot.
2. **Labeled with Trigger Label**: Pull Requests labeled with the trigger label.

---

## 4. Task Dispatch Matrix

Once the watcher has consolidated the active issues and PRs, it performs check-status queries to map them to specific task types:

| Item Type | Condition | Task Type | Spawned Command | Identity Selection |
| :--- | :--- | :--- | :--- | :--- |
| **Issue** | Open issue assigned/labeled | `issue-fix` | `factory fix` | Random user from `coder` pool |
| **PR** | CI check runs failed | `pr-investigate` | `factory pr investigate` | Match PR author (must be in `coder` pool) |
| **PR** | New review comment or PR comment since last push | `pr-comments` | `factory pr address-comments` | Match PR author (must be in `coder` pool) |
| **PR** | Merge conflicts / out-of-date branch status | `pr-iterate` | `factory pr iterate --prompt "rebase"` | Match PR author (must be in `coder` pool) |
| **PR** | Opened by third-party bot on allowlist | `pr-review` | `factory pr review` | Random user from `reviewer` pool |
| **PR** | Markdown workflow checklist file reference | `agent-chore` | `factory agent create` | Default fallback or mapped role |

---

## 5. Queueing & Concurrency Control

To ensure robustness, the watcher operates on a directory-based queue (`incoming/`, `processing/`, `processed/`):

1. **Scan Mode (`--mode=scan`)**: Performs the queries, builds the task specifications, and writes them into `incoming/<task-id>.yaml`.
2. **Run Mode (`--mode=run`)**: Periodically reads `incoming/` directory. It selects the next task, moves it atomically to `processing/`, runs the corresponding `factory` CLI command, and writes the output logs to `processing/<task-id>.log`.
3. **Completion**: Once the CLI command exits:
   * If successful: The task file and logs are moved to `processed/`.
   * If failed: The task status is marked as `Failed` with the error trace, and moved to `processed/`.
