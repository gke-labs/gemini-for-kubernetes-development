#!/bin/bash
set -e
set -x

export REPO_NAME="{{ .RepoName }}"
export REPO_OWNER="{{ .RepoOwner }}"
export CLONE_URL="{{ .CloneURL }}"
export CHORE_NAME="{{ .ChoreName }}"
export CHORE_FILE="{{ .ChoreFile }}"
export PROMPT_FILE="{{ .PromptFile }}"

# Use environment variables if they are set, otherwise use defaults
# These should be set in the AgentSandbox pod
export GITHUB_USER_ID="${GITHUB_USER_ID:-${GITHUB_USER_LOGIN}}"
export GITHUB_USER_EMAIL="${GITHUB_USER_EMAIL}"
export GITHUB_USER_NAME="${GITHUB_USER_NAME}"
export GITHUB_USER_TOKEN="${GITHUB_USER_TOKEN:-${GITHUB_TOKEN}}"

if [ -z "$GITHUB_USER_TOKEN" ]; then
    # Try other common names
    GITHUB_USER_TOKEN="${MANUAL_PAT:-${OAUTH_PAT}}"
fi

function setupGit {
    echo "Running setupGit..."
    mkdir -p /root/.config/gh

    local GH_USER="${GITHUB_USER_ID}"
    if [ -n "${GITHUB_BOT_LOGIN}" ]; then
        GH_USER="${GITHUB_BOT_LOGIN}"
    fi

    cat <<EOF > /root/.config/gh/hosts.yml
github.com:
    users:
        ${GH_USER}:
            oauth_token: ${GITHUB_USER_TOKEN}
    git_protocol: https
    oauth_token: ${GITHUB_USER_TOKEN}
    user: ${GH_USER}
EOF

    if [ -n "$GITHUB_BOT_EMAIL" ]; then
        git config --global user.email "${GITHUB_BOT_EMAIL}"
        git config --global user.name "${GITHUB_BOT_NAME}"
    else
        git config --global user.email "${GITHUB_USER_EMAIL}"
        git config --global user.name "${GITHUB_USER_NAME}"
    fi

    gh auth setup-git
}

function setupGitRepos {
    echo "Running setupGitRepos..."
    if [ -d "/workspaces/${REPO_NAME}" ]; then
        echo "Repository already exists at /workspaces/${REPO_NAME}"
        return
    fi
    
    echo "cloning repository"
    # Clone into the specific REPO_NAME directory to ensure consistency
    git clone "${CLONE_URL}" "/workspaces/${REPO_NAME}"

    echo "running gh repo fork"
    (cd "/workspaces/${REPO_NAME}" && gh repo fork --remote || true)

    echo "running gh repo set-default"
    (cd "/workspaces/${REPO_NAME}" && gh repo set-default "${CLONE_URL}" || true)

    echo "running git config local user.email"
    if [ -n "$GITHUB_BOT_EMAIL" ]; then
        (cd "/workspaces/${REPO_NAME}" && git config user.email "${GITHUB_BOT_EMAIL}" || true)
        (cd "/workspaces/${REPO_NAME}" && git config user.name "${GITHUB_BOT_NAME}" || true)
    else
        (cd "/workspaces/${REPO_NAME}" && git config user.email "${GITHUB_USER_EMAIL}" || true)
        (cd "/workspaces/${REPO_NAME}" && git config user.name "${GITHUB_USER_NAME}" || true)
    fi
}

function runChore {
    pushd "/workspaces/${REPO_NAME}" > /dev/null
    
    # Identify the base branch (usually main or master)
    BASE_BRANCH=$(git symbolic-ref --short HEAD 2>/dev/null || echo "main")
    
    # Create a unique branch for this chore run
    SLUGIFIED_NAME=$(echo "${CHORE_NAME}" | tr '[:upper:]' '[:lower:]' | tr -c '[:alnum:]' '-' | sed 's/^-//;s/-$//')
    BRANCH_NAME="chore/${SLUGIFIED_NAME}-$(date +%Y%m%d-%H%M%S)"
    
    git checkout -b "${BRANCH_NAME}"

    echo "Running gemini for chore: ${CHORE_NAME}"
    set +x
    export GEMINI_API_KEY="${GEMINI_API_KEY}"

    if gemini --yolo < "${PROMPT_FILE}"; then
        echo "Gemini execution successful"
    else
        echo "Gemini execution encountered errors, but we will check for changes anyway."
    fi
    set -x

    # Check for changes (uncommitted or committed on this branch)
    # If gemini --yolo committed, we can check changes against BASE_BRANCH
    if [ -n "$(git status --porcelain)" ] || [ "$(git rev-parse HEAD)" != "$(git rev-parse ${BASE_BRANCH})" ]; then
        echo "Changes detected."
        
        # If there are uncommitted changes, commit them
        if [ -n "$(git status --porcelain)" ]; then
            echo "Committing uncommitted changes..."
            
            # Generate commit message using gemini
            git diff > /tmp/chore_diff.txt
            
            COMMIT_MSG=$(gemini "Generate a concise, meaningful commit message for the following changes.
The changes are part of a chore named '${CHORE_NAME}' (defined in ${CHORE_FILE}).

DIFF:
$(cat /tmp/chore_diff.txt | head -c 2000)

The commit message should be prefixed with 'chore: ' and should explicitly mention it was automatically generated as part of a chore.
Only output the commit message itself.")
            
            if [ -z "$COMMIT_MSG" ]; then
                COMMIT_MSG="chore: automatic updates from ${CHORE_NAME}"
            fi

            git add .
            git commit -m "${COMMIT_MSG}"
        else
            COMMIT_MSG=$(git log -1 --pretty=%B)
        fi

        # Push the branch
        git push origin "${BRANCH_NAME}"
        
        # Determine Repo Owner for the link
        REPO_URL=$(git remote get-url origin)
        # Assuming github.com:owner/repo or https://github.com/owner/repo
        REPO_PATH=$(echo $REPO_URL | sed -E 's/.*github.com[:\/]//;s/\.git$//')
        FORK_OWNER=$(echo "$REPO_PATH" | cut -d'/' -f1)
        
        PR_BODY="This Pull Request was automatically generated by **Overseer** for the **${CHORE_NAME}** chore.

**Chore Definition:** [${CHORE_FILE}](https://github.com/${REPO_OWNER}/${REPO_NAME}/blob/${BASE_BRANCH}/${CHORE_FILE})

---
### Changes
${COMMIT_MSG}"

        # Try to create PR
        PR_URL=$(gh pr create --title "chore: ${CHORE_NAME}" --body "${PR_BODY}" --head "${FORK_OWNER}:${BRANCH_NAME}" --base "${BASE_BRANCH}" || true)
        if [ -n "$PR_URL" ]; then
            echo "$PR_URL" > "$(dirname "${PROMPT_FILE}")/agent-output.txt"
        else
            echo "Failed to create PR or no changes compared to base branch."
            echo "Failed to create PR" > "$(dirname "${PROMPT_FILE}")/agent-output.txt"
        fi
    else
        echo "No changes detected, no PR created."
        echo "No changes detected, no PR created." > "$(dirname "${PROMPT_FILE}")/agent-output.txt"
    fi

    popd > /dev/null
}

setupGit
setupGitRepos
runChore
