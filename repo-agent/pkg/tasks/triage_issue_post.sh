#!/bin/bash
set -e

export PROMPT_FILE="{{ .PromptFile }}"

OUTPUT_DIR="$(dirname "${PROMPT_FILE}")"

cat "${OUTPUT_DIR}/raw-agent-output.txt"
# Extract prow /kind command and everything following it (including metadata) from agent response
sed -n '/^\/kind /,$p' "${OUTPUT_DIR}/raw-agent-output.txt" > "${OUTPUT_DIR}/agent-output.txt" || true
