# Overseer

Overseer is an autonomous agent responsible for orchestrating other agents and managing the state of a repository in a Kubernetes-based agentic system. It combines deterministic task supervision with an LLM-driven autonomous phase (`gemini-cli`) to continuously observe repository events and drive workflows toward desired states.

## Documentation

For instructions, architectural deep-dives, and design notes, consult the documentation under `docs/`:

- **[User Guide & Installation Manual](docs/user-guide.md)**: Comprehensive instructions for cluster prerequisites, setting up robot accounts, local development with `kind`, operational management via the Review UI dashboard, and real-world configuration walkthroughs (such as the enterprise KCC deployment).
- **[System Architecture Guide & Interaction Diagrams](docs/architecture-overseer-factory.md)**: An end-to-end technical breakdown of the dual-loop supervisor model, worker sandbox lifecycle states, token usage telemetry, and git-backed filesystem queue management between Overseer and Factory.
- **[Core Design Principles](docs/design-overseer.md)**: Foundational concepts governing autonomous agentic loops, intent grokking, and multi-agent orchestration.

## Core Components

- `pkg/overseer`: Go package for reconciling Overseer sandboxes and managing tenant namespace isolation.
- `images/overseer`: Dockerfile and execution scripts (`run.sh`, `bootstrap.sh`) for the Overseer watch daemon container image.
- `k8s/token-usage.yaml`: Kubernetes manifests for the telemetry token-usage collector (reusing the overseer image to run `factory token-daemon`), which durably records per-task Gemini token consumption on a PVC and serves aggregated usage rollups over HTTP as a StatefulSet (`token-usage.overseer-system:8080`).
- `examples/`: Ready-to-deploy custom resource configurations for real-world repositories, including [Kubernetes Config Connector (KCC)](examples/kcc.yaml), [Gateway API Reference](examples/gwapi-ref.yaml), and [AI Factory](examples/ai-factory.yaml).
