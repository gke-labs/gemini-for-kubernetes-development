# Design Note: VM-Based Sandboxes Exploration

## Context
The current sandbox implementation in `repo-agent` uses Kubernetes Pods (containers) to isolate agent workloads. While efficient, this may not provide sufficient isolation for running untrusted code or tasks that require low-level kernel access. This note explores options for implementing VM-based sandboxes, specifically looking at:
1.  Creating VMs on Google Cloud Platform (GCP).
2.  Using in-cluster virtualization frameworks like Firecracker, KVM/QEMU (KubeVirt), or Kata Containers.
3.  Compatibility with GKE Autopilot clusters.
4.  Compatibility and requirements for Standard GKE clusters.

## Constraint Analysis: GKE Autopilot
A critical factor in this exploration is compatibility with GKE Autopilot, the managed mode of operation for GKE.

*   **No Privileged Containers**: Autopilot restricts the use of privileged containers, which are often required to set up networking or storage for in-cluster VM managers.
*   **No Nested Virtualization**: Autopilot nodes do not support nested virtualization. This means the `/dev/kvm` device is not available to Pods.
    *   **Impact**: Technologies relying on hardware-assisted virtualization (KVM) — including **Firecracker**, **KubeVirt** (with KVM), and **Kata Containers** (standard configuration) — **cannot run on GKE Autopilot nodes**.

## Constraint Analysis: GKE Standard
While GKE Standard offers more flexibility, running VM-based sandboxes still requires specific configurations:

*   **Nested Virtualization**: To run VMs inside Pods (e.g., Firecracker), the underlying GKE nodes must support and have nested virtualization enabled.
    *   **Requirement**: This is generally supported on N1, N2, and N2D machine series. It must be explicitly enabled when creating the node pool.
*   **Privileged Mode**: Many virtualization controllers require privileged containers to manage the host kernel or network namespaces.
    *   **Requirement**: GKE Standard allows privileged containers, making it compatible with tools like KubeVirt.

## Options Exploration

### Option 1: In-Cluster Virtualization (KubeVirt, Firecracker, Kata)
This approach involves running VMs *inside* the Kubernetes cluster, managed as Pods or CRDs.

*   **Technologies**:
    *   **KubeVirt**: Manages VMs as Kubernetes resources. Requires `/dev/kvm` for performance.
    *   **Firecracker**: Lightweight microVMs. Fast startup (~100ms). Requires `/dev/kvm`.
    *   **Kata Containers**: OCI-compliant runtime that wraps containers in lightweight VMs.
*   **Pros**:
    *   **Speed**: MicroVMs (Firecracker/Kata) have startup times comparable to containers (milliseconds to seconds).
    *   **Kubernetes Native**: Managed via Kubernetes APIs (Pods, CRDs).
    *   **Networking**: Easier integration with cluster networking.
*   **Cons**:
    *   **GKE Autopilot Incompatible**: Requires direct access to virtualization hardware (`/dev/kvm`), which is blocked on Autopilot.
    *   **Node Requirements**: Requires GKE Standard nodes with "Nested Virtualization" enabled (specifically N1, N2, N2D machine series).
*   **Verdict**: Viable only for **GKE Standard** clusters configured with nested virtualization. Not an option for Autopilot.

### Option 2: Cloud Provider VMs (GCP Compute Engine)
This approach involves the `repo-agent` controller managing the lifecycle of standard Google Compute Engine (GCE) VMs directly, essentially acting as a cloud orchestrator.

*   **Mechanism**:
    *   The `ReviewSandbox` controller uses the GCP API (or a tool like Config Connector, Crossplane, or **Cluster API**) to create a `ComputeInstance` for each sandbox.
    *   The VM runs the `repo-sandbox` agent.
*   **Pros**:
    *   **Autopilot Compatible**: The controller runs in the cluster, but the VMs run as top-level GCP resources, bypassing Autopilot constraints.
    *   **Strong Isolation**: Full hardware virtualization isolation.
    *   **Flexibility**: Access to any GCP machine type, GPU, or OS.
