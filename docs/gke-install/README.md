# Installing Gemini for Kubernetes Development on GKE

This guide covers deploying the repo-agent stack onto an existing **Google Kubernetes Engine (GKE)** cluster, including the GKE-specific fixes required for the release `v0.1.0-rc.3` manifests.

> **TL;DR** — Run the interactive Go installer and follow the prompts:
> ```bash
> cd docs/gke-install/installer
> go run . --project=my-project --cluster=my-cluster
> ```

---

## Contents

1. [Architecture overview](#architecture-overview)
2. [Prerequisites](#prerequisites)
3. [Before you begin](#before-you-begin)
4. [Quick install (Go installer)](#quick-install-go-installer)
5. [Manual install](#manual-install)
6. [GKE-specific compatibility fixes](#gke-specific-compatibility-fixes)
7. [Creating a RepoWatch](#creating-a-repowatch)
8. [Accessing the UI](#accessing-the-ui)
9. [Troubleshooting](#troubleshooting)

---

## Architecture overview

```
┌─────────────────────────────────────────────────────────┐
│  repo-agent-system namespace                            │
│                                                         │
│  ┌──────────────┐  ┌───────────────┐  ┌─────────────┐  │
│  │ pr-review-ui │  │ pr-review-api │  │   registry  │  │
│  └──────┬───────┘  └───────┬───────┘  └─────────────┘  │
│         │                  │                            │
│  ┌──────▼──────────────────▼──────────────────────────┐ │
│  │         repowatch-controller (StatefulSet)         │ │
│  └──────────────────────────┬───────────────────────── ┘ │
│                             │ creates                   │
│  ┌──────────────────────────▼───────────────────────┐  │
│  │  Per-repo namespace (e.g. "my-repo")             │  │
│  │  ┌─────────────────┐  ┌───────────────────────┐  │  │
│  │  │  Issue sandbox  │  │  PR review sandbox    │  │  │
│  │  │  (Gemini agent) │  │  (envbuilder + agent) │  │  │
│  │  └─────────────────┘  └───────────────────────┘  │  │
│  └───────────────────────────────────────────────────┘  │
└─────────────────────────────────────────────────────────┘
```

The **repowatch-controller** watches a `RepoWatch` custom resource and
automatically provisions sandboxes — ephemeral pods running the Gemini CLI agent
— whenever it detects a new PR, issue, or CI failure in the watched repository.

---

## Prerequisites

| Requirement | Version | Notes |
|---|---|---|
| GKE cluster | Standard or Autopilot | Must already exist |
| `kubectl` | ≥ 1.28 | Configured with cluster credentials |
| `gcloud` CLI | latest | Authenticated (`gcloud auth login`) |
| `helm` | ≥ 3.14 | |
| `gh` CLI | ≥ 2.40 | Authenticated (`gh auth login`) |
| Go toolchain | ≥ 1.22 | Only needed to build the installer |
| GitHub PAT | — | Bot account; needs `repo` + `workflow` scopes |
| Gemini API key | — | From [Google AI Studio](https://aistudio.google.com/app/apikey) |

### Optional (multi-user UI)

A **GitHub OAuth App** is needed so multiple users can log in to the web UI.
Create one at **GitHub → Settings → Developer settings → OAuth Apps** with:

- **Homepage URL**: `https://<your-cluster-ip>`
- **Authorization callback URL**: `https://<your-cluster-ip>/api/auth/callback`

Note the **Client ID** and generate a **Client Secret**.

---

## Before you begin

### 1 — Obtain cluster credentials

```bash
gcloud container clusters get-credentials CLUSTER_NAME \
  --location REGION \
  --project PROJECT_ID
```

### 2 — Verify connectivity

```bash
kubectl cluster-info
```

### 3 — Note your cluster's external IP range

The PR review UI is exposed via a `LoadBalancer` service.  If your cluster is
private you will need an Ingress or Envoy Gateway route instead.

---

## Quick install (Go installer)

```bash
# Clone or download the installer
cd docs/gke-install/installer

# Run interactively — you will be prompted for all required values
go run .

# Or supply values as flags to skip prompts
go run . \
  --project=my-gcp-project \
  --cluster=my-gke-cluster \
  --region=us-central1 \
  --repo=https://github.com/my-org/my-repo \
  --gemini-api-key="AIza..." \
  --github-pat="ghp_..." \
  --bot-name="My Bot" \
  --bot-email="bot@example.com"
```

The installer performs these steps in order:

1. Checks that `kubectl`, `helm`, `gcloud`, and `gh` are in `PATH`
2. Runs `gcloud container clusters get-credentials` to configure `kubectl`
3. Installs **Kyverno** (required for image-reference rewriting; skipped if already present)
4. Installs Helm charts: Envoy Gateway, KRO, Agent Sandbox
5. Applies the release manifest (`manifest.yaml`)
6. Creates the `gemini-api-key` and `github-token` secrets
7. Applies the [GKE compatibility fixes](#gke-specific-compatibility-fixes)
8. Creates the `RepoWatch` CR in a dedicated namespace
9. Waits for all deployments and the repowatch-controller to become ready
10. Prints the UI endpoint and next steps

---

## Manual install

If you prefer to install step by step, follow the sections below.

### 1 — Install Kyverno

Kyverno is required to rewrite the `ko://` build-time image references that
exist in the sandbox pod specs at runtime.  See
[GKE-specific compatibility fixes](#gke-specific-compatibility-fixes) for why
this is needed.

```bash
helm repo add kyverno https://kyverno.github.io/kyverno/
helm repo update
helm upgrade --install kyverno kyverno/kyverno \
  --namespace kyverno --create-namespace \
  --set admissionController.replicas=1 \
  --wait
```

### 2 — Install Helm dependencies

```bash
# Envoy Gateway (API gateway / ingress)
helm upgrade --install envoy-gateway \
  oci://docker.io/envoyproxy/gateway-helm \
  --version v1.5.2 \
  --namespace envoy-gateway-system --create-namespace --wait

# KRO (Kubernetes Resource Orchestration)
helm upgrade --install kro \
  oci://registry.k8s.io/kro/charts/kro \
  --version 0.5.1 \
  --namespace kro --create-namespace --wait

# Agent Sandbox framework
helm upgrade --install agent-sandbox \
  oci://ghcr.io/gke-labs/gemini-for-kubernetes-development/charts/agent-sandbox \
  --version v0.1.0-rc.3 \
  --namespace agent-sandbox-system --create-namespace --wait
```

### 3 — Apply the release manifest

```bash
kubectl apply -f https://github.com/gke-labs/gemini-for-kubernetes-development/releases/download/v0.1.0-rc.3/manifest.yaml
```

### 4 — Create secrets

```bash
# Gemini API key
kubectl create secret generic gemini-api-key \
  -n repo-agent-system \
  --from-literal=gemini="$GEMINI_API_KEY" \
  --dry-run=client -o yaml | kubectl apply -f -

# GitHub token — single-user mode
kubectl create secret generic github-token \
  -n repo-agent-system \
  --from-literal=token="$GITHUB_PAT" \
  --dry-run=client -o yaml | kubectl apply -f -

# GitHub token — multi-user mode (add OAuth credentials)
kubectl create secret generic github-token \
  -n repo-agent-system \
  --from-literal=token="$GITHUB_PAT" \
  --from-literal=github-client-id="$GITHUB_CLIENT_ID" \
  --from-literal=github-client-secret="$GITHUB_CLIENT_SECRET" \
  --dry-run=client -o yaml | kubectl apply -f -
```

### 5 — Apply GKE compatibility fixes

See the [next section](#gke-specific-compatibility-fixes) for full explanation.

```bash
kubectl apply -f docs/gke-install/manifests/kyverno-gke-compat.yaml
kubectl patch configmap -n repo-agent-system devcontainer-json \
  --type=merge \
  -p "$(cat docs/gke-install/manifests/devcontainer-patch.json)"
```

---

## GKE-specific compatibility fixes

Two fixes are required on GKE that are not needed with the default `kind`-based
install.

### Fix 1 — Image reference rewriting (Kyverno ClusterPolicy)

The release manifest contains sandbox pod specs whose image fields use `ko://`
build-time references — these are resolved by the `ko` build tool during
development but are **not typically supported** by GKE (or other standard K8s distributions).

Kyverno intercepts sandbox pod creation and rewrites these to the production
`ghcr.io` images.

### Fix 2 — Development container branch reset

When an agent-based sandbox starts, it attempts to checkout the PR branch. On
some GKE storage backends (especially when using PVCs), the git state may become
"dirty" or diverged from the upstream branch if the pod restarts.

The `devcontainer-json` patch adds a wrapper around the `gh` CLI that performs a
force-reset to the upstream state whenever `gh pr checkout` is called, ensuring
the agent always works from a clean, up-to-date branch.

---

## Creating a RepoWatch

Once the stack is installed, you need to tell it which repository to monitor.

```yaml
apiVersion: review.gemini.google.com/v1alpha1
kind: RepoWatch
metadata:
  name: my-repo
  namespace: my-repo-ns
spec:
  repoURL: https://github.com/my-org/my-repo
  githubSecretName: github-token
  pollIntervalSeconds: 60
  review:
    llm:
      provider: gemini-cli
      apiKeySecretRef: gemini-api-key
    maxActiveSandboxes: 5
  issue:
    llm:
      provider: gemini-cli
      apiKeySecretRef: gemini-api-key
    maxActiveSandboxes: 2
```

Apply this with `kubectl apply -f`.  The controller will create a dedicated
namespace for the repository and begin polling.

---

## Accessing the UI

1. Get the external IP of the UI service:
   ```bash
   kubectl get svc -n repo-agent-system pr-review-ui
   ```
2. Open `https://<EXTERNAL_IP>` in your browser.
3. If you configured multi-user mode, you will need to navigate to
   `https://<EXTERNAL_IP>/api/auth/login` and complete the GitHub OAuth flow.
- **Multi-user mode**: Click **Login with GitHub** on the home page.

> **Note:** The `pr-review-api` stores sessions in memory.  If the pod restarts
> (e.g. after a `kubectl delete pod`), users must log in again.

---

## Troubleshooting

### Sandbox pods stuck in `CrashLoopBackOff`

```bash
kubectl logs -n repo-agent-system <pod-name>
```

Common causes:

| Error | Fix |
|---|---|
| `open /devcontainer.json: no such file or directory` | Kyverno `mount-devcontainer-json` rule not applied — re-apply `kyverno-gke-compat.yaml` and delete the pod |
| `ImagePullBackOff` on `ko://...` image | Kyverno `replace-*` rules not applied — re-apply policy |
| `fatal: Not possible to fast-forward` | `gh` wrapper not installed — check `devcontainer-json` ConfigMap `postCreateCommand` |

### UI shows "Please select or add a repository"

The `pr-review-api` session was lost (pod restart).  Navigate to
`https://<IP>/api/auth/login` to re-authenticate.

### `repowatch-controller` not creating sandbox pods

```bash
kubectl logs -n repo-agent-system -l app=repowatch-controller -f
```

Look for `Skipping investigate-failures: last attempt was after latest commit`.
This means no new commit has been pushed since the last (failed) attempt.
Push a new commit (or bump an annotation on the `RepoWatch` CR) to unblock:

```bash
kubectl annotate repowatch -n <namespace> <name> \
  reconcile-trigger="$(date +%s)" --overwrite
```

### Kyverno policy not mutating pods

Verify the policy is installed and not in error:

```bash
kubectl get clusterpolicy gfk-gke-compat
kubectl describe clusterpolicy gfk-gke-compat | grep -A5 "Ready\|Error"
```

Delete and re-create any affected pods to force Kyverno to re-evaluate.

---

## Upgrading

### From v0.1.0-rc.3 to v0.1.0

> [!IMPORTANT]
> **Breaking Change: Gemini API Secret Key**
> The expected key within the `gemini-api-key` secret has changed from `key` to `gemini`. Existing secrets must be updated or re-created, otherwise the Gemini agent will fail to authenticate.
>
> To update an existing secret:
> ```bash
> kubectl patch secret gemini-api-key -n repo-agent-system --type=json -p='[{"op": "add", "path": "/data/gemini", "value": "'$(kubectl get secret gemini-api-key -n repo-agent-system -o jsonpath="{.data.key}")'"}]'
> ```

---

## Reference

| Resource | Description |
|---|---|
| [Release v0.1.0-rc.3](https://github.com/gke-labs/gemini-for-kubernetes-development/releases/tag/v0.1.0-rc.3) | Release manifest and installer |
| [RepoWatch CRD spec](https://github.com/gke-labs/gemini-for-kubernetes-development) | Full spec reference |
| [Kyverno docs](https://kyverno.io/docs/) | Policy engine reference |
| [Envoy Gateway](https://gateway.envoyproxy.io/) | Ingress controller |
| [KRO](https://kro.run/) | Kubernetes Resource Orchestration |
