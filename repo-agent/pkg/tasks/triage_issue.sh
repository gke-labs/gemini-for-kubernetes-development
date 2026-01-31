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
    # remove agent thoughts using python to parse JSON
    python3 -c '
import sys
import json
import re

try:
    with open(sys.argv[1], "r") as f:
        content = f.read()
    
    # Clean up markdown code blocks if present
    content = re.sub(r"^```json\s*", "", content, flags=re.MULTILINE)
    content = re.sub(r"^```\s*$", "", content, flags=re.MULTILINE)

    # Find JSON block (simple approach: find first { and last })
    start = content.find("{")
    end = content.rfind("}")
    
    if start != -1 and end != -1:
        json_str = content[start:end+1]
        data = json.loads(json_str)
        kind = data.get("kind", "unknown")
        explanation = data.get("explanation", "")
        print(f"/kind {kind}")
        print(explanation)
    else:
        # Fallback if no JSON found
        print("Error: Could not parse JSON from agent output.")
        print(content)
except Exception as e:
    print(f"Error processing output: {e}")
    # Print original content as fallback
    print(content)
' "$(dirname "${PROMPT_FILE}")/raw-agent-output.txt" > "$(dirname "${PROMPT_FILE}")/agent-output.txt"
}

# Main execution
configureGemini
runGemini
