# LLM Provider Architecture & Integration Guide

This document describes how the Gemini Code Repo Agent integrates with Large Language Models (LLMs). It covers the high-level "CLI-First" architecture, the dynamic sandbox environment powered by DevContainers, and a step-by-step guide for adding new providers.

---

## 1. Architectural Strategy: CLI-First

The Repo Agent follows a **CLI-First Architecture** for agentic tasks. While raw HTTP APIs are suitable for stateless text generation, complex software engineering tasks (like multi-step code reviews or bug fixing) require specialized "action loops."

### 1.1 Why CLIs?
Instead of building complex "ReAct" loops or tool-use frameworks in Go, we delegate these behaviors to model-specific CLI binaries like `gemini` (Gemini CLI) and `claude` (Claude Code).
*   **Optimized Reasoning**: Vendor-provided CLIs are tuned for the specific strengths of their models (e.g., Claude's reasoning loop for terminal interaction).
*   **Tool-Use Capabilities**: These CLIs handle local tool execution (file reads, shell commands, git operations) natively.
*   **Reduced Complexity**: The Repo Agent core remains a lightweight orchestrator, while the model logic stays encapsulated within the binary.

### 1.2 Interactive vs. Non-Interactive
In the Repo Agent, these CLIs are executed in **non-interactive mode** within an ephemeral sandbox.
*   **Gemini**: Uses `gemini -y --output-format json`.
*   **Claude**: Uses `claude --print --output-format json`.

---

## 2. Sandbox Environment & CLI Delivery

A critical challenge in the Repo Agent architecture is delivering model-specific CLI binaries (like `claude` or `gemini`) into the ephemeral sandbox where the task executes. The system supports two primary delivery models: **Dynamic Injection** and **Pre-baked "Fat" Images**.

### 2.1 Dynamic Injection (DevContainer Features)
This model leverages `envbuilder` to construct a development environment at runtime.
*   **Mechanism**: The sandbox starts from a generic base image (e.g., `ubuntu`). `envbuilder` reads a `devcontainer.json` and dynamically downloads/installs "Features" (modular scripts and binaries) from a remote registry.
*   **Pros**: Highly flexible and modular. Allows users to "mix and match" tools without rebuilding images.
*   **Cons**: High runtime latency. The sandbox must clone the repo and install tools (often taking 2-5 minutes) before the agent can start. It also introduces a runtime dependency on external registries.

### 2.2 Pre-baked "Fat" Images (Preferred Approach)
The **preferred approach** for production and performance-sensitive environments is to use a pre-baked, self-contained image (e.g., `images/generic-golang/Dockerfile`).

*   **Mechanism**: The LLM CLI and all its dependencies (like Node.js for Claude) are installed during the Docker build phase:
    ```dockerfile
    RUN npm install -g @anthropic-ai/claude-code
    ```
*   **Performance Benefit**: By eliminating the `envbuilder` installation phase, the sandbox enters the "Ready" state in **seconds rather than minutes**. All tools are immediately available in the `$PATH`.
*   **Reliability**: The sandbox is entirely self-contained. It does not depend on dynamic network requests to a Feature registry at runtime, making it more robust against network flakes or registry outages.
*   **Configuration**: To use this model, specify the `image` field directly in the `RepoWatch` manifest and omit the `devcontainerConfigRef`:
    ```yaml
    review:
      image: ghcr.io/your-org/generic-golang:latest
      llm:
        provider: claude-cli
    ```

### 2.3 Injection Logic Summary
When the `RepoWatch` controller reconciles a task:
1.  If **`spec.review.image`** is set: It uses the specified image and executes `repo-sandbox dev-daemon`. This daemon handles cloning and task execution using the binaries already present in the image.
2.  If **`spec.review.devcontainerConfigRef`** is set (and no image is specified): It defaults to the `envbuilder` workflow to dynamically assemble the environment.

---

## 3. The `Provider` Interface

All LLM logic in the Go codebase is abstracted behind the `Provider` interface in `pkg/llm/provider.go`.

```go
type Provider interface {
	Setup() error
	Cleanup() error
	ExpandPrompt(prompt string) (string, error)
	Run(prompt string) ([]byte, *Stats, error)
	AddPostProcessor(p PostProcessor)
	QuotaCheck() bool
}
```

### Key Methods
*   **`Setup()`**: Handles authentication. It reads API keys from a mounted secret volume (usually `/tokens/`) and sets the required environment variables (e.g., `ANTHROPIC_API_KEY`).
*   **`Run(prompt)`**: Executes the CLI binary. It **MUST** request JSON output to enable programmatic parsing of results and token usage.
*   **`ExpandPrompt()`**: Allows for provider-specific prompt transformations (e.g., expanding custom command macros).

---

## 4. Case Study: `claude-cli`

The `claude-cli` provider (implemented in `pkg/llm/claude-cli.go`) illustrates the standard integration pattern.

### 4.1 Implementation Mechanics
1.  **Binary**: `@anthropic-ai/claude-code` (via npm).
2.  **Execution**: It runs `claude --print --output-format json "prompt"`.
3.  **Parsing**: It expects a JSON envelope:
    ```json
    {
      "result": "...",
      "modelUsage": {
        "claude-3-7-sonnet": {
          "inputTokens": 123,
          "outputTokens": 456
        }
      }
    }
    ```
4.  **Stats Mapping**: The `modelUsage` is mapped to the internal `llm.Stats` struct for observability and usage tracking.

---

## 5. Adding a New Provider (Developer Guide)

### Step 1: Implement the Logic
Create `pkg/llm/my-provider.go`. Implement the `Provider` interface. If it's a CLI provider, use the `CommandExecutor` to run the binary.

### Step 2: Register the Provider
Update the factory function in `pkg/llm/provider.go`:
```go
case "my-provider":
    return &MyProvider{...}, nil
```

### Step 3: Update API Types
Add the new provider to the `LLMConfig` enum in `api/repowatch/v1alpha1/repowatch_types.go`:
```go
// +kubebuilder:validation:Enum=gemini-cli;claude;claude-cli;my-provider
Provider string `json:"provider,omitempty"`
```
Then run `make manifests` to regenerate the CRD YAMLs.

### Step 4: Infrastructure (DevContainer)
1.  Ensure a DevContainer Feature image exists for your provider (or that it's pre-installed in the base image).
2.  Update the default `devcontainer-json` ConfigMap in `k8s/configmap-devcontainer.yaml` to include the feature.

### Step 5: Secrets
Update the `Makefile` and `review-api` (if necessary) to handle the provisioning and copying of the required API key secrets.

---

## 6. Common Pitfalls & FAQs

**Q: Should I use the raw API or the CLI?**
**A**: If the task requires "doing" things in the repo (editing files, running tests), use the CLI. If it's just a simple text transformation, the raw API is faster.

**Q: How do I handle non-JSON output?**
**A**: Many CLIs output progress bars or warnings to stdout. Your parser should find the first `{` character to locate the start of the JSON response.

**Q: What about authentication?**
**A**: Always use the "Secret-to-Env" pattern. Mount the secret to `/tokens/`, read it in `Setup()`, and set it as an environment variable. Never hardcode keys.
