#!/bin/bash
set -e
set -o pipefail
set -x

if [ "${DISABLE_GITHUB_PROXY:-false}" != "true" ]; then
    if [ ! -f /usr/local/bin/gh ]; then
        echo "creating gh wrapper script"
        cat <<'EOF' > /usr/local/bin/gh
#!/bin/bash
HTTPS_PROXY=http://github-portal.overseer-system.svc.cluster.local SSL_CERT_FILE="${SSL_CERT_FILE:-/etc/github-portal/ca/tls.crt}" /usr/bin/gh "$@"
EOF
        chmod +x /usr/local/bin/gh
    fi
fi

export REPO_OWNER="{{ .Repo.Owner }}"
export REPO_NAME="{{ .Repo.Name }}"
export CLONE_URL="{{ .Repo.CloneURL }}"
export COMMIT_SHA="{{ .CommitSHA }}"
export BRANCH="{{ .Branch }}"
export REMOTE="{{ .Remote }}"
export GITHUB_USER_ID="{{ .User.UserID }}"
export GITHUB_USER_EMAIL="{{ .User.Email }}"
export GITHUB_USER_NAME="{{ .User.Name }}"
export PR_NUMBER={{ .PullRequestID }}

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
        git config --global user.name "${GITHUB_BOT_NAME}"
    else
        git config --global user.email "${GITHUB_USER_EMAIL}"
        git config --global user.name "${GITHUB_USER_NAME}"
    fi

    gh auth setup-git
}

function setupGitRepos {
    echo "Running setupGitRepos..."
    if [ -d "/workspaces/${REPO_NAME}" ]; then
        echo "Repository already exists at /workspaces/${REPO_NAME}, cleaning up previous git state..."
        (cd "/workspaces/${REPO_NAME}" && git rebase --abort 2>/dev/null || true)
        (cd "/workspaces/${REPO_NAME}" && git merge --abort 2>/dev/null || true)
        (cd "/workspaces/${REPO_NAME}" && git cherry-pick --abort 2>/dev/null || true)
        (cd "/workspaces/${REPO_NAME}" && git reset --hard HEAD && git clean -fd)
        (cd "/workspaces/${REPO_NAME}" && git fetch origin)
    else
        echo "cloning repository"
        git clone "${CLONE_URL}" "/workspaces/${REPO_NAME}"
    fi

    pushd "/workspaces/${REPO_NAME}" > /dev/null
    
    echo "running gh repo fork"
    gh repo fork --remote || true

    echo "running gh repo set-default"
    gh repo set-default "${CLONE_URL}" || true

    echo "running git config local user.email"
    git config user.email "${GITHUB_USER_EMAIL}" || true

    echo "running git config local user.name"
    git config user.name "${GITHUB_USER_NAME}" || true
    
    popd > /dev/null
}

function checkoutPRBranch {
    echo "Running checkoutPRBranch..."
    echo "checking out PR #${PR_NUMBER}"
    (cd "/workspaces/${REPO_NAME}" && git rebase --abort 2>/dev/null || true)
    (cd "/workspaces/${REPO_NAME}" && git merge --abort 2>/dev/null || true)
    (cd "/workspaces/${REPO_NAME}" && git cherry-pick --abort 2>/dev/null || true)
    (cd "/workspaces/${REPO_NAME}" && git reset --hard HEAD && git clean -fd && /usr/bin/gh pr checkout ${PR_NUMBER})
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
