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

export REPO_NAME={{ printf "%q" .RepoName }}
export REPO_OWNER={{ printf "%q" .RepoOwner }}
export CLONE_URL={{ printf "%q" .Repo.CloneURL }}
export PROMPT_FILE={{ printf "%q" .PromptFile }}
export GITHUB_USER_ID={{ printf "%q" .User.UserID }}
export GITHUB_USER_EMAIL={{ printf "%q" .User.Email }}
export GITHUB_USER_NAME={{ printf "%q" .User.Name }}
export PR_NUMBER={{ .PullRequest.Number }}
export BASE_REF={{ printf "%q" .BaseRef }}

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
        # If directory exists but is not a git repo (e.g. leftover from failed run), remove it first.
        if [ -d "/workspaces/${REPO_NAME}" ]; then
            rm -rf "/workspaces/${REPO_NAME}"
        fi
        (cd /workspaces/ && git clone "${CLONE_URL}" "${REPO_NAME}")
    else
        echo "repository already exists"
        # Ensure a pristine state before doing anything
        # We use git clean -fd (without x) to avoid wiping out toolchains or configs that might be in .gitignore
        cd "/workspaces/${REPO_NAME}"
        git merge --abort || true
        git reset --hard
        git clean -fd
        git fetch origin
    fi

    (cd "/workspaces/${REPO_NAME}" && gh repo set-default "${REPO_OWNER}/${REPO_NAME}" || true)
}

function checkoutPRBranch {
    echo "Running checkoutPRBranch..."
    # gh pr checkout handles forks by adding a remote for the fork and setting up tracking.
    cd "/workspaces/${REPO_NAME}"
    gh pr checkout "${PR_NUMBER}" --force
}

function verifyResolution {
    echo "Verifying conflict resolution..."
    # We check for conflict markers excluding the .git directory.
    # We check for the start and end markers: <<<<<<< and >>>>>>>.
    # We omit ======= because it falsely trips on Markdown headers.
    if grep -rE --exclude-dir=.git "^(<<<<<<< |>>>>>>> |<{7}$|>{7}$)" .; then
        echo "Conflict markers still present!"
        return 1
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
        echo "Found Go project, running tests..."
        (go mod tidy && go test ./...) || TEST_FAILED=true
    fi
    
    # Node.js
    if [ -n "$(find . -maxdepth 2 -name "package.json" -print -quit)" ]; then
        echo "Found Node.js project, running tests..."
        # Find all directories with package.json and run tests.
        # Use process substitution to avoid subshell issues with TEST_FAILED variable.
        while read -r dir; do
            echo "Running tests in $dir"
            (
                cd "$dir"
                if [ -f "yarn.lock" ]; then
                    yarn install --frozen-lockfile && yarn test || exit 1
                else
                    npm ci && npm test || npm install && npm test || exit 1
                fi
            ) || TEST_FAILED=true
        done < <(find . -maxdepth 2 -name "package.json" -exec dirname {} \;)
    fi
    
    # Python
    if [ -n "$(find . -maxdepth 2 -name "pyproject.toml" -o -name "requirements.txt" -print -quit)" ]; then
        echo "Found Python project, running tests..."
        # Find all directories with python configs.
        while read -r dir; do
             echo "Running tests in $dir"
             (
                 cd "$dir"
                 # Use a virtual environment for isolation, outside the repo to keep it clean.
                 local VENV_DIR="/tmp/venv-$(echo -n "$dir" | md5sum | cut -d' ' -f1)"
                 python3 -m venv "$VENV_DIR" && source "$VENV_DIR/bin/activate"
                 if [ -f "requirements.txt" ]; then
                     pip install -r requirements.txt || true
                 fi
                 if [ -f "pyproject.toml" ]; then
                     pip install . || true
                 fi
                 if command -v pytest >/dev/null 2>&1; then
                     pytest || exit 1
                 else
                     python3 -m unittest discover || exit 1
                 fi
             ) || TEST_FAILED=true
        done < <(find . -maxdepth 2 -name "pyproject.toml" -o -name "requirements.txt" -exec dirname {} \; | sort -u)
    fi

    # Makefile (usually at root)
    if [ -f "Makefile" ]; then
        if make -n test &>/dev/null; then
            echo "Found Makefile with test target, running 'make test'..."
            make test || TEST_FAILED=true
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

    # Identify the current branch and its remote (handles forks)
    local CURRENT_BRANCH
    CURRENT_BRANCH="$(git branch --show-current)"
    if [ -z "$CURRENT_BRANCH" ]; then
        echo "Error: Detached HEAD state detected or unable to identify current branch."
        exit 1
    fi
    local REMOTE
    REMOTE="$(git config "branch.${CURRENT_BRANCH}.remote" || echo "origin")"
    
    echo "Current branch: ${CURRENT_BRANCH}, Remote: ${REMOTE}"

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
        # We use FETCH_HEAD to ensure we merge exactly what we just fetched.
        echo "Attempting to merge FETCH_HEAD (origin/${BASE_REF}) into ${CURRENT_BRANCH}..."
        # We use --no-ff to ensure we get a merge commit if it succeeds, and --no-commit to verify before finalizing.
        if git merge FETCH_HEAD --no-ff --no-commit -m "chore: merge branch 'origin/${BASE_REF}' into ${CURRENT_BRANCH}"; then
             echo "Merge successful without conflicts (unexpected in loop). Verifying..."
             if verifyResolution && runTests; then
                 # If it succeeded without conflicts and passed tests, finalize the commit.
                 git commit --no-verify -m "chore: merge branch 'origin/${BASE_REF}' into ${CURRENT_BRANCH}" || echo "Nothing to commit"
                 SUCCESS=true
                 break
             fi
             # If verification failed even if merge "succeeded", something is wrong, continue to next model
             git merge --abort || true
             continue
        fi

        echo "Conflicts detected. Calling Gemini with model $MODEL..."
        # We use --yolo because this runs in a sandboxed pod and we need automated resolution.
        if gemini --yolo --model "$MODEL" --output-format stream-json < "${PROMPT_FILE}" | /opt/repo-agent/gemini-stream-processor --output "${TASK_DIR}/gemini-output-${MODEL}.json"; then
             echo "Gemini execution successful with model: $MODEL"
             
             if verifyResolution && runTests; then
                 echo "Resolution verified with model: $MODEL. Staging and committing..."
                 # Copy the successful output to a standard location for stats tracking
                 cp "${TASK_DIR}/gemini-output-${MODEL}.json" "${TASK_DIR}/gemini-output.json" || true
                 # Only stage tracked and unmerged files to avoid garbage.
                 git add -u
                 # Complete the merge commit
                 git commit --no-verify -m "chore: resolve merge conflicts using Gemini ($MODEL)" || echo "Nothing to commit"
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
git fetch origin "${BASE_REF}" || { echo "Failed to fetch base branch origin/${BASE_REF}. Perhaps it was deleted?"; exit 1; }
echo "Attempting initial merge of FETCH_HEAD (origin/${BASE_REF})..."
BEFORE_MERGE_SHA=$(git rev-parse HEAD)
# Use --no-ff and --no-commit to verify behavioral correctness even for clean merges.
# Use FETCH_HEAD to ensure we merge exactly what we just fetched.
if git merge FETCH_HEAD --no-ff --no-commit -m "chore: merge branch 'origin/${BASE_REF}' into HEAD"; then
    if verifyResolution && runTests; then
        echo "Merge successful without conflicts and tests passed."
        git commit --no-verify -m "chore: merge branch 'origin/${BASE_REF}' into HEAD" || echo "Nothing to commit"
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
