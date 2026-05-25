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

function setupGit {
    echo "Running setupGit..."
    echo "creating /root/.config/gh directory"
    mkdir -p /root/.config/gh

    local GH_USER="${GITHUB_USER_ID}"
    if [ -n "${GITHUB_BOT_LOGIN}" ]; then
        GH_USER="${GITHUB_BOT_LOGIN}"
    fi

    echo "writing gh config"
    cat <<EOF > /root/.config/gh/hosts.yml
github.com:
    users:
        ${GH_USER}:
            oauth_token: ${GITHUB_USER_TOKEN}
    git_protocol: https
    oauth_token: ${GITHUB_USER_TOKEN}
    user: ${GH_USER}
EOF

    echo "running git config user.email"
    if [ -n "$GITHUB_BOT_EMAIL" ]; then
        git config --global user.email "${GITHUB_BOT_EMAIL}"
    else
        git config --global user.email "${GITHUB_USER_EMAIL}"
    fi

    echo "running git config user.name"
    if [ -n "$GITHUB_BOT_NAME" ]; then
        git config --global user.name "${GITHUB_BOT_NAME}"
    else
        git config --global user.name "${GITHUB_USER_NAME}"
    fi

    echo "running gh auth setup-git"
    gh auth setup-git

    echo "Configuring global git ignore"
    git config --global core.excludesfile /root/.gitignore_global
    cat <<EOF > /root/.gitignore_global
manager
bin/
EOF
}

function setupGitRepos {
    echo "Running setupGitRepos..."
    
    # Check if repo already exists (reuse sandbox case)
    if [ ! -d "/workspaces/${REPO_NAME}" ]; then
        echo "cloning repository"
        (cd /workspaces/ && git clone ${CLONE_URL})
    else
        echo "repository already exists, cleaning up previous git state..."
        (cd "/workspaces/${REPO_NAME}" && git rebase --abort 2>/dev/null || true)
        (cd "/workspaces/${REPO_NAME}" && git merge --abort 2>/dev/null || true)
        (cd "/workspaces/${REPO_NAME}" && git cherry-pick --abort 2>/dev/null || true)
        (cd "/workspaces/${REPO_NAME}" && git reset --hard HEAD && git clean -fd)
        # Optional: fetch latest changes
        (cd "/workspaces/${REPO_NAME}" && git fetch origin)
    fi

    echo "running gh repo fork"
    (cd "/workspaces/${REPO_NAME}" && gh repo fork --remote || true)

    echo "running gh repo set-default"
    (cd "/workspaces/${REPO_NAME}" && gh repo set-default "${CLONE_URL}" || true)
}

function checkoutPRBranch {
    echo "Running checkoutPRBranch..."
    echo "checking out PR #${PR_NUMBER}"
    (cd "/workspaces/${REPO_NAME}" && git rebase --abort 2>/dev/null || true)
    (cd "/workspaces/${REPO_NAME}" && git merge --abort 2>/dev/null || true)
    (cd "/workspaces/${REPO_NAME}" && git cherry-pick --abort 2>/dev/null || true)
    (cd "/workspaces/${REPO_NAME}" && git reset --hard HEAD && git clean -fd && /usr/bin/gh pr checkout ${PR_NUMBER} && git pull origin HEAD)
}

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

function configureGemini {
    echo "Running configureGemini..."
    echo "creating /root/.gemini directory"
    mkdir -p /root/.gemini

    echo "writing gemini config"
    cat <<EOF > /root/.gemini/settings.json
{
  "general": {
    "enableAutoUpdate": false,
    "retryFetchErrors": true
  }
}
EOF
}

function installExtensions {
    echo "Installing extensions..."
    if [ -n "$EXTENSIONS" ]; then
        for ext in $EXTENSIONS; do
            gemini extensions install "$ext" --consent
        done
    fi
}

function runGemini {
    echo "Running runGemini..."
    echo "running gemini in yolo mode"

    if [ -n "$GITHUB_BOT_NAME" ]; then
        echo "Using bot identity for commits"
        export GIT_AUTHOR_NAME="$GITHUB_BOT_NAME"
        export GIT_AUTHOR_EMAIL="$GITHUB_BOT_EMAIL"
        export GIT_COMMITTER_NAME="$GITHUB_BOT_NAME"
        export GIT_COMMITTER_EMAIL="$GITHUB_BOT_EMAIL"
    fi

    MODELS_LIST="${MODELS:-gemini-3.5-flash gemini-3-flash-preview gemini-3.1-pro-preview gemini-2.5-pro}"
    SUCCESS=false
    for MODEL in $MODELS_LIST; do
        echo "Trying model: $MODEL"
        GEMINI_ARGS=("--yolo" "--model" "$MODEL" "--output-format" "json")
        if [ "$GEMINI_CONTINUE_SESSION" = "true" ]; then
            GEMINI_ARGS+=("--resume" "latest")
        fi
        if (cd "/workspaces/${REPO_NAME}" && export GEMINI_API_KEY="${GEMINI_API_KEY}" && gemini "${GEMINI_ARGS[@]}" < ${PROMPT_FILE} > "$(dirname "${PROMPT_FILE}")/gemini-output.json"); then
             echo "Gemini execution successful with model: $MODEL"
             SUCCESS=true
             break
        else
             echo "Gemini execution failed with model: $MODEL. Retrying with next model..."
        fi
    done
    
    if [ "$SUCCESS" = false ]; then
        echo "All models failed."
        exit 1
    fi
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
runGemini
