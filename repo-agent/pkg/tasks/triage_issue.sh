#!/bin/bash
set -e

# It expects the following environment variables to be set:
# - GEMINI_API_KEY
# - GITHUB_USER_TOKEN

export PROMPT_FILE="{{ .PromptFile }}"

function configureGemini {
    echo "Running configureGemini..."
    echo "creating /root/.gemini directory"
    mkdir -p /root/.gemini

    echo "writing gemini config"
    cat <<EOF > /root/.gemini/settings.json
{
  "general": {
    "enableAutoUpdate": false,
    "retryFetchErrors": true
  }
}
EOF
}

function runGemini {
    echo "Running runGemini..."
    echo "running gemini in yolo mode"
    export GEMINI_API_KEY="${GEMINI_API_KEY}"
    gemini --yolo --model gemini-3-pro-preview < ${PROMPT_FILE} > "$(dirname "${PROMPT_FILE}")/raw-agent-output.txt" 2>&1
    cat "$(dirname "${PROMPT_FILE}")/raw-agent-output.txt"
    # remove agent thoughts (extract prow command)
    grep "^/kind " "$(dirname "${PROMPT_FILE}")/raw-agent-output.txt" > "$(dirname "${PROMPT_FILE}")/agent-output.txt" || true
}

# Main execution
configureGemini
runGemini
