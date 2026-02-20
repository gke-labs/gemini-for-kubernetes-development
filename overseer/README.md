# Overseer

Overseer is an autonomous agent responsible for orchestrating other agents and managing the state of a repository in a Kubernetes-based agentic system.

## Components

- `cmd/overseer-cli`: A CLI tool used by the Overseer agent to manage sandboxes and tasks.
- `pkg/overseer`: Go package for reconciling Overseer sandboxes.
- `images/overseer`: Dockerfile and scripts for the Overseer agent image.

## Getting Started

### Building the CLI

```bash
make build
```

The binary will be available in `bin/overseer-cli`.

### Building the Image

You can build the image using `ko` or `docker`.

Using `ko`:
```bash
ko build --local ./images/overseer
```

## Usage

Overseer is typically run as a sandbox in a Kubernetes cluster, managed by the `repowatch` controller.

For more details on the architecture and design, see [docs/design-overseer.md](docs/design-overseer.md).
