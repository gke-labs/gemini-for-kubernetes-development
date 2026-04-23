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
export CLONE_URL={{ printf "%q" .Repo.CloneURL }}
export PROMPT_FILE={{ printf "%q" .PromptFile }}
export GITHUB_USER_ID={{ printf "%q" .User.UserID }}
export GITHUB_USER_EMAIL={{ printf "%q" .User.Email }}
export GITHUB_USER_NAME={{ printf "%q" .User.Name }}
export PR_NUMBER={{ printf "%q" .PullRequest.Number }}

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
    echo "creating /root/.config/gh directory"
    mkdir -p /root/.config/gh

    local GH_USER; GH_USER="${GITHUB_USER_ID}"
    if [ -n "${GITHUB_BOT_LOGIN}" ]; then
        GH_USER="${GITHUB_BOT_LOGIN}"
    fi

    echo "writing gh config"
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

    echo "running git config user.email"
    if [ -n "${GITHUB_BOT_EMAIL}" ]; then
        git config --global user.email "${GITHUB_BOT_EMAIL}"
    else
        git config --global user.email "${GITHUB_USER_EMAIL}"
    fi

    echo "running git config user.name"
    if [ -n "${GITHUB_BOT_NAME}" ]; then
        git config --global user.name "${GITHUB_BOT_NAME}"
    else
        git config --global user.name "${GITHUB_USER_NAME}"
    fi

    echo "running gh auth setup-git"
    gh auth setup-git

    echo "Configuring global git ignore"
    git config --global core.excludesfile /root/.gitignore_global
    cat <<'EOF' > /root/.gitignore_global
manager
bin/
EOF
}

function setupGitRepos {
    echo "Running setupGitRepos..."
    
    # Check if repo already exists (reuse sandbox case)
    if [ ! -d "/workspaces/${REPO_NAME}" ]; then
        echo "cloning repository ${REPO_OWNER}/${REPO_NAME}"
        gh repo clone "${REPO_OWNER}/${REPO_NAME}" "/workspaces/${REPO_NAME}"
    else
        echo "repository already exists"
        # Optional: fetch latest changes
        (
            cd "/workspaces/${REPO_NAME}"
            for i in $(seq 1 3); do
                if git fetch origin; then
                    break
                fi
                echo "git fetch failed, retrying in 5s... (${i}/3)"
                sleep 5
            done
        )
    fi

    echo "running gh repo set-default"
    (
        cd "/workspaces/${REPO_NAME}"
        gh repo set-default "${REPO_OWNER}/${REPO_NAME}" || true
    )
}

function checkoutPRBranch {
    echo "Running checkoutPRBranch..."
    echo "checking out PR #${PR_NUMBER}"
    (
        cd "/workspaces/${REPO_NAME}"
        gh pr checkout "${PR_NUMBER}" --force
    )
}

function configureGemini {
    echo "Running configureGemini..."
    echo "creating /root/.gemini directory"
    mkdir -p /root/.gemini

    echo "writing gemini config"
    cat <<'EOF' > /root/.gemini/settings.json
{
  "general": {
    "enableAutoUpdate": false,
    "retryFetchErrors": true
  }
}
EOF
}

function runGemini {
    echo "Running runGemini..."
    echo "running gemini in yolo mode"

    if [ -n "${GITHUB_BOT_NAME}" ]; then
        echo "Using bot identity for commits"
        export GIT_AUTHOR_NAME="${GITHUB_BOT_NAME}"
        export GIT_AUTHOR_EMAIL="${GITHUB_BOT_EMAIL}"
        export GIT_COMMITTER_NAME="${GITHUB_BOT_NAME}"
        export GIT_COMMITTER_EMAIL="${GITHUB_BOT_EMAIL}"
    fi

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

    MODELS=( {{ range .Models }}{{ printf "%q" . }} {{ end }} )
    SUCCESS=false
    for MODEL in "${MODELS[@]}"; do
        echo "Trying model: ${MODEL}"
        if (
            cd "/workspaces/${REPO_NAME}"
            export GEMINI_API_KEY="${GEMINI_API_KEY}"
            gemini --yolo --model "${MODEL}" --output-format stream-json < "${PROMPT_FILE}" | /opt/repo-agent/gemini-stream-processor --output "$(dirname "${PROMPT_FILE}")/gemini-output.json"
        ); then
             echo "Gemini execution successful with model: ${MODEL}"
             SUCCESS=true
             break
        else
             echo "Gemini execution failed with model: ${MODEL}. Retrying with next model..."
        fi
    done
    
    # Security: Restore GitHub config and token after untrusted code execution.
    if [ -d "${TEMP_GH_CONFIG_DIR}" ]; then
        mv "${TEMP_GH_CONFIG_DIR}" "${ORIG_GH_CONFIG_DIR}"
    fi
    export GITHUB_USER_TOKEN="${ORIG_GITHUB_USER_TOKEN}"
    export GITHUB_TOKEN="${ORIG_GITHUB_TOKEN}"

    if [ "${SUCCESS}" = false ]; then
        echo "All models failed."
        exit 1
    fi
}

