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
# - PROMPT_FILE
# - GITHUB_USER_ID
# - GITHUB_USER_EMAIL
# - GITHUB_USER_NAME
# - AGENT_NAME
# - AGENT_FILE
# - SKIP_PR
# - PR_NUMBER
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
        # Fetch latest changes from remotes
        (cd "/workspaces/${REPO_NAME}" && git fetch origin && (git fetch upstream 2>/dev/null || true))
    fi

    echo "running gh repo fork"
    (cd "/workspaces/${REPO_NAME}" && gh repo fork --remote || true)

    echo "Configuring git remotes to ensure origin is the fork and upstream is the base..."
    local GH_USER="${GITHUB_BOT_LOGIN:-${GITHUB_USER_ID}}"
    if [ -n "${GH_USER}" ]; then
        local FORK_URL="https://github.com/${GH_USER}/${REPO_NAME}.git"
        echo "Setting origin remote to fork URL: ${FORK_URL}"
        if ! (cd "/workspaces/${REPO_NAME}" && git remote | grep -q "^origin$"); then
            (cd "/workspaces/${REPO_NAME}" && git remote add origin "${FORK_URL}" || true)
        fi
        (cd "/workspaces/${REPO_NAME}" && git remote set-url origin "${FORK_URL}")
    fi

    if ! (cd "/workspaces/${REPO_NAME}" && git remote | grep -q "^upstream$"); then
        (cd "/workspaces/${REPO_NAME}" && git remote add upstream "${CLONE_URL}" || true)
    fi
    (cd "/workspaces/${REPO_NAME}" && git remote set-url upstream "${CLONE_URL}")

    echo "running gh repo set-default"
    (cd "/workspaces/${REPO_NAME}" && gh repo set-default "${CLONE_URL}" || true)

    echo "running git config local user.email"
    (cd "/workspaces/${REPO_NAME}" && git config user.email "${GITHUB_USER_EMAIL}")

    echo "running git config local user.name"
    (cd "/workspaces/${REPO_NAME}" && git config user.name "${GITHUB_USER_NAME}")
}

