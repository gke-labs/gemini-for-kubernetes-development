#!/bin/bash
set -e

# Usage: ./update-overseer-factory.sh <overseer-name> [timeout-in-seconds] [force-after-seconds]
# Example: ./update-overseer-factory.sh kcc 1800 1200
#
# Drains the overseer's watch, waits for running tasks, then restarts the
# overseer's pod on its updated spec. After force-after-seconds (default
# 1200, FORCE_AFTER; 0 waits up to the timeout) it stops waiting: a task
# still running goes on in its own sandbox, and the new overseer recovers
# it from its queue and follows it to its end by its run name.

OVERSEER_INPUT="${1:-kcc}"
OVERSEER_NAME="${OVERSEER_INPUT#overseer-}"
POD_NAME="overseer-${OVERSEER_NAME}"
NAMESPACE="overseer-${OVERSEER_NAME}"
TIMEOUT="${2:-1800}"
FORCE_AFTER="${3:-${FORCE_AFTER:-1200}}"

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
INTERVAL=10
# Wall-clock time: a pass of the loop takes far longer than INTERVAL on a
# namespace with many sandboxes, so counting passes undercounts.
START=$(date +%s)

while true; do
    ELAPSED=$(( $(date +%s) - START ))
    ACTIVE_COUNT=0
    ACTIVE_NAMES=""

    # One call for every sandbox's name, last task state and replicas
    # (ignoring the overseer's own): a get per sandbox is hundreds of calls
    # a pass in a namespace like overseer-kcc's.
    while read -r sb state replicas; do
        [ -n "$sb" ] || continue
        case "$sb" in overseer-*) continue ;; esac
        if [ "$state" = "Running" ] && [ "${replicas:-0}" -gt 0 ] 2>/dev/null; then
            ACTIVE_COUNT=$((ACTIVE_COUNT + 1))
            ACTIVE_NAMES="${ACTIVE_NAMES} ${sb}"
        fi
    done < <(kubectl get sandboxes.agents.x-k8s.io -n "$NAMESPACE" \
        -o jsonpath='{range .items[*]}{.metadata.name}{" "}{.metadata.annotations.sandbox\.gemini\.google\.com/last-task-state}{" "}{.spec.replicas}{"\n"}{end}' 2>/dev/null || true)

    if [ "$ACTIVE_COUNT" -eq 0 ]; then
        echo "--> All pending tasks have completed (0 active task sandboxes running)."
        break
    fi

    if [ "$FORCE_AFTER" -gt 0 ] && [ "$ELAPSED" -ge "$FORCE_AFTER" ]; then
        echo "--> ${ACTIVE_COUNT} task sandbox(es) still running after ${FORCE_AFTER}s:${ACTIVE_NAMES}"
        echo "    Upgrading anyway: their runs go on in their own sandboxes, and the new overseer recovers and follows them."
        break
    fi

    if [ "$ELAPSED" -ge "$TIMEOUT" ]; then
        echo "Error: Timeout (${TIMEOUT}s) reached waiting for active tasks to complete. Aborting upgrade." >&2
        exit 1
    fi

    echo "[+${ELAPSED}s] Waiting... (${ACTIVE_COUNT} active task sandbox(es) still running:${ACTIVE_NAMES})"
    sleep "$INTERVAL"
done

echo ""
echo "[Step 3] Cleaning up DO NOT PROCESS drain flags before recreation..."
kubectl exec -n "$NAMESPACE" "$POD_NAME" -- rm -f /workspaces/.do_not_process /workspaces/do_not_process /workspaces/.drain /workspaces/drain /workspaces/queues/.do_not_process /workspaces/queues/do_not_process /workspaces/queues/.drain /workspaces/queues/drain || true

echo ""
echo "[Step 4] Nudging the Overseer object (${OVERSEER_NAME}) to reconcile its sandbox's spec, and clearing upgrade mode..."
NOW=$(date -u +"%Y-%m-%dT%H:%M:%SZ")
kubectl annotate overseer.overseer.gemini.google.com "${OVERSEER_NAME}" \
    overseer.gemini.google.com/recreate-timestamp="$NOW" \
    overseer.gemini.google.com/upgrade-mode- \
    overseer.gemini.google.com/upgrade-timestamp- --overwrite || {
    echo "Warning: Failed to annotate overseer CR. Trying patch..."
    kubectl patch overseer.overseer.gemini.google.com "${OVERSEER_NAME}" --type=merge -p "{\"metadata\":{\"annotations\":{\"overseer.gemini.google.com/recreate-timestamp\":\"${NOW}\",\"overseer.gemini.google.com/upgrade-mode\":null,\"overseer.gemini.google.com/upgrade-timestamp\":null}}}" || true
}

# The controller updates the overseer sandbox's spec (image, env) when it
# changes, but agent-sandbox never changes a running pod. Restarting the
# pod picks the spec up, on the same disk: deleting the Sandbox would also
# delete its workspace PVC (queue checkout, logs, caches).
echo ""
echo "[Step 5] Restarting the overseer pod (${POD_NAME}) on its sandbox's spec..."
spec_image() { kubectl get sandbox.agents.x-k8s.io -n "$NAMESPACE" "$POD_NAME" -o jsonpath='{.spec.podTemplate.spec.containers[0].image}' 2>/dev/null || true; }
pod_image() { kubectl get pod -n "$NAMESPACE" "$POD_NAME" -o jsonpath='{.spec.containers[0].image}' 2>/dev/null || true; }
pod_uid() { kubectl get pod -n "$NAMESPACE" "$POD_NAME" -o jsonpath='{.metadata.uid}' 2>/dev/null || true; }
# give the controller a minute to write the new spec
for _ in $(seq 1 12); do
    [ "$(spec_image)" != "$(pod_image)" ] && break
    sleep 5
done
echo "    sandbox spec: $(spec_image)"
echo "    running pod:  $(pod_image)"
OLD_UID="$(pod_uid)"
kubectl delete pod -n "$NAMESPACE" "$POD_NAME" --wait=false || true
RESTARTED=""
for _ in $(seq 1 60); do
    sleep 5
    UID_NOW="$(pod_uid)"
    PHASE=$(kubectl get pod -n "$NAMESPACE" "$POD_NAME" -o jsonpath='{.status.phase}' 2>/dev/null || true)
    if [ -n "$UID_NOW" ] && [ "$UID_NOW" != "$OLD_UID" ] && [ "$PHASE" = "Running" ]; then
        RESTARTED=1
        break
    fi
done
if [ -n "$RESTARTED" ]; then
    echo "--> The overseer pod is running again on $(pod_image)."
else
    # No new pod after 5 minutes: fall back to deleting the Sandbox, which
    # the controller recreates (with a new, empty workspace disk).
    echo "Warning: no new overseer pod after 5 minutes; deleting the sandbox for the controller to recreate." >&2
    kubectl delete sandbox.agents.x-k8s.io -n "$NAMESPACE" "$POD_NAME" --wait=false || true
    kubectl annotate overseer.overseer.gemini.google.com "${OVERSEER_NAME}" \
        overseer.gemini.google.com/recreate-timestamp="$(date -u +"%Y-%m-%dT%H:%M:%SZ")" --overwrite || true
fi

echo ""
echo "=========================================================="
echo " Upgrade done for overseer '${OVERSEER_NAME}'."
echo " Check progress with: kubectl get pods -n ${NAMESPACE} -w"
echo "=========================================================="
