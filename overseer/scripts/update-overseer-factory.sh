#!/bin/bash
set -e

# Usage: ./upgrade.sh <overseer-name> [timeout-in-seconds]
# Example: ./upgrade.sh kcc 1800

OVERSEER_INPUT="${1:-kcc}"
OVERSEER_NAME="${OVERSEER_INPUT#overseer-}"
POD_NAME="overseer-${OVERSEER_NAME}"
NAMESPACE="overseer-${OVERSEER_NAME}"
TIMEOUT="${2:-1800}"

echo "=========================================================="
echo " Starting Safe Upgrade for Overseer: ${OVERSEER_NAME}"
echo " Namespace: ${NAMESPACE}, Pod/Sandbox: ${POD_NAME}"
echo "=========================================================="

if ! kubectl get namespace "$NAMESPACE" >/dev/null 2>&1; then
    echo "Namespace '${NAMESPACE}' not found. Exiting early without error."
    exit 0
fi

echo ""
echo "[Step 1] Marking watch loop as DO NOT PROCESS (/workspaces/.do_not_process) and setting upgrade mode on Overseer CR..."
NOW=$(date -u +"%Y-%m-%dT%H:%M:%SZ")
kubectl annotate overseer.overseer.gemini.google.com "${OVERSEER_NAME}" \
    overseer.gemini.google.com/upgrade-mode="true" \
    overseer.gemini.google.com/upgrade-timestamp="$NOW" --overwrite || {
    echo "Warning: Failed to annotate overseer CR with upgrade-mode."
}
kubectl exec -n "$NAMESPACE" "$POD_NAME" -- touch /workspaces/.do_not_process || {
    echo "Warning: Could not touch /workspaces/.do_not_process in pod ${POD_NAME}. Pod might not be running or ready."
}

echo ""
echo "[Step 2] Waiting for existing sandboxes with pending tasks to complete..."
ELAPSED=0
INTERVAL=10

while true; do
    ACTIVE_COUNT=0
    
    # Check running sandboxes in the namespace (ignoring the overseer pod itself)
    SB_NAMES=$(kubectl get sandboxes.agents.x-k8s.io -n "$NAMESPACE" -o jsonpath='{range .items[*]}{.metadata.name}{"\n"}{end}' 2>/dev/null | grep -v "^overseer-" || true)
    
    for sb in $SB_NAMES; do
        STATE=$(kubectl get sandbox.agents.x-k8s.io "$sb" -n "$NAMESPACE" -o jsonpath='{.metadata.annotations.sandbox\.gemini\.google\.com/last-task-state}' 2>/dev/null || true)
        REPLICAS=$(kubectl get sandbox.agents.x-k8s.io "$sb" -n "$NAMESPACE" -o jsonpath='{.spec.replicas}' 2>/dev/null || echo 0)
        if [ "$STATE" = "Running" ] && [ "${REPLICAS:-0}" -gt 0 ]; then
            ACTIVE_COUNT=$((ACTIVE_COUNT + 1))
        fi
    done

    if [ "$ACTIVE_COUNT" -eq 0 ]; then
        echo "--> All pending tasks have completed (0 active task sandboxes running)."
        break
    fi

    if [ "$ELAPSED" -ge "$TIMEOUT" ]; then
        echo "Error: Timeout (${TIMEOUT}s) reached waiting for active tasks to complete. Aborting upgrade." >&2
        exit 1
    fi

    echo "[+${ELAPSED}s] Waiting... (${ACTIVE_COUNT} active task sandbox(es) still running)"
    sleep "$INTERVAL"
    ELAPSED=$((ELAPSED + INTERVAL))
done

echo ""
echo "[Step 3] Cleaning up DO NOT PROCESS drain flags before recreation..."
kubectl exec -n "$NAMESPACE" "$POD_NAME" -- rm -f /workspaces/.do_not_process /workspaces/do_not_process /workspaces/.drain /workspaces/drain /workspaces/queues/.do_not_process /workspaces/queues/do_not_process /workspaces/queues/.drain /workspaces/queues/drain || true

echo ""
echo "[Step 4] Deleting the overseer sandbox CR (${POD_NAME})..."
kubectl delete sandbox.agents.x-k8s.io -n "$NAMESPACE" "$POD_NAME" --wait=false || true

echo ""
echo "[Step 5] Nudging the Overseer object (${OVERSEER_NAME}) to trigger recreation and clearing upgrade mode..."
NOW=$(date -u +"%Y-%m-%dT%H:%M:%SZ")
kubectl annotate overseer.overseer.gemini.google.com "${OVERSEER_NAME}" \
    overseer.gemini.google.com/recreate-timestamp="$NOW" \
    overseer.gemini.google.com/upgrade-mode- \
    overseer.gemini.google.com/upgrade-timestamp- --overwrite || {
    echo "Warning: Failed to annotate overseer CR. Trying patch..."
    kubectl patch overseer.overseer.gemini.google.com "${OVERSEER_NAME}" --type=merge -p "{\"metadata\":{\"annotations\":{\"overseer.gemini.google.com/recreate-timestamp\":\"${NOW}\",\"overseer.gemini.google.com/upgrade-mode\":null,\"overseer.gemini.google.com/upgrade-timestamp\":null}}}" || true
}

echo ""
echo "=========================================================="
echo " Upgrade initiated successfully! Overseer '${OVERSEER_NAME}' is recreating."
echo " Check progress with: kubectl get pods -n ${NAMESPACE} -w"
echo "=========================================================="