function commitChanges {
    if [ "$AGENT_MODE" = "workflow" ]; then
        if [ -n "$SESSION_ID" ] && [[ "$SESSION_ID" == issue-* ]]; then
            ISSUE_NUM="${SESSION_ID#issue-}"
            ISSUE_STATE=$(gh issue view "${ISSUE_NUM}" --json state --jq .state 2>/dev/null || echo "OPEN")
            if [ "${ISSUE_STATE}" = "CLOSED" ]; then
                echo "Parent issue #${ISSUE_NUM} is closed. Deleting remote branch ${BRANCH_NAME}..."
                git push origin --delete "${BRANCH_NAME}" || true
                echo "Cleaned up remote branch for completed workflow." > "$(dirname "${PROMPT_FILE}")/agent-output.txt"
                return
            fi
        fi

        if [ -n "$(git status --porcelain)" ]; then
            echo "Committing workflow changes..."
            git add .
            git commit -m "chore(workflow): update workflow state/journal [skip ci]"
            git push --force origin "${BRANCH_NAME}"
            echo "Directly pushed workflow changes to branch." > "$(dirname "${PROMPT_FILE}")/agent-output.txt"
        else
            echo "No workflow changes to commit."
            echo "No changes detected." > "$(dirname "${PROMPT_FILE}")/agent-output.txt"
        fi
    else
        BASE_BRANCH=$(gh repo view --json defaultBranchRef --jq .defaultBranchRef.name)
        
        # Check for changes (uncommitted or committed on this branch compared to base branch)
        if [ -n "$(git status --porcelain)" ] || [ "$(git rev-parse HEAD)" != "$(git rev-parse ${BASE_BRANCH})" ]; then
            echo "Changes detected."
            
            # If there are uncommitted changes, commit them
            if [ -n "$(git status --porcelain)" ]; then
                echo "Committing uncommitted changes..."
                git diff > /tmp/agent_diff.txt
                
                gemini --yolo --output-format json -p "Generate a concise, meaningful commit message for the following changes.
    The changes are part of an agent named '${AGENT_NAME}' (defined in ${AGENT_FILE}).
    
    DIFF:
    $(cat /tmp/agent_diff.txt | head -c 2000)
    
    The commit message should be prefixed with 'chore: ' and should explicitly mention it was automatically generated.
    Only output the commit message itself." > /tmp/gemini-commit-msg.json || true
                if [ -f /tmp/gemini-commit-msg.json ]; then
                    record_gemini_usage /tmp/gemini-commit-msg.json
                    COMMIT_MSG=$(python3 -c 'import json, sys; print(json.load(open(sys.argv[1])).get("response", "").strip())' /tmp/gemini-commit-msg.json 2>/dev/null)
                fi
                
                if [ -z "$COMMIT_MSG" ]; then
                    COMMIT_MSG="chore: automatic updates from agent ${AGENT_NAME}"
                fi
    
                git add .
                git commit -m "${COMMIT_MSG}"
            else
                COMMIT_MSG=$(git log -1 --pretty=%B)
            fi
    
            # Push the branch
            git push --force origin "${BRANCH_NAME}"
            
            if [ "${PR_NUMBER:-0}" -gt 0 ]; then
                echo "PR already exists (#${PR_NUMBER}), pushed changes to branch."
                echo "https://github.com/${REPO_OWNER}/${REPO_NAME}/pull/${PR_NUMBER}" > "$(dirname "${PROMPT_FILE}")/agent-output.txt"
            else
                # Determine Repo Owner for the link
                REPO_URL=$(git remote get-url origin)
                REPO_PATH=$(echo $REPO_URL | sed -E 's/.*github.com[:\/]//;s/\.git$//')
                FORK_OWNER=$(echo "$REPO_PATH" | cut -d'/' -f1)
                
                PR_BODY="This Pull Request was automatically generated by **Factory Agent** for the **${AGENT_NAME}** agent.
    
    **Agent Definition:** [${AGENT_FILE}](https://github.com/${REPO_OWNER}/${REPO_NAME}/blob/${BASE_BRANCH}/${AGENT_FILE})
    
    ---
    ### Changes
    ${COMMIT_MSG}"
    
                # Resolve labels from factory config if present
                PR_LABELS="factory"
                if [ -n "$FACTORY_CONFIG" ] && [ -f "$FACTORY_CONFIG" ]; then
                    RESOLVED_LABELS=$(python3 -c "import yaml; cfg = yaml.safe_load(open('$FACTORY_CONFIG')) or {}; trigger = cfg.get('triggerLabel', 'factory'); additional = cfg.get('additionalLabels') or []; print(','.join([trigger] + additional))" 2>/dev/null || true)
                    if [ -n "$RESOLVED_LABELS" ]; then
                        PR_LABELS="$RESOLVED_LABELS"
                    fi
                fi

                # Try to create PR
                PR_URL=$(gh pr create --title "chore: ${AGENT_NAME}" --body "${PR_BODY}" --head "${FORK_OWNER}:${BRANCH_NAME}" --base "${BASE_BRANCH}" --label "${PR_LABELS}" || true)
                
                if [ -n "$PR_URL" ] && [[ "$PR_URL" == http* ]]; then
                    echo "$PR_URL" > "$(dirname "${PROMPT_FILE}")/agent-output.txt"
                    gh pr edit "$PR_URL" --add-label "${PR_LABELS}" || echo "Warning: failed to add labels ${PR_LABELS} to $PR_URL"
                else
                    echo "Failed to create PR" > "$(dirname "${PROMPT_FILE}")/agent-output.txt"
                fi
            fi
        else
            echo "No changes detected."
            echo "No changes detected." > "$(dirname "${PROMPT_FILE}")/agent-output.txt"
        fi
    fi
}

function runAgent {
    pushd "/workspaces/${REPO_NAME}" > /dev/null
    
    # Identify the base branch (usually main or master)
    BASE_BRANCH=$(gh repo view --json defaultBranchRef --jq .defaultBranchRef.name)

    if [ "${PR_NUMBER:-0}" -gt 0 ]; then
        echo "Checking out PR #${PR_NUMBER} branch..."
        (cd "/workspaces/${REPO_NAME}" && git rebase --abort 2>/dev/null || true)
        (cd "/workspaces/${REPO_NAME}" && git merge --abort 2>/dev/null || true)
        (cd "/workspaces/${REPO_NAME}" && git cherry-pick --abort 2>/dev/null || true)
        (cd "/workspaces/${REPO_NAME}" && git reset --hard HEAD && git clean -fd && /usr/bin/gh pr checkout ${PR_NUMBER} --force && git pull origin HEAD || true)
        BRANCH_NAME=$(git branch --show-current)
    else
        # Create a unique branch for this agent run if skip PR is not true
        if [ "$SKIP_PR" = "true" ]; then
            echo "SkipPR is true. Running on default branch ${BASE_BRANCH}"
            BRANCH_NAME="${BASE_BRANCH}"
        elif [ "$AGENT_MODE" = "workflow" ]; then
            if [ -n "$SESSION_ID" ] && [[ "$SESSION_ID" == issue-* ]]; then
                BRANCH_NAME="factory-${SESSION_ID#issue-}"
            else
                BRANCH_NAME="factory"
            fi
            echo "Agent mode is workflow. Checking out branch ${BRANCH_NAME}..."
            (cd "/workspaces/${REPO_NAME}" && git rebase --abort 2>/dev/null || true)
            (cd "/workspaces/${REPO_NAME}" && git merge --abort 2>/dev/null || true)
            (cd "/workspaces/${REPO_NAME}" && git cherry-pick --abort 2>/dev/null || true)
            (cd "/workspaces/${REPO_NAME}" && git reset --hard HEAD && git clean -fd)
            (cd "/workspaces/${REPO_NAME}" && git fetch origin && (git fetch upstream 2>/dev/null || true))
            if (cd "/workspaces/${REPO_NAME}" && git show-ref --verify --quiet "refs/heads/${BRANCH_NAME}"); then
                echo "Local branch ${BRANCH_NAME} already exists. Checking out..."
                (cd "/workspaces/${REPO_NAME}" && git checkout "${BRANCH_NAME}")
                if (cd "/workspaces/${REPO_NAME}" && git show-ref --verify --quiet "refs/remotes/origin/${BRANCH_NAME}"); then
                    echo "Resetting local branch to latest remote origin/${BRANCH_NAME}..."
                    (cd "/workspaces/${REPO_NAME}" && git reset --hard "origin/${BRANCH_NAME}" && git clean -fd)
                else
                    echo "Remote branch ${BRANCH_NAME} does not exist on origin. Continuing with local branch."
                fi
            elif (cd "/workspaces/${REPO_NAME}" && git show-ref --verify --quiet "refs/remotes/origin/${BRANCH_NAME}"); then
                echo "Remote branch ${BRANCH_NAME} exists on origin. Checking out with tracking..."
                (cd "/workspaces/${REPO_NAME}" && git checkout -b "${BRANCH_NAME}" --track "origin/${BRANCH_NAME}")
            else
                echo "Remote branch ${BRANCH_NAME} does not exist on origin yet. Creating new branch..."
                (cd "/workspaces/${REPO_NAME}" && git checkout -b "${BRANCH_NAME}")
            fi

            # Periodic rebase to upstream default branch for workflow branch
            UPSTREAM_REMOTE="origin"
            if (cd "/workspaces/${REPO_NAME}" && git remote | grep -q "^upstream$"); then
                UPSTREAM_REMOTE="upstream"
            fi
            echo "Rebasing workflow branch ${BRANCH_NAME} onto ${UPSTREAM_REMOTE}/${BASE_BRANCH}..."
            if ! (cd "/workspaces/${REPO_NAME}" && git rebase "${UPSTREAM_REMOTE}/${BASE_BRANCH}"); then
                echo "Warning: Rebase of workflow branch onto ${UPSTREAM_REMOTE}/${BASE_BRANCH} failed. Aborting and continuing with existing branch state."
                (cd "/workspaces/${REPO_NAME}" && git rebase --abort 2>/dev/null || true)
            fi
        else
            SLUGIFIED_NAME=$(echo "${AGENT_NAME}" | tr '[:upper:]' '[:lower:]' | tr -c '[:alnum:]' '-' | sed 's/^-//;s/-$//')
            
            # Check if there is an existing open PR for this agent chore
            EXISTING_PR_NUM=$(gh pr list --state open --search "\"chore: ${AGENT_NAME}\" in:title" --json number --jq '.[0].number' 2>/dev/null || true)
            
            if [ -n "$EXISTING_PR_NUM" ] && [ "$EXISTING_PR_NUM" != "null" ]; then
                echo "Found existing open PR #${EXISTING_PR_NUM} for ${AGENT_NAME}. Checking out its branch..."
                git rebase --abort 2>/dev/null || true
                git merge --abort 2>/dev/null || true
                git cherry-pick --abort 2>/dev/null || true
                git reset --hard HEAD
                git clean -fd
                gh pr checkout "${EXISTING_PR_NUM}" --force
                BRANCH_NAME=$(git branch --show-current)
                PR_NUMBER="${EXISTING_PR_NUM}"
                
                # Rebasing existing branch onto latest default branch to keep it up to date
                echo "Rebasing existing branch ${BRANCH_NAME} onto origin/${BASE_BRANCH}..."
                if ! git rebase "origin/${BASE_BRANCH}"; then
                    echo "Warning: Rebase failed. Aborting and continuing with existing branch state."
                    git rebase --abort 2>/dev/null || true
                fi
            else
                BRANCH_NAME="agent/${SLUGIFIED_NAME}"
                
                # Start from base branch
                git rebase --abort 2>/dev/null || true
                git merge --abort 2>/dev/null || true
                git cherry-pick --abort 2>/dev/null || true
                git reset --hard HEAD
                git clean -fd
                git checkout "${BASE_BRANCH}"
                git reset --hard "origin/${BASE_BRANCH}" || true
                git checkout -B "${BRANCH_NAME}"
            fi
        fi
    fi
 
    echo "Running Gemini in YOLO mode..."
    set +x
    export GEMINI_API_KEY="${GEMINI_API_KEY}"
 
    if [ -n "$GITHUB_BOT_NAME" ]; then
        echo "Using bot identity for commits"
        export GIT_AUTHOR_NAME="$GITHUB_BOT_NAME"
        export GIT_AUTHOR_EMAIL="$GITHUB_BOT_EMAIL"
        export GIT_COMMITTER_NAME="$GITHUB_BOT_NAME"
        export GIT_COMMITTER_EMAIL="$GITHUB_BOT_EMAIL"
    fi
 
    # Do not resume previous sessions across separate workflow runs; each workflow invocation starts fresh
    # to avoid unbounded token accumulation in long-lived periodic workflows.
    RESUME_FLAG=""

    MODELS_LIST="${MODELS:-__DEFAULT_MODELS__}"
    SUCCESS=false
    for MODEL in $MODELS_LIST; do
        echo "Trying model: $MODEL"
        if [ "${DRY_RUN:-false}" = "true" ]; then
            echo "[dry-run] Would run: gemini --yolo --model \"$MODEL\" ${RESUME_FLAG} --include-directories \"$(dirname "${PROMPT_FILE}")\" --output-format json < \"${PROMPT_FILE}\""
            SUCCESS=true
            break
        fi
        if gemini --yolo --model "$MODEL" ${RESUME_FLAG} --include-directories "$(dirname "${PROMPT_FILE}")" --output-format json < "${PROMPT_FILE}" > "$(dirname "${PROMPT_FILE}")/gemini-output.json"; then
            echo "Gemini execution successful with model: $MODEL"
            record_gemini_usage "$(dirname "${PROMPT_FILE}")/gemini-output.json"
            SUCCESS=true
            break
        else
            echo "Gemini execution encountered errors with model: $MODEL. Retrying next model..."
        fi
    done

    if [ "$SUCCESS" = false ]; then
        echo "All models failed."
        exit 1
    fi
    set -x
 
    if [ "$SKIP_PR" = "true" ]; then
        echo "SkipPR is true, skipping commit and push."
        echo "Agent executed successfully without creating/updating a PR." > "$(dirname "${PROMPT_FILE}")/agent-output.txt"
    else
        commitChanges
    fi
    
    popd > /dev/null
}

function runPrecondition {
    if [ -n "$PRECONDITION_FILE" ] && [ -f "$PRECONDITION_FILE" ]; then
        echo "Running precondition script..."
        chmod +x "$PRECONDITION_FILE"
        
        pushd "/workspaces/${REPO_NAME}" > /dev/null
        
        if ! "$PRECONDITION_FILE"; then
            echo "Precondition script failed (exit status $?). Deferring workflow." > "$(dirname "${PROMPT_FILE}")/agent-output.txt"
            echo "Precondition script failed. Deferring workflow."
            popd > /dev/null
            exit 0
        fi
        
        echo "Precondition script passed."
        popd > /dev/null
    fi
}

setupGit
setupGitRepos
# HACK: Avoid git lock issues
sleep 5
configureGemini
runPrecondition
runAgent
