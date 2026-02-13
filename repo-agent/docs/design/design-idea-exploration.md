# Design Doc: Idea Exploration & Iterative Development

## 1. Motivation

Developers often need to explore multiple approaches to a problem before settling on a final solution. For example, when refactoring a database layer, one might want to prototype an approach using an ORM (Approach A) and another using raw SQL (Approach B). 

Currently, the `DevSandbox` (`IssueSandbox` with `type=dev`) allows creating ad-hoc development environments, but they are treated as flat, independent entities. There is no built-in way to:
1. Group multiple sandboxes/branches under a single "Idea" or "Feature".
2. Easily switch contexts between these approaches without losing state.
3. Compare approaches side-by-side.

This design proposes a system to support **Idea Exploration**, allowing users to manage multiple iterations of a concept, refining them independently, and eventually promoting a winner to a Pull Request.

## 2. Goals

*   **Logical Grouping**: Ability to group multiple development sandboxes and git branches under a single "Idea" entity.
*   **State Persistence**: Ensure work is saved to Git branches so sandboxes can be ephemeral (scaled to 0) without data loss.
*   **Context Switching**: Allow users to easily switch between "Approach A" and "Approach B".
*   **Minimal Architectural Change**: Leverage the existing `RepoWatch` and `IssueSandbox` CRDs as much as possible.

## 3. Architecture Options

### Option 1: The Metadata Layer (Recommended)

This approach uses **Kubernetes Labels** and **Git Branch Naming Conventions** to create a logical layer on top of the existing infrastructure.

*   **Idea**: A logical grouping defined by a unique ID (e.g., `idea-refactor-auth`).
*   **Approach**: A standard `DevSandbox` backed by a specific git branch (e.g., `ideas/refactor-auth/approach-a`).
*   **Mechanism**: The `IssueSandbox` CRs are tagged with specific labels. The UI queries these labels to present a grouped view.

**Pros:**
*   Zero changes to CRD schemas (`RepoWatch`, `IssueSandbox`).
*   Fully compatible with existing controllers.
*   Git-native: The state exists in the repo even if the cluster is destroyed.
*   Parallelism: Multiple approaches can run simultaneously (quota permitting).

**Cons:**
*   Requires strict adherence to naming conventions.
*   "Idea" state (e.g., description) is not strictly stored in K8s (unless we use a ConfigMap or relying on the common labels).

### Option 2: The Multi-Worktree Sandbox

This approach uses a single `DevSandbox` pod that manages multiple "approaches" internally using `git worktree`.

*   **Idea**: Maps 1:1 to a `DevSandbox`.
*   **Approach**: A directory inside `/workspaces/` (e.g., `/workspaces/approach-a`, `/workspaces/approach-b`) backed by `git worktree`.
*   **Mechanism**: The Agent logic is updated to support context switching commands (e.g., `/idea switch approach-b`).

**Pros:**
*   Fast switching (no pod startup latency).
*   Shared resources (one pod quota).

**Cons:**
*   High complexity in the Agent/Sandbox logic.
*   Risk of cross-contamination (shared env vars, processes).
*   Harder to integrate with standard VS Code remote (needs custom path mapping).

### Option 3: The "Idea" CRD

Formalize the concept by introducing a new Custom Resource Definition.

```yaml
kind: Idea
spec:
  title: "Refactor Auth"
  approaches:
    - name: "v1"
      sandboxRef: "..."
```

**Pros:**
*   Strong typing and validation.
*   Explicit lifecycle management (delete Idea -> delete all Sandboxes).

**Cons:**
*   High engineering effort (new Controller, new CRD).
*   Overkill for a UI grouping feature.

## 4. Detailed Design: The Metadata Layer

We will proceed with **Option 1** as it offers the best balance of feature set and implementation velocity.

### 4.1. Data Model

#### Git Branch Convention
Branches for explorations will follow a strict hierarchy:
`ideas/<idea-slug>/<approach-name>`

