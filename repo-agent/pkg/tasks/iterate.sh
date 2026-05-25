#!/bin/bash
set -e
set -o pipefail
set -x

# It expects the following environment variables to be set:
# - GEMINI_API_KEY
# - GITHUB_USER_TOKEN

export REPO_OWNER="{{ .Repo.Owner }}"
export REPO_NAME="{{ .Repo.Name }}"
export CLONE_URL="{{ .Repo.CloneURL }}"
export BRANCH_NAME="{{ .BranchName }}"
export PRID="{{ .PRID }}"
export PROMPT_FILE="{{ .PromptFile }}"
export GITHUB_USER_ID="{{ .User.UserID }}"
export GITHUB_USER_EMAIL="{{ .User.Email }}"
export GITHUB_USER_NAME="{{ .User.Name }}"

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
    echo "creating /root/.config/gh directory"
    mkdir -p /root/.config/gh

    local GH_USER="${GITHUB_USER_ID}"
    if [ -n "${GITHUB_BOT_LOGIN}" ]; then
        GH_USER="${GITHUB_BOT_LOGIN}"
    fi

    echo "writing gh config"
    cat <<EOF > /root/.config/gh/hosts.yml
github.com:
    users:
        ${GH_USER}:
            oauth_token: ${GITHUB_USER_TOKEN}
    git_protocol: https
    oauth_token: ${GITHUB_USER_TOKEN}
    user: ${GH_USER}
EOF

    echo "running git config user.email"
    if [ -n "$GITHUB_BOT_EMAIL" ]; then
        git config --global user.email "${GITHUB_BOT_EMAIL}"
    else
        git config --global user.email "${GITHUB_USER_EMAIL}"
    fi

    echo "running git config user.name"
    if [ -n "$GITHUB_BOT_NAME" ]; then
        git config --global user.name "${GITHUB_BOT_NAME}"
    else
        git config --global user.name "${GITHUB_USER_NAME}"
    fi

    echo "running gh auth setup-git"
    gh auth setup-git

    if [ "${DISABLE_GITHUB_PROXY:-false}" != "true" ]; then
        if [ ! -f /usr/local/bin/gh ]; then
            echo "creating gh wrapper script"
            cat <<'EOF' > /usr/local/bin/gh
#!/bin/bash
HTTPS_PROXY=http://github-portal.overseer-system.svc.cluster.local:80 SSL_CERT_FILE="${SSL_CERT_FILE:-/etc/github-portal/ca/tls.crt}" GIT_SSL_CAINFO="${SSL_CERT_FILE:-/etc/github-portal/ca/tls.crt}" /usr/bin/gh "$@"
EOF
            chmod +x /usr/local/bin/gh
        fi
    fi

    echo "Configuring global git ignore"
    git config --global core.excludesfile /root/.gitignore_global
    cat <<EOF > /root/.gitignore_global
manager
bin/
EOF
}