function installExtensions {
    echo "Installing extensions..."

    # Security: Hide GitHub OAuth token and config directory before executing untrusted code (extensions)
    local ORIG_GH_CONFIG_DIR; ORIG_GH_CONFIG_DIR="/root/.config/gh"
    local TEMP_GH_CONFIG_DIR; TEMP_GH_CONFIG_DIR="/tmp/gh-config-hidden-ext-$(date +%s)"
    if [ -d "${ORIG_GH_CONFIG_DIR}" ]; then
        mv "${ORIG_GH_CONFIG_DIR}" "${TEMP_GH_CONFIG_DIR}"
    fi
    local ORIG_GITHUB_USER_TOKEN; ORIG_GITHUB_USER_TOKEN="${GITHUB_USER_TOKEN}"
    local ORIG_GITHUB_TOKEN; ORIG_GITHUB_TOKEN="${GITHUB_TOKEN}"
    unset GITHUB_USER_TOKEN
    unset GITHUB_TOKEN

    {{- range .Extensions }}
    echo "Installing extension: {{ printf "%q" .Source }}"
    for i in $(seq 1 3); do
        if gemini extensions install {{ printf "%q" .Source }} {{ if .Ref }}--ref {{ printf "%q" .Ref }}{{ end }} --consent; then
            break
        fi
        if [ "${i}" -lt 3 ]; then
            echo "Extension installation failed, retrying in 5s... (${i}/3)"
            sleep 5
        else
            echo "Warning: Extension installation failed after 3 attempts. Continuing anyway..."
        fi
    done
    {{- end }}

    # Security: Restore GitHub config and token
    if [ -d "${TEMP_GH_CONFIG_DIR}" ]; then
        mv "${TEMP_GH_CONFIG_DIR}" "${ORIG_GH_CONFIG_DIR}"
    fi
    export GITHUB_USER_TOKEN="${ORIG_GITHUB_USER_TOKEN}"
    export GITHUB_TOKEN="${ORIG_GITHUB_TOKEN}"
}

function pushChangesAndRespond {
    echo "Pushing changes and responding to feedback..."
    pushd "/workspaces/${REPO_NAME}" > /dev/null

    # Check if there are changes
    if [ -n "$(git status --porcelain)" ]; then
        echo "Changes detected, committing and pushing..."
        git add .
        git commit -m "Address review feedback" || echo "Nothing to commit"
        git push
    else
        echo "No changes to commit."
    fi

    # Extract response text from Gemini output
    local output_file; output_file="$(dirname "${PROMPT_FILE}")/raw-agent-output.txt"
    
    if [ -s "${output_file}" ]; then
        echo "Posting response to PR #${PR_NUMBER}..."
        # Add a footer and post as a comment
        echo -e "\n\n*(This comment was generated by Overseer)*" >> "${output_file}"
        gh pr review "${PR_NUMBER}" --comment --body-file "${output_file}" || gh issue comment "${PR_NUMBER}" --body-file "${output_file}"
    else
        echo "Gemini output not found, cannot post response."
    fi

    popd > /dev/null
}

# Main execution
setupGit
setupGitRepos
# HACK: Avoid git lock issues
sleep 5
checkoutPRBranch
configureGemini
installExtensions
runGemini
pushChangesAndRespond
