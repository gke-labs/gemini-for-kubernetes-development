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
export CLONE_URL={{ printf "%q" .Repo.CloneURL }}
export PROMPT_FILE={{ printf "%q" .PromptFile }}
export GITHUB_USER_ID={{ printf "%q" .User.UserID }}
export GITHUB_USER_EMAIL={{ printf "%q" .User.Email }}
export GITHUB_USER_NAME={{ printf "%q" .User.Name }}
export BRANCH_NAME={{ printf "%q" .BranchName }}
export SOURCE_BRANCH={{ printf "%q" .SourceBranch }}

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
}

function setupGitRepos {
    echo "Running setupGitRepos..."
    if [ ! -d "/workspaces/${REPO_NAME}/.git" ]; then
        echo "cloning repository"
        rm -rf "/workspaces/${REPO_NAME}"
        gh repo clone "{{ .Repo.Owner }}/${REPO_NAME}" "/workspaces/${REPO_NAME}"
    else
        echo "repository already exists"
        cd "/workspaces/${REPO_NAME}"
        git reset --hard
        git clean -fdx
        git fetch origin
    fi

    cd "/workspaces/${REPO_NAME}"
    gh repo set-default "{{ .Repo.Owner }}/${REPO_NAME}" || true
}

function checkoutBranch {
    echo "Running checkoutBranch..."
    cd "/workspaces/${REPO_NAME}"
    
    # Check if branch exists on remote
    if git ls-remote --heads origin "${BRANCH_NAME}" | grep -q "${BRANCH_NAME}"; then
        echo "Branch ${BRANCH_NAME} exists on remote, checking it out..."
        git fetch origin "${BRANCH_NAME}"
        git checkout "${BRANCH_NAME}"
    else
        echo "Branch ${BRANCH_NAME} does not exist on remote, creating from ${SOURCE_BRANCH:-HEAD}..."
        if [ -n "${SOURCE_BRANCH}" ]; then
            git fetch origin "${SOURCE_BRANCH}" || true
            git checkout -b "${BRANCH_NAME}" "origin/${SOURCE_BRANCH}" || git checkout -b "${BRANCH_NAME}"
        else
            git checkout -b "${BRANCH_NAME}"
        fi
        # Push initial branch to remote so tracking is set up
        git push -u origin "${BRANCH_NAME}"
    fi
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

function installExtensions {
    echo "Installing extensions..."
    {{- range .Extensions }}
    gemini extensions install {{ printf "%q" .Source }} {{ if .Ref }}--ref {{ printf "%q" .Ref }}{{ end }} --consent || true
    {{- end }}
}

function runSetup {
    echo "Running runSetup..."
    cd "/workspaces/${REPO_NAME}"
    
    # If there is a setup script in the repo, run it.
    if [ -f "./scripts/setup-agent.sh" ]; then
        echo "Running ./scripts/setup-agent.sh..."
        chmod +x ./scripts/setup-agent.sh
        ./scripts/setup-agent.sh
    fi
}

# Main execution
setupGit
setupGitRepos
checkoutBranch
configureGemini
installExtensions
runSetup
