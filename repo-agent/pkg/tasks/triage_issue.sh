#!/bin/bash
# Copyright 2026 The Kubernetes Authors.
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

set -e
set -o pipefail

# It expects the following environment variables to be set:
# - GEMINI_API_KEY
# - GITHUB_USER_TOKEN

export PROMPT_FILE={{ printf "%q" .PromptFile }}

function configureGemini {
    echo "Running configureGemini..."
    echo "creating /root/.gemini directory"
    mkdir -p /root/.gemini

    echo "writing gemini config"
    cat <<'EOF' > /root/.gemini/settings.json
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

    # Security: Hide GitHub OAuth token and config directory before executing untrusted code (gemini --yolo)
    # to prevent token exfiltration.
    local ORIG_GH_CONFIG_DIR="/root/.config/gh"
    local TEMP_GH_CONFIG_DIR="/tmp/gh-config-hidden-$(date +%s)"
    if [ -d "$ORIG_GH_CONFIG_DIR" ]; then
        mv "$ORIG_GH_CONFIG_DIR" "$TEMP_GH_CONFIG_DIR"
    fi
    local ORIG_GITHUB_USER_TOKEN="$GITHUB_USER_TOKEN"
    local ORIG_GITHUB_TOKEN="$GITHUB_TOKEN"
    unset GITHUB_USER_TOKEN
    unset GITHUB_TOKEN

    MODELS=( {{ range .Models }}{{ printf "%q" . }} {{ end }} )
    SUCCESS=false
    for MODEL in "${MODELS[@]}"; do
        echo "Trying model: $MODEL"
        if gemini --yolo --model "$MODEL" --output-format stream-json < "${PROMPT_FILE}" | /opt/repo-agent/gemini-stream-processor --output "$(dirname "${PROMPT_FILE}")/gemini-output.json"; then
             echo "Gemini execution successful with model: $MODEL"
             SUCCESS=true
             break
        else
             echo "Gemini execution failed with model: $MODEL. Retrying with next model..."
        fi
    done

    # Security: Restore GitHub config and token after untrusted code execution.
    if [ -d "$TEMP_GH_CONFIG_DIR" ]; then
        mv "$TEMP_GH_CONFIG_DIR" "$ORIG_GH_CONFIG_DIR"
    fi
    export GITHUB_USER_TOKEN="$ORIG_GITHUB_USER_TOKEN"
    export GITHUB_TOKEN="$ORIG_GITHUB_TOKEN"

    if [ "$SUCCESS" = false ]; then
        echo "All models failed."
        exit 1
    fi
{{ end }}
}

function installExtensions {
    echo "Installing extensions..."

    # Security: Hide GitHub OAuth token and config directory before executing untrusted code (extensions)
    local ORIG_GH_CONFIG_DIR="/root/.config/gh"
    local TEMP_GH_CONFIG_DIR="/tmp/gh-config-hidden-ext-$(date +%s)"
    if [ -d "$ORIG_GH_CONFIG_DIR" ]; then
        mv "$ORIG_GH_CONFIG_DIR" "$TEMP_GH_CONFIG_DIR"
    fi
    local ORIG_GITHUB_USER_TOKEN="$GITHUB_USER_TOKEN"
    local ORIG_GITHUB_TOKEN="$GITHUB_TOKEN"
    unset GITHUB_USER_TOKEN
    unset GITHUB_TOKEN

    {{- range .Extensions }}
    gemini extensions install {{ printf "%q" .Source }} {{ if .Ref }}--ref {{ printf "%q" .Ref }}{{ end }} --consent
    {{- end }}

    # Security: Restore GitHub config and token
    if [ -d "$TEMP_GH_CONFIG_DIR" ]; then
        mv "$TEMP_GH_CONFIG_DIR" "$ORIG_GH_CONFIG_DIR"
    fi
    export GITHUB_USER_TOKEN="$ORIG_GITHUB_USER_TOKEN"
    export GITHUB_TOKEN="$ORIG_GITHUB_TOKEN"
}

# Main execution
configureGemini
installExtensions
runGemini
