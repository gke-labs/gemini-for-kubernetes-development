#!/bin/bash
set -e

MODE=$1
TARGET=$2

LOG_DIR="/workspaces/logs"
SUMMARY_FILE="$LOG_DIR/summary-${MODE//--/}-$TARGET.log"

echo "Generating $MODE summary for $TARGET using Gemini..."

ALL_LOGS=""

if [ "$MODE" == "--daily" ]; then
  DATE_STR=$(echo "$TARGET" | tr -d '-')
  
  for log in "$LOG_DIR"/run-"$DATE_STR"-*.log; do
    if [ -f "$log" ]; then
      ALL_LOGS="$ALL_LOGS"$'\n\n'"--- Log: $(basename "$log") ---"$'\n'"$(cat "$log")"
    fi
  done
  
elif [ "$MODE" == "--weekly" ]; then
  # Aggregate daily summaries found in the last 7 days
  for log in $(find "$LOG_DIR" -type f -name "summary-daily-*.log" -mtime -7); do
    ALL_LOGS="$ALL_LOGS"$'\n\n'"--- Daily Summary: $(basename "$log") ---"$'\n'"$(cat "$log")"
  done
fi

if [ -z "$ALL_LOGS" ]; then
  echo "No logs found for $TARGET."
  exit 0
fi

# Get resource status
CURRENT_NS="${NAMESPACE:-overseer-kcc}"
RESOURCE_STATUS="Pods in $CURRENT_NS:"$'\n'"$(kubectl get pods -n "$CURRENT_NS" --no-headers || true)"$'\n\n'"Sandboxes:"$'\n'
RESOURCE_STATUS="$RESOURCE_STATUS""$(kubectl get sandboxes.agents.x-k8s.io -A --no-headers || true)"$'\n\n'"SandboxTasks:"$'\n'
RESOURCE_STATUS="$RESOURCE_STATUS""$(kubectl get sandboxtasks.custom.agents.x-k8s.io -A --no-headers || true)"

PROMPT="You are the Overseer Summarizer. Your job is to generate a clear, concise summary report based on the provided logs and resource status.

Target: $TARGET ($MODE)

Input Logs:
$ALL_LOGS

Current Kubernetes Resource Status:
$RESOURCE_STATUS

Please generate a report in Markdown containing:
1. **Summary of Actions Taken**: High-level summary of what the agent did.
2. **Failures & Issues**: List any failed pods, tasks, or errors encountered (check the Resource Status and logs).
3. **Status Table**: A table of resources and their status if applicable.

Be concise and professional."

# Call Gemini
gemini --yolo "$PROMPT" > "$SUMMARY_FILE"

# Print summary to stdout
cat "$SUMMARY_FILE"


