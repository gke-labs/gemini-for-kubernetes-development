#!/bin/bash
set -e
set -o pipefail
set -x

USER_HOME="${HOME:-/root}"
mkdir -p "${USER_HOME}"

export GITHUB_USER_TOKEN="${GITHUB_USER_TOKEN:-${GITHUB_TOKEN}}"
if [ -z "$GITHUB_USER_TOKEN" ]; then
    GITHUB_USER_TOKEN="${MANUAL_PAT:-${OAUTH_PAT}}"
fi

if [ -n "${GITHUB_BOT_LOGIN}" ]; then
    if [ -n "${GITHUB_BOT_TOKEN}" ] || [ -n "${GITHUB_BOT_OAUTH_PAT}" ] || [ -n "${GITHUB_BOT_MANUAL_PAT}" ]; then
        GITHUB_USER_TOKEN="${GITHUB_BOT_TOKEN:-${GITHUB_BOT_MANUAL_PAT:-${GITHUB_BOT_OAUTH_PAT}}}"
    fi
fi

function setupGit {
    echo "Running setupGit..."

    echo "creating ${USER_HOME}/.config/gh directory"
    mkdir -p "${USER_HOME}/.config/gh"

    local GH_USER="${GITHUB_USER_ID}"
    if [ -n "${GITHUB_BOT_LOGIN}" ]; then
        GH_USER="${GITHUB_BOT_LOGIN}"
    fi

    echo "writing gh config"
    cat <<EOF > "${USER_HOME}/.config/gh/hosts.yml"
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
    git config --global core.excludesfile "${USER_HOME}/.gitignore_global"
    cat <<EOF > "${USER_HOME}/.gitignore_global"
manager
bin/
EOF
}

setupGit

# Fork the repository if it doesn't already exist under the bot user account
GH_USER="${GITHUB_USER_ID}"
if [ -n "${GITHUB_BOT_LOGIN}" ]; then
    GH_USER="${GITHUB_BOT_LOGIN}"
fi

echo "Ensuring fork of ${REPO_OWNER}/${REPO_NAME} for user ${GH_USER}..."
gh repo fork "${REPO_OWNER}/${REPO_NAME}" --clone=false || true

if [ "$STRATEGY" = "reuse" ]; then
    echo "Executing 'reuse' strategy (git-based history preservation)..."
    
    # We clone to a temp dir to do the git checkout/fetch/push without polluting workspaces
    TMP_GIT_DIR=$(mktemp -d -t factory-pr-adopt-XXXXXX)
    cd "${TMP_GIT_DIR}"
    
    git init
    git config credential.helper "!gh auth git-credential"
    git remote add origin "${CLONE_URL}"
    
    echo "Fetching PR head commit..."
    git fetch origin "pull/${PR_NUMBER}/head:adopt-pr-${PR_NUMBER}"
    
    FORK_URL="https://github.com/${GH_USER}/${REPO_NAME}.git"
    git remote add fork "${FORK_URL}"
    
    echo "Pushing branch adopt-pr-${PR_NUMBER} to fork..."
    git push -f fork "adopt-pr-${PR_NUMBER}"
    
    cd /workspaces
    rm -rf "${TMP_GIT_DIR}"
    
    # Switch to the workspaces repository directory (if any) and register git branch
    if [ -d "/workspaces/${REPO_NAME}" ]; then
        cd "/workspaces/${REPO_NAME}"
        git remote add fork "${FORK_URL}" || true
        git fetch fork "adopt-pr-${PR_NUMBER}" || true
    fi

