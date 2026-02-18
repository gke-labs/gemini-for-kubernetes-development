# Agent Sandboxes

This package provides a Go client and tooling for managing agent sandboxes based on [agent-sandbox](https://github.com/kubernetes-sigs/agent-sandbox).

## Components

- **Go Client**: A fluent API for creating and managing sandboxes in Kubernetes.
- **CLI Tool (`agentsandboxes`)**: A command-line interface for manual management.
- **MCP Server (`agentsandboxes-mcp`)**: A Model Context Protocol server for use by LLM agents.

## Usage (Go Client)

```go
import "github.com/gke-labs/gemini-for-kubernetes-development/agentsandboxes"

// Create a new sandbox
sandbox, err := agentsandboxes.New("my-sandbox").
    Image("gcr.io/my-project/my-agent:latest").
    Env("DEBUG", "true").
    Create(ctx)

// List sandboxes
sandboxes, err := agentsandboxes.List(ctx)
```

## CLI Usage

```bash
# List sandboxes
agentsandboxes list

# Create a sandbox
agentsandboxes create my-sandbox --image my-image
```
