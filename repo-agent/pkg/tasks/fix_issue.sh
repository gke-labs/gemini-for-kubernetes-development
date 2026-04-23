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

export REPO_OWNER={{ printf "%q" .RepoOwner }}
export REPO_NAME={{ printf "%q" .RepoName }}
export CLONE_URL={{ printf "%q" .Repo.CloneURL }}
export ISSUE_NUMBER={{ printf "%q" .Issue.Number }}
export PROMPT_FILE={{ printf "%q" .PromptFile }}
export GITHUB_USER_ID={{ printf "%q" .User.UserID }}
export GITHUB_USER_EMAIL={{ printf "%q" .User.Email }}
export GITHUB_USER_NAME={{ printf "%q" .User.Name }}

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

    echo "running git config local user.email"
    (
        cd "/workspaces/${REPO_NAME}"
        git config user.email "${GITHUB_USER_EMAIL}"
    )

    echo "running git config local user.name"
    (
        cd "/workspaces/${REPO_NAME}"
        git config user.name "${GITHUB_USER_NAME}"
    )

    echo "waiting for checkout to be ready (branch check)"
    (
        cd "/workspaces/${REPO_NAME}"
        git branch --show-current
    )
}

function checkForExistingPR {
    echo "Checking for existing PRs..."
    pushd "/workspaces/${REPO_NAME}" > /dev/null

    # Try to find a PR by the current user first
    local pr_number; pr_number=$(gh search prs "${ISSUE_NUMBER}" --state open --repo "${REPO_OWNER}/${REPO_NAME}" --author "${GITHUB_USER_ID}" --json number --jq '.[0] | "\(.number)"' --limit 1)
    local pr_url; pr_url=$(gh search prs "${ISSUE_NUMBER}" --state open --repo "${REPO_OWNER}/${REPO_NAME}" --author "${GITHUB_USER_ID}" --json url --jq '.[0] | "\(.url)"' --limit 1)

    # If not found, look for any PR
    if [ -z "${pr_number}" ] || [ "${pr_number}" == "null" ]; then
        pr_number=$(gh search prs "${ISSUE_NUMBER}" --repo "${REPO_OWNER}/${REPO_NAME}" --state open --json number --jq '.[0] | "\(.number)"' --limit 1)
        pr_url=$(gh search prs "${ISSUE_NUMBER}" --repo "${REPO_OWNER}/${REPO_NAME}" --state open --json url --jq '.[0] | "\(.url)"' --limit 1)
    fi

    if [ -n "${pr_number}" ] && [ "${pr_number}" != "null" ]; then
        echo "Found existing PR #${pr_number}: ${pr_url}"
        gh pr checkout "${pr_number}" --force

        local output_file; output_file="$(dirname "${PROMPT_FILE}")/agent-output.txt"

        echo "We are not generating anything because there is an existing PR." > "${output_file}"
        echo "${pr_url}" >> "${output_file}"
        exit 0
    fi

    popd > /dev/null
}

function checkoutNewBranch {
    echo "Running checkoutNewBranch..."
    echo "creating new branch"
    local branch_name; branch_name="issue-${ISSUE_NUMBER}"
    {{- if .Branch }}
    branch_name={{ printf "%q" .Branch }}
    {{- end }}
    (
        cd "/workspaces/${REPO_NAME}"
        git checkout -B "${branch_name}"
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

function runGemini {
    echo "running gemini in yolo mode"
    pushd "/workspaces/${REPO_NAME}" > /dev/null
    
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
            set +x
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
    popd > /dev/null
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

function recordPRLink {
    echo "Recording PR link..."
    pushd "/workspaces/${REPO_NAME}" > /dev/null
    local output_file; output_file="$(dirname "${PROMPT_FILE}")/agent-output.txt"
    local pr_url; pr_url=""

    # Try current branch PR status
    echo "Checking pr status..."
    pr_url=$(gh pr status --json url --jq '.currentBranch.url // empty')

    # If not found, try listing PRs for this branch
    if [ -z "${pr_url}" ] || [ "${pr_url}" == "null" ]; then
        echo "Checking pr list by branch..."
        pr_url=$(gh pr list --head "issue_${ISSUE_NUMBER}" --json url --jq '.[0].url // empty')
    fi

    # If still not found, try searching PRs by issue number and author
    if [ -z "${pr_url}" ] || [ "${pr_url}" == "null" ]; then
        echo "Searching for PR..."
        pr_url=$(gh search prs "${ISSUE_NUMBER}" --state open --repo "${REPO_OWNER}/${REPO_NAME}" --author "${GITHUB_USER_ID}" --json url --jq '.[0].url // empty' --limit 1)
    fi

    if [ -n "${pr_url}" ] && [ "${pr_url}" != "null" ]; then
        echo "Successfully found PR: ${pr_url}"
        echo "${pr_url}" > "${output_file}"
    else
        echo "Could not find PR link automatically."
        # Don't overwrite if it already exists (unlikely here but safe)
        if [ ! -s "${output_file}" ]; then
            echo "Could not find PR link automatically." > "${output_file}"
        fi
    fi
    popd > /dev/null
}

function createPullRequest {
    echo "Creating Pull Request..."
    pushd "/workspaces/${REPO_NAME}" > /dev/null

    # Check if there are changes
    if [ -z "$(git status --porcelain)" ]; then
        echo "No changes detected, skipping PR creation."
        popd > /dev/null
        return
    fi

    echo "Changes detected, committing and pushing..."
    local branch_name; branch_name="issue-${ISSUE_NUMBER}"
    {{- if .Branch }}
    branch_name={{ printf "%q" .Branch }}
    {{- end }}

    # Use a generic commit message if we can't find a better one
    git add .
    git commit -m "Fix issue #${ISSUE_NUMBER}" || echo "Nothing to commit"
    git push --force --set-upstream origin "${branch_name}"

    # Extract PR title and body from Gemini output
    local output_file; output_file="$(dirname "${PROMPT_FILE}")/raw-agent-output.txt"
    
    if [ -s "${output_file}" ]; then
        # Simple extraction: first non-empty line as title, rest as body
        local pr_title; pr_title=$(grep -m 1 "." "${output_file}" | sed 's/^#* //')
        if [ -z "${pr_title}" ]; then
            pr_title="Fix issue #${ISSUE_NUMBER}"
        fi
        
        # Create PR
        echo "Creating PR with title: ${pr_title}"
        local pr_url; pr_url=$(gh pr create --title "${pr_title}" --body-file "${output_file}" --label "overseer" || gh pr view --json url --jq .url)
        
        if [ -n "${pr_url}" ]; then
            echo "Successfully created/found PR: ${pr_url}"
            echo "${pr_url}" > "$(dirname "${PROMPT_FILE}")/agent-output.txt"
        fi
    else
        echo "Gemini output not found, creating minimal PR..."
        local pr_url; pr_url=$(gh pr create --title "Fix issue #${ISSUE_NUMBER}" --body "Automated fix for issue #${ISSUE_NUMBER}" --label "overseer" || gh pr view --json url --jq .url)
        if [ -n "${pr_url}" ]; then
            echo "${pr_url}" > "$(dirname "${PROMPT_FILE}")/agent-output.txt"
        fi
    fi

    popd > /dev/null
}

# Main execution
setupGit
setupGitRepos
# HACK: Avoid git lock issues
sleep 5
checkForExistingPR
checkoutNewBranch
configureGemini
installExtensions
injectConfigDirData
runGemini
createPullRequest
recordPRLink
