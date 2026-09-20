#!/bin/bash
set -e
set -o pipefail
set -x


export GITHUB_USER_TOKEN="${GITHUB_USER_TOKEN:-${GITHUB_TOKEN}}"
if [ -z "$GITHUB_USER_TOKEN" ]; then
    GITHUB_USER_TOKEN="${MANUAL_PAT:-${OAUTH_PAT}}"
fi

if [ -n "${GITHUB_BOT_LOGIN}" ]; then
    if [ -n "${GITHUB_BOT_TOKEN}" ] || [ -n "${GITHUB_BOT_OAUTH_PAT}" ] || [ -n "${GITHUB_BOT_MANUAL_PAT}" ]; then
        GITHUB_USER_TOKEN="${GITHUB_BOT_TOKEN:-${GITHUB_BOT_MANUAL_PAT:-${GITHUB_BOT_OAUTH_PAT}}}"
    fi
fi

setupGit
configureGemini

# Fork the repository if it doesn't already exist under the bot user account
GH_USER="${GITHUB_USER_ID}"
if [ -n "${GITHUB_BOT_LOGIN}" ]; then
    GH_USER="${GITHUB_BOT_LOGIN}"
fi

echo "Ensuring fork of ${REPO_OWNER}/${REPO_NAME} for user ${GH_USER}..."
gh repo fork "${REPO_OWNER}/${REPO_NAME}" --clone=false || true

# Clone the repository if it doesn't exist under /workspaces
if [ ! -d "/workspaces/${REPO_NAME}" ]; then
    echo "Cloning repository ${CLONE_URL}..."
    (cd /workspaces/ && git clone "${CLONE_URL}")
fi

cd "/workspaces/${REPO_NAME}"
FORK_URL="https://github.com/${GH_USER}/${REPO_NAME}.git"
git remote add fork "${FORK_URL}" || true
git fetch origin

if [ "$STRATEGY" = "reuse" ]; then
    echo "Executing 'reuse' strategy (git-based history preservation)..."
    
    echo "Fetching PR head commit..."
    git fetch origin "pull/${PR_NUMBER}/head:adopt-pr-${PR_NUMBER}"
    
    echo "Pushing branch adopt-pr-${PR_NUMBER} to fork..."
    git push -f fork "adopt-pr-${PR_NUMBER}"
    
    git checkout "adopt-pr-${PR_NUMBER}"

elif [ "$STRATEGY" = "reimplement" ]; then
    echo "Executing 'reimplement' strategy (LLM-based re-implementation)..."
    
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
    
    MODELS_LIST="${MODELS:-__DEFAULT_MODELS__}"
    SUCCESS=false
    for MODEL in $MODELS_LIST; do
        echo "Trying model: $MODEL"
        if gemini --yolo --model "$MODEL" --output-format json < "${PROMPT_FILE}" > "$(dirname "${PROMPT_FILE}")/gemini-output.json"; then
            echo "Gemini execution successful with model: $MODEL"
            record_gemini_usage "$(dirname "${PROMPT_FILE}")/gemini-output.json"
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
