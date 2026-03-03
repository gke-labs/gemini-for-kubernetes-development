#!/bin/bash
set -e

export PROMPT_FILE="{{ .PromptFile }}"

OUTPUT_DIR="$(dirname "${PROMPT_FILE}")"

cat "${OUTPUT_DIR}/raw-agent-output.txt"
# Extract prow /kind command from agent response
grep "^/kind " "${OUTPUT_DIR}/raw-agent-output.txt" > "${OUTPUT_DIR}/agent-output.txt" || true
