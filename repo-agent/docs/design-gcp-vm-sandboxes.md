# Design Note: Native GCP VM Sandboxes

This design note explores options for using native Google Cloud Platform (GCP) Virtual Machines (Compute Engine) as sandboxes for the `repo-agent`.

This approach avoids the complexities of nested virtualization and GKE Autopilot constraints by moving the sandbox environment out of the Kubernetes cluster entirely, while still managing it via Kubernetes controllers.

## Goals
*   **True Isolation**: Provide a completely isolated kernel and OS for the agent, distinct from the GKE node.
*   **GKE Autopilot Compatibility**: Enable "heavy" sandboxes (e.g., those needing custom kernels or raw socket access) without violating Autopilot constraints.
*   **Flexibility**: Allow the use of arbitrary machine types (e.g., GPUs, high-memory instances) not limited by the GKE node pool configuration.

## Option 1: Cluster API (Cluster API Provider GCP - CAPG)

[Cluster API (CAPI)](https://cluster-api.sigs.k8s.io/) is a Kubernetes sub-project that brings declarative, Kubernetes-style APIs to cluster creation, configuration, and management.

### Architecture
1.  **Management Cluster**: The GKE cluster where `repo-agent` runs acts as the "Management Cluster".
2.  **Providers**: We install the CAPI Core Provider and the GCP Infrastructure Provider (CAPG).
3.  **Resources**:
    *   `Cluster`: Defines the target infrastructure boundaries.
    *   `Machine`: Defines a single VM. CAPI reconciles this into a GCP VM.

### Workflow
1.  **Provisioning**: The `repo-agent` creates a `Machine` CR (and associated `GCPMachine` template).
2.  **Reconciliation**: The CAPI controllers detect the new CRs and call GCP APIs to provision the VM.
3.  **Bootstrapping**: The VM boots using cloud-init (defined in `KubeadmConfig` or similar) to install and start the `repo-sandbox` agent.
4.  **Connection**: The agent connects back to the `repo-agent` API (likely via a public LoadBalancer or VPC Peering).

### Pros
*   **Standardization**: Uses industry-standard tooling for infrastructure management.
*   **Robustness**: CAPI handles many edge cases (retries, drift detection, deletion) out of the box.

### Cons
*   **Overhead**: CAPI is designed for managing full Kubernetes clusters (Control Planes + Workers). Using it just for "naked" VMs (Machines without a cluster) is possible but adds significant CRD weight and controller complexity to the project.
*   **Latency**: CAPI reconciliation loops can add overhead to the raw VM boot time.

## Option 2: Custom Controller (Direct GCP API)

This approach involves writing a specialized controller within `repo-agent` that communicates directly with the Google Compute Engine API.

### Architecture
1.  **CRD**: Define a simple CRD, e.g., `GCPInstance`.
    ```yaml
    apiVersion: v1alpha1
    kind: GCPInstance
    spec:
      machineType: e2-medium
      image: projects/ubuntu-os-cloud/global/images/family/ubuntu-2204-lts
      startupScript: |
        #!/bin/bash
        ./repo-sandbox --server-addr=...
    ```
2.  **Controller**: A standard Kubernetes controller (using `controller-runtime`) watching these resources.
3.  **API**: The controller uses the Go client for Google Cloud (`google.golang.org/api/compute/v1`) to `Insert`, `Get`, and `Delete` instances.

### Workflow
1.  **Request**: User/Agent requests a sandbox with `isolationStrategy: VM`.
2.  **Creation**: Controller generates a unique instance name and calls `instances.insert`.
3.  **Metadata**: The startup script is injected via VM metadata. This script downloads the agent binary and starts it.
4.  **Networking**: The VM is tagged with a network tag allowing egress to the `repo-agent` service (exposed via Internal LoadBalancer if on same VPC, or External).
5.  **Cleanup**: When the CR is deleted, the controller calls `instances.delete`.

### Pros
*   **Simplicity**: No external dependencies like CAPI. We control the exact API calls.
*   **Performance**: Direct API calls are faster than multi-stage controller reconciliation.
*   **Tailored**: We can expose exactly the fields we need (machine type, disk size) and hide the rest.

### Cons
*   **Maintenance**: We must write the logic to handle long-running operations, retries, and API rate limits.
*   **Security**: The controller pod needs a GCP Service Account with `compute.instances.create` permissions.

## Recommendation

**Start with Option 2 (Custom Controller)**.

For our use case—launching ephemeral, single-purpose VMs—Cluster API is likely overkill. A custom controller allows for tight integration (e.g., injecting the exact auth tokens needed for the agent) and keeps the dependency footprint small.

### Prototype Plan
1.  **Service Account**: Create a GCP SA with `compute.admin` (or scoped permissions) and Workload Identity binding for the controller.
2.  **API Client**: Add `google.golang.org/api/compute/v1` to `go.mod`.
3.  **Reconciliation Loop**: Implement a controller that:
    *   Checks if a VM with the deterministic name exists.
    *   If not, creates it with a startup script that `curl`s the agent binary.
    *   Updates the CR status with the VM's external/internal IP.
    *   Deletes the VM on CR deletion.
