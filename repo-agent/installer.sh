#!/bin/bash
set -euo pipefail

echo "Checking params"


: "${GEMINI_API_KEY:?Error: GEMINI_API_KEY is not set. Please set it before running this script.}"
: "${GITHUB_PAT:?Error: GITHUB_PAT is not set. Please set it before running this script.}"
: ${NAMESPACE:=default}

echo "Getting git config..."
GIT_USER_NAME=$(git config --global user.name)
if [ -z "$GIT_USER_NAME" ]; then
    echo >&2 "Error: git config --global user.name is not set. Please configure it with 'git config --global user.name \"Your Name\"'."
    exit 1
fi
GIT_USER_EMAIL=$(git config --global user.email)
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
helm install eg oci://docker.io/envoyproxy/gateway-helm --version v1.5.2 -n envoy-gateway-system --create-namespace
sleep 5
kubectl wait --timeout=5m -n envoy-gateway-system deployment/envoy-gateway --for=condition=Available

echo "Installing KRO"
KRO_VERSION=0.5.1
helm install kro oci://registry.k8s.io/kro/charts/kro --namespace kro --create-namespace --version=${KRO_VERSION}
helm -n kro list
sleep 5
kubectl get pods -n kro

echo "Installing Sandbox"
kubectl apply -f https://github.com/kubernetes-sigs/agent-sandbox/releases/download/v0.1.0-rc.0/manifest.yaml

# Create namespace if it doesn't exist
kubectl get namespace ${NAMESPACE} >/dev/null 2>&1 || kubectl create namespace ${NAMESPACE}

echo "Create secrets for use.."
kubectl create secret -n ${NAMESPACE} generic gemini-vscode-tokens --from-literal=gemini=${GEMINI_API_KEY}
kubectl create secret -n ${NAMESPACE} generic github-pat --from-literal=pat=${GITHUB_PAT} --from-literal=name="`git config --global user.name`" --from-literal=email=`git config --global user.email`

echo "Install repo agent"
: ${REPO_AGENT_VERSION:=v0.1.0-rc.2}
kubectl apply -f https://github.com/gke-labs/gemini-for-kubernetes-development/releases/download/${REPO_AGENT_VERSION}/manifest.yaml

echo "Setting up repo-agent for namespace ${NAMESPACE}"
URL_PREFIX=https://raw.githubusercontent.com/gke-labs/gemini-for-kubernetes-development/refs/tags/${REPO_AGENT_VERSION}/repo-agent/examples
curl ${URL_PREFIX}/go-configmap-devcontainer.yaml  | kubectl apply -n ${NAMESPACE} -f -
curl ${URL_PREFIX}/sandbox-rbac.yaml  | kubectl apply -n ${NAMESPACE} -f -
kubectl set subject clusterrolebinding review-sandbox --serviceaccount=${NAMESPACE}:review-sandbox
kubectl set subject clusterrolebinding issue-sandbox --serviceaccount=${NAMESPACE}:issue-sandbox
