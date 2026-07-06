# Rotating and Syncing Coder Robot Credentials

This guide explains how to sync fresh GitHub Personal Access Tokens (PATs) for coder robots across GKE staging namespaces.

## Background
The orchestrator environment (`overseer-kcc`) manages a pool of coder and reviewer robots (e.g. `lovelace-coder-bot`, `hopper-coder-bot`, `ada-coder-bot`, `reviewbot-robot`).

When credentials for these robots are rotated:
1. The new credentials are updated in the central namespace (`overseer-kcc`).
2. The secrets are stored using the new schema keys: `GITHUB_TOKEN`, `GITHUB_LOGIN`, `GITHUB_EMAIL`, and `GEMINI_API_KEY`.
3. Legacy sandbox deployment structures expect the old schema keys: `pat`, `userid`, `email`, and `name`.

If a developer sandbox tries to run with rotated/new credentials without the compatibility mapping, tasks will fail with `401 Bad credentials` or `no oauth token found for github.com`.

---

## Automated Resolution (For all namespaces)
An automated sync script is provided to fetch the latest robot secrets from the central controller namespace, map them with compatibility keys, and propagate them to all active developer namespaces.

To sync all credentials and refresh active sandboxes, run:
```bash
./dev/tools/sync-coder-secrets.sh
```

This script will:
1. Find all active developer namespaces containing `RepoWatch` resources.
2. Extract the fresh keys from the central `overseer-kcc` namespace.
3. Apply compatibility-mapped secret entries in each target namespace.
4. Auto-update the legacy `codebot-robot` secret in each namespace to point to the fresh `lovelace` token as a default fallback.
5. Gracefully restart active sandbox pods to load the new credentials.

---

## Manual Resolution (For a single namespace)
If you want to manually update a single developer namespace (e.g. `ldanielmadariaga`) to use a fresh bot identity:

### Step 1: Copy and Map the Secret
Run this command to copy the secret from `overseer-kcc` to your target namespace with compatibility mappings (replace `<namespace>` with your developer namespace):
```bash
# Retrieve token details
TOKEN=$(kubectl get secret user-lovelace-coder-bot -n overseer-kcc -o jsonpath='{.data.GITHUB_TOKEN}' | base64 -d)
LOGIN=$(kubectl get secret user-lovelace-coder-bot -n overseer-kcc -o jsonpath='{.data.GITHUB_LOGIN}' | base64 -d)
EMAIL=$(kubectl get secret user-lovelace-coder-bot -n overseer-kcc -o jsonpath='{.data.GITHUB_EMAIL}' | base64 -d)

# Create compatibility secret
kubectl create secret generic user-lovelace-coder-bot -n <namespace> \
  --from-literal=pat="${TOKEN}" \
  --from-literal=userid="${LOGIN}" \
  --from-literal=email="${EMAIL}" \
  --from-literal=name="${LOGIN}" \
  --from-literal=GITHUB_TOKEN="${TOKEN}" \
  --from-literal=GITHUB_LOGIN="${LOGIN}" \
  --from-literal=GITHUB_EMAIL="${EMAIL}" \
  --dry-run=client -o yaml | kubectl apply -f -
```

### Step 2: Configure RepoWatch to use the new Robot
Patch your namespace's `RepoWatch` spec to use `user-lovelace-coder-bot` as the robot account:
```bash
kubectl patch repowatch k8s-config-connector -n <namespace> --type=merge -p '{"spec":{"issue":{"robotAccount":"user-lovelace-coder-bot"}}}'
```

### Step 3: Restart Sandboxes
Delete any active sandbox pods to force the deployment to reload with the new secrets:
```bash
kubectl delete pod k8s-config-connector-issue-11096 -n <namespace>
```
