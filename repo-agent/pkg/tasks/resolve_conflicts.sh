#!/bin/bash
set -e
set -o pipefail
#set -x

# It expects the following environment variables to be set:
# - GEMINI_API_KEY
# - GITHUB_USER_TOKEN

export REPO_NAME="{{ .Repo.Name }}"
export CLONE_URL="{{ .Repo.CloneURL }}"
export PROMPT_FILE="{{ .PromptFile }}"
export GITHUB_USER_ID="{{ .User.UserID }}"
export GITHUB_USER_EMAIL="{{ .User.Email }}"
export GITHUB_USER_NAME="{{ .User.Name }}"
export PR_NUMBER={{ .PullRequest.Number }}
export BASE_REF="{{ .BaseRef }}"

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
github.com:
    users:
        ${GH_USER}:
            oauth_token: ${GITHUB_USER_TOKEN}
    git_protocol: https
    oauth_token: ${GITHUB_USER_TOKEN}
    user: ${GH_USER}
EOF

    if [ -n "$GITHUB_BOT_EMAIL" ]; then
        git config --global user.email "${GITHUB_BOT_EMAIL}"
    else
        git config --global user.email "${GITHUB_USER_EMAIL}"
    fi

    if [ -n "$GITHUB_BOT_NAME" ]; then
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
    if [ ! -d "/workspaces/${REPO_NAME}" ]; then
        echo "cloning repository"
        (cd /workspaces/ && git clone ${CLONE_URL})
    else
        echo "repository already exists"
        (cd "/workspaces/${REPO_NAME}" && git fetch origin)
    fi

    (cd "/workspaces/${REPO_NAME}" && gh repo fork --remote || true)
    (cd "/workspaces/${REPO_NAME}" && gh repo set-default "${CLONE_URL}" || true)
}

function checkoutPRBranch {
    echo "Running checkoutPRBranch..."
    (cd "/workspaces/${REPO_NAME}" && gh pr checkout ${PR_NUMBER})
}

function attemptMerge {
    echo "Attempting to merge ${BASE_REF} into current branch..."
    cd "/workspaces/${REPO_NAME}"
    git fetch origin "${BASE_REF}"
    # Ensure we have the latest of the current branch too
    git pull origin $(git branch --show-current) || echo "Pull failed, proceeding with current state"
    if git merge "origin/${BASE_REF}" -m "Merge branch 'origin/${BASE_REF}' into HEAD"; then
        echo "Merge successful without conflicts."
        return 0
    else
        echo "Merge conflicts detected. Proceeding with LLM resolution."
        return 1
    fi
}

function runGemini {
    echo "Running Gemini to resolve conflicts..."
    
    if [ -n "$GITHUB_BOT_NAME" ]; then
        export GIT_AUTHOR_NAME="$GITHUB_BOT_NAME"
        export GIT_AUTHOR_EMAIL="$GITHUB_BOT_EMAIL"
        export GIT_COMMITTER_NAME="$GITHUB_BOT_NAME"
        export GIT_COMMITTER_EMAIL="$GITHUB_BOT_EMAIL"
    fi

    MODELS=( {{ range .Models }}"{{ . }}" {{ end }} )
    SUCCESS=false
    for MODEL in "${MODELS[@]}"; do
        echo "Trying model: $MODEL"
        # We use --yolo because this runs in a sandboxed pod and we need automated resolution.
        if (cd "/workspaces/${REPO_NAME}" && export GEMINI_API_KEY="${GEMINI_API_KEY}" && gemini --yolo --model "$MODEL" --output-format stream-json < ${PROMPT_FILE} | /opt/repo-agent/gemini-stream-processor --output "$(dirname "${PROMPT_FILE}")/gemini-output.json"); then
             echo "Gemini execution successful with model: $MODEL"
             SUCCESS=true
             break
        else
             echo "Gemini execution failed with model: $MODEL. Retrying with next model..."
        fi
    done
    
    if [ "$SUCCESS" = false ]; then
        echo "All models failed to resolve conflicts."
        exit 1
    fi

    echo "Staging and committing resolved files..."
    cd "/workspaces/${REPO_NAME}"
    git add .
    git commit -m "chore: resolve merge conflicts using Gemini" || echo "Nothing to commit"
}

function verifyResolution {
    echo "Verifying conflict resolution..."
    cd "/workspaces/${REPO_NAME}"
    if grep -r --exclude-dir=.git "^<<<<<<<" .; then
        echo "Conflict markers still present! Resolution failed."
        exit 1
    fi
    echo "No conflict markers found. Resolution looks good."
}

function pushChanges {
    echo "Pushing resolved changes..."
    cd "/workspaces/${REPO_NAME}"
    git push
}

# Main execution
setupGit
setupGitRepos
sleep 5
checkoutPRBranch
if attemptMerge; then
    pushChanges
else
    # Install extensions if any
    {{- range .Extensions }}
    gemini extensions install "{{ .Source }}" {{ if .Ref }}--ref "{{ .Ref }}"{{ end }} --consent
    {{- end }}
    
    runGemini
    verifyResolution
    # Run tests if available
    if [ -f "Makefile" ]; then
        make test || echo "Tests failed but proceeding with push"
    elif [ -f "go.mod" ]; then
        go test ./... || echo "Tests failed but proceeding with push"
    fi
    pushChanges
fi
