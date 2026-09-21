#!/bin/bash
set -e
set -o pipefail
set -x


# It expects the following environment variables to be set:
# - GEMINI_API_KEY
# - GITHUB_USER_TOKEN
# - REPO_OWNER
# - REPO_NAME
# - CLONE_URL
# - ISSUE_NUMBER
# - PROMPT_FILE
# - GITHUB_USER_ID
# - GITHUB_USER_EMAIL
# - GITHUB_USER_NAME
# - BRANCH_NAME
# - MODELS

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

function setupGitRepos {
    echo "Running setupGitRepos..."
    
    # Check if repo already exists and is a valid git repository
    if [ ! -d "/workspaces/${REPO_NAME}/.git" ]; then
        echo "repository does not exist or is invalid, cleaning up destination and cloning..."
        rm -rf "/workspaces/${REPO_NAME}"
        (cd /workspaces/ && git clone ${CLONE_URL})
    else
        echo "repository already exists, cleaning up previous git state..."
        (cd "/workspaces/${REPO_NAME}" && git rebase --abort 2>/dev/null || true)
        (cd "/workspaces/${REPO_NAME}" && git merge --abort 2>/dev/null || true)
        (cd "/workspaces/${REPO_NAME}" && git cherry-pick --abort 2>/dev/null || true)
        (cd "/workspaces/${REPO_NAME}" && git reset --hard HEAD && git clean -fd)
        # Optional: fetch latest changes
        (cd "/workspaces/${REPO_NAME}" && git fetch origin)
    fi

    echo "running gh repo fork"
    (cd "/workspaces/${REPO_NAME}" && gh repo fork --remote || true)

    echo "running gh repo set-default"
    (cd "/workspaces/${REPO_NAME}" && gh repo set-default "${CLONE_URL}" || true)

    echo "running git config local user.email"
    (cd "/workspaces/${REPO_NAME}" && git config user.email "${GITHUB_USER_EMAIL}")

    echo "running git config local user.name"
    (cd "/workspaces/${REPO_NAME}" && git config user.name "${GITHUB_USER_NAME}")

    echo "waiting for checkout to be ready (branch check)"
    (cd "/workspaces/${REPO_NAME}" && git branch --show-current)
}

function checkForExistingPR {
    echo "Checking for existing PRs..."
    if [ "$NO_PR" = "true" ]; then
        echo "NO_PR is true; skipping check for existing PR."
        return
    fi
    if [ "${ISSUE_NUMBER:-0}" -eq 0 ]; then
        echo "No issue number specified; skipping check for existing PR."
        return
    fi
    pushd "/workspaces/${REPO_NAME}" > /dev/null

    # Try to find a PR by the current user first, restricting search to title and body to be safer
    local pr_number=$(gh search prs "${ISSUE_NUMBER}" --state open --repo "${REPO_OWNER}/${REPO_NAME}" --author "${GITHUB_USER_ID}" --match title,body --json number --jq '.[0] | "\(.number)"' --limit 1 2>/dev/null)
    local pr_url=$(gh search prs "${ISSUE_NUMBER}" --state open --repo "${REPO_OWNER}/${REPO_NAME}" --author "${GITHUB_USER_ID}" --match title,body --json url --jq '.[0] | "\(.url)"' --limit 1 2>/dev/null)

    # If not found, look for any PR linked to the issue via the timeline API
    if [ -z "$pr_number" ] || [ "$pr_number" == "null" ]; then
        pr_number=$(gh api "repos/${REPO_OWNER}/${REPO_NAME}/issues/${ISSUE_NUMBER}/timeline" \
            --jq '.[] | select(.event == "cross-referenced" and .source.issue.pull_request != null and .source.issue.state == "open") | .source.issue.number' 2>/dev/null | head -n 1)
        pr_url=$(gh api "repos/${REPO_OWNER}/${REPO_NAME}/issues/${ISSUE_NUMBER}/timeline" \
            --jq '.[] | select(.event == "cross-referenced" and .source.issue.pull_request != null and .source.issue.state == "open") | .source.issue.html_url' 2>/dev/null | head -n 1)
    fi

    if [ -n "$pr_number" ] && [ "$pr_number" != "null" ]; then
        echo "Found existing PR:"
        echo $pr_number
        echo $pr_url

        echo "Found existing PR #${pr_number}"
        git rebase --abort 2>/dev/null || true
        git merge --abort 2>/dev/null || true
        git cherry-pick --abort 2>/dev/null || true
        git reset --hard HEAD
        git clean -fd
        /usr/bin/gh pr checkout "$pr_number" --force

        local output_file="$(dirname "${PROMPT_FILE}")/agent-output.txt"

        echo "We are not generating anything because there is an existing PR." > "$output_file"
        echo "${pr_url}" >> "$output_file"
        # The earlier run may have been killed before anything labelled this PR.
        applyPRLabels "$pr_url"
        exit 0
    fi

    popd > /dev/null
}

