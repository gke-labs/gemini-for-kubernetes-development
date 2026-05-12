#!/bin/bash
set -e
set -o pipefail

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
    SUCCESS=false
    for MODEL in "${MODELS[@]}"; do
        echo "Trying model: $MODEL"
        if gemini --yolo --model "$MODEL" --output-format stream-json < ${PROMPT_FILE} | /opt/repo-agent/gemini-stream-processor --output "$(dirname "${PROMPT_FILE}")/gemini-output.json"; then
             echo "Gemini execution successful with model: $MODEL"
             SUCCESS=true
             break
        else
             echo "Gemini execution failed with model: $MODEL. Retrying with next model..."
        fi
    done

    if [ "$SUCCESS" = false ]; then
        echo "All models failed."
        exit 1
    fi
{{ end }}
}

function installExtensions {
    echo "Installing extensions..."
    {{- range .Extensions }}
    gemini extensions install "{{ .Source }}" {{ if .Ref }}--ref "{{ .Ref }}"{{ end }} --consent
    {{- end }}
}

# Main execution
configureGemini
installExtensions
runGemini
