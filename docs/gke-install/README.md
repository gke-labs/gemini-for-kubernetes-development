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
  --from-literal=key="$GEMINI_API_KEY" \
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
development but are **not valid image names at runtime**.  On a `kind` cluster
the project's local build pipeline resolves them automatically; on GKE they
cause `ImagePullBackOff`.

The fix is a Kyverno `ClusterPolicy` with four mutation rules:

| Rule | Rewrites | To |
|---|---|---|
| `replace-repo-sandbox-initcontainer` | `ko://repo-agent/images/repo-sandbox` (init) | `ghcr.io/.../repo-sandbox:latest` |
| `replace-sandbox-main-container` | `ko://repo-agent/images/repo-sandbox` (main) | `ghcr.io/.../generic-golang:latest` |
| `replace-configdir-initcontainer` | `ko://repo-agent/images/configdir-cli` | `ghcr.io/.../configdir-cli:latest` |
| `replace-overseer-container` | `ko://overseer/images/overseer` | `ghcr.io/.../overseer:latest` |

A fifth rule (`mount-devcontainer-json`) is described below.

### Fix 2 — `devcontainer.json` mount for PR review pods

PR review sandbox pods use `envbuilder` as their container entrypoint.
`envbuilder` looks for a `devcontainer.json` at the path specified by
`ENVBUILDER_DEVCONTAINER_DIR` (set to `/` in the manifest), but the
`repowatch-controller` does not mount the `devcontainer-json` ConfigMap into
these pods.

Without the ConfigMap mounted, envbuilder exits immediately with:
```
error: open devcontainer.json: open /devcontainer.json: no such file or directory
```

The fifth Kyverno rule (`mount-devcontainer-json`) injects the volume and
mount into any pod in `repo-agent-system` whose main container is named
`sandbox`.

The `devcontainer-json` ConfigMap is also patched to use the pre-built
`generic-golang:latest` image as the `devcontainer.json` base, rather than
`mcr.microsoft.com/devcontainers/base:ubuntu`, which would trigger a full
feature-install build on every pod restart.

### Fix 3 — `gh pr checkout` fast-forward failure

When the Overseer agent creates an `investigate-failures` task, the pre-script
runs `gh pr checkout <N>`.  If external commits have been pushed to the PR
branch after the sandbox last checked it out, the local branch diverges from
upstream and `gh pr checkout` fails with:

```
fatal: Not possible to fast-forward, aborting.
```

The `devcontainer.json` `postCreateCommand` installs a wrapper at
`/usr/local/bin/gh` (which takes precedence over the system `gh` at
`/usr/bin/gh`) that intercepts `gh pr checkout` and replaces the fast-forward
pull with `git reset --hard upstream/<branch>`, ensuring the sandbox always
reflects the current upstream state.

---

## Creating a RepoWatch

After the system components are ready, create a `RepoWatch` CR to start
watching a repository:

```yaml
apiVersion: review.gemini.google.com/v1alpha1
kind: RepoWatch
metadata:
  name: my-repo
  namespace: my-repo          # create this namespace first
spec:
  repoURL: https://github.com/my-org/my-repo
  githubSecretName: github-token
  pollIntervalSeconds: 60
  review:
    llm:
      provider: gemini-cli
      apiKeySecretRef: gemini-api-key
    maxActiveSandboxes: 2
    workspaceDiskSize: 10Gi
  issue:
    llm:
      provider: gemini-cli
      apiKeySecretRef: gemini-api-key
    maxActiveSandboxes: 2
    workspaceDiskSize: 10Gi
```

Copy the required secrets into the RepoWatch namespace:

```bash
NS=my-repo
kubectl create namespace $NS

for SECRET in github-token gemini-api-key; do
  kubectl get secret $SECRET -n repo-agent-system -o json \
    | jq 'del(.metadata.resourceVersion,.metadata.uid,.metadata.creationTimestamp,.metadata.annotations) | .metadata.namespace = "'$NS'"' \
    | kubectl apply -f -
done

kubectl apply -f repowatch.yaml
```

---

## Accessing the UI

```bash
# Get the external IP of the pr-review-ui service
kubectl get svc -n repo-agent-system pr-review-ui \
  -o jsonpath='{.status.loadBalancer.ingress[0].ip}'
```

Open `https://<EXTERNAL_IP>` in a browser.

**Authentication:**
- **Single-user mode** (no OAuth App configured): The UI may show the repository
  dashboard directly.  If it shows "Please select or add a repository", navigate
  to `https://<EXTERNAL_IP>/api/auth/login` and complete the GitHub OAuth flow.
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

## Reference

| Resource | Description |
|---|---|
| [Release v0.1.0-rc.3](https://github.com/gke-labs/gemini-for-kubernetes-development/releases/tag/v0.1.0-rc.3) | Release manifest and installer |
| [RepoWatch CRD spec](https://github.com/gke-labs/gemini-for-kubernetes-development) | Full spec reference |
| [Kyverno docs](https://kyverno.io/docs/) | Policy engine reference |
| [Envoy Gateway](https://gateway.envoyproxy.io/) | Ingress controller |
| [KRO](https://kro.run/) | Kubernetes Resource Orchestration |
