# Overseer User Guide: Installation, Configuration & Operations

Welcome to the **Overseer User Guide**. This document walks administrators and tech leads through deploying, configuring, and managing an autonomous Overseer agent for a software repository in Kubernetes. 

This guide builds directly upon the concepts outlined in [architecture-overseer-factory.md](./architecture-overseer-factory.md) and uses the enterprise **Kubernetes Config Connector (KCC)** configuration ([examples/kcc.yaml](../examples/kcc.yaml)) as a concrete real-world implementation example.

---

## 1. Prerequisites & Preparation

Before deploying an Overseer instance to supervise your repository, ensure your target Kubernetes cluster is prepared with the necessary identities and infrastructure credentials.

### 1.1 Cluster & Custom Resource Definitions
Ensure your cluster has the required Kubernetes Custom Resource Definitions installed:
- `Overseer` (`overseer.gemini.google.com/v1alpha1`)
- `Sandbox` (`agents.x-k8s.io/v1alpha1`)
- `SandboxTask` (`sandboxtask.gemini.google.com/v1alpha1`)

*Tip: For local testing or development, see **Section 1.5** below to automatically bootstrap a local `kind` cluster with all required CRDs and controllers pre-installed.*

### 1.2 LLM Credentials Secret
Overseer and its worker sandboxes rely on Google Gemini models for intent evaluation and automated code generation. Create a Kubernetes Secret containing your valid API token:
```bash
kubectl create secret generic gemini-vscode-tokens \
  --from-literal=GEMINI_API_KEY="your-api-key-here" \
  -n default
```

### 1.3 GitHub Robot Accounts
Overseer uses GitHub accounts to clone repositories, sync task queues, post comments, and submit pull requests. To avoid API throttling and cleanly separate responsibilities, enterprise installations should provision multiple dedicated robot accounts (e.g., watcher bots, coding bots, reviewer bots) with valid Personal Access Tokens (PATs) or GitHub App credentials.

### 1.4 Domain-Specific Test Credentials (GCP, AWS, DBs)
If your agents are expected to run automated integration tests or compile code against real cloud providers, package those authentication keys into a Kubernetes Secret so they can be injected into worker sandboxes. For example, to allow KCC developers to verify Google Cloud resources:
```bash
# Create a secret containing a GCP service account JSON key
kubectl create secret generic projectaccess \
  --from-file=keys.json=/path/to/sa-key.json \
  -n default
```

### 1.5 Local Development Setup with kind
To try out Overseer locally or develop new features, you can spin up an isolated environment inside a `kind` Kubernetes cluster.

1. **Export Environment Variables**: Before starting, provide your Gemini API key and GitHub credentials for your test robot account. These are automatically packaged into Kubernetes secrets by the setup scripts:
```bash
export GEMINI_API_KEY="your-gemini-api-key"
export ROBOT1_GH_PAT="your-github-personal-access-token"
export ROBOT1_GH_USERID="your-github-username"
export ROBOT1_GH_NAME="Your Name"
export ROBOT1_GH_EMAIL="your-email@example.com"
```

2. **Deploy with Make**: Simply run `make` inside the `overseer/` directory:
```bash
make
```
This command checks prerequisites, initializes a `kind` cluster named `overseer-agent`, installs all required CRDs, imports your credentials as secrets, builds the Overseer images, and deploys the Overseer custom resource controller into the `overseer-system` namespace.

---

## 2. Step-by-Step Configuration: The KCC Example

