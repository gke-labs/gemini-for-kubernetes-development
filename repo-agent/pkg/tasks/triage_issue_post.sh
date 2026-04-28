#!/bin/bash
set -e

export PROMPT_FILE="{{ .PromptFile }}"

OUTPUT_DIR="$(dirname "${PROMPT_FILE}")"

cat "${OUTPUT_DIR}/raw-agent-output.txt"
# Extract prow /kind command from agent response
grep "^/kind " "${OUTPUT_DIR}/raw-agent-output.txt" > "${OUTPUT_DIR}/agent-output.txt" || true

{{- if .TraceabilityEnabled }}

# Append traceability metadata footer
cat >> "${OUTPUT_DIR}/agent-output.txt" <<EOF

---
<!-- repo-agent-metadata
sandbox-task: {{ .SandboxTaskNamespace }}/{{ .SandboxTaskName }}
sandbox-task-uid: {{ .SandboxTaskUID }}
sandbox: {{ .SandboxName }}
repowatch: {{ .RepoWatchName }}
task-type: {{ .TaskType }}
timestamp: {{ .Timestamp }}
-->
EOF
{{- end }}
