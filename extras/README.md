# Extras

Smaller projects that live in this repository but are independent of [factory](../factory/), [overseer](../overseer/) and [repo-agent](../repo-agent/). Nothing in those three imports them.

| Path | What it is |
|---|---|
| [`gemini-extension/`](gemini-extension/) | A Gemini CLI extension for `kubernetes/kubernetes` development: skills and `/dv:*` commands for declarative validation authoring and review, and SIG API Machinery issue and PR triage. |
| [`devcontainer-features/`](devcontainer-features/) | Dev container features that install Gemini CLI, Claude Code and Antigravity, published to `ghcr.io/gke-labs/gemini-for-kubernetes-development/<feature>` by the `release-devcontainer-features` workflow. |
| [`agentsandboxes/`](agentsandboxes/) | A Go client, CLI and MCP server for managing [agent-sandbox](https://github.com/kubernetes-sigs/agent-sandbox) sandboxes. |
| [`container-registry/`](container-registry/) | A lightweight, transient OCI registry for sandboxes to push images and run them in the same cluster. |

The two Go modules (`agentsandboxes`, `container-registry`) are still listed in the top-level `go.work`.
