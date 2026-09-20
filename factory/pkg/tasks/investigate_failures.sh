#!/bin/bash
set -e
set -o pipefail


# It expects the following environment variables to be set:
# - GEMINI_API_KEY
# - GITHUB_USER_TOKEN
# - REPO_NAME
# - CLONE_URL
# - PROMPT_FILE
# - GITHUB_USER_ID
# - GITHUB_USER_EMAIL
# - GITHUB_USER_NAME
# - PR_NUMBER
# - FAILED_RUNS
# - MODELS
# - EXTENSIONS

export GITHUB_USER_TOKEN="${GITHUB_USER_TOKEN:-${GITHUB_TOKEN}}"
if [ -z "$GITHUB_USER_TOKEN" ]; then
    # Try other common names
    GITHUB_USER_TOKEN="${MANUAL_PAT:-${OAUTH_PAT}}"
fi

if [ -n "${GITHUB_BOT_LOGIN}" ]; then
    if [ -n "${GITHUB_BOT_TOKEN}" ] || [ -n "${GITHUB_BOT_OAUTH_PAT}" ] || [ -n "${GITHUB_BOT_MANUAL_PAT}" ]; then
        GITHUB_USER_TOKEN="${GITHUB_BOT_TOKEN:-${GITHUB_BOT_MANUAL_PAT:-${GITHUB_BOT_OAUTH_PAT}}}"
    fi
fi

function fetchLogs {
    echo "Fetching logs for failed runs..."
    cd "/workspaces/${REPO_NAME}"
    if [ -n "$FAILED_RUNS" ]; then
        for runID in $FAILED_RUNS; do
            echo "Downloading logs for run $runID"
            gh run view "$runID" --log-failed > "run-${runID}-failed.log" || echo "Failed to download logs for run $runID"
        done
    fi
    if [ -n "$FAILED_PROW_RUNS" ]; then
        for prowRun in $FAILED_PROW_RUNS; do
            runID="${prowRun%%|*}"
            targetURL="${prowRun#*|}"
            echo "Downloading Prow logs for run $runID from $targetURL"
            rawURL="$(echo "$targetURL" | sed 's|https://prow.k8s.io/view/gcs/|https://storage.googleapis.com/|')/build-log.txt"
            curl -sSL "$rawURL" > "run-${runID}-failed.log" || echo "Failed to download prow logs for run $runID"
            if [ ! -s "run-${runID}-failed.log" ] || grep -q "<Error>" "run-${runID}-failed.log"; then
                echo "Log not found at $rawURL. Please view the failure URL directly: $targetURL" > "run-${runID}-failed.log"
            fi
        done
    fi
}

function commitAndPush {
    echo "Running commitAndPush..."
    pushd "/workspaces/${REPO_NAME}" > /dev/null
    
    NEW_HEAD=$(git rev-parse HEAD)

    # check if there are changes
    if [ -z "$(git status --porcelain)" ]; then 
        if [ "$OLD_HEAD" != "$NEW_HEAD" ]; then
            echo "HEAD has changed (committed or rebased by agent). Pushing changes..."
            git push --force origin HEAD
        else
            echo "No changes to commit."
        fi
    else
        echo "Changes detected in working directory, committing..."
        git add .
        git commit -m "Fix CI failures: Apply changes"
        if [ "$OLD_HEAD" != "$NEW_HEAD" ]; then
            echo "HEAD has changed and working directory has changes. Pushing changes..."
            git push --force origin HEAD
        else
            git push origin HEAD
        fi
    fi
    popd > /dev/null
}

# Main execution
setupGit
setupGitRepos
# HACK: Avoid git lock issues
sleep 5
checkoutPRBranch
fetchLogs
configureGemini
installExtensions
runEngine
commitAndPush

