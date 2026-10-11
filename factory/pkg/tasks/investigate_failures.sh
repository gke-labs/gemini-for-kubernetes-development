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

# fetchLogs saves the failed jobs' logs in the task's directory, outside
# the checkout (commitAndPush adds everything in it), and appends what
# each says about why it failed to the prompt.
function fetchLogs {
    echo "Fetching logs for failed runs..."
    local dir repo runID prowRun targetURL rawURL log
    dir="$(dirname "${PROMPT_FILE}")/ci-logs"
    mkdir -p "${dir}"
    cd "/workspaces/${REPO_NAME}"
    if [ -n "$FAILED_RUNS" ]; then
        repo="$(gh repo view --json nameWithOwner -q .nameWithOwner)"
        # shellcheck disable=SC2086 # run IDs, one word each
        ciFailedJobsOfRuns "${repo}" $FAILED_RUNS | fetchCIJobLogs "${repo}" "${dir}"
    fi
    if [ -n "$FAILED_PROW_RUNS" ]; then
        for prowRun in $FAILED_PROW_RUNS; do
            runID="${prowRun%%|*}"
            targetURL="${prowRun#*|}"
            log="${dir}/prow-${runID}.log"
            echo "Downloading Prow logs for run $runID from $targetURL"
            rawURL="$(echo "$targetURL" | sed 's|https://prow.k8s.io/view/gcs/|https://storage.googleapis.com/|')/build-log.txt"
            curl -sSL "$rawURL" > "${log}" || echo "Failed to download prow logs for run $runID"
            if [ ! -s "${log}" ] || grep -q "<Error>" "${log}"; then
                echo "Log not found at $rawURL. Please view the failure URL directly: $targetURL" > "${log}"
            fi
            printf '### Prow run %s\n\nIts whole log: %s\n\n```\n' "${runID}" "${log}" >> "${dir}/excerpts.md"
            tail -n 100 "${log}" >> "${dir}/excerpts.md"
            printf '```\n\n' >> "${dir}/excerpts.md"
        done
    fi
    {
        printf '\n## Logs of the failed jobs\n\n'
        if [ -s "${dir}/excerpts.md" ]; then
            printf 'Their whole logs are in %s, outside the repository: read them with grep, sed -n or tail. What each says about why it failed:\n\n' "${dir}"
            cat "${dir}/excerpts.md"
        else
            printf 'No log of a failed job could be downloaded: find the failed checks with `gh pr checks %s`.\n' "${PR_NUMBER}"
        fi
    } >> "${PROMPT_FILE}"
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

