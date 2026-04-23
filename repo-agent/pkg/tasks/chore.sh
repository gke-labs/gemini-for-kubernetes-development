#!/bin/bash
# Copyright 2026 The Kubernetes Authors.
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

set -e
#set -x

export REPO_NAME={{ printf "%q" .RepoName }}
export REPO_OWNER={{ printf "%q" .RepoOwner }}
export CLONE_URL={{ printf "%q" .CloneURL }}
export CHORE_NAME={{ printf "%q" .ChoreName }}
export CHORE_FILE={{ printf "%q" .ChoreFile }}
export PROMPT_FILE={{ printf "%q" .PromptFile }}

# Use environment variables if they are set, otherwise use defaults
# These should be set in the AgentSandbox pod
export GITHUB_USER_ID="${GITHUB_USER_ID:-${GITHUB_USER_LOGIN}}"
export GITHUB_USER_EMAIL="${GITHUB_USER_EMAIL}"
export GITHUB_USER_NAME="${GITHUB_USER_NAME}"
export GITHUB_USER_TOKEN="${GITHUB_USER_TOKEN:-${GITHUB_TOKEN}}"

if [ -z "${GITHUB_USER_TOKEN}" ]; then
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
    mkdir -p /root/.config/gh

    local GH_USER; GH_USER="${GITHUB_USER_ID}"
    if [ -n "${GITHUB_BOT_LOGIN}" ]; then
        GH_USER="${GITHUB_BOT_LOGIN}"
    fi

    local SAFE_GH_USER; SAFE_GH_USER=$(printf "%q" "${GH_USER}")
    local SAFE_TOKEN; SAFE_TOKEN=$(printf "%q" "${GITHUB_USER_TOKEN}")
    cat <<EOF > /root/.config/gh/hosts.yml
github.com:
    users:
        ${SAFE_GH_USER}:
            oauth_token: ${SAFE_TOKEN}
    git_protocol: https
    oauth_token: ${SAFE_TOKEN}
    user: ${SAFE_GH_USER}
EOF

    if [ -n "${GITHUB_BOT_EMAIL}" ]; then
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
    gh repo clone "${CLONE_URL}" "/workspaces/${REPO_NAME}"

    echo "running gh repo fork"
    (
        cd "/workspaces/${REPO_NAME}"
        gh repo fork --remote || true
    )

    echo "running gh repo set-default"
    (
        cd "/workspaces/${REPO_NAME}"
        gh repo set-default "${CLONE_URL}" || true
    )

    echo "running git config local user.email"
    if [ -n "${GITHUB_BOT_EMAIL}" ]; then
        (
            cd "/workspaces/${REPO_NAME}"
            git config user.email "${GITHUB_BOT_EMAIL}" || true
            git config user.name "${GITHUB_BOT_NAME}" || true
        )
    else
        (
            cd "/workspaces/${REPO_NAME}"
            git config user.email "${GITHUB_USER_EMAIL}" || true
            git config user.name "${GITHUB_USER_NAME}" || true
        )
    fi
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

function runGemini {
    echo "Running gemini for chore: ${CHORE_NAME}"
    set +x
    export GEMINI_API_KEY="${GEMINI_API_KEY}"

    # Security: Hide GitHub OAuth token and config directory before executing untrusted code (gemini --yolo)
    # to prevent token exfiltration.
    local ORIG_GH_CONFIG_DIR; ORIG_GH_CONFIG_DIR="/root/.config/gh"
    local TEMP_GH_CONFIG_DIR; TEMP_GH_CONFIG_DIR="/tmp/gh-config-hidden-$(date +%s)"
    if [ -d "${ORIG_GH_CONFIG_DIR}" ]; then
        mv "${ORIG_GH_CONFIG_DIR}" "${TEMP_GH_CONFIG_DIR}"
    fi
    local ORIG_GITHUB_USER_TOKEN; ORIG_GITHUB_USER_TOKEN="${GITHUB_USER_TOKEN}"
    local ORIG_GITHUB_TOKEN; ORIG_GITHUB_TOKEN="${GITHUB_TOKEN}"
    unset GITHUB_USER_TOKEN
    unset GITHUB_TOKEN

    if gemini --yolo < "${PROMPT_FILE}"; then
        echo "Gemini execution successful"
    else
        echo "Gemini execution encountered errors, but we will check for changes anyway."
    fi

    # Security: Restore GitHub config and token after untrusted code execution.
    if [ -d "${TEMP_GH_CONFIG_DIR}" ]; then
        mv "${TEMP_GH_CONFIG_DIR}" "${ORIG_GH_CONFIG_DIR}"
    fi
    export GITHUB_USER_TOKEN="${ORIG_GITHUB_USER_TOKEN}"
    export GITHUB_TOKEN="${ORIG_GITHUB_TOKEN}"
    set -x
}

