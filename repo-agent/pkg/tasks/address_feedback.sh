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

function setupGitIdentity {
    echo "Setting up git identity from BOT environment variables..."
    if [ -n "$GITHUB_BOT_NAME" ]; then
        export GIT_AUTHOR_NAME="$GITHUB_BOT_NAME"
        export GIT_COMMITTER_NAME="$GITHUB_BOT_NAME"
    fi
    if [ -n "$GITHUB_BOT_EMAIL" ]; then
        export GIT_AUTHOR_EMAIL="$GITHUB_BOT_EMAIL"
        export GIT_COMMITTER_EMAIL="$GITHUB_BOT_EMAIL"
    fi
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
setupGitIdentity
setupGitRepos
# HACK: Avoid git lock issues
sleep 5
checkoutPRBranch
configureGemini
runGemini
