#!/bin/bash
# Copyright 2026.
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

export REPO_NAME="{{ .Repo.Name }}"
export CLONE_URL="{{ .Repo.CloneURL }}"
export PROMPT_FILE="{{ .PromptFile }}"
export GITHUB_USER_ID="{{ .User.UserID }}"
export GITHUB_USER_EMAIL="{{ .User.Email }}"
export GITHUB_USER_NAME="{{ .User.Name }}"
export PR_NUMBER={{ .PullRequest.Number }}
export BASE_REF="{{ .BaseRef }}"

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
    elif [ -n "$GITHUB_USER_EMAIL" ] && [ "$GITHUB_USER_EMAIL" != "<nil>" ] && [ "$GITHUB_USER_EMAIL" != "" ]; then
        git config --global user.email "${GITHUB_USER_EMAIL}"
    else
        git config --global user.email "bot@example.com"
    fi

    if [ -n "$GITHUB_BOT_NAME" ]; then
        git config --global user.name "${GITHUB_BOT_NAME}"
    elif [ -n "$GITHUB_USER_NAME" ] && [ "$GITHUB_USER_NAME" != "<nil>" ] && [ "$GITHUB_USER_NAME" != "" ]; then
        git config --global user.name "${GITHUB_USER_NAME}"
    else
        git config --global user.name "Gemini Bot"
    fi

    gh auth setup-git
}

function setupGitRepos {
    echo "Running setupGitRepos..."
    if [ ! -d "/workspaces/${REPO_NAME}/.git" ]; then
        echo "cloning repository"
        (cd /workspaces/ && git clone "${CLONE_URL}")
    else
        echo "repository already exists"
        # Ensure a pristine state before doing anything
        (cd "/workspaces/${REPO_NAME}" && git reset --hard && git clean -fd && git fetch origin)
    fi

    (cd "/workspaces/${REPO_NAME}" && gh repo set-default "${CLONE_URL}" || true)
}

function checkoutPRBranch {
    echo "Running checkoutPRBranch..."
    # gh pr checkout handles forks by adding a remote for the fork and setting up tracking.
    cd "/workspaces/${REPO_NAME}"
    gh pr checkout "${PR_NUMBER}" --force
    # Ensure we are up to date with the remote branch
    git pull --rebase || true
}

function verifyResolution {
    echo "Verifying conflict resolution..."
    # We check for conflict markers excluding the .git directory
    if grep -r --exclude-dir=.git "^<<<<<<<" .; then
        echo "Conflict markers still present!"
        return 1
    fi
    echo "No conflict markers found. Resolution looks good."
    return 0
}

