#!/usr/bin/env bash
set -euo pipefail

if [[ $# -lt 2 ]]; then
    echo "Usage: $0 <secret-name> <github-token> [namespace1 namespace2 ...]"
    echo "Example: $0 user-argus-watcher-bot ghp_xxx"
    exit 1
fi

SECRET_NAME="$1"
GITHUB_TOKEN="$2"
shift 2

# If the user passed 'argus-watcher-bot' instead of 'user-argus-watcher-bot'
if [[ "${SECRET_NAME}" != "factory-user" && "${SECRET_NAME}" != user-* ]]; then
    SECRET_NAME="user-${SECRET_NAME}"
fi

NAMESPACES=("$@")
if [[ ${#NAMESPACES[@]} -eq 0 ]]; then
    NAMESPACES=("overseer-system" "overseer-kcc")
fi

echo "Verifying token against GitHub API..."
GH_LOGIN=$(curl -sS -f -H "Authorization: token ${GITHUB_TOKEN}" -H "User-Agent: overseer-secret-updater" https://api.github.com/user | grep -o '"login": *"[^"]*"' | head -n1 | cut -d'"' -f4)
if [[ -z "${GH_LOGIN}" ]]; then
    echo "Error: Failed to validate token with GitHub API"
    exit 1
fi
echo "Verified GitHub token belongs to user: ${GH_LOGIN}"

B64_TOKEN=$(echo -n "${GITHUB_TOKEN}" | base64 -w 0)

for ns in "${NAMESPACES[@]}"; do
    echo "Updating secret '${SECRET_NAME}' in namespace '${ns}'..."
    if kubectl get secret "${SECRET_NAME}" -n "${ns}" >/dev/null 2>&1; then
        kubectl patch secret "${SECRET_NAME}" -n "${ns}" --type='json' \
            -p="[{\"op\": \"replace\", \"path\": \"/data/GITHUB_TOKEN\", \"value\": \"${B64_TOKEN}\"}]"
        echo "  -> Successfully patched ${ns}/${SECRET_NAME}"
    else
        echo "  -> Warning: Secret '${SECRET_NAME}' not found in namespace '${ns}', skipping."
    fi
done

echo "Done updating '${SECRET_NAME}' across namespaces: ${NAMESPACES[*]}"
