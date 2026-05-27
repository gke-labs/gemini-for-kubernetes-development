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
# - PROMPT_FILE
# - GITHUB_USER_ID
# - GITHUB_USER_EMAIL
# - GITHUB_USER_NAME
# - AGENT_NAME
# - AGENT_FILE
# - SKIP_PR
# - PR_NUMBER
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

function commitChanges {
    BASE_BRANCH=$(gh repo view --json defaultBranchRef --jq .defaultBranchRef.name)
    
    # Check for changes (uncommitted or committed on this branch compared to base branch)
    if [ -n "$(git status --porcelain)" ] || [ "$(git rev-parse HEAD)" != "$(git rev-parse ${BASE_BRANCH})" ]; then
        echo "Changes detected."
        
        # If there are uncommitted changes, commit them
        if [ -n "$(git status --porcelain)" ]; then
            echo "Committing uncommitted changes..."
            git diff > /tmp/agent_diff.txt
            
            COMMIT_MSG=$(gemini "Generate a concise, meaningful commit message for the following changes.
The changes are part of an agent named '${AGENT_NAME}' (defined in ${AGENT_FILE}).

DIFF:
$(cat /tmp/agent_diff.txt | head -c 2000)

The commit message should be prefixed with 'chore: ' and should explicitly mention it was automatically generated.
Only output the commit message itself.")
            
            if [ -z "$COMMIT_MSG" ]; then
                COMMIT_MSG="chore: automatic updates from agent ${AGENT_NAME}"
            fi

            git add .
            git commit -m "${COMMIT_MSG}"
        else
            COMMIT_MSG=$(git log -1 --pretty=%B)
        fi

        # Push the branch
        git push origin "${BRANCH_NAME}"
        
        if [ "${PR_NUMBER:-0}" -gt 0 ]; then
            echo "PR already exists (#${PR_NUMBER}), pushed changes to branch."
            echo "Pushed changes to PR #${PR_NUMBER}" > "$(dirname "${PROMPT_FILE}")/agent-output.txt"
        else
            # Determine Repo Owner for the link
            REPO_URL=$(git remote get-url origin)
            REPO_PATH=$(echo $REPO_URL | sed -E 's/.*github.com[:\/]//;s/\.git$//')
            FORK_OWNER=$(echo "$REPO_PATH" | cut -d'/' -f1)
            
            PR_BODY="This Pull Request was automatically generated by **Factory Agent** for the **${AGENT_NAME}** agent.

**Agent Definition:** [${AGENT_FILE}](https://github.com/${REPO_OWNER}/${REPO_NAME}/blob/${BASE_BRANCH}/${AGENT_FILE})

---
### Changes
${COMMIT_MSG}"

            # Try to create PR
            PR_URL=$(gh pr create --title "chore: ${AGENT_NAME}" --body "${PR_BODY}" --head "${FORK_OWNER}:${BRANCH_NAME}" --base "${BASE_BRANCH}" || true)
            
            if [ -n "$PR_URL" ] && [[ "$PR_URL" == http* ]]; then
                echo "$PR_URL" > "$(dirname "${PROMPT_FILE}")/agent-output.txt"
                gh pr edit "$PR_URL" --add-label "factory" || echo "Warning: failed to add label factory to $PR_URL"
            else
                echo "Failed to create PR" > "$(dirname "${PROMPT_FILE}")/agent-output.txt"
            fi
        fi
    else
        echo "No changes detected."
        echo "No changes detected." > "$(dirname "${PROMPT_FILE}")/agent-output.txt"
    fi
}

function runAgent {
    pushd "/workspaces/${REPO_NAME}" > /dev/null
    
    # Identify the base branch (usually main or master)
    BASE_BRANCH=$(gh repo view --json defaultBranchRef --jq .defaultBranchRef.name)

    if [ "${PR_NUMBER:-0}" -gt 0 ]; then
        echo "Checking out PR #${PR_NUMBER} branch..."
        (cd "/workspaces/${REPO_NAME}" && git rebase --abort 2>/dev/null || true)
        (cd "/workspaces/${REPO_NAME}" && git merge --abort 2>/dev/null || true)
        (cd "/workspaces/${REPO_NAME}" && git cherry-pick --abort 2>/dev/null || true)
        (cd "/workspaces/${REPO_NAME}" && git reset --hard HEAD && git clean -fd && /usr/bin/gh pr checkout ${PR_NUMBER} && git pull origin HEAD || true)
        BRANCH_NAME=$(git branch --show-current)
    else
        # Create a unique branch for this agent run if skip PR is not true
        if [ "$SKIP_PR" = "true" ]; then
            echo "SkipPR is true. Running on default branch ${BASE_BRANCH}"
            BRANCH_NAME="${BASE_BRANCH}"
        else
            SLUGIFIED_NAME=$(echo "${AGENT_NAME}" | tr '[:upper:]' '[:lower:]' | tr -c '[:alnum:]' '-' | sed 's/^-//;s/-$//')
            BRANCH_NAME="agent/${SLUGIFIED_NAME}-$(date +%Y%m%d-%H%M%S)"
            
            # Start from base branch
            git rebase --abort 2>/dev/null || true
            git merge --abort 2>/dev/null || true
            git cherry-pick --abort 2>/dev/null || true
            git reset --hard HEAD
            git clean -fd
            git checkout "${BASE_BRANCH}"
            git checkout -b "${BRANCH_NAME}"
        fi
    fi

    echo "Running Gemini in YOLO mode..."
    set +x
    export GEMINI_API_KEY="${GEMINI_API_KEY}"

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
        if [ "${DRY_RUN:-false}" = "true" ]; then
            echo "[dry-run] Would run: gemini --yolo --model \"$MODEL\" --output-format json < \"${PROMPT_FILE}\""
            SUCCESS=true
            break
        fi
        if gemini --yolo --model "$MODEL" --output-format json < "${PROMPT_FILE}" > "$(dirname "${PROMPT_FILE}")/gemini-output.json"; then
            echo "Gemini execution successful with model: $MODEL"
            SUCCESS=true
            break
        else
            echo "Gemini execution encountered errors with model: $MODEL. Retrying next model..."
        fi
    done

    if [ "$SUCCESS" = false ]; then
        echo "All models failed."
        exit 1
    fi
    set -x

    if [ "$SKIP_PR" = "true" ]; then
        echo "SkipPR is true, skipping commit and push."
        echo "Agent executed successfully without creating/updating a PR." > "$(dirname "${PROMPT_FILE}")/agent-output.txt"
    else
        commitChanges
    fi
    
    popd > /dev/null
}

setupGit
setupGitRepos
# HACK: Avoid git lock issues
sleep 5
configureGemini
runAgent
