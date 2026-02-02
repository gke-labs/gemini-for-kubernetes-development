# Design Note: Support for Multiple Gemini Tokens and Rotation

## Context
Currently, `repo-agent` relies on a single Gemini API token (supplied via `GEMINI_API_KEY` environment variable or `/tokens/gemini` file). Heavy usage often leads to `429 Resource Exhausted` errors (Quota limits).

To mitigate this, we want to support multiple Gemini tokens and rotate between them.

## Current Architecture
*   **Token Source:** Tokens are read from Kubernetes Secrets (`gemini-vscode-tokens`) mounted at `/tokens/gemini` or passed as env vars.
*   **Consumption:**
    *   `pkg/llm/gemini.go`: Reads `/tokens/gemini` and sets `GEMINI_API_KEY` for the `gemini` CLI subprocess.
    *   `pkg/commands/geminikey.go`: Helper function `GetGeminiAPIKey` supports `exec:` prefix to execute a command to get the token. This is used by high-level commands (`github-fix-issue`, etc.).

## Design Options

We can categorize the solution into **Rotation Scope** (When do we switch?) and **Rotation Mechanism** (How do we get the next token?).

### 1. Rotation Scope

#### Option A: Sandbox-Level Rotation (Sticky)
*   **Description:** When a Repo Sandbox is created (pod startup), a token is selected from the available pool. This token remains assigned to the sandbox for its lifetime.
*   **Pros:**
    *   Simple implementation.
    *   Consistent identity/usage per sandbox.
*   **Cons:**
    *   If the selected token hits the quota, the entire sandbox becomes unusable until the quota resets or the pod is restarted.
    *   Does not effectively load balance short-term spikes if one token is "unlucky".

#### Option B: Task-Level Rotation
*   **Description:** A new token is selected for each distinct task (e.g., "Fix Issue", "Triage", "Code Review") or even per LLM request.
*   **Pros:**
    *   Better resilience. If a token is exhausted, the next operation can try a different one.
    *   Maximizes utilization of the aggregate quota.
*   **Cons:**
    *   More complex state management (need to access the pool at runtime).
    *   Potential context switching issues (though Gemini API is stateless, so this is minor).

### 2. Rotation Mechanism

#### Option 1: Multi-line Token File (Client-side Randomization)
*   **Description:** The secret mounted at `/tokens/gemini` contains multiple tokens, one per line.
*   **Implementation:**
    *   Update consumers (Go code, shell scripts) to read the file.
    *   If multiple lines exist, pick one randomly or round-robin.
*   **Pros:**
    *   No external dependencies.
    *   Easy to manage via K8s secrets.
*   **Cons:**
    *   Requires updating all call sites (Go code, bash scripts, etc.) to handle multi-line files.

#### Option 2: CLI Utility (`exec:` pattern)
*   **Description:** Leverage the existing `exec:` support. The `GEMINI_API_KEY` env var would be set to `exec: /usr/local/bin/get-token`.
*   **Implementation:**
    *   Implement a lightweight binary `get-token`.
    *   This binary reads the pool of tokens (e.g., from a mounted secret) and returns one.
    *   It can implement smart logic (random, round-robin, health-aware).
*   **Pros:**
    *   Decouples token selection logic from the main application code.
    *   Compatible with existing `exec:` support in `pkg/commands`.
    *   Can be extended easily (e.g., to fetch from a remote service later).
*   **Cons:**
    *   Requires ensuring `pkg/llm` and all scripts support the `exec:` pattern or rely on a wrapper.
    *   `pkg/llm/gemini.go` currently reads the file directly; it would need to be updated to shell out if the content starts with `exec:`.

#### Option 3: Central Token Service (HTTP Endpoint)
*   **Description:** A standalone service manages the token pool and quotas. Clients fetch tokens via HTTP.
*   **Pros:**
    *   Centralized rate limiting and monitoring.
    *   Can dynamically add/remove tokens without pod restarts.
*   **Cons:**
    *   Overhead of maintaining another service.
    *   Network dependency.

## Recommended Approach

**Phase 1: Task-Level Rotation with CLI Utility (Option B + Option 2)**

1.  **Token Storage:** Store multiple tokens in the Kubernetes Secret, mounted as a file (e.g., one per line or JSON).
2.  **Token Selector:** Create a small CLI tool (e.g., `gemini-token-rotator`) bundled in the image.
    *   Input: Path to token file.
    *   Logic: Reads file, picks a random token (or uses a seed for stickiness if needed).
    *   Output: The raw token string.
3.  **Integration:**
    *   Update `GEMINI_API_KEY` (or the file content) to `exec: gemini-token-rotator`.
    *   Update `pkg/llm/gemini.go` to respect the `exec:` prefix (it currently treats the file content as the key).
    *   Ensure shell scripts (`fix_issue.sh`, etc.) use the helper or `gemini-token-rotator` directly if they consume the key.

This approach offers the best balance of flexibility and simplicity. It allows moving to a Central Service later by just updating the `gemini-token-rotator` implementation without changing the main application code.

## Implementation Details for Phase 1

1.  **Modify `repo-agent/pkg/llm/gemini.go`**:
    *   Update `Setup()` to check if the content of `/tokens/gemini` starts with `exec:`.
    *   If yes, execute the command to get the actual key before setting `GEMINI_API_KEY` for the `gemini` subprocess, OR pass the `exec:` string if the `gemini` CLI supports it (unlikely, so we should resolve it).
    *   Actually, for `pkg/llm/gemini.go`, since it runs `gemini` CLI command, we might need to resolve the token *per request* or *at setup*. If we resolve at setup, it becomes Sticky (Sandbox level). To achieve Task Level, we might need to wrap the `gemini` command execution or resolve it just before calling `g.Executor.Run`.

2.  **Modify `repo-agent/pkg/commands/geminikey.go`**:
    *   It already supports `exec:`. Ensure it robustly handles the output of the rotation tool.

3.  **New Tool `cmd/token-rotator`**:
    *   Simple Go program.
    *   Reads `/tokens/gemini-pool` (or similar).
    *   Prints one token.

4.  **Configuration**:
    *   Update the K8s secret generation to populate the pool.
    *   Set `/tokens/gemini` to `exec: /path/to/token-rotator`.

## Security Considerations
*   Tokens are still stored in K8s Secrets.
*   The `exec:` command is run within the container context.
*   Ensure the rotator tool doesn't log tokens.
