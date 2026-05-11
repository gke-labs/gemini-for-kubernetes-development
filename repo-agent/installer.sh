#!/bin/bash
set -euo pipefail

echo "Checking params"

# Installation mode: personal or multi-user
: ${INSTALL_MODE:="single-user"}

if [ "$INSTALL_MODE" != "single-user" ] && [ "$INSTALL_MODE" != "multi-user" ]; then
    echo "Error: INSTALL_MODE must be either 'single-user' or 'multi-user'."
    exit 1
fi

# Required environment variables
if [ "$INSTALL_MODE" = "single-user" ]; then
: "${GEMINI_API_KEY:?Error: GEMINI_API_KEY is not set. It is required for 'single-user' installation mode.}"
: "${GITHUB_PAT:?Error: GITHUB_PAT is not set. It is required for 'single-user' installation mode.}"
else
# GITHUB_PAT is optional for multi-user mode if OAuth flow is used
: "${GITHUB_CLIENT_ID:?Error: GITHUB_CLIENT_ID is not set. Is is required for 'multi-user' installation mode.}" 
: "${GITHUB_CLIENT_SECRET:?Error: GITHUB_CLIENT_SECRET is not set. Is is required for 'multi-user' installation mode.}" 
fi

: ${ENVOY_GW_VERSION:=v1.5.2}
: ${AGENT_SANDBOX_VERSION:=v0.4.5}
: ${REPO_AGENT_VERSION:=v0.1.0-rc.3}


echo "Getting git config..."
GIT_USER_NAME=$(git config --global user.name || true)
if [ -z "$GIT_USER_NAME" ]; then
    echo >&2 "Error: git config --global user.name is not set. Please configure it with 'git config --global user.name \"Your Name\"'."
    exit 1
fi
GIT_USER_EMAIL=$(git config --global user.email || true)
if [ -z "$GIT_USER_EMAIL" ]; then
    echo >&2 "Error: git config --global user.email is not set. Please configure it with 'git config --global user.email \"email@domain.com\"'."
    exit 1
fi

echo "Checking for prerequisites..."
command -v kind >/dev/null 2>&1 || { echo >&2 "kind not found. Please install it. https://kind.sigs.k8s.io/docs/user/quick-start/#installation"; exit 1; }
command -v kubectl >/dev/null 2>&1 || { echo >&2 "kubectl not found. Please install it. https://kubernetes.io/docs/tasks/tools/install-kubectl/"; exit 1; }
command -v helm >/dev/null 2>&1 || { echo >&2 "helm not found. Please install it. https://helm.sh/docs/intro/install/"; exit 1; }
echo "All prerequisites are installed."

echo "Installing Envoy GW"
helm install eg oci://docker.io/envoyproxy/gateway-helm --version ${ENVOY_GW_VERSION} -n envoy-gateway-system --create-namespace
sleep 5
kubectl wait --timeout=5m -n envoy-gateway-system deployment/envoy-gateway --for=condition=Available

echo "Uninstalling KRO (if present)"
helm uninstall kro --namespace kro || true
kubectl delete namespace kro || true

echo "Installing Sandbox"
kubectl apply -f https://github.com/kubernetes-sigs/agent-sandbox/releases/download/${AGENT_SANDBOX_VERSION}/manifest.yaml

echo "Install repo agent"
kubectl apply -f https://github.com/gke-labs/gemini-for-kubernetes-development/releases/download/${REPO_AGENT_VERSION}/manifest.yaml
kubectl create secret -n repo-agent-system generic gemini-vscode-tokens --from-literal=gemini="${GEMINI_API_KEY:-}" --dry-run=client -o yaml | kubectl apply -f -
if [ ! -z "${ANTHROPIC_API_KEY:-}" ]; then
  echo "Creating Anthropic API key secret"
  kubectl create secret -n repo-agent-system generic anthropic-api-key --from-literal=claude=${ANTHROPIC_API_KEY} --dry-run=client -o yaml | kubectl apply -f -
else
  echo "No Anthropic API key (ANTHROPIC_API_KEY) provided, skipping creation of secret"
fi

if [ -n "${GITHUB_PAT:-}" ]; then
  kubectl create secret -n repo-agent-system generic github-pat --from-literal=pat=${GITHUB_PAT} --from-literal=name="`git config --global user.name`" --from-literal=email=`git config --global user.email` --dry-run=client -o yaml | kubectl apply -f -
else
  # Create a placeholder secret with an empty PAT.
  # This prevents other components (like the controller) from crashing due to a missing secret,
  # while allowing them to detect the missing token and wait gracefully for the user to log in.
  kubectl create secret -n repo-agent-system generic github-pat --from-literal=pat="" --from-literal=name="`git config --global user.name`" --from-literal=email=`git config --global user.email` --dry-run=client -o yaml | kubectl apply -f -
fi

# TODO (barney-s): Refactor this once we cleanup the single vs multi user installation process
if [ "$INSTALL_MODE" != "multi-user" ]; then
  echo "Setting up repo-agent for namespace ${NAMESPACE} for single user"

  # Create github pat secret for the API
  kubectl create secret -n repo-agent-system generic github-token \
    --from-literal=token=${GITHUB_PAT} \
    --dry-run=client -o yaml | kubectl apply -f -;
else
  echo "Setting up repo-agent for multi-user mode"
  # Create github-token secret for the API, optionally including OAuth credentials
  cmd="kubectl create secret -n repo-agent-system generic github-token \
    --from-literal=github-client-id=${GITHUB_CLIENT_ID} \
    --from-literal=github-client-secret=${GITHUB_CLIENT_SECRET}"

  if [ -n "${GITHUB_PAT:-}" ]; then
      cmd="$cmd --from-literal=token=${GITHUB_PAT}"
  fi

  $cmd --dry-run=client -o yaml | kubectl apply -f -;
fi