function runGemini {
    echo "Running Gemini to resolve conflicts..."
    cd "/workspaces/${REPO_NAME}"
    
    if [ -n "$GITHUB_BOT_NAME" ]; then
        export GIT_AUTHOR_NAME="$GITHUB_BOT_NAME"
        export GIT_AUTHOR_EMAIL="$GITHUB_BOT_EMAIL"
        export GIT_COMMITTER_NAME="$GITHUB_BOT_NAME"
        export GIT_COMMITTER_EMAIL="$GITHUB_BOT_EMAIL"
    fi

    # Identify the current branch and its remote (handles forks)
    local CURRENT_BRANCH
    CURRENT_BRANCH="$(git branch --show-current)"
    local REMOTE
    REMOTE="$(git config "branch.${CURRENT_BRANCH}.remote" || echo "origin")"
    
    echo "Current branch: ${CURRENT_BRANCH}, Remote: ${REMOTE}"

    # Ensure we have the latest of the current branch and the base branch
    git fetch "${REMOTE}" "${CURRENT_BRANCH}" || true
    git fetch origin "${BASE_REF}" || { echo "Failed to fetch base branch origin/${BASE_REF}. Perhaps it was deleted?"; exit 1; }

    MODELS=( {{ range .Models }}"{{ . }}" {{ end }} )
    SUCCESS=false
    for MODEL in "${MODELS[@]}"; do
        echo "Trying model: $MODEL"
        
        # Abort any previous merge and reset to clean state
        git merge --abort || true
        git reset --hard "HEAD"
        git clean -fd
        
        # Re-attempt merge to get conflicts for the model to work on.
        # We MUST re-create the conflicts so the model has something to resolve.
        echo "Attempting to merge origin/${BASE_REF} into ${CURRENT_BRANCH}..."
        # We use --no-commit to keep it in a merging state if it succeeds, but usually it fails with conflicts.
        if git merge "origin/${BASE_REF}" --no-commit -m "Merge branch 'origin/${BASE_REF}' into ${CURRENT_BRANCH}"; then
             echo "Merge successful without conflicts (unexpected in loop). Committing..."
             if verifyResolution; then
                 git commit -m "chore: merge branch 'origin/${BASE_REF}' into ${CURRENT_BRANCH}" || echo "Nothing to commit"
                 SUCCESS=true
                 break
             fi
             # If verification failed even if merge "succeeded", something is wrong, continue to next model
             git merge --abort || true
             continue
        fi

        echo "Conflicts detected. Calling Gemini with model $MODEL..."
        # We use --yolo because this runs in a sandboxed pod and we need automated resolution.
        if (export GEMINI_API_KEY="${GEMINI_API_KEY}" && gemini --yolo --model "$MODEL" --output-format stream-json < "${PROMPT_FILE}" | /opt/repo-agent/gemini-stream-processor --output "$(dirname "${PROMPT_FILE}")/gemini-output.json"); then
             echo "Gemini execution successful with model: $MODEL"
             
             if verifyResolution; then
                 echo "Resolution verified with model: $MODEL. Staging and committing..."
                 git add .
                 # Complete the merge commit
                 git commit -m "chore: resolve merge conflicts using Gemini ($MODEL)" || echo "Nothing to commit"
                 SUCCESS=true
                 break
             else
                 echo "Resolution verification failed: conflict markers still present with model $MODEL."
                 # The merge is still in progress (with conflict markers).
                 # We will reset in the next iteration.
             fi
        else
             echo "Gemini execution failed with model: $MODEL."
             # Gemini might have left the repo in a messy state.
        fi
    done
    
    if [ "$SUCCESS" = false ]; then
        echo "All models failed to resolve conflicts."
        exit 1
    fi
}

function pushChanges {
    echo "Pushing resolved changes..."
    cd "/workspaces/${REPO_NAME}"
    # Safer push that handles tracking correctly
    git push
}

# Main execution
setupGit
setupGitRepos
sleep 5
checkoutPRBranch

# Attempt initial merge to see if we even need LLM
cd "/workspaces/${REPO_NAME}"
# Ensure we have base branch
git fetch origin "${BASE_REF}" || { echo "Failed to fetch base branch origin/${BASE_REF}. Perhaps it was deleted?"; exit 1; }
echo "Attempting initial merge of origin/${BASE_REF}..."
if git merge "origin/${BASE_REF}" -m "Merge branch 'origin/${BASE_REF}' into HEAD"; then
    if verifyResolution; then
        echo "Merge successful without conflicts."
        pushChanges
        exit 0
    fi
    echo "Merge succeeded but conflict markers found? Proceeding to LLM loop."
    git reset --hard HEAD^ # Undo the merge
fi

echo "Merge conflicts detected or verification failed. Proceeding with LLM resolution loop."

# Install extensions if any
{{- range .Extensions }}
gemini extensions install "{{ .Source }}" {{ if .Ref }}--ref "{{ .Ref }}"{{ end }} --consent
{{- end }}

runGemini

# Run tests if available
cd "/workspaces/${REPO_NAME}"
TEST_FAILED=false
if [ -f "Makefile" ]; then
    make test || TEST_FAILED=true
elif [ -f "go.mod" ]; then
    go test ./... || TEST_FAILED=true
elif [ -f "package.json" ]; then
    if [ -f "yarn.lock" ]; then
        yarn install && yarn test || TEST_FAILED=true
    else
        npm install && npm test || TEST_FAILED=true
    fi
fi

if [ "$TEST_FAILED" = true ]; then
    echo "Tests failed after conflict resolution. Not pushing changes."
    exit 1
fi

pushChanges

