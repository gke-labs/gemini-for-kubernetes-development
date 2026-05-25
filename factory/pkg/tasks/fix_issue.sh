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
# - ISSUE_NUMBER
# - PROMPT_FILE
# - GITHUB_USER_ID
# - GITHUB_USER_EMAIL
# - GITHUB_USER_NAME
# - BRANCH_NAME
# - MODELS

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

    echo "running git config local user.email"
    (cd "/workspaces/${REPO_NAME}" && git config user.email "${GITHUB_USER_EMAIL}")

    echo "running git config local user.name"
    (cd "/workspaces/${REPO_NAME}" && git config user.name "${GITHUB_USER_NAME}")

    echo "waiting for checkout to be ready (branch check)"
    (cd "/workspaces/${REPO_NAME}" && git branch --show-current)
}

function checkForExistingPR {
    echo "Checking for existing PRs..."
    if [ "$NO_PR" = "true" ]; then
        echo "NO_PR is true; skipping check for existing PR."
        return
    fi
    if [ "${ISSUE_NUMBER:-0}" -eq 0 ]; then
        echo "No issue number specified; skipping check for existing PR."
        return
    fi
    pushd "/workspaces/${REPO_NAME}" > /dev/null

    # Try to find a PR by the current user first
    local pr_number=$(gh search prs "${ISSUE_NUMBER}" --state open --repo "${REPO_OWNER}/${REPO_NAME}" --author "${GITHUB_USER_ID}" --json number --jq '.[0] | "\(.number)"' --limit 1)
    local pr_url=$(gh search prs "${ISSUE_NUMBER}" --state open --repo "${REPO_OWNER}/${REPO_NAME}" --author "${GITHUB_USER_ID}" --json url --jq '.[0] | "\(.url)"' --limit 1)

    # If not found, look for any PR
    if [ -z "$pr_number" ] || [ "$pr_number" == "null" ]; then
        pr_number=$(gh search prs "${ISSUE_NUMBER}" --repo "${REPO_OWNER}/${REPO_NAME}" --state open --json number --jq '.[0] | "\(.number)"' --limit 1)
        pr_url=$(gh search prs "${ISSUE_NUMBER}" --repo "${REPO_OWNER}/${REPO_NAME}" --state open --json url --jq '.[0] | "\(.url)"' --limit 1)
    fi

    if [ -n "$pr_number" ] && [ "$pr_number" != "null" ]; then
        echo "Found existing PR:"
        echo $pr_number
        echo $pr_url

        echo "Found existing PR #${pr_number}"
        git rebase --abort 2>/dev/null || true
        git merge --abort 2>/dev/null || true
        git cherry-pick --abort 2>/dev/null || true
        git reset --hard HEAD
        git clean -fd
        /usr/bin/gh pr checkout "$pr_number"

        local output_file="$(dirname "${PROMPT_FILE}")/agent-output.txt"

        echo "We are not generating anything because there is an existing PR." > "$output_file"
        echo "${pr_url}" >> "$output_file"
        exit 0
    fi

    popd > /dev/null
}

function checkoutNewBranch {
    echo "Running checkoutNewBranch..."
    echo "creating new branch"
    local branch_name="${BRANCH_NAME:-issue-${ISSUE_NUMBER}}"
    (cd "/workspaces/${REPO_NAME}" && git rebase --abort 2>/dev/null || true)
    (cd "/workspaces/${REPO_NAME}" && git merge --abort 2>/dev/null || true)
    (cd "/workspaces/${REPO_NAME}" && git cherry-pick --abort 2>/dev/null || true)
    (cd "/workspaces/${REPO_NAME}" && git reset --hard HEAD && git clean -fd && git checkout -B "$branch_name")
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
    echo "running gemini in yolo mode"
    pushd "/workspaces/${REPO_NAME}" > /dev/null
    set +x
    export GEMINI_API_KEY="${GEMINI_API_KEY}"

    if [ -n "$GITHUB_BOT_NAME" ]; then
        echo "Using bot identity for commits"
        export GIT_AUTHOR_NAME="$GITHUB_BOT_NAME"
        export GIT_AUTHOR_EMAIL="$GITHUB_BOT_EMAIL"
        export GIT_COMMITTER_NAME="$GITHUB_BOT_NAME"
        export GIT_COMMITTER_EMAIL="$GITHUB_BOT_EMAIL"
    fi

    MODELS_LIST="${MODELS:-gemini-2.5-flash}"
    SUCCESS=false
    for MODEL in $MODELS_LIST; do
        echo "Trying model: $MODEL"
        if gemini --yolo --model "$MODEL" --output-format json < ${PROMPT_FILE} > "$(dirname "${PROMPT_FILE}")/gemini-output.json"; then
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
    set -x
    popd > /dev/null
}

function injectConfigDirData {
    pushd "/workspaces/${REPO_NAME}" > /dev/null
    if [ -d "/configdir" ] && [ "$(ls -A /configdir)" ]; then
      echo "Injecting configdir files into repository..."
      shopt -s dotglob
      cp -R /configdir/* .
      shopt -u dotglob
    fi
    popd > /dev/null
}

function recordPRLink {
    echo "Recording PR link..."
    pushd "/workspaces/${REPO_NAME}" > /dev/null
    local output_file="$(dirname "${PROMPT_FILE}")/agent-output.txt"
    if [ "$NO_PR" = "true" ]; then
        echo "Branch successfully pushed to origin/${BRANCH_NAME}" > "$output_file"
        popd > /dev/null
        return
    fi
    local pr_url=""

    # Try current branch PR status
    echo "Checking pr status..."
    pr_url=$(gh pr status --json url --jq '.currentBranch.url // empty')

    # If not found, try listing PRs for this branch
    if [ -z "$pr_url" ] || [ "$pr_url" == "null" ]; then
        echo "Checking pr list by branch..."
        pr_url=$(gh pr list --head "${BRANCH_NAME:-issue_${ISSUE_NUMBER}}" --json url --jq '.[0].url // empty')
    fi

    # If still not found, try searching PRs by issue number and author
    if [ -z "$pr_url" ] || [ "$pr_url" == "null" ]; then
        if [ "${ISSUE_NUMBER:-0}" -gt 0 ]; then
            echo "Searching for PR..."
            pr_url=$(gh search prs "${ISSUE_NUMBER}" --state open --repo "${REPO_OWNER}/${REPO_NAME}" --author "${GITHUB_USER_ID}" --json url --jq '.[0].url // empty' --limit 1)
        fi
    fi

    if [ -n "$pr_url" ] && [ "$pr_url" != "null" ]; then
        echo "Successfully found PR: ${pr_url}"
        echo "${pr_url}" > "$output_file"
    else
        echo "Could not find PR link automatically."
        # Don't overwrite if it already exists (unlikely here but safe)
        if [ ! -s "$output_file" ]; then
            echo "Could not find PR link automatically." > "$output_file"
        fi
    fi
    popd > /dev/null
}

# Main execution
setupGit
setupGitRepos
# HACK: Avoid git lock issues
sleep 5
checkForExistingPR
checkoutNewBranch
configureGemini
installExtensions
injectConfigDirData
runGemini
recordPRLink
