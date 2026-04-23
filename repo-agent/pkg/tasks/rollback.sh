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

export REPO_OWNER={{ printf "%q" .RepoOwner }}
export REPO_NAME={{ printf "%q" .RepoName }}
export CLONE_URL={{ printf "%q" .Repo.CloneURL }}
export COMMIT_SHA={{ printf "%q" .CommitSHA }}
export BRANCH={{ printf "%q" .Branch }}
export REMOTE={{ printf "%q" .Remote }}
export GITHUB_USER_ID={{ printf "%q" .User.UserID }}
export GITHUB_USER_EMAIL={{ printf "%q" .User.Email }}
export GITHUB_USER_NAME={{ printf "%q" .User.Name }}
export PR_NUMBER={{ printf "%q" .PullRequestID }}

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
        git config --global user.name "${GITHUB_BOT_NAME}"
    else
        git config --global user.email "${GITHUB_USER_EMAIL}"
        git config --global user.name "${GITHUB_USER_NAME}"
    fi

    gh auth setup-git
}

function setupGitRepos {
    echo "Running setupGitRepos..."
    if [ ! -d "/workspaces/${REPO_NAME}" ]; then
        echo "cloning repository ${REPO_OWNER}/${REPO_NAME}"
        gh repo clone "${REPO_OWNER}/${REPO_NAME}" "/workspaces/${REPO_NAME}"
    else
        echo "Repository already exists at /workspaces/${REPO_NAME}"
        # Optional: fetch latest changes
        (
            cd "/workspaces/${REPO_NAME}"
            for i in $(seq 1 3); do
                if git fetch origin; then
                    break
                fi
                echo "git fetch failed, retrying in 5s... ($i/3)"
                sleep 5
            done
        )
    fi

    pushd "/workspaces/${REPO_NAME}" > /dev/null
    
    echo "running gh repo set-default"
    gh repo set-default "${REPO_OWNER}/${REPO_NAME}" || true

    echo "running git config local user.email"
    git config user.email "${GITHUB_USER_EMAIL}" || true

    echo "running git config local user.name"
    git config user.name "${GITHUB_USER_NAME}" || true
    
    popd > /dev/null
}

function checkoutPRBranch {
    echo "Running checkoutPRBranch..."
    echo "checking out PR #${PR_NUMBER}"
    (
        cd "/workspaces/${REPO_NAME}"
        gh pr checkout "${PR_NUMBER}" --force
    )
}

function runRollback {
    echo "Running rollback to ${COMMIT_SHA} ..."
    pushd "/workspaces/${REPO_NAME}" > /dev/null

    # Perform the hard reset
    git reset --hard "${COMMIT_SHA}"

    # Force push to the remote
    # We use -u to set upstream which might help with the refspec error
    git push --force -u "${REMOTE}"

    popd > /dev/null
}

setupGit
setupGitRepos
checkoutPRBranch
runRollback