function setupGitRepos {
    echo "Running setupGitRepos..."
    
    # only clone if doesn't exist
    if [ ! -d "/workspaces/${REPO_NAME}" ]; then
        echo "cloning repository"
        (cd /workspaces/ && git clone ${CLONE_URL})
    else
        echo "repository already exists, cleaning up previous git state..."
        (cd "/workspaces/${REPO_NAME}" && git rebase --abort 2>/dev/null || true)
        (cd "/workspaces/${REPO_NAME}" && git merge --abort 2>/dev/null || true)
        (cd "/workspaces/${REPO_NAME}" && git cherry-pick --abort 2>/dev/null || true)
        (cd "/workspaces/${REPO_NAME}" && git reset --hard HEAD && git clean -fd)
    fi

    echo "running gh repo fork --remote"
    (cd "/workspaces/${REPO_NAME}" && gh repo fork --remote)
    (cd "/workspaces/${REPO_NAME}" && gh repo fork --remote)

    echo "running gh repo set-default"
    (cd "/workspaces/${REPO_NAME}" && gh repo set-default "${CLONE_URL}")

    echo "running git config local user.email"
    (cd "/workspaces/${REPO_NAME}" && git config user.email "${GITHUB_USER_EMAIL}")

    echo "running git config local user.name"
    (cd "/workspaces/${REPO_NAME}" && git config user.name "${GITHUB_USER_NAME}")

    if [ -n "$PRID" ] && [ "$PRID" != "null" ]; then
        echo "Checking out PR $PRID"
        (cd "/workspaces/${REPO_NAME}" && git rebase --abort 2>/dev/null || true)
        (cd "/workspaces/${REPO_NAME}" && git merge --abort 2>/dev/null || true)
        (cd "/workspaces/${REPO_NAME}" && git cherry-pick --abort 2>/dev/null || true)
        (cd "/workspaces/${REPO_NAME}" && git reset --hard HEAD && git clean -fd && /usr/bin/gh pr checkout "$PRID")
    elif [ -n "$BRANCH_NAME" ]; then
        echo "Checking out branch $BRANCH_NAME"
        (cd "/workspaces/${REPO_NAME}" && git rebase --abort 2>/dev/null || true)
        (cd "/workspaces/${REPO_NAME}" && git merge --abort 2>/dev/null || true)
        (cd "/workspaces/${REPO_NAME}" && git cherry-pick --abort 2>/dev/null || true)
        (cd "/workspaces/${REPO_NAME}" && git reset --hard HEAD && git clean -fd && git checkout "$BRANCH_NAME")
    fi

    echo "waiting for checkout to be ready (branch check)"
    (cd "/workspaces/${REPO_NAME}" && git branch --show-current)

    echo "recording initial HEAD"
    pushd "/workspaces/${REPO_NAME}" > /dev/null
    OLD_HEAD=$(git rev-parse HEAD)
    popd > /dev/null
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

function installExtensions {
    echo "Installing extensions..."
    {{- range .Extensions }}
    gemini extensions install "{{ .Source }}" {{ if .Ref }}--ref "{{ .Ref }}"{{ end }} --consent
    {{- end }}
}

function runGemini {
    # Only run gemini if a prompt was actually provided in env or prompt file is non-empty
    if [ -s "${PROMPT_FILE}" ]; then
        echo "Running runGemini..."
        echo "running gemini in yolo mode"

        if [ -n "$GITHUB_BOT_NAME" ]; then
            echo "Using bot identity for commits"
            export GIT_AUTHOR_NAME="$GITHUB_BOT_NAME"
            export GIT_AUTHOR_EMAIL="$GITHUB_BOT_EMAIL"
            export GIT_COMMITTER_NAME="$GITHUB_BOT_NAME"
            export GIT_COMMITTER_EMAIL="$GITHUB_BOT_EMAIL"
        fi

        MODELS=( {{ range .Models }}"{{ . }}" {{ end }} )
        SUCCESS=false
        for MODEL in "${MODELS[@]}"; do
            echo "Trying model: $MODEL"
            if (cd "/workspaces/${REPO_NAME}" && export GEMINI_API_KEY="${GEMINI_API_KEY}" && gemini --yolo --model "$MODEL" --output-format stream-json < ${PROMPT_FILE} | /opt/repo-agent/gemini-stream-processor --output "$(dirname "${PROMPT_FILE}")/gemini-output.json"); then
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

function commitAndPush {
    echo "Running commitAndPush..."
    pushd "/workspaces/${REPO_NAME}" > /dev/null
    
    NEW_HEAD=$(git rev-parse HEAD)

    # check if there are changes
    if [ -z "$(git status --porcelain)" ]; then 
        if [ "$OLD_HEAD" != "$NEW_HEAD" ]; then
            echo "HEAD has changed (likely rebased by agent). Pushing changes..."
            git push --force origin HEAD
        else
            echo "No changes to commit."
        fi
    else
        echo "Changes detected, committing..."
        git add .
        git commit -m "Agent iteration: Apply changes"
        if [ "$OLD_HEAD" != "$NEW_HEAD" ]; then
            echo "HEAD has changed and working directory has changes. Pushing changes..."
            git push --force origin HEAD
        else
            git push origin HEAD
        fi
    fi
    popd > /dev/null
}

# Main execution
setupGit
setupGitRepos
# HACK: Avoid git lock issues
sleep 5
configureGemini
installExtensions
runGemini
commitAndPush
