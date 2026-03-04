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
set -x

# It expects the following environment variables to be set:
# - GEMINI_API_KEY
# - GITHUB_USER_TOKEN

export REPO_NAME="{{ .Repo.Name }}"
export CLONE_URL={{ .Repo.CloneURL }}
export BRANCH_NAME="{{ .BranchName }}"
export SOURCE_BRANCH="{{ .SourceBranch }}"
export PROMPT_FILE="{{ .PromptFile }}"
export GITHUB_USER_ID={{ .User.UserID }}
export GITHUB_USER_EMAIL={{ .User.Email }}
export GITHUB_USER_NAME="{{ .User.Name }}"

function setupGit {
    echo "Running setupGit..."
    echo "creating /root/.config/gh directory"
    mkdir -p /root/.config/gh

    echo "writing gh config"
    cat <<EOF > /root/.config/gh/hosts.yml
github.com:
    users:
        ${GITHUB_USER_ID}:
            oauth_token: ${GITHUB_USER_TOKEN}
    git_protocol: https
    oauth_token: ${GITHUB_USER_TOKEN}
    user: ${GITHUB_USER_ID}
EOF

    echo "running git config user.email"
    git config --global user.email ${GITHUB_USER_EMAIL}

    echo "running git config user.name"
    git config --global user.name ${GITHUB_USER_NAME}

    echo "running gh auth setup-git"
    gh auth setup-git
}

function setupGitRepos {
    echo "Running setupGitRepos..."
    
    # Check if repo already exists (reuse sandbox case)
    if [ ! -d "/workspaces/${REPO_NAME}" ]; then
        echo "cloning repository"
        (cd /workspaces/ && git clone ${CLONE_URL})
    else
        echo "repository already exists"
        # Optional: fetch latest changes
        (cd "/workspaces/${REPO_NAME}" && git fetch origin)
    fi
}

function checkoutBranch {
    echo "Running checkoutBranch..."
    cd "/workspaces/${REPO_NAME}"
    
    # Check if branch exists locally
    if git show-ref --verify --quiet "refs/heads/${BRANCH_NAME}"; then
        echo "Branch ${BRANCH_NAME} exists locally, checking out..."
        git checkout "${BRANCH_NAME}"
    # Check if branch exists remotely
    elif git show-ref --verify --quiet "refs/remotes/origin/${BRANCH_NAME}"; then
        echo "Branch ${BRANCH_NAME} exists remotely, checking out..."
        git checkout "${BRANCH_NAME}"
    else
        echo "Branch ${BRANCH_NAME} does not exist."
        if [ -n "${SOURCE_BRANCH}" ] && [ "${SOURCE_BRANCH}" != "${BRANCH_NAME}" ]; then
             echo "Creating ${BRANCH_NAME} from ${SOURCE_BRANCH}..."
             # Try remote source first
             if git show-ref --verify --quiet "refs/remotes/origin/${SOURCE_BRANCH}"; then
                 git checkout -b "${BRANCH_NAME}" "origin/${SOURCE_BRANCH}"
             elif git show-ref --verify --quiet "refs/heads/${SOURCE_BRANCH}"; then
                 git checkout -b "${BRANCH_NAME}" "${SOURCE_BRANCH}"
             else
                 echo "Source branch ${SOURCE_BRANCH} not found, creating from default..."
                 git checkout -b "${BRANCH_NAME}"
             fi
        else
             echo "Creating ${BRANCH_NAME}..."
             git checkout -b "${BRANCH_NAME}"
        fi

        echo "Pushing ${BRANCH_NAME} to origin..."
        git push -u origin "${BRANCH_NAME}"
    fi
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

function runGemini {
    # Only run gemini if a prompt was actually provided in env or prompt file is non-empty
    if [ -s "${PROMPT_FILE}" ]; then
        echo "Running runGemini..."
        echo "running gemini in yolo mode"
        MODELS=( {{ range .Models }}"{{ . }}" {{ end }} )
        SUCCESS=false
        for MODEL in "${MODELS[@]}"; do
            echo "Trying model: $MODEL"
            if (cd "/workspaces/${REPO_NAME}" && export GEMINI_API_KEY="${GEMINI_API_KEY}" && gemini --yolo --model "$MODEL" < ${PROMPT_FILE}); then
                 echo "Gemini execution successful with model: $MODEL"
                 SUCCESS=true
                 break
            else
                 echo "Gemini execution failed with model: $MODEL. Retrying with next model..."
            fi
        done
        
        if [ "$SUCCESS" = false ]; then
            echo "All models failed."
            exit 1
        fi
    else
        echo "No prompt provided, skipping gemini execution."
    fi
}

function installExtensions {
    echo "Installing extensions..."
    {{- range .Extensions }}
    gemini extensions install "{{ .Source }}" {{ if .Ref }}--ref "{{ .Ref }}"{{ end }} --consent
    {{- end }}
}

# Main execution
setupGit
setupGitRepos
# HACK: Avoid git lock issues
sleep 5
checkoutBranch
configureGemini
installExtensions
#runGemini
