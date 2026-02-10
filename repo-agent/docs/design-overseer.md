# Overseer Design Options

This document outlines design options for the "Overseer" component in `repo-agent`.
The Overseer is responsible for orchestrating agents defined in `.agent/` folder of the repository.

## Requirements
1.  Watch for repository events (Issues, PRs, Comments).
2.  Read agent definitions from `.agent/*.md` in the repository.
3.  Match events to agents.
4.  Orchestrate execution (Plan -> Task -> Sandbox).

## Option 1: Extend `repowatch-controller` (The Integrated Approach)

In this option, we enhance the existing `repowatch-controller` to handle dynamic agent definitions.

### Architecture
*   **Component**: `repo-agent/cmd/repowatch-controller`
*   **Logic**:
    *   The `Reconcile` loop fetches `.agent/*.md` files using the GitHub client.
    *   It parses these definitions into in-memory structures.
    *   When processing Issues/PRs, it checks against these dynamic definitions in addition to the static CRD spec.
    *   It creates `Sandbox` and `SandboxTask` resources accordingly.

### Pros
*   **Simplicity**: Reuses existing GitHub client, authentication, and event loop.
*   **Efficiency**: No need to duplicate event fetching logic.
*   **Consistency**: Single source of truth for repository watching.

### Cons
*   **Complexity**: Increases the complexity of the already large `repowatch-controller`.
*   **Coupling**: Tightly couples infrastructure provisioning (Sandboxes) with agent orchestration logic.

## Option 2: Dedicated `overseer-controller` (The Decoupled Approach)

In this option, we create a new controller specifically for agent orchestration.

### Architecture
*   **Component**: `repo-agent/cmd/overseer`
*   **Logic**:
    *   Watches `RepoWatch` CRs to know which repositories to monitor.
    *   Implements its own polling/webhook handling for GitHub events (or shares a bus).
    *   Fetches and parses `.agent/*.md` files.
    *   Decides *what* needs to be done and creates `Sandbox` and `SandboxTask` CRs.
    *   `repowatch-controller` (or a thinner `sandbox-controller`) is responsible only for fulfilling the `Sandbox` CRs (spinning up Pods).

### Pros
*   **Separation of Concerns**: Infrastructure (Pods/Sandboxes) is separated from Business Logic (Agents/Workflows).
*   **Scalability**: Can scale independently.
*   **Extensibility**: Easier to add new event sources or logic without touching core infrastructure code.

### Cons
*   **Overhead**: Requires a new controller binary, deployment, and RBAC.
*   **Duplication**: Might duplicate some GitHub API calls if not careful (e.g., polling).

## Option 3: In-Sandbox Agent (The Distributed Approach)

In this option, the controller is dumb and just spins up a sandbox. The logic resides inside the sandbox.

### Architecture
*   **Component**: `repo-agent/pkg/agent` (running inside the sandbox)
*   **Logic**:
    *   `repowatch-controller` spins up a generic "Overseer Sandbox" for the repo.
    *   Inside this sandbox, a process monitors `.agent/` files and GitHub events.
    *   It executes agents locally or requests new Sandboxes via K8s API (if it has permission).

### Pros
*   **Isolation**: User code (agents) runs in a sandbox, reducing risk to the controller.
*   **Flexibility**: Agents can be complex scripts without burdening the K8s controller.

### Cons
*   **Resource Usage**: Requires a running pod for every repo just to watch.
*   **Latency**: Spin-up time for handling events might be higher if not persistent.
*   **Permissions**: The in-sandbox agent needs K8s API access to create other sandboxes.

## Recommendation

**Option 1 (Integrated)** is recommended for the initial implementation because:
1.  It builds directly on the existing working machinery.
2.  It avoids the operational overhead of a new controller.
3.  We can still structure the code internally (e.g., `pkg/overseer`) to allow for future extraction into Option 2.

## Agent Definition Format (Draft)

We propose a Markdown-based format for agent definitions, compatible with `gemini-cli` skills.

```markdown
---
name: "Bug Triager"
description: "Triages new bugs"
triggers:
  - type: issue
    action: opened
    labels: ["bug"]
---

# Instructions

You are a helpful assistant that triages bugs.
When a new bug is opened:
1. Check if it has a reproduction.
2. If not, ask for one.
3. If yes, try to reproduce it.
```
