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

export REPO_NAME={{ printf "%q" .RepoName }}
export REPO_OWNER={{ printf "%q" .RepoOwner }}
export CLONE_URL={{ printf "%q" .Repo.CloneURL }}
export PROMPT_FILE={{ printf "%q" .PromptFile }}
export GITHUB_USER_ID={{ printf "%q" .User.UserID }}
export GITHUB_USER_EMAIL={{ printf "%q" .User.Email }}
export GITHUB_USER_NAME={{ printf "%q" .User.Name }}
export PR_NUMBER={{ .PullRequest.Number }}
export BASE_REF={{ printf "%q" .BaseRef }}

if [ -z "${REPO_NAME}" ]; then
    echo "Error: REPO_NAME environment variable is not set or empty. Aborting to prevent accidental deletion."
    echo "Context: REPO_OWNER=${REPO_OWNER}, CLONE_URL=${CLONE_URL}, PR_NUMBER=${PR_NUMBER}"
    exit 1
fi

TASK_DIR="$(dirname "${PROMPT_FILE}")"

# Disable git hooks for automated operations to prevent local hooks from blocking progress or causing side effects.
export GIT_CONFIG_PARAMETERS="'core.hooksPath=/dev/null'"

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
{{ .Repo.Host }}:
    users:
        ${GH_USER}:
            oauth_token: ${GITHUB_USER_TOKEN}
    git_protocol: https
    oauth_token: ${GITHUB_USER_TOKEN}
    user: ${GH_USER}
EOF

    if [ -n "$GITHUB_BOT_EMAIL" ]; then
        git config --global user.email "${GITHUB_BOT_EMAIL}"
    elif [ -n "$GITHUB_USER_EMAIL" ] && [ "$GITHUB_USER_EMAIL" != "" ]; then
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
        # Ensure directory is removed if it exists but is not a git repo (more robust than [ -d ])
        rm -rf "/workspaces/${REPO_NAME}"
        # Use gh repo clone for better auth handling
        gh repo clone "${REPO_OWNER}/${REPO_NAME}" "/workspaces/${REPO_NAME}"
    else
        echo "repository already exists"
        # Ensure a pristine state before doing anything
        cd "/workspaces/${REPO_NAME}"
        git merge --abort || true
        git reset --hard
        git clean -fd
        
        # Retry loop for fetch to handle transient network issues
        for i in {1..3}; do
            if git fetch origin; then
                break
            fi
            echo "git fetch failed, retrying in 5s... ($i/3)"
            sleep 5
            if [ $i -eq 3 ]; then
                echo "Error: git fetch failed after 3 attempts."
                exit 1
            fi
        done
    fi

    cd "/workspaces/${REPO_NAME}"
    gh repo set-default "${REPO_OWNER}/${REPO_NAME}" || true
}

function checkoutPRBranch {
    echo "Running checkoutPRBranch..."
    # gh pr checkout handles forks by adding a remote for the fork and setting up tracking.
    cd "/workspaces/${REPO_NAME}"
    gh pr checkout "${PR_NUMBER}" --force
}

function verifyResolution {
    echo "Verifying conflict resolution..."
    # Use git diff HEAD --check to identify conflict markers in both staged and unstaged changes.
    # We disable common whitespace checks that might be introduced by the LLM but aren't merge conflicts.
    if ! git -c core.whitespace=blank-at-eol,-blank-at-eof,-space-before-tab,-trailing-space diff HEAD --check; then
        echo "Conflict markers found by git diff --check"
        return 1
    fi
    # Supplemental check for conflict markers using grep as a secondary verification,
    # specifically targeting only files modified in this merge to ensure performance.
    local MODIFIED_FILES
    MODIFIED_FILES=$(git diff --name-only HEAD)
    if [ -n "$MODIFIED_FILES" ]; then
        if echo "$MODIFIED_FILES" | xargs grep -E "^<{7}([[:space:]]|$)|^>{7}([[:space:]]|$)" > /dev/null 2>&1; then
            echo "Conflict markers still present!"
            return 1
        fi
    fi
    echo "No conflict markers found. Resolution looks good."
    return 0
}

