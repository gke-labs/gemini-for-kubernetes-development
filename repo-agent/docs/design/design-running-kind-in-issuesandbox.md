# Design Note: Running Kind in IssueSandbox

## Context
We need to be able to run a `kind` (Kubernetes IN Docker) cluster inside the `IssueSandbox` environment. This is essential for agents that need to test Kubernetes integrations or validate changes against a cluster.

## Architecture Analysis
The `IssueSandbox` is defined as a `ResourceGraphDefinition` (RGD) using [Kro](https://kro.run/). The definition is located in `repo-agent/k8s/issue-sandbox-rgd.yaml`.
This RGD defines a schema for `IssueSandbox` custom resources and maps them to a `Sandbox` resource (from `agent-sandbox`), which ultimately creates a Pod.

## Challenges
Running `kind` requires:
1.  **Docker Daemon**: A running Docker daemon (or compatible runtime like Podman) is required to launch the `kind` nodes (which are containers).
2.  **Privileges**: The container running the Docker daemon needs privileged access (specifically for cgroups management and mounting). `securityContext.privileged: true` is typically required.
3.  **Networking**: Access to the Kubernetes API server running inside `kind`.

## Options

### Option 1: Privileged Mode + Docker-in-Docker (DinD) Sidecar
We can modify the `IssueSandbox` RGD to support an optional "docker enabled" mode.
*   **Mechanism**:
    *   Add a field `dindSupport` to the `IssueSandbox` spec.
    *   When set to `privileged`, the RGD template adds a `dind` (Docker-in-Docker) sidecar container to the Pod.
    *   The `dind` sidecar runs with `privileged: true`.
    *   Both the main container and the `dind` sidecar mount a shared volume for the Docker socket (`/var/run/docker.sock`).
*   **Pros**:
    *   Separation of concerns: The main agent environment remains clean; Docker is provided as a service.
    *   Standard pattern for CI/CD environments (like Jenkins, GitLab CI).
*   **Cons**:
    *   Requires multi-container coordination (waiting for socket).
    *   Security implications of privileged mode (unavoidable for `kind`).

### Option 2: Privileged Main Container + Envbuilder/Devcontainer Features
If using `envbuilder` (the default base for `repo-sandbox`), we can leverage devcontainer features.
*   **Mechanism**:
    *   Add `dindSupport: privileged` flag to `IssueSandbox` spec.
    *   The user provides a `devcontainer.json` that includes the `docker-in-docker` feature.
    *   `envbuilder` handles installing Docker and starting it.
    *   We simply need to ensure the Pod is created with `securityContext.privileged: true`.
*   **Pros**:
    *   Leverages existing devcontainer ecosystem.
    *   `envbuilder` abstracts the setup.
*   **Cons**:
    *   Couples the implementation to `envbuilder` capabilities.
    *   The main container runs privileged, which might be less secure/isolation than a sidecar (though if they share the docker socket, the difference is minimal).

### Option 3: User-Mode Docker (Rootless)
*   **Mechanism**: Run rootless Docker or generic-worker with user namespaces.
*   **Pros**: More secure.
*   **Cons**: `kind` often has issues with rootless docker or requires specific cgroup v2 setup and host configuration which might not be guaranteed in all K8s environments where `repo-agent` runs.

## Selected Approach: Privileged Main Container (Option 2) or gVisor
We modify the `IssueSandbox` RGD to expose a `dindSupport` field.
Since `envbuilder` is a core part of the stack, enabling `privileged` mode allows `envbuilder` to do its job (installing Docker) if configured via `devcontainer.json`.
Alternatively, `gvisor` mode can be used for better isolation on clusters that support it, but it might have limitations for complex workloads like `kind`.

**Implementation**:
1.  Update `IssueSandbox` RGD schema to include `dindSupport` enum (`none`, `gvisor`, `privileged`).
2.  When `dindSupport` is `privileged`:
    *   Set `securityContext.privileged: true` on the main container.
3.  When `dindSupport` is `gvisor`:
    *   Set `runtimeClassName: gvisor` and appropriate capabilities.

This allows the main container to run Docker (via `envbuilder` or custom setup) and `kind`.

## Implementation Steps
1.  Modify `repo-agent/k8s/issue-sandbox-rgd.yaml`:
    *   Add `dindSupport` to `spec.schema.spec`.
    *   Update `spec.resources[0].template.spec.podTemplate.spec.containers[0].securityContext.privileged` and `runtimeClassName` to be set based on the `dindSupport` flag.
2.  Verify creation of `IssueSandbox` with `dindSupport: privileged`.
3.  Verify `kind create cluster` works inside the sandbox.

## Considerations for GKE Autopilot and Rootless Docker
This implementation leverages `privileged` containers for `privileged` mode, which are standard for running Docker-in-Docker (and thus `kind`).

*   **GKE Autopilot**: By default, GKE Autopilot adheres to strong security standards and disallows privileged containers. For Autopilot, `dindSupport: gvisor` should be used.
*   **Rootless Docker**: A "rootless" approach (Option 3) would be required to support strict environments like GKE Autopilot without gVisor. This involves running Docker (and `kind`) without root privileges, often requiring:
    *   User Namespaces enabled on the host.
    *   Cgroup v2 delegation.
    *   Specialized container images (e.g., `kind` rootless variants).

Support for rootless Docker is deferred to future work.
