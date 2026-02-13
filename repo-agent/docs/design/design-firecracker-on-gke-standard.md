# Detailed Design: Firecracker & Kind on GKE Standard

This design note explores the specific implementation steps required to run **Kind (Kubernetes in Docker)** clusters inside **Firecracker** microVMs on **GKE Standard**.

This setup provides strong isolation for disposable Kubernetes environments (sandboxes) while maintaining the ability to run containerized workloads (like Kind) inside them.

## Prerequisites

To run Firecracker VMs inside a GKE Pod ("Nested Virtualization"), specific hardware and software requirements must be met:

1.  **GKE Standard**: GKE Autopilot does not support the required privileges or nested virtualization.
2.  **Machine Series**: Google Cloud **N2** or **N2D** machine series are recommended for nested virtualization support. (N1 is also supported but older).
3.  **Operating System**: The GKE Node image must support KVM. The **Ubuntu** image type is the most reliable choice for enabling `/dev/kvm` out of the box on GKE.

## Step 1: GKE Cluster Creation

We need to create a GKE cluster (or node pool) that supports nested virtualization.

**Key Flags:**
*   `--machine-type=n2-standard-4`: Uses the N2 machine series which supports nested virtualization.
*   `--image-type=UBUNTU_CONTAINERD`: Ensures the host OS has the necessary KVM kernel modules available.

```bash
export CLUSTER_NAME="firecracker-cluster"
export ZONE="us-central1-a"
export PROJECT_ID=$(gcloud config get-value project)

gcloud container clusters create $CLUSTER_NAME \
    --project $PROJECT_ID \
    --zone $ZONE \
    --machine-type n2-standard-4 \
    --image-type UBUNTU_CONTAINERD \
    --num-nodes 1 \
    --enable-ip-alias
```

*Note: Depending on the specific GCP zone and project settings, you might need to ensure the `compute.vm.enable_nested_virtualization` constraint is allowed if you were creating raw VMs, but for GKE N2 instances, this is typically available.*

## Step 2: Configure Firecracker as a CRI (via Kata Containers)

Firecracker is a Virtual Machine Monitor (VMM). Kubernetes uses the Container Runtime Interface (CRI). To bridge the two, we use **Kata Containers**, which provides a CRI-compliant runtime that spins up lightweight VMs (using QEMU or Firecracker) for each Pod.

### 2.1 Install Kata Containers on GKE

The easiest way to install Kata Containers on a running cluster is using the official `kata-deploy` DaemonSet.

```bash
# Apply the Kata Deploy DaemonSet
kubectl apply -f https://raw.githubusercontent.com/kata-containers/kata-containers/main/tools/packaging/kata-deploy/kata-deploy/base/kata-deploy.yaml
```

This DaemonSet installs the Kata binaries and configuration on every node in the cluster.

### 2.2 Create a RuntimeClass

We need to define a `RuntimeClass` that tells Kubernetes to use Kata (configured for Firecracker) when requested.

```yaml
apiVersion: node.k8s.io/v1
kind: RuntimeClass
metadata:
  name: kata-fc
handler: kata-fc
overhead:
  podFixed:
    memory: "120Mi"
    cpu: "250m"
scheduling:
  nodeSelector:
    katacontainers.io/kata-runtime: "true"
```

*Note: The `kata-deploy` installation typically sets up default handlers like `kata-qemu` and `kata-fc` (Firecracker). Check the node labels or kata configuration on the node to verify `kata-fc` is available.*

## Step 3: Creating a Pod with Firecracker

Now we can create a Pod that uses the `kata-fc` RuntimeClass. This Pod will run inside a Firecracker microVM.

For running **Kind**, the Pod needs to be able to run Docker (Docker-in-Docker) or Containerd. This often requires `privileged: true` *inside* the VM context, but since the VM itself is isolated, this is safe for the host.

```yaml
apiVersion: v1
kind: Pod
metadata:
  name: firecracker-kind-sandbox
spec:
  runtimeClassName: kata-fc
  containers:
  - name: kind-node
    # Use a generic image that can run Docker/Kind
    image: docker:dind
    securityContext:
      privileged: true # Required for running dockerd inside the VM
    resources:
      requests:
        cpu: 2
        memory: 4Gi
```

## Step 4: Running Kind inside the Pod

Once the Pod is running, the container process is actually running inside a dedicated Linux kernel (the Firecracker VM). We can now execute commands to start a cluster.

1.  **Exec into the Pod**:
    ```bash
    kubectl exec -it firecracker-kind-sandbox -- sh
    ```

2.  **Verify Virtualization (Optional)**:
    Inside the pod, check the kernel version. It should be the Kata Guest Kernel, not the GKE Host Kernel.
    ```bash
    uname -a
    ```

3.  **Install Kind (if not in image)**:
    ```bash
    apk add --no-cache curl
    curl -Lo ./kind https://kind.sigs.k8s.io/dl/v0.20.0/kind-linux-amd64
    chmod +x ./kind
    mv ./kind /usr/local/bin/kind
    ```

4.  **Create the Cluster**:
    ```bash
    kind create cluster
    ```

### Challenges & Considerations for Kind in Firecracker

*   **Storage**: Kind uses overlayfs. Firecracker's root filesystem (and the passed-through container filesystem) needs to support this. Kata Containers 2.x/3.x handles storage passthrough efficiently (typically using virtio-fs), which generally supports standard operations.
*   **Networking**: Kind creates a docker network. This is contained entirely inside the Firecracker VM. The "Host" for Kind is the Firecracker VM, not the GKE Node.
*   **Performance**: There is overhead for the double-nesting (GKE Node VM -> Firecracker VM -> Kind Docker Containers). N2 CPUs are performant, but expect some latency.
*   **KVM Passthrough**: Kind uses container-based virtualization (namespaces/cgroups), not hardware virtualization. Therefore, it does **not** require `/dev/kvm` to be passed through to the Firecracker guest. The `/dev/kvm` device is utilized by the GKE Node to spawn the Firecracker VM itself.

## Architecture Summary

```text
[ GKE Node (N2 VM) ]
    |
    |-- [ Kata Shim Process ]
    |
    |-- [ Firecracker VMM Process ]
          |
          |-- [ Guest Linux Kernel ]
                |
                |-- [ Pod Container (docker:dind) ]
                      |
                      |-- [ Dockerd ]
                            |
                            |-- [ Kind Control Plane Container ]
                            |-- [ Kind Worker Container ]
```
