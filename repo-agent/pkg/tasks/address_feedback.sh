#!/bin/bash
set -e
#set -x

# It expects the following environment variables to be set:
# - GEMINI_API_KEY
# - GITHUB_USER_TOKEN

export REPO_NAME="{{ .Repo.Name }}"
export CLONE_URL={{ .Repo.CloneURL }}
export PROMPT_FILE="{{ .PromptFile }}"
export GITHUB_USER_ID={{ .User.UserID }}
export GITHUB_USER_EMAIL={{ .User.Email }}
export GITHUB_USER_NAME="{{ .User.Name }}"
export PR_NUMBER={{ .PullRequest.Number }}

function setupGit {
    echo "Running setupGit..."
    
    # Use a temporary directory for gh config to avoid overwriting user's config
    export GH_CONFIG_DIR=$(mktemp -d)
    echo "using GH_CONFIG_DIR: ${GH_CONFIG_DIR}"
    
    echo "creating ${GH_CONFIG_DIR}/hosts.yml"
    mkdir -p "${GH_CONFIG_DIR}"

    echo "writing gh config"
    cat <<EOF > "${GH_CONFIG_DIR}/hosts.yml"
github.com:
    users:
        ${GITHUB_USER_ID}:
            oauth_token: ${GITHUB_USER_TOKEN}
    git_protocol: https
    oauth_token: ${GITHUB_USER_TOKEN}
    user: ${GITHUB_USER_ID}
EOF

    echo "setting git identity via environment variables"
    export GIT_AUTHOR_NAME="${GITHUB_USER_NAME}"
    export GIT_AUTHOR_EMAIL="${GITHUB_USER_EMAIL}"
    export GIT_COMMITTER_NAME="${GITHUB_USER_NAME}"
    export GIT_COMMITTER_EMAIL="${GITHUB_USER_EMAIL}"

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

function checkoutPRBranch {
    echo "Running checkoutPRBranch..."
    echo "checking out PR #${PR_NUMBER}"
    (cd "/workspaces/${REPO_NAME}" && gh pr checkout ${PR_NUMBER})
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
    echo "Running runGemini..."
    echo "running gemini in yolo mode"
    (cd "/workspaces/${REPO_NAME}" && export GEMINI_API_KEY="${GEMINI_API_KEY}" && gemini --yolo --model {{ .Model }} < ${PROMPT_FILE})
}

# Main execution
setupGit
setupGitRepos
# HACK: Avoid git lock issues
sleep 5
checkoutPRBranch
configureGemini
runGemini
