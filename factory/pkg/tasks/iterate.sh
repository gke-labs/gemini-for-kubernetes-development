#!/bin/bash
set -e
set -o pipefail
set -x

# It expects the following environment variables to be set:
# - GEMINI_API_KEY
# - GITHUB_USER_TOKEN
# - REPO_OWNER
# - REPO_NAME
# - CLONE_URL
# - BRANCH_NAME
# - PR_NUMBER
# - PROMPT_FILE
# - GITHUB_USER_ID
# - GITHUB_USER_EMAIL
# - GITHUB_USER_NAME
# - MODELS

export REPO_OWNER="${REPO_OWNER}"
export REPO_NAME="${REPO_NAME}"
export CLONE_URL="${CLONE_URL}"
export BRANCH_NAME="${BRANCH_NAME}"
export PRID="${PR_NUMBER}"
export PROMPT_FILE="${PROMPT_FILE}"
export GITHUB_USER_ID="${GITHUB_USER_ID}"
export GITHUB_USER_EMAIL="${GITHUB_USER_EMAIL}"
export GITHUB_USER_NAME="${GITHUB_USER_NAME}"

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

function setupGitRepos {
    echo "Running setupGitRepos..."
    
    # only clone if doesn't exist and is a valid git repository
    if [ ! -d "/workspaces/${REPO_NAME}/.git" ]; then
        echo "repository does not exist or is invalid, cleaning up destination and cloning..."
        rm -rf "/workspaces/${REPO_NAME}"
        (cd /workspaces/ && git clone ${CLONE_URL})
    else
        echo "repository already exists, cleaning up previous git state..."
        (cd "/workspaces/${REPO_NAME}" && git rebase --abort 2>/dev/null || true)
        (cd "/workspaces/${REPO_NAME}" && git merge --abort 2>/dev/null || true)
        (cd "/workspaces/${REPO_NAME}" && git cherry-pick --abort 2>/dev/null || true)
        (cd "/workspaces/${REPO_NAME}" && git reset --hard HEAD && git clean -fd)
    fi

    echo "running gh repo fork --remote"
    (cd "/workspaces/${REPO_NAME}" && gh repo fork --remote || true)

    echo "running gh repo set-default"
    (cd "/workspaces/${REPO_NAME}" && gh repo set-default "${CLONE_URL}" || true)

    echo "running git config local user.email"
    (cd "/workspaces/${REPO_NAME}" && git config user.email "${GITHUB_USER_EMAIL}")

    echo "running git config local user.name"
    (cd "/workspaces/${REPO_NAME}" && git config user.name "${GITHUB_USER_NAME}")

    if [ -n "$PRID" ] && [ "$PRID" != "null" ]; then
        echo "Checking out PR $PRID"
        (cd "/workspaces/${REPO_NAME}" && git rebase --abort 2>/dev/null || true)
        (cd "/workspaces/${REPO_NAME}" && git merge --abort 2>/dev/null || true)
        (cd "/workspaces/${REPO_NAME}" && git cherry-pick --abort 2>/dev/null || true)
        (cd "/workspaces/${REPO_NAME}" && git reset --hard HEAD && git clean -fd && /usr/bin/gh pr checkout "$PRID" --force)
    elif [ -n "$BRANCH_NAME" ]; then
        echo "Checking out branch $BRANCH_NAME"
        (cd "/workspaces/${REPO_NAME}" && git rebase --abort 2>/dev/null || true)
        (cd "/workspaces/${REPO_NAME}" && git merge --abort 2>/dev/null || true)
        (cd "/workspaces/${REPO_NAME}" && git cherry-pick --abort 2>/dev/null || true)
        (cd "/workspaces/${REPO_NAME}" && git reset --hard HEAD && git clean -fd && git checkout "$BRANCH_NAME")
    fi

    echo "waiting for checkout to be ready (branch check)"
    (cd "/workspaces/${REPO_NAME}" && git branch --show-current)

    echo "recording initial HEAD"
    pushd "/workspaces/${REPO_NAME}" > /dev/null
    OLD_HEAD=$(git rev-parse HEAD)
    popd > /dev/null
}

function commitAndPush {
    echo "Running commitAndPush..."
    pushd "/workspaces/${REPO_NAME}" > /dev/null
    
    NEW_HEAD=$(git rev-parse HEAD)

    # check if there are changes
    if [ -z "$(git status --porcelain)" ]; then 
        if [ "$OLD_HEAD" != "$NEW_HEAD" ]; then
            echo "HEAD has changed (likely rebased by agent). Pushing changes..."
            git push --force origin HEAD
        else
            echo "No changes to commit."
        fi
    else
        echo "Changes detected, committing..."
        git add .
        git commit -m "Agent iteration: Apply changes"
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
configureGemini
installExtensions
SKIP_EMPTY_PROMPT=true runEngine
commitAndPush
