# Gemini Guide for Agentsandboxes

This package is designed to be the primary way for Gemini CLI and other agents to interact with sandboxed environments.

## Architecture

- `client.go`: Contains the core `Client` and `SandboxBuilder`. It wraps the Kubernetes dynamic client to interact with the `Sandbox` CRD from `sigs.k8s.io/agent-sandbox`.
- `cmd/agentsandboxes`: A Cobra-based CLI.
- `cmd/agentsandboxes-mcp`: An MCP (Model Context Protocol) server stub.

## Future Work

- Implement more methods in `Client` (e.g., `Exec`, `Logs`, `GetPod`).
- Enhance the MCP server with more tools and resources.
- Add unit tests for the client using a fake dynamic client.
- Improve the CLI with more formatting options and advanced configuration.

## Key Dependencies

- `sigs.k8s.io/agent-sandbox`: Provides the `Sandbox` CRD and API.
- `k8s.io/client-go`: Kubernetes client.
- `github.com/spf13/cobra`: CLI framework.
