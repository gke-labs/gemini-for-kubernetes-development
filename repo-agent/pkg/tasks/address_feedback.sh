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

export REPO_NAME={{ printf "%q" .Repo.Name }}
export REPO_OWNER={{ printf "%q" .Repo.Owner }}
export CLONE_URL={{ printf "%q" .Repo.CloneURL }}
export PROMPT_FILE={{ printf "%q" .PromptFile }}
export GITHUB_USER_ID={{ printf "%q" .User.UserID }}
export GITHUB_USER_EMAIL={{ printf "%q" .User.Email }}
export GITHUB_USER_NAME={{ printf "%q" .User.Name }}
export PR_NUMBER={{ printf "%q" .PullRequest.Number }}

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

    local GH_USER; GH_USER="${GITHUB_USER_ID}"
    if [ -n "${GITHUB_BOT_LOGIN}" ]; then
        GH_USER="${GITHUB_BOT_LOGIN}"
    fi

    local SAFE_GH_USER; SAFE_GH_USER=$(printf "%q" "${GH_USER}")
    local SAFE_TOKEN; SAFE_TOKEN=$(printf "%q" "${GITHUB_USER_TOKEN}")
    cat <<EOF > /root/.config/gh/hosts.yml
{{ .Repo.Host }}:
    users:
        ${SAFE_GH_USER}:
            oauth_token: ${SAFE_TOKEN}
    git_protocol: https
    oauth_token: ${SAFE_TOKEN}
    user: ${SAFE_GH_USER}
EOF

    if [ -n "${GITHUB_BOT_EMAIL}" ]; then
        git config --global user.email "${GITHUB_BOT_EMAIL}"
    else
        git config --global user.email "${GITHUB_USER_EMAIL}"
    fi

    if [ -n "${GITHUB_BOT_NAME}" ]; then
        git config --global user.name "${GITHUB_BOT_NAME}"
    else
        git config --global user.name "${GITHUB_USER_NAME}"
    fi

    gh auth setup-git

    git config --global core.excludesfile /root/.gitignore_global
    cat <<EOF > /root/.gitignore_global
manager
bin/
EOF
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

function checkoutPRBranch {
    echo "Running checkoutPRBranch..."
    cd "/workspaces/${REPO_NAME}"
    gh pr checkout "${PR_NUMBER}" --force
}

function configureGemini {
    echo "Running configureGemini..."
    mkdir -p /root/.gemini
    cat <<EOF > /root/.gemini/settings.json
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
    cd "/workspaces/${REPO_NAME}"

    if [ -n "${GITHUB_BOT_NAME}" ]; then
        export GIT_AUTHOR_NAME="${GITHUB_BOT_NAME}"
        export GIT_AUTHOR_EMAIL="${GITHUB_BOT_EMAIL:-bot@example.com}"
        export GIT_COMMITTER_NAME="${GITHUB_BOT_NAME}"
        export GIT_COMMITTER_EMAIL="${GITHUB_BOT_EMAIL:-bot@example.com}"
    fi

    MODELS=( {{ range .Models }}{{ printf "%q" . }} {{ end }} )
    SUCCESS=false
    for MODEL in "${MODELS[@]}"; do
        echo "Trying model: ${MODEL}"
        if gemini --yolo --model "${MODEL}" --output-format stream-json < "${PROMPT_FILE}" | /opt/repo-agent/gemini-stream-processor --output "$(dirname "${PROMPT_FILE}")/gemini-output.json"; then
             echo "Gemini execution successful with model: ${MODEL}"
             SUCCESS=true
             break
        else
             echo "Gemini execution failed with model: ${MODEL}. Retrying..."
        fi
    done
    
    if [ "${SUCCESS}" = false ]; then
        echo "All models failed."
        exit 1
    fi
}

function installExtensions {
    echo "Installing extensions..."
    {{- range .Extensions }}
    gemini extensions install {{ printf "%q" .Source }} {{ if .Ref }}--ref {{ printf "%q" .Ref }}{{ end }} --consent || true
    {{- end }}
}

# Main execution
setupGit
setupGitRepos
checkoutPRBranch
configureGemini
installExtensions
runGemini