function restoreConfigDirFiles {
    BASE_BRANCH=$(gh repo view --json defaultBranchRef --jq .defaultBranchRef.name)
    if [ -d "/configdir" ] && [ "$(ls -A /configdir)" ]; then
      echo "Restoring files changed by configdir injection..."
      pushd "/configdir" > /dev/null
      find . -type f -print0 | while IFS= read -r -d '' file; do
          rel_file="${file#./}"
          pushd "/workspaces/${REPO_NAME}" > /dev/null
          if git rev-parse --verify "${BASE_BRANCH}:${rel_file}" >/dev/null 2>&1; then
              echo "Restoring tracked file: ${rel_file}"
              git checkout "${BASE_BRANCH}" -- "${rel_file}"
          else
              echo "Removing untracked file: ${rel_file}"
              rm -f "${rel_file}"
          fi
          popd > /dev/null
      done
      popd > /dev/null
    fi
}

function commitChanges {
    BASE_BRANCH=$(gh repo view --json defaultBranchRef --jq .defaultBranchRef.name)
    # Check for changes (uncommitted or committed on this branch)
    # If gemini --yolo committed, we can check changes against BASE_BRANCH
    if [ -n "$(git status --porcelain)" ] || [ "$(git rev-parse HEAD)" != "$(git rev-parse "${BASE_BRANCH}")" ]; then
        echo "Changes detected."
        
        # If there are uncommitted changes, commit them
        if [ -n "$(git status --porcelain)" ]; then
            echo "Committing uncommitted changes..."
            
            # Generate commit message using gemini
            git diff > /tmp/chore_diff.txt
            
            # Security: Hide GitHub OAuth token and config directory before executing Gemini
            local ORIG_GH_CONFIG_DIR; ORIG_GH_CONFIG_DIR="/root/.config/gh"
            local TEMP_GH_CONFIG_DIR; TEMP_GH_CONFIG_DIR="/tmp/gh-config-hidden-msg-$(date +%s)"
            if [ -d "${ORIG_GH_CONFIG_DIR}" ]; then
                mv "${ORIG_GH_CONFIG_DIR}" "${TEMP_GH_CONFIG_DIR}"
            fi
            local ORIG_GITHUB_USER_TOKEN; ORIG_GITHUB_USER_TOKEN="${GITHUB_USER_TOKEN}"
            local ORIG_GITHUB_TOKEN; ORIG_GITHUB_TOKEN="${GITHUB_TOKEN}"
            unset GITHUB_USER_TOKEN
            unset GITHUB_TOKEN

            local COMMIT_MSG; COMMIT_MSG=$(gemini "Generate a concise, meaningful commit message for the following changes.
The changes are part of a chore named '${CHORE_NAME}' (defined in ${CHORE_FILE}).

DIFF:
$(cat /tmp/chore_diff.txt | head -c 2000)

The commit message should be prefixed with 'chore: ' and should explicitly mention it was automatically generated as part of a chore.
Only output the commit message itself.")
            
            # Security: Restore GitHub config and token
            if [ -d "${TEMP_GH_CONFIG_DIR}" ]; then
                mv "${TEMP_GH_CONFIG_DIR}" "${ORIG_GH_CONFIG_DIR}"
            fi
            export GITHUB_USER_TOKEN="${ORIG_GITHUB_USER_TOKEN}"
            export GITHUB_TOKEN="${ORIG_GITHUB_TOKEN}"

            if [ -z "${COMMIT_MSG}" ]; then
                COMMIT_MSG="chore: automatic updates from ${CHORE_NAME}"
            fi

            git add .
            git commit -m "${COMMIT_MSG}"
        else
            COMMIT_MSG=$(git log -1 --pretty=%B)
        fi

        # Push the branch
        git push origin "${BRANCH_NAME}"
        
        # Determine Fork Owner
        local FORK_OWNER; FORK_OWNER=$(gh repo view --json owner --jq .owner.login)
        
        local pr_body_file; pr_body_file="/tmp/chore_pr_body.txt"
        cat <<EOF > "${pr_body_file}"
This Pull Request was automatically generated by **Overseer** for the **${CHORE_NAME}** chore.

**Chore Definition:** [${CHORE_FILE}](https://github.com/${REPO_OWNER}/${REPO_NAME}/blob/${BASE_BRANCH}/${CHORE_FILE})

---
### Changes
${COMMIT_MSG}
EOF

        # Try to create PR
        local PR_URL; PR_URL=$(gh pr create --title "chore: ${CHORE_NAME}" --body-file "${pr_body_file}" --head "${FORK_OWNER}:${BRANCH_NAME}" --base "${BASE_BRANCH}" || gh pr view --json url --jq .url || true)
        
        if [ -n "${PR_URL}" ] && [[ "${PR_URL}" == http* ]]; then
            echo "${PR_URL}" > "$(dirname "${PROMPT_FILE}")/agent-output.txt"
            # Add label after creation
            gh pr edit "${PR_URL}" --add-label "overseer" || echo "Warning: failed to add label overseer to ${PR_URL}"
        else
            echo "Failed to create PR or no changes compared to base branch."
            echo "Failed to create PR" > "$(dirname "${PROMPT_FILE}")/agent-output.txt"
        fi
    else
        echo "No changes detected, no PR created."
        echo "No changes detected, no PR created." > "$(dirname "${PROMPT_FILE}")/agent-output.txt"
    fi
}

function runChore {
    pushd "/workspaces/${REPO_NAME}" > /dev/null
    
    # Identify the base branch (usually main or master)
    BASE_BRANCH=$(gh repo view --json defaultBranchRef --jq .defaultBranchRef.name)

    if [ "{{ .SkipPR }}" = "true" ]; then
        echo "SkipPR is true, skipping PR check."
    else
        # Check for existing open PRs for this chore
        local EXISTING_PR; EXISTING_PR=$(gh pr list --state open --search "\"chore: ${CHORE_NAME}\" in:title" --json url --jq '.[0].url')
        if [ -n "${EXISTING_PR}" ]; then
            echo "An open PR already exists for chore ${CHORE_NAME}: ${EXISTING_PR}"
            echo "An open PR already exists for chore ${CHORE_NAME}: ${EXISTING_PR}" > "$(dirname "${PROMPT_FILE}")/agent-output.txt"
            popd > /dev/null
            return
        fi
    fi
    
    # Create a unique branch for this chore run
    local SLUGIFIED_NAME; SLUGIFIED_NAME=$(echo "${CHORE_NAME}" | tr '[:upper:]' '[:lower:]' | tr -c '[:alnum:]' '-' | sed 's/^-//;s/-$//')
    BRANCH_NAME="chore/${SLUGIFIED_NAME}-$(date +%Y%m%d-%H%M%S)"
    
    # start from base branch
    git checkout "${BASE_BRANCH}"
    git checkout -b "${BRANCH_NAME}"

    runGemini
    
    restoreConfigDirFiles

    if [ "{{ .SkipPR }}" = "true" ]; then
        echo "SkipPR is true, skipping commit and push."
        echo "Chore executed successfully without creating a PR." > "$(dirname "${PROMPT_FILE}")/agent-output.txt"
    else
        commitChanges
    fi
    
    popd > /dev/null
}

setupGit
setupGitRepos
injectConfigDirData
runChore
