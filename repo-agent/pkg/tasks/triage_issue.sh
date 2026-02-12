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
{{ if eq .AgentName "dummy" }}
    echo "Running in dummy mode..."
    echo "/kind bug" > "$(dirname "${PROMPT_FILE}")/raw-agent-output.txt"
    echo "This is a dummy response for triage." >> "$(dirname "${PROMPT_FILE}")/raw-agent-output.txt"
{{ else }}
    echo "running gemini in yolo mode"
    export GEMINI_API_KEY="${GEMINI_API_KEY}"
    local OUTPUT_FILE="$(dirname "${PROMPT_FILE}")/raw-agent-output.txt"
    if ! gemini --yolo --model {{ .Model }} < ${PROMPT_FILE} > "$OUTPUT_FILE" 2>&1; then
        echo "Gemini failed with model {{ .Model }}."
        if grep -qE "quota|429" "$OUTPUT_FILE"; then
             echo "Quota exhausted. Retrying with gemini-3-flash-preview..."
             gemini --yolo --model gemini-3-flash-preview < ${PROMPT_FILE} > "$OUTPUT_FILE" 2>&1
        fi
    fi
{{ end }}
    cat "$(dirname "${PROMPT_FILE}")/raw-agent-output.txt"
    # remove agent thoughts (extract prow command)
    grep "^/kind " "$(dirname "${PROMPT_FILE}")/raw-agent-output.txt" > "$(dirname "${PROMPT_FILE}")/agent-output.txt" || true
}

# Main execution
configureGemini
runGemini
