#!/bin/bash
set -e
set -o pipefail

# Runbook task: execute a runbook scenario (or its teardown) in a dedicated
# run sandbox. The runbook is the source, the emitted script is the
# build artifact, the receipt is the test result — all three live on
# the exploration/notes branch of the member's fork. The script owns
# the deterministic parts (branch, commit, push); the engine owns
# execution and judgment.
#
# It expects the following environment variables to be set:
# - GEMINI_API_KEY or ANTHROPIC_API_KEY (per ENGINE)
# - GITHUB_TOKEN
# - REPO_NAME / CLONE_URL / PROMPT_FILE
# - GITHUB_USER_ID / GITHUB_USER_EMAIL / GITHUB_USER_NAME
# - RUNBOOK_SCENARIO (deploy | upgrade | …)
# - RUNBOOK_PATH (target path within the runbook, may be empty)
# - RUNBOOK_INSTANCE (deployment instance name)
# - RUNBOOK_MODE (run | teardown)
# - PREPARE_PROMPT_FILE / EXECUTE_PROMPT_FILE (run mode: the two phases)
# - MODELS
# - GOOGLE_CLOUD_PROJECT / CLOUDSDK_* when the member configured a project

export GITHUB_USER_TOKEN="${GITHUB_USER_TOKEN:-${GITHUB_TOKEN}}"

NOTES_BRANCH="exploration/notes"

function ensureNotesBranch {
    echo "Ensuring notes branch ${NOTES_BRANCH}..."
    pushd "/workspaces/${REPO_NAME}" > /dev/null
    if git fetch origin "${NOTES_BRANCH}" 2>/dev/null; then
        git checkout -B "${NOTES_BRANCH}" "origin/${NOTES_BRANCH}"
    else
        git checkout -B "${NOTES_BRANCH}"
    fi
    # Runs must execute against LATEST code (same rule as explore).
    SRC_REMOTE="upstream"
    git remote get-url upstream >/dev/null 2>&1 || SRC_REMOTE="origin"
    DEFAULT_BRANCH=$(gh repo view --json defaultBranchRef --jq .defaultBranchRef.name)
    if [ -n "${DEFAULT_BRANCH}" ] && git fetch "${SRC_REMOTE}" "${DEFAULT_BRANCH}"; then
        git merge --no-edit -X ours "${SRC_REMOTE}/${DEFAULT_BRANCH}" || {
            git merge --abort 2>/dev/null || true
            echo "WARN: could not refresh the code base; running from the branch as-is."
        }
    fi
    # Idempotent setup beats reliable cleanup: a previous run killed
    # mid-phase (SIGKILL, eviction, OOM) can leave sibling instance
    # directories moved aside. Restore the subtree from the branch
    # before anything reads or commits it. Scoped to the deployments
    # tree so unsaved chat-session work elsewhere survives.
    git checkout -f -- docs-exploration/runbook-deployments 2>/dev/null || true
    mkdir -p "docs-exploration/runbook-deployments/${RUNBOOK_INSTANCE}"
    popd > /dev/null
}

# The prepare phase implements THIS instance from THE RUNBOOK. Sibling
# instances are pure contamination surface — a script repaired in one
# instance gets copied into the next, and the runbook quietly rots
# (live case: instance2 inherited a helm install from instance1's
# repair while its runbook still said build from source). Hide them
# for the phase rather than asking nicely.
SIBLING_STASH="/workspaces/.tmp/sibling-instances"

function hideSiblingInstances {
    local base="/workspaces/${REPO_NAME}/docs-exploration/runbook-deployments"
    [ -d "${base}" ] || return 0
    mkdir -p "${SIBLING_STASH}"
    for d in "${base}"/*; do
        if [ -d "${d}" ] && [ "$(basename "${d}")" != "${RUNBOOK_INSTANCE}" ]; then
            mv "${d}" "${SIBLING_STASH}/" || true
        fi
    done
    echo "Prepare scope: this instance + the runbook (siblings hidden)."
}

function restoreSiblingInstances {
    local base="/workspaces/${REPO_NAME}/docs-exploration/runbook-deployments"
    [ -d "${SIBLING_STASH}" ] || return 0
    mkdir -p "${base}"
    for d in "${SIBLING_STASH}"/*; do
        if [ -e "${d}" ]; then
            mv "${d}" "${base}/" || true
        fi
    done
    rmdir "${SIBLING_STASH}" 2>/dev/null || true
}

function commitAndPushArtifacts {
    local what="$1"
    echo "Committing and pushing ${what}..."
    pushd "/workspaces/${REPO_NAME}" > /dev/null
    # The runbook is read-only to a deploy run: discard any edits the
    # engine made to it (they belong in the receipt as recommendations
    # the owner applies deliberately), and stage only this instance's
    # directory. --ignore-removal keeps the harness from recording a
    # deletion as a side effect — whatever a killed phase left missing
    # from the worktree, the branch keeps it.
    git checkout -f -- docs-exploration/runbooks 2>/dev/null || true
    git add --ignore-removal "docs-exploration/runbook-deployments/${RUNBOOK_INSTANCE}" 2>/dev/null || true
    if git commit -m "runbook(${RUNBOOK_INSTANCE}): ${what}"; then
        # A dropped connection after a successful server-side push makes
        # the retry fail with 'cannot lock ref … is at <our sha>'. If the
        # remote is already at our commit, the push succeeded.
        if ! git push origin "${NOTES_BRANCH}"; then
            git fetch origin "${NOTES_BRANCH}"
            if [ "$(git rev-parse HEAD)" = "$(git rev-parse "origin/${NOTES_BRANCH}")" ]; then
                echo "Remote already at our commit; push had succeeded."
            else
                echo "ERROR: push failed and remote differs." >&2
                exit 1
            fi
        fi
        echo "Pushed ${what} to origin/${NOTES_BRANCH}"
    else
        echo "No changes to commit for ${what}."
    fi
    popd > /dev/null
}

# Main execution
setupGit
setupGitRepos
# HACK: Avoid git lock issues
sleep 5
checkoutDefaultBranch
ensureNotesBranch
configureGemini
case "${RUNBOOK_MODE}" in
run)
    # Two phases: the scripts land on the branch BEFORE anything
    # executes — durable and reviewable even if execution dies.
    hideSiblingInstances
    PROMPT_FILE="${PREPARE_PROMPT_FILE}" runEngine
    restoreSiblingInstances
    commitAndPushArtifacts "prepared scripts (pre-execution)"
    PROMPT_FILE="${EXECUTE_PROMPT_FILE}" runEngine
    commitAndPushArtifacts "execution receipt"
    ;;
plan)
    # The gate: prepare only — scripts + a PLANNED receipt land on the
    # branch; nothing executes until the owner explicitly deploys.
    hideSiblingInstances
    PROMPT_FILE="${PREPARE_PROMPT_FILE}" runEngine
    restoreSiblingInstances
    commitAndPushArtifacts "plan (scripts + PLANNED receipt, nothing executed)"
    ;;
deploy)
    # The owner reviewed the plan and clicked Deploy: execute only.
    PROMPT_FILE="${EXECUTE_PROMPT_FILE}" runEngine
    commitAndPushArtifacts "execution receipt"
    ;;
*)
    runEngine
    commitAndPushArtifacts "${RUNBOOK_MODE} receipt"
    ;;
esac
