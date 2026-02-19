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
    
    MODELS=( {{ range .Models }}"{{ . }}" {{ end }} )
    API_KEYS=( $(echo $GEMINI_API_KEY | tr ',' ' ') )
    SUCCESS=false
    for MODEL in "${MODELS[@]}"; do
        for API_KEY in "${API_KEYS[@]}"; do
            echo "Trying model: $MODEL with API key: ${API_KEY:0:4}..."
            if export GEMINI_API_KEY="${API_KEY}" && gemini --yolo --model "$MODEL" < ${PROMPT_FILE} > "$(dirname "${PROMPT_FILE}")/raw-agent-output.txt" 2>&1; then
                 echo "Gemini execution successful with model: $MODEL"
                 SUCCESS=true
                 break 2
            else
                 echo "Gemini execution failed with model: $MODEL and API key: ${API_KEY:0:4}. Retrying..."
            fi
        done
    done

    if [ "$SUCCESS" = false ]; then
        echo "All models failed."
        exit 1
    fi
{{ end }}
    cat "$(dirname "${PROMPT_FILE}")/raw-agent-output.txt"
    # remove agent thoughts (extract prow command)
    grep "^/kind " "$(dirname "${PROMPT_FILE}")/raw-agent-output.txt" > "$(dirname "${PROMPT_FILE}")/agent-output.txt" || true
}

# Main execution
configureGemini
runGemini