*   **Cons**:
    *   **Latency**: VM provisioning and boot time is significantly slower (30s - 1min+) compared to containers.
    *   **Cost & Billing**: Per-second billing, but potentially higher overhead than packed containers.
    *   **Complexity**: Requires managing external resources, networking peering/connectivity back to the cluster (for controller communication), and separate quota management.
*   **Verdict**: The **only viable "true VM" option for GKE Autopilot**. Best for long-running dev sandboxes, less ideal for ephemeral review sandboxes due to startup latency.

### Option 3: GKE Sandbox (gVisor)
GKE Sandbox uses **gVisor**, a userspace kernel, to intercept application system calls. While not a "Virtual Machine" in the hardware sense, it provides a security boundary comparable to VMs.

*   **Mechanism**:
    *   Enable `gvisor` RuntimeClass on the Pod.
*   **Pros**:
    *   **Autopilot Compatible**: Supported and often the default mechanism for strong isolation on Autopilot.
    *   **Speed**: Fast startup (container-like).
    *   **Simplicity**: It's just a Pod with a specific `runtimeClassName`.
*   **Cons**:
    *   **Compatibility**: Some syscalls are not implemented; rare incompatibility with some applications (e.g., those needing direct hardware access or exotic protocols).
    *   **Not a VM**: Does not provide a separate kernel for the guest; cannot load kernel modules.
*   **Verdict**: The **recommended path for "Sandboxing" on Autopilot** if the goal is security/isolation rather than running a specific guest OS kernel.

## Summary & Recommendations

| Feature | In-Cluster VMs (Firecracker/KubeVirt) | Cloud VMs (GCP GCE) | GKE Sandbox (gVisor) |
| :--- | :--- | :--- | :--- |
| **Isolation** | High (Hardware VM) | High (Hardware VM) | High (Syscall Interception) |
| **GKE Autopilot** | ❌ **No** | ✅ **Yes** | ✅ **Yes** |
| **Startup Time** | Fast (~100ms - 2s) | Slow (~30s+) | Fast (Container speeds) |
| **Complexity** | High (Custom setup/Standard Cluster) | High (External resource mgmt) | Low (RuntimeClass) |

### Recommendation

1.  **For GKE Autopilot Users**:
    *   **Primary Choice**: Use **GKE Sandbox (gVisor)**. It creates a robust security boundary without the operational overhead of managing external VMs or the incompatibility of in-cluster VMs.
    *   **If Full VM is Required** (e.g., need custom kernel modules, nested Docker with specific storage drivers): Use **Cloud Provider VMs (GCP Compute Engine)** managed by the controller. Accept the slower startup time.

2.  **For GKE Standard Users**:
    *   If minimal latency and high isolation are required, **Kata Containers** or **Firecracker** (via a specialized controller) are excellent options, but require enabling nested virtualization on the node pools.

### Next Steps for `repo-agent`
To support these options, we can extend the `ReviewSandbox` / `IssueSandbox` CRDs:

1.  **Add `isolationStrategy` field**:
    ```yaml
    spec:
      isolationStrategy: "Pod" (default) | "gVisor" | "VM"
    ```
2.  **Implement `gVisor` support**:
    *   Simply map `isolationStrategy: gVisor` to `runtimeClassName: gvisor` in the generated Pod.
3.  **Explore `VM` support (Future)**:
    *   Create a prototype controller that spins up a GCE instance when `isolationStrategy: VM` is selected, potentially using a sidecar in the cluster to proxy traffic to the VM.
4.  **Explore Firecracker on Standard GKE (Future)**:
    *   Investigate integrating a Firecracker-based runtime (like `firecracker-containerd` or Kata Containers with Firecracker shim) for GKE Standard users.
    *   This would require validating that the node pool has nested virtualization enabled.
