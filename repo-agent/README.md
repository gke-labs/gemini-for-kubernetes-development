# Gemini Code Repo Agent

This project provides a Kubernetes-based framework for running Gemini agents that can review code, create, and review issues in Github. It is designed to be deployed on a Kubernetes cluster and uses custom controllers to manage its operations.

The key components of the framework include:
*   **Repo Watch Controller**: Monitors GitHub repositories for new pull requests and other events.
*   **Review Sandbox**: Spins up isolated environments to perform automated code reviews using a Gemini agent.
*   **Issue Sandbox**: Provides a similar sandboxed environment for creating and managing GitHub issues.
*   **Review UI and API**: A web-based interface and backend service to visualize and interact with the review process.
*   **Configdir API**: API analogous to configmap used to preserve directory structure and load multiple files to be projected in a volume using a sidecar.

## Prerequisites

Before you begin, ensure you have the following tools installed:

| Tool                                                              | Description                                       |
| ----------------------------------------------------------------- | ------------------------------------------------- |
| [Gemini API Key](https://aistudio.google.com)                     | Required to authenticate with the Gemini API.     |
| [GitHub Personal Access Token](https://github.com/settings/tokens) | Required to interact with the GitHub API.         |
| [KinD](https://kind.sigs.k8s.io/)                                 | A tool for running local Kubernetes clusters.     |
| [kubectl](https://kubernetes.io/docs/tasks/tools/)                | The Kubernetes command-line tool.                 |
| [Helm](https://helm.sh/docs/intro/install/)                       | The package manager for Kubernetes.               |

## Quick start

For installing the latest release follow the [Quick Start Guide](docs/quick-start.md)

## Installing from source-code

Please follow the [Development Guide](docs/development.md)

## Using repo-agent

Please follow the [Usage Guide](docs/usage.md) to understand how to create `repoagent` CRDs.

## Development

*   **[Adding a New LLM Provider](docs/adding-a-new-llm-provider.md)**: A guide for extending the agent to support other Large Language Models.
*   **[Architecture](docs/architecture.md)**: High-level overview of the system components and data flow.
*   **[Multi-Tenant Architecture](docs/tenancy.md)**: A guide to understanding the multi-tenant architecture, isolation, and security model.
