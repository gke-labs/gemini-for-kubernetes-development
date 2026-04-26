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
set -o pipefail
#set -x

# It expects the following environment variables to be set:
# - GEMINI_API_KEY
# - GITHUB_USER_TOKEN

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
{{ if .Repo }}{{ .Repo.Host }}{{ else }}github.com{{ end }}:
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
    if [ ! -d "/workspaces/${REPO_NAME}/.git" ]; then
        echo "cloning repository"
        rm -rf "/workspaces/${REPO_NAME}"
        gh repo clone "${REPO_OWNER}/${REPO_NAME}" "/workspaces/${REPO_NAME}"
    else
        echo "repository already exists"
        cd "/workspaces/${REPO_NAME}"
        git reset --hard
        git clean -fdx
        git fetch origin
    fi

    cd "/workspaces/${REPO_NAME}"
    gh repo set-default "${REPO_OWNER}/${REPO_NAME}" || true
}

function injectConfigDirData {
    cd "/workspaces/${REPO_NAME}"
    if [ -d "/configdir" ] && [ "$(ls -A /configdir)" ]; then
      echo "Injecting configdir files into repository..."
      shopt -s dotglob
      cp -R /configdir/* .
      shopt -u dotglob
    fi
}

function runGemini {
    echo "Running gemini for chore: ${CHORE_NAME}"
    cd "/workspaces/${REPO_NAME}"
    
    if [ -n "${GITHUB_BOT_NAME}" ]; then
        export GIT_AUTHOR_NAME="${GITHUB_BOT_NAME}"
        export GIT_AUTHOR_EMAIL="${GITHUB_BOT_EMAIL:-bot@example.com}"
        export GIT_COMMITTER_NAME="${GITHUB_BOT_NAME}"
        export GIT_COMMITTER_EMAIL="${GITHUB_BOT_EMAIL:-bot@example.com}"
    fi

    if gemini --yolo < "${PROMPT_FILE}"; then
        echo "Gemini execution successful"
    else
        echo "Gemini execution encountered errors, but we will check for changes anyway."
    fi
}

function restoreConfigDirFiles {
    cd "/workspaces/${REPO_NAME}"
    BASE_BRANCH=$(gh repo view --json defaultBranchRef --jq .defaultBranchRef.name)
    if [ -d "/configdir" ] && [ "$(ls -A /configdir)" ]; then
      echo "Restoring files changed by configdir injection..."
      pushd "/configdir" > /dev/null
      find . -type f -print0 | while IFS= read -r -d '' file; do
          rel_file="${file#./}"
          pushd "/workspaces/${REPO_NAME}" > /dev/null
          if git rev-parse --verify "${BASE_BRANCH}:${rel_file}" >/dev/null 2>&1; then
              echo "Restoring tracked file: $rel_file"
              git checkout "${BASE_BRANCH}" -- "$rel_file"
          else
              echo "Removing untracked file: $rel_file"
              rm -f "$rel_file"
          fi
          popd > /dev/null
      done
      popd > /dev/null
    fi
}

function commitChanges {
    cd "/workspaces/${REPO_NAME}"
    BASE_BRANCH=$(gh repo view --json defaultBranchRef --jq .defaultBranchRef.name)
    # Check for changes (uncommitted or committed on this branch)
    if [ -n "$(git status --porcelain)" ] || [ "$(git rev-parse HEAD)" != "$(git rev-parse ${BASE_BRANCH})" ]; then
        echo "Changes detected."
        
        # If there are uncommitted changes, commit them
        if [ -n "$(git status --porcelain)" ]; then
            echo "Committing uncommitted changes..."
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
        REPO_PATH=$(echo $REPO_URL | sed -E 's/.*github.com[:\/]//;s/\.git$//')
        FORK_OWNER=$(echo "$REPO_PATH" | cut -d'/' -f1)
        
        PR_BODY="This Pull Request was automatically generated by **Overseer** for the **${CHORE_NAME}** chore.

**Chore Definition:** [${CHORE_FILE}](https://github.com/${REPO_OWNER}/${REPO_NAME}/blob/${BASE_BRANCH}/${CHORE_FILE})

---
### Changes
${COMMIT_MSG}"

        PR_URL=$(gh pr create --title "chore: ${CHORE_NAME}" --body "${PR_BODY}" --head "${FORK_OWNER}:${BRANCH_NAME}" --base "${BASE_BRANCH}" || true)
        
        if [ -n "$PR_URL" ] && [[ "$PR_URL" == http* ]]; then
            echo "$PR_URL" > "$(dirname "${PROMPT_FILE}")/agent-output.txt"
            gh pr edit "$PR_URL" --add-label "overseer" || echo "Warning: failed to add label overseer to $PR_URL"
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
    cd "/workspaces/${REPO_NAME}"
    BASE_BRANCH=$(gh repo view --json defaultBranchRef --jq .defaultBranchRef.name)

    EXISTING_PR=$(gh pr list --state open --search "\"chore: ${CHORE_NAME}\" in:title" --json url --jq '.[0].url')
    if [ -n "$EXISTING_PR" ]; then
        echo "An open PR already exists for chore ${CHORE_NAME}: ${EXISTING_PR}"
        echo "An open PR already exists for chore ${CHORE_NAME}: ${EXISTING_PR}" > "$(dirname "${PROMPT_FILE}")/agent-output.txt"
        return
    fi
    
    SLUGIFIED_NAME=$(echo "${CHORE_NAME}" | tr '[:upper:]' '[:lower:]' | tr -c '[:alnum:]' '-' | sed 's/^-//;s/-$//')
    export BRANCH_NAME="chore/${SLUGIFIED_NAME}-$(date +%Y%m%d-%H%M%S)"
    
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
}

setupGit
setupGitRepos
injectConfigDirData
runChore