Example:
*   `ideas/optimize-startup/lazy-load`
*   `ideas/optimize-startup/binary-strip`

#### Kubernetes Labels
We will introduce standard labels on the `IssueSandbox` (DevSandbox) resources:

| Label Key | Description | Example |
| :--- | :--- | :--- |
| `repo-agent.gemini.google.com/idea-id` | Unique slug for the idea group | `optimize-startup` |
| `repo-agent.gemini.google.com/approach` | Name of the specific variation | `lazy-load` |
| `repo-agent.gemini.google.com/base-branch`| The parent branch (optional) | `main` |

### 4.2. Workflow

#### 1. Creating an Idea
*   **User Action**: Clicks "New Exploration" in the UI.
*   **Input**: Title ("Optimize Startup"), Base Branch ("main").
*   **System**:
    *   Generates `idea-id` (slug): `optimize-startup`.
    *   (Optional) Creates a "metadata" branch or ConfigMap to store the Idea's description, though implicitly it just starts empty.

#### 2. Creating an Approach
*   **User Action**: Clicks "Add Approach" inside the Idea view.
*   **Input**: Name ("Lazy Loading").
*   **System**:
    *   Determines Branch Name: `ideas/optimize-startup/lazy-loading`.
    *   Calls API `POST /api/repo/:repo/dev` with metadata.
    *   **API Logic**:
        *   Creates `IssueSandbox` CR.
        *   Applies Labels: `idea-id=optimize-startup`, `approach=lazy-loading`.
        *   Sets `spec.destination.branch` to the calculated branch name.

#### 3. Working & Switching
*   **UI**: Groups all `DevSandbox` items by their `idea-id` label.
*   **Parallel**: If the user has quota, they can have "Approach A" and "Approach B" running simultaneously (2 pods).
*   **Sequential**: If limited, the user "Pauses" (scales to 0) Approach A and "Resumes" (scales to 1) Approach B. The state is preserved in the Git branch `ideas/...`.

#### 4. Promoting
*   **User Action**: Clicks "Create PR" on "Approach A".
*   **System**: Opens a GitHub PR from `ideas/optimize-startup/lazy-loading` to `main`.

### 4.3. API Changes

Update `POST /api/repo/:repo/dev` to accept optional metadata fields.

**Request Payload:**
```json
{
  "branch": "optional-custom-branch",
  "prompt": "Try using lazy loading...",
  "ideaID": "optimize-startup",       // New
  "approach": "lazy-loading"          // New
}
```

**Server Logic (`handlers_dev.go`):**
If `ideaID` and `approach` are present:
1.  Construct `branchName` automatically: `ideas/<ideaID>/<approach>`.
2.  Add Labels to the `DevSandboxOptions`:
    *   `repo-agent.gemini.google.com/idea-id`: `<ideaID>`
    *   `repo-agent.gemini.google.com/approach`: `<approach>`

### 4.4. UI Changes

**Dashboard**:
*   New Section: "Explorations" (distinct from flat "Dev Sandboxes").
*   Display logic:
    *   Fetch all `type=dev` sandboxes.
    *   Group by `idea-id` label.
    *   Items without `idea-id` go to "Misc / Scratchpad".

**Exploration Card**:
```text
┌──────────────────────────────────────────────────────────────┐
│  Idea: Optimize Startup                                      │
│  Base: main                                                  │
│                                                              │
│  [ + Add Approach ]                                          │
│                                                              │
│  • Lazy Loading (Active)            [ Open ] [ Stop ]        │
│    Branch: ideas/optimize/lazy                               │
│                                                              │
│  • Binary Stripping (Paused)        [ Resume ] [ Delete ]    │
│    Branch: ideas/optimize/strip                              │
└──────────────────────────────────────────────────────────────┘
```

## 5. Future Extensions

*   **Diff View**: Add a feature to show the diff between two approaches (e.g., `git diff ideas/opt/a...ideas/opt/b`).
*   **Merge Approaches**: Use the Agent to "Combine the best parts of Approach A and Approach B into a new Approach C".