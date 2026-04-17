#!/bin/bash
set -e
set -o pipefail
set -x

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
        echo "Repository already exists at /workspaces/${REPO_NAME}"
        # Ensure origin is renamed to upstream if it exists and upstream does not
        (cd "/workspaces/${REPO_NAME}" && git remote get-url upstream >/dev/null 2>&1 || git remote rename origin upstream >/dev/null 2>&1 || true)
    else
        echo "cloning repository"
        git clone "${CLONE_URL}" "/workspaces/${REPO_NAME}"
        echo "renaming origin to upstream"
        (cd "/workspaces/${REPO_NAME}" && git remote rename origin upstream)
    fi

    pushd "/workspaces/${REPO_NAME}" > /dev/null
    
    echo "running gh repo fork"
    gh repo fork --remote --remote-name origin || echo 'WARNING: Failed to fork repository. origin remote may be missing.'

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
    (cd "/workspaces/${REPO_NAME}" && gh pr checkout ${PR_NUMBER})
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
