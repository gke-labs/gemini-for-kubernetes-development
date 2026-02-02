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
    (cd "/workspaces/${REPO_NAME}" && export GEMINI_API_KEY="${GEMINI_API_KEY}" && gemini --yolo --model gemini-3-pro-preview < ${PROMPT_FILE})
}

# Main execution
setupGit
setupGitRepos
# HACK: Avoid git lock issues
sleep 5
checkoutPRBranch
configureGemini
runGemini