function runTests {
    echo "Running tests to verify resolution..."
    cd "/workspaces/${REPO_NAME}"
    
    local TEST_FAILED=false
    
    # Discovery and execution for multiple frameworks/languages.
    # In monorepos, we search for markers in subdirectories too.
    
    # Go
    if [ -n "$(find . -maxdepth 2 -name "go.mod" -print -quit)" ]; then
        echo "Found Go project(s), running tests (no cache)..."
        while read -r dir; do
            echo "Running tests in Go module at $dir"
            (
                set -e
                cd "$dir"
                go mod tidy
                go test -count=1 ./...
            )
            if [ $? -ne 0 ]; then
                TEST_FAILED=true
            fi
        done < <(find . -maxdepth 2 -name "go.mod" -exec dirname {} \; | sort -u)
    fi
    
    # Node.js
    if [ -n "$(find . -maxdepth 2 \( -name "package.json" -o -name "pnpm-lock.yaml" -o -name "yarn.lock" \) -print -quit)" ]; then
        echo "Found Node.js project, running tests..."
        while read -r dir; do
            echo "Running tests in $dir"
            (
                set -e
                cd "$dir"
                if [ -f "pnpm-lock.yaml" ] && command -v pnpm >/dev/null 2>&1; then
                    pnpm install --frozen-lockfile
                    pnpm test
                elif [ -f "yarn.lock" ] && command -v yarn >/dev/null 2>&1; then
                    yarn install --frozen-lockfile
                    yarn test
                elif [ -f "package-lock.json" ]; then
                    npm ci
                    npm test
                else
                    npm install
                    npm test
                fi
            )
            if [ $? -ne 0 ]; then
                TEST_FAILED=true
            fi
        done < <(find . -maxdepth 2 -name "package.json" -exec dirname {} \; | sort -u)
    fi
    
    # Python
    if [ -n "$(find . -maxdepth 2 \( -name "pyproject.toml" -o -name "requirements.txt" -o -name "setup.py" \) -print -quit)" ]; then
        echo "Found Python project, running tests..."
        while read -r dir; do
             echo "Running tests in $dir"
             (
             set -e
             cd "$dir"
             local V_DIR
             V_DIR="/tmp/venv-$(echo -n "$dir" | md5sum | cut -d' ' -f1)"
             python3 -m venv "$V_DIR"
             source "$V_DIR/bin/activate"
             if [ -f "pyproject.toml" ]; then
                 pip install .
             elif [ -f "setup.py" ]; then
                 pip install .
             elif [ -f "requirements.txt" ]; then
                 pip install -r requirements.txt
             fi
                 if command -v pytest >/dev/null 2>&1; then
                     pytest
                 else
                     # Capture output to check if any tests were found
                     local UT_OUTPUT
                     UT_OUTPUT=$(python3 -m unittest discover 2>&1)
                     echo "$UT_OUTPUT"
                     if echo "$UT_OUTPUT" | grep -q "Ran 0 tests"; then
                         echo "Warning: No tests found by unittest discover in $dir"
                     fi
                 fi
             )
             if [ $? -ne 0 ]; then
                 TEST_FAILED=true
             fi
        done < <(find . -maxdepth 2 \( -name "pyproject.toml" -o -name "requirements.txt" -o -name "setup.py" \) -exec dirname {} \; | sort -u)
    fi

    # Makefile (usually at root)
    if [ -f "Makefile" ]; then
        if make -n test &>/dev/null; then
            echo "Found Makefile with test target, running 'make test'..."
            if ! make test; then
                TEST_FAILED=true
            fi
        else
            echo "Found Makefile but no test target, skipping."
        fi
    fi
    
    if [ "$TEST_FAILED" = true ]; then
        return 1
    fi
    return 0
}

