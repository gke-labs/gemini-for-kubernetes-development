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

    local pr_number pr_url pr_fields page api_failed page_data count slurped_json gh_host host_temp output_file attempt max_attempts fetch_success err_file saved_traps
    local -a timeline_pages=()

    pushd "/workspaces/${REPO_NAME}" > /dev/null

    # Try to find a PR by the current user first, restricting search to title and body to be safer
    pr_fields=$(gh search prs "${ISSUE_NUMBER}" --state open --repo "${REPO_OWNER}/${REPO_NAME}" --author "${GITHUB_USER_ID}" --match title,body --json number,url --jq '.[0] | if . then "\(.number) \(.url)" else empty end' --limit 1 2>/dev/null || true)
    if [ -n "$pr_fields" ]; then
        pr_number="${pr_fields%% *}"
        pr_url="${pr_fields#* }"
    fi

    # If not found, look for any PR linked to the issue via the timeline API.
    # To avoid skipping issues merely referenced/mentioned, we only count open PRs
    # that are connected and not since disconnected, matching TimelineHasOpenLinkedPR logic.
    if [ -z "$pr_number" ] || [ "$pr_number" == "null" ]; then
        # To match the Go-based watcher's page limit (maxTimelinePages = 20) and protect against
        # rate-limit exhaustion, we paginate manually up to 20 pages with per_page=100.
        page=1
        api_failed=false
        # Save any existing traps for EXIT to avoid overriding them globally.
        saved_traps=$(trap -p EXIT)
        err_file=$(mktemp) || { echo "Error: Failed to create temporary file" >&2; exit 1; }
        trap 'rm -f "$err_file"' EXIT
        while [ "$page" -le 20 ]; do
            attempt=1
            max_attempts=3
            fetch_success=false
            while [ "$attempt" -le "$max_attempts" ]; do
                if page_data=$(gh api --method GET "repos/${REPO_OWNER}/${REPO_NAME}/issues/${ISSUE_NUMBER}/timeline" -F per_page=100 -F page=$page 2>"$err_file"); then
                    fetch_success=true
                    break
                else
                    # Check for permanent non-retryable API errors (e.g., 401 Unauthorized, 403 Forbidden, 404 Not Found)
                    if grep -qE "HTTP (401|403|404)" "$err_file" 2>/dev/null; then
                        echo "Warning: Permanent API error encountered while fetching timeline page $page:" >&2
                        cat "$err_file" >&2
                        break
                    fi
                    echo "Warning: Failed to fetch timeline page $page (attempt $attempt/$max_attempts). Retrying in 2 seconds..." >&2
                    sleep 2
                    attempt=$((attempt + 1))
                fi
            done
            if [ "$fetch_success" = "false" ]; then
                api_failed=true
                break
            fi
            if [ "$page_data" = "[]" ] || [ -z "$page_data" ] || [ "$page_data" = "null" ]; then
                break
            fi
            if ! printf '%s' "$page_data" | jq -e 'type == "array"' >/dev/null 2>&1; then
                api_failed=true
                break
            fi
            timeline_pages+=("$page_data")
            # If the page contains fewer than 100 elements, we've reached the end.
            count=$(printf '%s' "$page_data" | jq 'length' 2>/dev/null || echo "0")
            if [[ ! "$count" =~ ^[0-9]+$ ]] || [ "$count" -lt 100 ]; then
                break
            fi
            page=$((page + 1))
        done

        if [ "$page" -gt 20 ]; then
            echo "Warning: Issue #${ISSUE_NUMBER} has more than 20 pages of timeline events; results are truncated" >&2
        fi
        rm -f "$err_file"
        trap - EXIT
        if [ -n "$saved_traps" ]; then
            eval "$saved_traps"
        fi

        if [ "$api_failed" = "true" ]; then
            pr_fields="null null"
        elif [ ${#timeline_pages[@]} -gt 0 ]; then
            # Join all page arrays into a single JSON array of arrays (mirroring --slurp format)
            # and process with the state-building jq reduction pipeline.
            if ! slurped_json=$(printf '%s\n' "${timeline_pages[@]}" | jq -s '.') || [ -z "$slurped_json" ]; then
                pr_fields="null null"
            else
                pr_fields=$(printf '%s\n' "$slurped_json" | jq -r '
                  reduce (.[] | .[] | select(type == "object")) as $event ({};
                    if $event.source?.issue?.number != null then
                      if $event.event == "connected" and $event.source.issue.pull_request != null and $event.source.issue.state == "open" then
                        .[$event.source.issue.number | tostring] = $event.source.issue
                      elif $event.event == "disconnected" then
                        del(.[$event.source.issue.number | tostring])
                      else
                        .
                      end
                    else
                      .
                    end
                  ) | to_entries | .[0].value | if . then "\(.number) \(.html_url)" else empty end
                ' || echo "null null")
            fi
        else
            pr_fields=""
        fi

        if [ "$pr_fields" = "null null" ]; then
            echo "Error: GitHub Timeline API or parsing failed. Aborting task to prevent duplicate PRs (fail-closed)." >&2
            exit 1
        elif [ -n "$pr_fields" ]; then
            pr_number="${pr_fields%% *}"
            pr_url="${pr_fields#* }"
            if [ "$pr_url" = "null" ] || [ -z "$pr_url" ]; then
                # Dynamically determine the GitHub host from CLONE_URL, fallback to github.com
                gh_host="github.com"
                if [ -n "$CLONE_URL" ]; then
                    host_temp="${CLONE_URL#*://}"
                    host_temp="${host_temp#*@}"
                    host_temp="${host_temp%%/*}"
                    host_temp="${host_temp%%:*}"
                    if [ -n "$host_temp" ]; then
                        gh_host="$host_temp"
                    fi
                fi
                pr_url="https://${gh_host}/${REPO_OWNER}/${REPO_NAME}/pull/${pr_number}"
            fi
        fi
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

        output_file="$(dirname "${PROMPT_FILE}")/agent-output.txt"

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
