#!/bin/bash
set -e

if [ "${DISABLE_GITHUB_PROXY:-false}" != "true" ]; then
    if [ ! -f /usr/local/bin/gh ]; then
        echo "creating gh wrapper script"
        cat <<'EOF' > /usr/local/bin/gh
#!/bin/bash
HTTPS_PROXY=http://github-portal.overseer-system.svc.cluster.local SSL_CERT_FILE="${SSL_CERT_FILE:-/etc/github-portal/ca/tls.crt}" /usr/bin/gh "$@"
EOF
        chmod +x /usr/local/bin/gh
    fi
fi

export PROMPT_FILE="{{ .PromptFile }}"

OUTPUT_DIR="$(dirname "${PROMPT_FILE}")"

cat "${OUTPUT_DIR}/raw-agent-output.txt"
# Extract prow /kind command from agent response
grep "^/kind " "${OUTPUT_DIR}/raw-agent-output.txt" > "${OUTPUT_DIR}/agent-output.txt" || true
