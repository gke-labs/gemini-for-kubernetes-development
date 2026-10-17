#!/bin/bash
# Tests for queue-state.sh, with a throwaway clone and remote. Run by
# overseer/pkg/overseer's TestQueueStateScript, or directly.
set -euo pipefail

HERE="$(cd "$(dirname "$0")" && pwd)"
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT
export GIT_AUTHOR_NAME=t GIT_AUTHOR_EMAIL=t@example.com GIT_COMMITTER_NAME=t GIT_COMMITTER_EMAIL=t@example.com
export QUEUE_CARRY="$WORK/carry" DRAIN_MARKER="$WORK/drain" STATE_BRANCH=overseer PUSH_QUEUE_STATE=true
# shellcheck source=queue-state.sh
source "$HERE/queue-state.sh"

fail() { echo "FAIL: $*" >&2; exit 1; }
remote_has() { git --git-dir="$WORK/origin.git" cat-file -e "overseer:$1" 2>/dev/null; }

# A remote with main and a state branch holding one processed task and a
# journal line; a clone of it on the state branch, as run.sh leaves it.
setup() {
    rm -rf "$WORK"/*
    git init -q --bare -b main "$WORK/origin.git"
    git clone -q "$WORK/origin.git" "$WORK/clone" 2>/dev/null
    cd "$WORK/clone"
    echo code > code.txt && git add code.txt && git commit -q -m code && git push -q origin main
    git checkout -q -b overseer
    mkdir -p overseer/queues/processed
    echo a > overseer/queues/processed/task-a.yaml
    echo '{"event":"Created"}' > overseer/queues/journal.jsonl
    git add overseer && git commit -q -m "chore(watch): sync queue state" && git push -q origin overseer
}

# The start of a cycle, as runWatchCycle does it: carry, reset and clean,
# main moves on and is pulled, the state branch is rebased (or, when
# $1 = fail, reset to main as the failed-rebase fallback), restore.
cycle_start() {
    carryQueueState
    git reset -q --hard HEAD
    git clean -q -fd
    git checkout -q main
    echo more >> code.txt && git commit -q -am "main moves on"
    git checkout -q overseer
    if [ "${1:-}" = fail ]; then git reset -q --hard main; else git rebase -q main; fi
    restoreQueueState overseer
}

# A cycle cut short: a new processed task (untracked) and a journal line
# (tracked, modified) since the last push survive the next cycle's reset,
# and are committed and pushed.
setup
echo b > overseer/queues/processed/task-b.yaml
echo '{"event":"Failed"}' >> overseer/queues/journal.jsonl
cycle_start
[ -f overseer/queues/processed/task-b.yaml ] || fail "the cut-short cycle's processed task was lost"
[ "$(wc -l < overseer/queues/journal.jsonl)" -eq 2 ] || fail "the cut-short cycle's journal line was lost"
[ -z "$(git status --porcelain -- overseer/queues)" ] || fail "the kept queue state was not committed"
remote_has overseer/queues/processed/task-b.yaml || fail "the kept queue state was not pushed"
[ -f code.txt ] && grep -q more code.txt || fail "the state branch was not rebased onto main"
[ ! -d "$QUEUE_CARRY" ] || fail "the carry was left behind"
echo "ok: a cut-short cycle's queue state is kept"

# A cycle that ended normally pushed everything: nothing to commit.
setup
before=$(git rev-list --count HEAD)
cycle_start
[ "$(git rev-list --count HEAD)" -eq $((before + 1)) ] || fail "a normal cycle start committed queue state (want only main's new commit)"
echo "ok: a normal cycle start commits nothing"

# A failed rebase resets the state branch to main; the queue comes back.
setup
echo c > overseer/queues/processed/task-c.yaml
cycle_start fail
[ -f overseer/queues/processed/task-a.yaml ] && [ -f overseer/queues/processed/task-c.yaml ] || fail "the failed-rebase fallback lost the queue"
remote_has overseer/queues/processed/task-a.yaml || fail "the queue kept over a failed rebase was not pushed"
echo "ok: a failed rebase keeps the queue"

# A restore cut short: the carry from before is the whole queue, kept over
# the half-restored directory.
setup
echo d > overseer/queues/processed/task-d.yaml
carryQueueState
rm -rf overseer/queues/processed
cycle_start
[ -f overseer/queues/processed/task-d.yaml ] || fail "the carry from an interrupted restore was not used"
echo "ok: an interrupted restore's carry is kept"

# syncQueueState commits and pushes on the state branch, and leaves the
# disk alone on any other.
setup
echo e > overseer/queues/processed/task-e.yaml
git checkout -q main 2>/dev/null
syncQueueState overseer "sync" >/dev/null
remote_has overseer/queues/processed/task-e.yaml && fail "a sync off the state branch pushed"
git checkout -q overseer
syncQueueState overseer "sync" >/dev/null
remote_has overseer/queues/processed/task-e.yaml || fail "a sync on the state branch did not push"
echo "ok: sync pushes only from the state branch"

# The stop handler stops factory watch and pushes the queue state.
setup
echo f > overseer/queues/processed/task-f.yaml
( exec -a "factory watch" sleep 300 ) &
CYCLE_PID=$!
( STOP_WAIT=5 stopOverseer ) >/dev/null
sleep 0.5
kill -0 "$CYCLE_PID" 2>/dev/null && fail "factory watch was not stopped"
remote_has overseer/queues/processed/task-f.yaml || fail "the stop handler did not push the queue state"
[ -f "$DRAIN_MARKER" ] || fail "the stop handler did not drain"
echo "ok: the stop handler stops the watch and pushes"

echo PASS