elif [ "$STRATEGY" = "reimplement" ]; then
    echo "Executing 'reimplement' strategy (LLM-based re-implementation)..."
    
    # Clone the repository if it doesn't exist under /workspaces
    if [ ! -d "/workspaces/${REPO_NAME}" ]; then
        (cd /workspaces/ && git clone "${CLONE_URL}")
    fi
    
    cd "/workspaces/${REPO_NAME}"
    git remote add fork "https://github.com/${GH_USER}/${REPO_NAME}.git" || true
    git fetch origin
    
    BASE_BRANCH=$(gh repo view --json defaultBranchRef --jq .defaultBranchRef.name)
    
    git rebase --abort 2>/dev/null || true
    git merge --abort 2>/dev/null || true
    git cherry-pick --abort 2>/dev/null || true
    git reset --hard HEAD
    git clean -fd
    git checkout "${BASE_BRANCH}"
    
    BRANCH_NAME="adopt-reimplement-pr-${PR_NUMBER}"
    git checkout -B "${BRANCH_NAME}"
    
    echo "Running Gemini in YOLO mode..."
    set +x
    export GEMINI_API_KEY="${GEMINI_API_KEY}"
    
    MODELS_LIST="${MODELS:-gemini-3.5-flash gemini-3-flash-preview gemini-3.1-pro-preview gemini-2.5-pro}"
    SUCCESS=false
    for MODEL in $MODELS_LIST; do
        echo "Trying model: $MODEL"
        if gemini --yolo --model "$MODEL" --output-format json < "${PROMPT_FILE}" > "$(dirname "${PROMPT_FILE}")/gemini-output.json"; then
            echo "Gemini execution successful with model: $MODEL"
            SUCCESS=true
            break
        else
            echo "Gemini execution encountered errors with model: $MODEL. Retrying next model..."
        fi
    done
    
    if [ "$SUCCESS" = false ]; then
        echo "All models failed to implement changes."
        exit 1
    fi
    set -x
    
    if [ -n "$(git status --porcelain)" ]; then
        echo "Committing reimplemented changes..."
        git add .
        git commit -m "chore: adopt PR #${PR_NUMBER} by reimplementing changes on latest ${BASE_BRANCH}"
        git push -f fork "${BRANCH_NAME}"
    else
        echo "Error: No changes were implemented by the model."
        exit 1
    fi
    
else
    echo "Unknown strategy: $STRATEGY"
    exit 1
fi

# ----------------- Create New Adopted PR -----------------
cd "/workspaces"
if [ -d "/workspaces/${REPO_NAME}" ]; then
    cd "/workspaces/${REPO_NAME}"
fi

NEW_PR_TITLE=$(gh pr view "${PR_URL}" --json title --jq .title)
ORIGINAL_PR_BODY=$(gh pr view "${PR_URL}" --json body --jq .body)
NEW_PR_BODY="This Pull Request was adopted from original PR ${PR_URL}

---
### Original Description:
${ORIGINAL_PR_BODY}"

if [ "$STRATEGY" = "reuse" ]; then
    HEAD_BRANCH="${GH_USER}:adopt-pr-${PR_NUMBER}"
else
    HEAD_BRANCH="${GH_USER}:adopt-reimplement-pr-${PR_NUMBER}"
fi

BASE_BRANCH=$(gh repo view "${CLONE_URL}" --json defaultBranchRef --jq .defaultBranchRef.name)

echo "Creating adopted PR on GitHub..."
CREATED_PR_URL=$(gh pr create --title "adopt: ${NEW_PR_TITLE}" --body "${NEW_PR_BODY}" --head "${HEAD_BRANCH}" --base "${BASE_BRANCH}" || true)

if [ -n "${CREATED_PR_URL}" ] && [[ "${CREATED_PR_URL}" == http* ]]; then
    echo "PR successfully created: ${CREATED_PR_URL}"
    # Write to agent output so host can extract it
    echo "${CREATED_PR_URL}" > "$(dirname "${PROMPT_FILE}")/agent-output.txt"
    
    # ----------------- Comment & Close Original PR -----------------
    if [ "$ADOPT_FLAG" = "close" ]; then
        COMMENT_BODY="This PR has been adopted/forked here: ${CREATED_PR_URL} and closed."
        gh pr comment "${PR_URL}" --body "${COMMENT_BODY}" || true
        gh pr close "${PR_URL}" || true
    else
        COMMENT_BODY="This PR has been adopted/forked here: ${CREATED_PR_URL}"
        gh pr comment "${PR_URL}" --body "${COMMENT_BODY}" || true
    fi
else
    echo "Failed to create PR"
    exit 1
fi