To instruct Overseer to supervise a repository, you deploy an `Overseer` custom resource. Below is a deep dive into the real-world configuration used to monitor [GoogleCloudPlatform/k8s-config-connector](https://github.com/GoogleCloudPlatform/k8s-config-connector), available in [examples/kcc.yaml](../examples/kcc.yaml).

```yaml
apiVersion: overseer.gemini.google.com/v1alpha1
kind: Overseer
metadata:
  name: kcc
spec:
  repoURL: https://github.com/GoogleCloudPlatform/k8s-config-connector
  minNumber: 9000
  robotAccount: argus-watcher-bot
  maxActiveReviews: 20
  maxActiveIssues: 25
  sandboxIdleTimeout: 1h
  chores:
    mode: enabled
  geminiAPIKeySecretName: gemini-vscode-tokens
  image: ghcr.io/gke-labs/gemini-for-kubernetes-development/factory-golang:latest
  workspaceDiskSize: 40Gi
  ephemeralStorage: 10Gi
  sandboxCPURequest: "2000m"
  sandboxCPULimit: "8000m"
  sandboxMemoryRequest: "4Gi"
  sandboxMemoryLimit: "16Gi"
  secrets:
    - name: projectaccess
      mountPath: "/etc/pakey"
  env:
    - name: GOOGLE_APPLICATION_CREDENTIALS
      value: "/etc/pakey/keys.json"
    - name: CLOUDSDK_AUTH_CREDENTIAL_FILE_OVERRIDE
      value: "/etc/pakey/keys.json"
    - name: GOOGLE_CLOUD_PROJECT
      value: "cnrm-barni-4"
    - name: CLOUDSDK_CORE_PROJECT
      value: "cnrm-barni-4"
    - name: GCP_PROJECT_ID
      value: "cnrm-barni-4"
  repo:
    issueMode: enabled
    prMode: enabled
    reviewMode: disabled
  roles:
    watcher:
      users:
        - argus-watcher-bot
    coder:
      users:
        - hopper-coder-bot
        - ada-coder-bot
        - neumann-coder-bot
        - lovelace-coder-bot
    reviewer:
      users:
        - reviewbot-robot
    agent:
      users:
        - daedalus-agent-bot
        - feynman-agent-bot
        - walle-agent-bot
```

### Key Configuration Breakdown:

#### 1. Target & Ticket Filtering
- **`repoURL`**: The GitHub HTTPS URL of the target repository.
- **`minNumber: 9000`**: A crucial setting for large existing repositories. Overseer ignores issues or PRs numbered below `9000`, preventing the AI from accidentally processing thousands of historical or legacy tickets during onboarding.
- **`robotAccount`**: The primary GitHub identity (`argus-watcher-bot`) responsible for synchronizing the state tracking branch (`overseer`).

#### 2. Concurrency & Resource Sizing
- **`maxActiveIssues` & `maxActiveReviews`**: Sets ceilings (`25` and `20`) on how many simultaneous worker sandboxes can run in parallel, safeguarding your cluster from node exhaustion during high-volume event bursts.
- **Worker Compute Sizing**: KCC compilations require heavy resources. Setting CPU limits to `8000m` (8 cores) and memory limits to `16Gi` ensures fast Go compilation and e2e test execution.
- **Disk Allocation**: `workspaceDiskSize: 40Gi` and `ephemeralStorage: 10Gi` provide generous persistent disk volumes to prevent out-of-space errors during repeated container builds.

#### 3. Custom Worker Images
- **`image`**: Pointing to a tailored Docker container (`factory-golang:latest`) that comes pre-cached with Go toolchains, common linters, and dependency layers, significantly cutting down cold-start times when spinning up new sandboxes.

#### 4. Secrets & Cloud Credentials Injection
- **`secrets` & `env`**: Mounts the `projectaccess` secret directly into worker pods at `/etc/pakey/keys.json` and sets standard Google Cloud SDK environment variables (`GOOGLE_APPLICATION_CREDENTIALS`, `GCP_PROJECT_ID: cnrm-barni-4`). When an AI coder bot writes a bug fix, it can run integration tests against a dedicated test cloud environment seamlessly.

#### 5. Multi-Bot Role Separation (`roles`)
Rather than relying on a single bot account, tasks are routed to specialized personas:
- **`watcher`**: Synchronizes queues and observes GitHub events (`argus-watcher-bot`).
- **`coder`**: A pool of robot identities (`hopper-coder-bot`, `ada-coder-bot`, etc.) that adopt bugs, generate fixes, and push branches.
- **`reviewer`**: Dedicated account (`reviewbot-robot`) for leaving automated code reviews on community PRs.
- **`agent`**: Specialized investigative problem-solvers (`daedalus-agent-bot`, `feynman-agent-bot`, `walle-agent-bot`).

#### 6. Operational Modes (`repo` & `chores`)
- **`issueMode` & `prMode`**: Enabled to permit automatic bug fixing and PR assistance.
- **`reviewMode: disabled`**: Disables automated review comments on all incoming PRs (useful if you prefer human-only initial reviews).
- **`chores.mode: enabled`**: Instructs Overseer to run periodic maintenance routines defined in repository `.agents/*.md` workflows (such as dependency upgrades or automated release formatting).

---

## 3. Deployment & Verification

Once your configuration YAML is ready, deploy it to your cluster:

```bash
kubectl apply -f my-kcc-overseer.yaml
```

### Step 1: Verify Tenant Namespace Creation
When the `Overseer` custom resource is created, the controller automatically spins up an isolated namespace named `overseer-<resource-name>`. For KCC, check that `overseer-kcc` exists:
```bash
kubectl get namespaces | grep overseer-kcc
```

### Step 2: Inspect the Watch Daemon Pod
Verify that the supervisory watch daemon pod is running inside the new namespace:
```bash
kubectl get pods -n overseer-kcc
```

### Step 3: Stream Live Supervisory & Controller Logs

**Overseer Controller Logs:**
To monitor the central Kubernetes controller responsible for reconciling `Overseer` custom resources and creating tenant namespaces:
```bash
kubectl logs -n overseer-system -l app=overseer-controller -f
```

**Overseer Watch Daemon Agent Logs:**
To monitor the autonomous agent's continuous dual-loop (deterministic `factory watch` followed by Gemini LLM intent orchestration) inside the tenant namespace:
```bash
kubectl logs -n overseer-kcc -l sandbox=overseer-kcc-agent -f
```

---

## 4. Day-to-Day Operations & UI Management

Once installed, tech leads and operators can interface with Overseer via the **Review UI Dashboard** (typically served over port `80` or via port-forwarding on the `review-api` deployment).

### 4.1 Inspecting Active Worker Sandboxes
When an issue is labeled or assigned to one of your robot coder bots, Overseer spins up a dedicated worker sandbox pod (e.g., `kcc-issue-9102` or `kcc-pr-9105`). In the web dashboard:
- Click on any active sandbox row to open the **Sandbox Detail View**.
- **Live Terminal Access**: Click the terminal icon to establish a live interactive SSH session directly into the worker pod to debug test failures or inspect local git diffs alongside the AI.

### 4.2 Managing Sandbox Lifecycle: Pause & Unpause
To optimize compute resources, worker sandboxes feature automatic idle expiration:
- **Automatic Pausing**: Configured by `sandboxIdleTimeout: 1h` in your YAML. When a worker sandbox finishes its coding task and remains inactive for 1 hour, the controller sets `spec.replicas = 0` (displaying **`Sandbox Paused`** in yellow on the UI card). All disk state and logs are preserved, but CPU/RAM usage drops to zero.
- **Manual Unpausing (UI Button)**: If a developer wants to log into a paused sandbox to inspect artifacts or run tests, simply click the **"▶️ Unpause Sandbox"** button on the UI card or Sandbox Header Box. 
- **Timeout Retention Guarantee**: Unpausing sets `spec.replicas = 1` and records an unpause timestamp (`sandbox.gemini.google.com/unpaused-at`). The automated cleanup loops respect this timestamp, guaranteeing your manually unpaused sandbox stays running for at least the configured idle duration before it becomes eligible for pausing again.

### 4.3 Maintenance & Drain Mode
If you need to perform maintenance, test infrastructure upgrades, or halt LLM task generation without forcefully terminating currently executing worker jobs:
- Create a `.do_not_process` or `.drain` empty file within the supervisor workspace, or toggle Drain mode in the UI. 
- During drain mode, the watch daemon stops triggering Gemini LLM orchestration and stops spawning new sandboxes while allowing existing worker sandboxes to finish their assignments gracefully.

---

## 5. Best Practices for Enterprise Onboarding

1. **Always Set `minNumber` During Adoption**: When introducing Overseer to mature repositories, set `minNumber` just above your highest current issue/PR number. Lower this threshold incrementally as you get comfortable with the bot's workflow interactions.
2. **Pre-Warm Custom Docker Images**: Avoid installing compilers, SDKs, or massive dependency trees (like `node_modules` or Go vendors) at runtime. Build and maintain a custom base container image (as seen in `factory-golang:latest`) and reference it in `spec.image`.
3. **Audit Token Consumption with Token Daemon**: Overseer runs a token telemetry service (`token-usage.overseer-system:8080`). Use the Review UI rollups to audit total Gemini token consumption broken down by workflow, pull request number, and individual user bug report.
4. **Isolate Cloud Environments**: When injecting cloud provider keys (like GCP Service Accounts), never use production project credentials. Dedicated staging or disposable test projects (such as `cnrm-barni-4`) ensure agent automated testing remains safe and non-destructive.

---

## 6. Further Reading & References
- **End-to-End Architecture & Dual-Loop Diagrams**: [architecture-overseer-factory.md](./architecture-overseer-factory.md)
- **Overseer Foundational Design Note**: [design-overseer.md](./design-overseer.md)
- **AI Factory CLI Manual**: [../factory/README.md](../../factory/README.md)