# applyPRLabels puts the labels resolved by the factory CLI - the trigger label,
# the repository's additional labels, and the labels of the issue being fixed -
# onto the pull request.
#
# This runs here, inside the sandbox, rather than only in the factory process
# that started the task, because that process does not always survive to see the
# PR: a watch cycle that recycles mid-task kills it, while this workload keeps
# running detached and opens the PR anyway. A pull request that comes out of
# that carries no labels, and the watcher only ever scans labelled or assigned
# pull requests - so it is never looked at again.
#
# Failing to label must not fail the task. The pull request exists either way,
# and a label that is missing from the repository is a repository configuration
# problem, not a reason to report the fix as unsuccessful.
function applyPRLabels {
    local pr_url="$1"
    if [ -z "${PR_LABELS:-}" ] || [ -z "$pr_url" ] || [ "$pr_url" == "null" ]; then
        return 0
    fi
    echo "Applying labels ${PR_LABELS} to ${pr_url}..."
    # --add-label is idempotent: labels already on the PR are left as they are.
    gh pr edit "$pr_url" --add-label "${PR_LABELS}" \
        || echo "Warning: failed to add labels ${PR_LABELS} to ${pr_url}"
    return 0
}

function checkoutNewBranch {
    echo "Running checkoutNewBranch..."
    echo "creating new branch"
    local branch_name="${BRANCH_NAME:-issue-${ISSUE_NUMBER}}"
    (cd "/workspaces/${REPO_NAME}" && git rebase --abort 2>/dev/null || true)
    (cd "/workspaces/${REPO_NAME}" && git merge --abort 2>/dev/null || true)
    (cd "/workspaces/${REPO_NAME}" && git cherry-pick --abort 2>/dev/null || true)
    (cd "/workspaces/${REPO_NAME}" && git reset --hard HEAD && git clean -fd && git checkout -B "$branch_name")
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
    local output_file="$(dirname "${PROMPT_FILE}")/agent-output.txt"
    if [ "$NO_PR" = "true" ]; then
        echo "Branch successfully pushed to origin/${BRANCH_NAME}" > "$output_file"
        popd > /dev/null
        return
    fi
    local pr_url=""

    # Try current branch PR status
    echo "Checking pr status..."
    pr_url=$(gh pr status --json url --jq '.currentBranch.url // empty')

    # If not found, try listing PRs for this branch
    if [ -z "$pr_url" ] || [ "$pr_url" == "null" ]; then
        echo "Checking pr list by branch..."
        pr_url=$(gh pr list --head "${BRANCH_NAME:-issue_${ISSUE_NUMBER}}" --json url --jq '.[0].url // empty')
    fi

    # If still not found, try searching PRs by issue number and author (matching only title and body to be safer)
    if [ -z "$pr_url" ] || [ "$pr_url" == "null" ]; then
        if [ "${ISSUE_NUMBER:-0}" -gt 0 ]; then
            echo "Searching for PR..."
            pr_url=$(gh search prs "${ISSUE_NUMBER}" --state open --repo "${REPO_OWNER}/${REPO_NAME}" --author "${GITHUB_USER_ID}" --match title,body --json url --jq '.[0].url // empty' --limit 1 2>/dev/null)
        fi
    fi

    if [ -n "$pr_url" ] && [ "$pr_url" != "null" ]; then
        echo "Successfully found PR: ${pr_url}"
        echo "${pr_url}" > "$output_file"
        applyPRLabels "$pr_url"
        popd > /dev/null
    else
        echo "Could not find PR link automatically." | tee "$output_file"
        popd > /dev/null
        echo "Task finished without creating a pull request." >&2
        exit 1
    fi
}

function appendApprovedPlan {
    if [ "${WITH_PLAN:-false}" != "true" ]; then
        return
    fi
    if [ ! -f "${PLAN_FILE}" ]; then
        echo "WITH_PLAN=true but ${PLAN_FILE} not found; continuing without a plan"
        return
    fi
    echo "Folding approved plan from ${PLAN_FILE} into the prompt..."
    {
        echo ""
        echo "---"
        echo "APPROVED IMPLEMENTATION PLAN"
        echo "A human maintainer reviewed and approved the following plan. Follow it: it is the agreed scope and approach for this fix. If reality on the ground contradicts a step, deviate minimally and call the deviation out in the PR description."
        echo ""
        cat "${PLAN_FILE}"
        echo ""
        echo "Include a section titled '## Plan' in the pull request description containing this plan."
    } >> "${PROMPT_FILE}"
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
appendApprovedPlan
runEngine
recordPRLink
