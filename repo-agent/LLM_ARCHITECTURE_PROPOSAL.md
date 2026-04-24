# CLI-First LLM Provider Architecture (Claude Code)

## 1. Executive Summary
The Repo Agent is standardizing on a **CLI-First Architecture** for agentic tasks. While raw API providers remain available for stateless text generation, we delegate complex reasoning and "Action" loops to specialized LLM CLI binaries like `gemini` and Anthropic's `claude` (Claude Code).

For a detailed guide on implementing new providers, see the [LLM Provider Architecture & Integration Guide](docs/design/adding-a-new-llm-provider.md).

### 1.1 Rationale: Why not extend `gemini-cli`?
A common question is whether `gemini-cli` could be extended to support Claude models via Vertex AI. We have determined that a separate provider is necessary because:
*   **Incompatible Protocols**: Gemini and Claude use fundamentally different API schemas (e.g., Gemini's `contents` vs. Claude's `messages`). Even on Vertex AI, these models are exposed via different endpoints with distinct request/response structures.
*   **Specialized Reasoning Loops**: `gemini-cli` and `claude-cli` each contain unique, model-specific logic for tool use, terminal interaction, and "ReAct" loops. These are "black box" implementations tuned by their respective vendors for the specific behaviors of their models.
*   **Hardcoded Targets**: The `gemini-cli` binary is specifically built to target Google's Generative AI services. It does not have the internal routing logic to address the `publishers/anthropic` endpoints required for Claude on Vertex.
*   **Ecosystem Alignment**: Standardizing on each model's native CLI ensures we benefit from vendor-led innovation (like Claude's MCP support) without building complex translation layers.

---

## 2. Technical Details: Claude Code CLI (`claude-cli`)

### 2.1 Tooling & Runtime (DevContainer Feature Model)
*   **Delivery**: Instead of baking the binary into the `RepoSandboxImage`, we use **DevContainer Features**.
*   **Package**: `@anthropic-ai/claude-code` (via npm).
*   **Feature**: `ghcr.io/gke-labs/gemini-for-kubernetes-development/claude-code:latest`.
*   **Benefit**: This allows the sandbox to remain lightweight and allows the same binary to be injected into any base image supported by `envbuilder`.

### 2.2 Integration Mechanics
A new `ClaudeCLI` struct will implement the `Provider` interface in `pkg/llm/claude-cli.go`. The existing `Claude` struct in `pkg/llm/claude.go` will remain untouched.

*   **Non-Interactive Mode**: Claude Code will be executed using the `execute` command with a "yes" flag to prevent hanging on user prompts.
    *   *Draft Command*: `claude execute "task description" --yes`.
*   **Prompt Passing**: Tasks will be passed as the primary argument to the `execute` command.
*   **Usage Tracking**: We will attempt to parse Claude's output for token consumption to populate the `Stats` object.

### 2.3 Authentication (Secret-to-Env Pattern)
The `claude-cli` provider will follow the project's established pattern for secure credential injection:

*   **Secret Management**: The `anthropic-api-key` Kubernetes secret (managed by `installer.sh`) contains the API key under the `claude` key.
*   **Volume Mount**: The Sandbox controller mounts this secret into the agent container at `/tokens/`.
*   **Translation**: The `Setup()` method in `pkg/llm/claude-cli.go` will:
    1. Read the raw key from `/tokens/claude`.
    2. Set the `ANTHROPIC_API_KEY` environment variable.
    3. Ensure any CLI-specific config files (e.g., in `~/.claude/`) are initialized using this key.
*   **Non-Interactivity**: `Setup()` will also handle any one-time "consent" or "telemetry" flags required by the CLI to ensure subsequent `Run()` calls are fully automated.

---

## 3. Implementation Plan

### Phase 1: Sandbox Image Update
*   **Files**: `images/generic-golang/Dockerfile`, `images/dind-golang/Dockerfile`.
*   **Action**: Install Node.js, npm, and the `@anthropic-ai/claude-code` global package.
*   **Goal**: Ensure `claude` is available in the sandbox `$PATH`.

### Phase 2: Add `pkg/llm/claude-cli.go`
*   **New Struct**: `ClaudeCLI` using `CommandExecutor`.
*   **New Provider Registration**: Update `NewLLMProvider` in `pkg/llm/provider.go` to recognize the `claude-cli` name.
*   **Methods**:
    *   `Setup()`: Validates `ANTHROPIC_API_KEY`.
    *   `Run()`: Executes `claude execute ...`.

### Phase 3: Extension Support (MCP)
*   Claude Code uses the **Model Context Protocol (MCP)**.
*   **Action**: Update the provider to generate or link an `mcp_servers.json` configuration based on the `Extensions` provided in the config.

---

## 4. Lessons Learned & Operational Considerations

During the initial rollout and cluster re-initialization, several critical dependencies were identified:

### 4.1 Dependency on Registry-Based Features
The system has moved away from local `Makefile` builds for CLI binaries. It now relies on `ghcr.io` (or a local registry like `kind.local`) to host DevContainer Features. 
*   **Lesson**: If the `devcontainer.json` references a feature that isn't accessible, the sandbox will hang in the `envbuilder` phase.
*   **Fix**: Ensure `configmap-devcontainer.yaml` is updated to include the `claude-code` feature.

### 4.2 Secret Bootstrapping
The `repowatch-controller` and the sandboxes are highly sensitive to the presence of valid secrets (`anthropic-api-key`, `github-pat`).
*   **Lesson**: Re-creating the cluster without sourcing environment variables leads to `401 Bad Credentials` errors in the controller.
*   **Fix**: The `make create-secrets` target must be run with a fully populated environment to ensure the controller can authenticate with GitHub and the LLM providers can authenticate during `Setup()`.

### 4.3 Sandbox Defaulting
The transition to `envbuilder` means that sandboxes often start from a generic base image (e.g., `base:ubuntu`).
*   **Lesson**: Binaries like `claude` or `gemini` MUST be injected via the feature mechanism or the `inject-agent` init container.
*   **Fix**: Standardize on DevContainer features for all external LLM CLIs to ensure consistency across different user-provided base images.

---

## 5. Provider Comparison

| Feature | `gemini-cli` | `claude` (API) | `claude-cli` |
| :--- | :--- | :--- | :--- |
| **Logic** | Binary CLI | Raw HTTP API | Binary CLI |
| **Agentic** | Yes (ReAct) | No (Stateless) | Yes (ReAct) |
| **Primary Use** | Sandboxed Tasks | Simple Reviews | Sandboxed Tasks |
| **Runtime** | Go (bundled) | Go (native) | Node.js |