function runGemini {
    echo "Running Gemini to resolve conflicts..."
    cd "/workspaces/${REPO_NAME}"
    
    if [ -n "$GITHUB_BOT_NAME" ]; then
        export GIT_AUTHOR_NAME="$GITHUB_BOT_NAME"
        export GIT_COMMITTER_NAME="$GITHUB_BOT_NAME"
        export GIT_AUTHOR_EMAIL="${GITHUB_BOT_EMAIL:-bot@example.com}"
        export GIT_COMMITTER_EMAIL="${GITHUB_BOT_EMAIL:-bot@example.com}"
    fi

    # Identify the current branch (handles forks)
    local CURRENT_BRANCH
    CURRENT_BRANCH="$(git branch --show-current)"
    if [ -z "$CURRENT_BRANCH" ]; then
        echo "Error: Detached HEAD state detected or unable to identify current branch."
        exit 1
    fi
    
    echo "Current branch: ${CURRENT_BRANCH}"

    # Capture the original HEAD before the merge loop to ensure we can always reset to a clean state.
    local ORIG_HEAD
    ORIG_HEAD=$(git rev-parse HEAD)

    MODELS=( {{ range .Models }}"{{ . }}" {{ end }} )
    SUCCESS=false
    for MODEL in "${MODELS[@]}"; do
        echo "Trying model: $MODEL"
        
        # Abort any previous merge and reset to the original pristine state.
        git merge --abort || true
        git reset --hard "$ORIG_HEAD"
        git clean -fd
        
        # Re-attempt merge to get conflicts for the model to work on.
        echo "Attempting to merge base branch into ${CURRENT_BRANCH}..."
        # Re-fetch to ensure remote branch is fresh for this iteration
        git fetch origin "${BASE_REF}"
        # We merge from FETCH_HEAD to ensure we are using exactly what we just fetched.
        if git merge FETCH_HEAD --no-ff --no-commit; then
             echo "Merge successful without conflicts (unexpected in loop). Verifying..."
             set +e
             verifyResolution
             V_RES=$?
             if [ $V_RES -eq 0 ]; then
                 runTests
                 T_RES=$?
             else
                 T_RES=1
             fi
             set -e

             if [ $V_RES -eq 0 ] && [ $T_RES -eq 0 ]; then
                 # If it succeeded without conflicts and passed tests, finalize the commit.
                 # Use --no-edit to preserve standard MERGE_MSG
                 git commit --no-verify --no-edit || echo "Nothing to commit"
                 SUCCESS=true
                 break
             else
                 echo "Verification failed for clean merge. Exiting as LLM cannot fix broken base/branch."
                 exit 1
             fi
        fi

        echo "Conflicts detected. Calling Gemini with model $MODEL..."
        # We use --yolo because this runs in a sandboxed pod and we need automated resolution.
        if gemini --yolo --model "$MODEL" --output-format stream-json < "${PROMPT_FILE}" | /opt/repo-agent/gemini-stream-processor --output "${TASK_DIR}/gemini-output-${MODEL}.json"; then
             echo "Gemini execution successful with model: $MODEL"
             
             set +e
             verifyResolution
             V_RES=$?
             if [ $V_RES -eq 0 ]; then
                 runTests
                 T_RES=$?
             else
                 T_RES=1
             fi
             set -e

             if [ $V_RES -eq 0 ] && [ $T_RES -eq 0 ]; then
                 echo "Resolution verified with model: $MODEL. Staging and committing..."
                 # Copy the successful output to a standard location for stats tracking
                 cp "${TASK_DIR}/gemini-output-${MODEL}.json" "${TASK_DIR}/gemini-output.json" || true
                 # Only stage tracked and unmerged files to avoid garbage.
                 git add -u
                 # Complete the merge commit. We keep the standard MERGE_MSG but append Gemini info if we can.
                 # Since --no-edit is safer, we'll use that.
                 git commit --no-verify --no-edit || echo "Nothing to commit"
                 SUCCESS=true
                 break
             else
                 echo "Resolution verification or tests failed with model $MODEL."
                 # The merge is still in progress (with conflict markers or broken code).
                 # We will reset to ORIG_HEAD in the next iteration.
             fi
        else
             echo "Gemini execution failed with model: $MODEL."
             # Gemini might have left the repo in a messy state.
        fi
    done
    
    if [ "$SUCCESS" = false ]; then
        echo "All models failed to resolve conflicts or pass tests."
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
checkoutPRBranch

# Attempt initial merge to see if we even need LLM
cd "/workspaces/${REPO_NAME}"
# Ensure we have base branch
git fetch origin "${BASE_REF}" || { echo "Failed to fetch base branch. Perhaps it was deleted?"; exit 1; }
echo "Attempting initial merge of FETCH_HEAD..."
BEFORE_MERGE_SHA=$(git rev-parse HEAD)
# Use --no-ff and --no-commit to verify behavioral correctness even for clean merges.
if git merge FETCH_HEAD --no-ff --no-commit; then
    set +e
    verifyResolution
    V_RES=$?
    if [ $V_RES -eq 0 ]; then
        runTests
        T_RES=$?
    else
        T_RES=1
    fi
    set -e
    if [ $V_RES -eq 0 ] && [ $T_RES -eq 0 ]; then
        echo "Merge successful without conflicts and tests passed."
        git commit --no-verify --no-edit || echo "Nothing to commit"
        pushChanges
        exit 0
    fi
    echo "Merge succeeded but conflict markers found or tests failed. Proceeding to LLM loop."
    git merge --abort || true
    git reset --hard "$BEFORE_MERGE_SHA"
else
    echo "Initial merge failed with conflicts. Proceeding to LLM loop."
    git merge --abort || true
fi

echo "Proceeding with LLM resolution loop."

# Install extensions if any
{{- range .Extensions }}
echo "Installing extension: {{ .Source }}"
for i in $(seq 1 3); do
    if gemini extensions install "{{ .Source }}" {{ if .Ref }}--ref "{{ .Ref }}"{{ end }} --consent; then
        break
    fi
    if [ $i -lt 3 ]; then
        echo "Extension installation failed, retrying in 5s... ($i/3)"
        sleep 5
    else
        echo "Warning: Extension installation failed after 3 attempts. Continuing anyway..."
    fi
done
{{- end }}

runGemini

pushChanges
