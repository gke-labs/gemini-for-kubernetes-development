#!/bin/bash
# Copyright 2026 Google LLC
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#      http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

set -eo pipefail

SOURCE_NS="overseer-kcc"
ROBOT_SECRETS=("user-lovelace-coder-bot" "user-hopper-coder-bot" "user-ada-coder-bot" "user-reviewbot-robot")

echo "=== Coder Secrets Sync Utility ==="
echo "Source Namespace: ${SOURCE_NS}"

# 1. Fetch target namespaces that contain RepoWatches
echo "Scanning for active developer namespaces with RepoWatches..."
TARGET_NAMESPACES=$(kubectl get repowatches --all-namespaces -o jsonpath='{.items[*].metadata.namespace}' | tr ' ' '\n' | sort -u | grep -v -E "default|kube-system|repo-agent-system" || true)

if [ -z "${TARGET_NAMESPACES}" ]; then
  echo "No active developer namespaces found with RepoWatch resources."
  exit 0
fi

echo "Found active namespaces:"
echo "${TARGET_NAMESPACES}"
echo ""

for ns in ${TARGET_NAMESPACES}; do
  echo "----------------------------------------"
  echo "Syncing secrets to namespace: ${ns}"
  echo "----------------------------------------"

  # Randomly select a coder bot from the pool to act as the 'codebot-robot' fallback for this namespace
  CODER_BOTS=("user-lovelace-coder-bot" "user-hopper-coder-bot" "user-ada-coder-bot")
  RANDOM_INDEX=$(( RANDOM % ${#CODER_BOTS[@]} ))
  FALLBACK_BOT=${CODER_BOTS[$RANDOM_INDEX]}
  echo "Selected ${FALLBACK_BOT} as the codebot-robot fallback for namespace ${ns}"

  for secret_name in "${ROBOT_SECRETS[@]}"; do
    echo "Checking source secret ${secret_name}..."
    
    # Read source secret
    SECRET_JSON=$(kubectl get secret "${secret_name}" -n "${SOURCE_NS}" -o json 2>/dev/null || true)
    if [ -z "${SECRET_JSON}" ]; then
      echo "  [Warning] Source secret ${secret_name} not found in ${SOURCE_NS}. Skipping."
      continue
    fi

    # Decode keys
    TOKEN=$(echo "${SECRET_JSON}" | jq -r '.data.GITHUB_TOKEN // empty' | base64 -d || true)
    LOGIN=$(echo "${SECRET_JSON}" | jq -r '.data.GITHUB_LOGIN // empty' | base64 -d || true)
    EMAIL=$(echo "${SECRET_JSON}" | jq -r '.data.GITHUB_EMAIL // empty' | base64 -d || true)
    GEMINI=$(echo "${SECRET_JSON}" | jq -r '.data.GEMINI_API_KEY // empty' | base64 -d || true)

    if [ -z "${TOKEN}" ]; then
      echo "  [Warning] GITHUB_TOKEN is empty in ${secret_name}. Skipping."
      continue
    fi

    # Apply compatibility secret
    echo "  Applying secret ${secret_name} in ${ns} with compatibility mappings..."
    kubectl create secret generic "${secret_name}" -n "${ns}" \
      --from-literal=pat="${TOKEN}" \
      --from-literal=userid="${LOGIN}" \
      --from-literal=email="${EMAIL}" \
      --from-literal=name="${LOGIN}" \
      --from-literal=GITHUB_TOKEN="${TOKEN}" \
      --from-literal=GITHUB_LOGIN="${LOGIN}" \
      --from-literal=GITHUB_EMAIL="${EMAIL}" \
      --from-literal=GEMINI_API_KEY="${GEMINI}" \
      --dry-run=client -o yaml | kubectl apply -f -

    # If this is the chosen fallback bot for this namespace, also update 'codebot-robot' secret
    if [ "${secret_name}" == "${FALLBACK_BOT}" ]; then
      echo "  Updating legacy 'codebot-robot' fallback credentials in ${ns} using ${secret_name}..."
      kubectl create secret generic "codebot-robot" -n "${ns}" \
        --from-literal=pat="${TOKEN}" \
        --from-literal=userid="${LOGIN}" \
        --from-literal=email="${EMAIL}" \
        --from-literal=name="${LOGIN}" \
        --from-literal=GITHUB_TOKEN="${TOKEN}" \
        --from-literal=GITHUB_LOGIN="${LOGIN}" \
        --from-literal=GITHUB_EMAIL="${EMAIL}" \
        --from-literal=GEMINI_API_KEY="${GEMINI}" \
        --dry-run=client -o yaml | kubectl apply -f -
    fi
  done

  # 2. Restart sandbox pods in this namespace to apply updated configuration
  echo "Checking for active sandbox pods in ${ns}..."
  PODS=$(kubectl get pods -n "${ns}" -l "sandbox.gemini.google.com/type" -o jsonpath='{.items[*].metadata.name}' || true)
  for pod in ${PODS}; do
    echo "  Restarting sandbox pod ${pod}..."
    kubectl delete pod "${pod}" -n "${ns}" --wait=false || true
  done
done

echo "=== Sync complete! ==="
