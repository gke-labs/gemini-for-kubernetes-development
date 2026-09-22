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
# - RUNBOOK_MODE (run | teardown)
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
    mkdir -p docs-exploration/runbooks/scripts docs-exploration/runbooks/receipts
    popd > /dev/null
}

function commitAndPushArtifacts {
    echo "Committing and pushing run artifacts..."
    pushd "/workspaces/${REPO_NAME}" > /dev/null
    git add docs-exploration 2>/dev/null || true
    if git commit -m "runbook(${RUNBOOK_SCENARIO}${RUNBOOK_PATH:+/${RUNBOOK_PATH}}): ${RUNBOOK_MODE} receipt"; then
        git push origin "${NOTES_BRANCH}"
        echo "Artifacts pushed to origin/${NOTES_BRANCH}"
    else
        echo "No artifact changes to commit."
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
runEngine
commitAndPushArtifacts
