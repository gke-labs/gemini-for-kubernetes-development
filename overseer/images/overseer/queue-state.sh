#!/bin/bash
# Keeping the watch's queue state (./overseer/queues: incoming, processing,
# processed, journal.jsonl, chores_state.json) across a cycle that was cut
# short. Sourced by run.sh, from the repository's clone.
#
# The queue directory on disk is the queue's truth: factory watch writes it as
# it goes, and run.sh commits and pushes it to the state branch only when a
# cycle ends. A pod restarted mid-cycle (an upgrade, an eviction, a crash)
# keeps its disk, so what the cycle wrote since the last push is still there;
# the next cycle's reset of the clone must not throw it away.

# QUEUE_DIR is the queue directory, relative to the clone; QUEUE_CARRY is
# where carryQueueState keeps a copy of it while the clone is reset, outside
# the clone so that neither the reset nor git clean touches it.
QUEUE_DIR="${QUEUE_DIR:-./overseer/queues}"
QUEUE_CARRY="${QUEUE_CARRY:-/workspaces/.queue-carry}"

# carryQueueState copies the queue directory aside before the clone is reset.
# A copy already there is one a previous restoreQueueState did not finish
# with: it is the whole queue, where the directory may be half restored, so
# it is kept.
function carryQueueState {
    if [ -d "$QUEUE_CARRY" ]; then
        echo "$(date): Keeping the queue state carried from an interrupted cycle ($QUEUE_CARRY)."
        return 0
    fi
    if [ -d "$QUEUE_DIR" ]; then
        mkdir -p "$(dirname "$QUEUE_CARRY")"
        cp -a "$QUEUE_DIR" "$QUEUE_CARRY.tmp" && mv "$QUEUE_CARRY.tmp" "$QUEUE_CARRY"
    fi
}

# restoreQueueState puts the carried queue directory back over the checked-out
# state branch and commits it if it differs: what a cut-short cycle wrote
# after the last push, or the whole queue when a failed rebase reset the
# branch. A cycle that ended normally pushed everything, so then there is
# nothing to commit. With PUSH_QUEUE_STATE=true the commit is pushed at once.
function restoreQueueState {
    local branch="$1"
    [ -d "$QUEUE_CARRY" ] || return 0
    rm -rf "$QUEUE_DIR"
    mkdir -p "$(dirname "$QUEUE_DIR")"
    cp -a "$QUEUE_CARRY" "$QUEUE_DIR"
    git add -A "$QUEUE_DIR"
    if ! git diff --cached --quiet -- "$QUEUE_DIR"; then
        echo "$(date): Keeping queue state from an interrupted cycle on branch $branch..."
        git commit -q -m "chore(watch): keep queue state from an interrupted cycle at $(date -u +%Y-%m-%dT%H:%M:%SZ)" -- "$QUEUE_DIR"
        if [ "${PUSH_QUEUE_STATE:-true}" = "true" ]; then
            git push -q origin "$branch" --force || echo "$(date): Pushing the kept queue state failed; the next sync pushes it."
        fi
    fi
    rm -rf "$QUEUE_CARRY"
}

# syncQueueState commits the queue directory and pushes it to the state
# branch, when that branch is checked out (mid-reset or mid-rebase it is not,
# and the next cycle's carry keeps the state instead).
function syncQueueState {
    local branch="$1" message="$2"
    [ -d "$QUEUE_DIR" ] || return 0
    if [ "$(git symbolic-ref --short -q HEAD)" != "$branch" ] || [ -d .git/rebase-merge ] || [ -d .git/rebase-apply ]; then
        echo "$(date): Not on $branch; the queue state stays on disk for the next cycle to keep."
        return 0
    fi
    git add -A "$QUEUE_DIR"
    if ! git diff --cached --quiet -- "$QUEUE_DIR"; then
        echo "$(date): Committing and pushing queue updates to branch $branch..."
        git commit -q -m "$message" -- "$QUEUE_DIR"
        git push origin "$branch" --force
    else
        echo "$(date): No queue changes to push."
    fi
}

# stopOverseer runs on SIGTERM (a pod deletion) and SIGINT: it stops factory
# watch, gives the cycle (CYCLE_PID) STOP_WAIT seconds (default 60, within the
# pod's termination grace period) to end, then commits and pushes the queue
# state to STATE_BRANCH before exiting. Best effort: whatever it cannot push
# stays on the disk, and the next start keeps it (carryQueueState).
function stopOverseer {
    trap - TERM INT
    echo "$(date): Stopping: shutting factory watch down and syncing the queue state..."
    # Drained, so nothing new is dispatched and the orchestrator does not
    # start; bootstrap.sh clears the marker on the next start.
    touch "${DRAIN_MARKER:-/workspaces/.do_not_process}" 2>/dev/null || true
    pkill -TERM -f "factory watch" 2>/dev/null || true
    local waited=0
    while [ -n "$CYCLE_PID" ] && kill -0 "$CYCLE_PID" 2>/dev/null && [ "$waited" -lt "${STOP_WAIT:-60}" ]; do
        sleep 1
        waited=$((waited + 1))
    done
    if [ -n "$CYCLE_PID" ] && kill -0 "$CYCLE_PID" 2>/dev/null; then
        pkill -KILL -f "factory watch" 2>/dev/null || true
        pkill -KILL -P "$CYCLE_PID" 2>/dev/null || true
        kill -KILL "$CYCLE_PID" 2>/dev/null || true
        rm -f .git/index.lock
    fi
    syncQueueState "$STATE_BRANCH" "chore(watch): sync queue state on shutdown at $(date -u +%Y-%m-%dT%H:%M:%SZ)" || true
    exit 0
}
