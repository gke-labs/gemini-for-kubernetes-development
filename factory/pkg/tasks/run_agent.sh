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

function setupGit {
    echo "Running setupGit..."
    echo "creating ${HOME}/.config/gh directory"
    mkdir -p "${HOME}/.config/gh"

    local GH_USER="${GITHUB_USER_ID}"
    if [ -n "${GITHUB_BOT_LOGIN}" ]; then
        GH_USER="${GITHUB_BOT_LOGIN}"
    fi

    echo "writing gh config"
    cat <<EOF > "${HOME}/.config/gh/hosts.yml"
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
    gh auth setup-git || true
    echo "configuring git url fallback"
    git config --global url."https://${GH_USER}:${GITHUB_USER_TOKEN}@github.com/".insteadOf "https://github.com/"

    echo "Configuring git pull rebase"
    git config --global pull.rebase true

    echo "Configuring global git ignore"
    git config --global core.excludesfile "${HOME}/.gitignore_global"
    cat <<EOF > "${HOME}/.gitignore_global"
manager
bin/
EOF

    echo "Sanitizing workspace (cleaning stale git locks)..."
    find /workspaces -maxdepth 4 -name "*.lock" -path "*/.git/*" -delete 2>/dev/null || true
}

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
}

function configureGemini {
    echo "Running configureGemini..."
    echo "creating ${HOME}/.gemini directory"
    mkdir -p "${HOME}/.gemini"

    echo "writing gemini config"
    cat <<EOF > "${HOME}/.gemini/settings.json"
{
  "general": {
    "enableAutoUpdate": false,
    "retryFetchErrors": true,
    "previewFeatures": true
  }
}
EOF
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
                echo "Pushed changes to PR #${PR_NUMBER}" > "$(dirname "${PROMPT_FILE}")/agent-output.txt"
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

function record_gemini_usage {
    local output_file="$1"
    local task_dir="$(dirname "${PROMPT_FILE}")"
    if [ -f "$output_file" ]; then
        python3 -c '
import json, os, sys

output_file = sys.argv[1]
task_dir = sys.argv[2]

try:
    with open(output_file, "r") as f:
        data = json.load(f)
except Exception:
    sys.exit(0)

stats = data.get("stats", {})
models = stats.get("models", {})
if not models and ("total_tokens" in stats or "total" in stats or "totalRequests" in stats):
    models = {data.get("model", "gemini-cli"): {
        "api": {"totalRequests": stats.get("totalRequests", stats.get("tool_calls", 0) + 1), "totalErrors": stats.get("totalErrors", 0), "totalLatencyMs": stats.get("totalLatencyMs", stats.get("duration_ms", 0))},
        "tokens": {"input": stats.get("input", stats.get("input_tokens", 0)), "output": stats.get("candidates", stats.get("output", stats.get("output_tokens", 0))), "total": stats.get("total", stats.get("total_tokens", 0)), "cached": stats.get("cached", 0), "thoughts": stats.get("thoughts", 0)}
    }}

if not models:
    sys.exit(0)

usage_path = os.path.join(task_dir, "llm-usage.json")
token_path = os.path.join(task_dir, "token-usage.json")

existing = {"models": {}}
if os.path.exists(usage_path):
    try:
        with open(usage_path, "r") as f:
            existing = json.load(f)
    except Exception:
        pass
elif os.path.exists(token_path):
    try:
        with open(token_path, "r") as f:
            existing = json.load(f)
    except Exception:
        pass

for model_name, model_data in models.items():
    api = model_data.get("api", {})
    tokens = model_data.get("tokens", {})
    
    cur_model = existing.get("models", {}).get(model_name, {
        "api": {"totalRequests": 0, "totalErrors": 0, "totalLatencyMs": 0},
        "tokens": {"input": 0, "output": 0, "total": 0, "cached": 0, "thoughts": 0}
    })
    
    cur_model["api"]["totalRequests"] += api.get("totalRequests", api.get("total_requests", 1))
    cur_model["api"]["totalErrors"] += api.get("totalErrors", api.get("total_errors", 0))
    cur_model["api"]["totalLatencyMs"] += api.get("totalLatencyMs", api.get("total_latency_ms", 0))
    
    cur_model["tokens"]["input"] += tokens.get("input", 0)
    cur_model["tokens"]["output"] += tokens.get("candidates", tokens.get("output", 0))
    cur_model["tokens"]["total"] += tokens.get("total", 0)
    cur_model["tokens"]["cached"] += tokens.get("cached", 0)
    cur_model["tokens"]["thoughts"] += tokens.get("thoughts", 0)
    
    if "models" not in existing:
        existing["models"] = {}
    existing["models"][model_name] = cur_model

try:
    with open(usage_path, "w") as f:
        json.dump(existing, f, indent=2)
    with open(token_path, "w") as f:
        json.dump(existing, f, indent=2)
except Exception:
    pass

try:
    import glob as gb
    from datetime import datetime
    def parse_iso(ts):
        if not ts: return None
        try: return datetime.fromisoformat(ts.replace("Z", "+00:00"))
        except Exception: return None
    session_files = gb.glob("/root/.gemini/tmp/*/chats/session-*.jsonl") + gb.glob("/workspaces/.home/.gemini/tmp/*/chats/session-*.jsonl")
    if session_files:
        latest_session = max(session_files, key=os.path.getmtime)
        tool_metrics = {"total_tool_calls": 0, "total_tool_duration_sec": 0, "tools": {}}
        pending = {}
        with open(latest_session, "r", errors="ignore") as f:
            for line in f:
                try:
                    data = json.loads(line)
                    ts = parse_iso(data.get("timestamp"))
                    if not ts: continue
                    if "toolCalls" in data and isinstance(data["toolCalls"], list):
                        for fc in data["toolCalls"]:
                            cmd = fc.get("args", {}).get("command", "") or fc.get("args", {}).get("CommandLine", "") or fc.get("args", {}).get("TargetFile", "") or str(fc.get("args", {}))
                            if len(str(cmd)) > 120: cmd = str(cmd)[:120] + "..."
                            pending[fc.get("id")] = {"name": fc.get("name", "unknown"), "start": ts, "cmd": cmd}
                    c = data.get("content", "")
                    if isinstance(c, list):
                        for part in c:
                            if "functionResponse" in part:
                                fr = part["functionResponse"]
                                cid = fr.get("id")
                                if cid in pending:
                                    sinfo = pending.pop(cid)
                                    dur = round((ts - sinfo["start"]).total_seconds(), 3)
                                    tname = sinfo["name"]
                                    tstat = tool_metrics["tools"].setdefault(tname, {"count": 0, "total_sec": 0, "max_sec": 0, "slowest_cmd": ""})
                                    tstat["count"] += 1
                                    tstat["total_sec"] = round(tstat["total_sec"] + dur, 3)
                                    if dur >= tstat["max_sec"]:
                                        tstat["max_sec"] = dur
                                        tstat["slowest_cmd"] = sinfo.get("cmd", "")
                                    tool_metrics["total_tool_calls"] += 1
                                    tool_metrics["total_tool_duration_sec"] = round(tool_metrics["total_tool_duration_sec"] + dur, 3)
                except Exception:
                    pass
        with open(os.path.join(task_dir, "tool-telemetry.json"), "w") as tf:
            json.dump(tool_metrics, tf, indent=2)
except Exception:
    pass
' "$output_file" "$task_dir"
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
                if (cd "/workspaces/${REPO_NAME}" && git ls-remote --heads origin "${BRANCH_NAME}" | grep -q "refs/heads/${BRANCH_NAME}"); then
                    echo "Pulling latest changes from remote branch..."
                    (cd "/workspaces/${REPO_NAME}" && git pull origin "${BRANCH_NAME}")
                else
                    echo "Remote branch ${BRANCH_NAME} does not exist on origin. Skipping pull."
                fi
            elif (cd "/workspaces/${REPO_NAME}" && git ls-remote --heads origin "${BRANCH_NAME}" | grep -q "refs/heads/${BRANCH_NAME}"); then
                echo "Remote branch ${BRANCH_NAME} exists. Checking out with tracking..."
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
                echo "Warning: Rebase of workflow branch onto ${UPSTREAM_REMOTE}/${BASE_BRANCH} failed. Aborting."
                (cd "/workspaces/${REPO_NAME}" && git rebase --abort)
            fi
        else
            SLUGIFIED_NAME=$(echo "${AGENT_NAME}" | tr '[:upper:]' '[:lower:]' | tr -c '[:alnum:]' '-' | sed 's/^-//;s/-$//')
            BRANCH_NAME="agent/${SLUGIFIED_NAME}-$(date +%Y%m%d-%H%M%S)"
            
            # Start from base branch
            git rebase --abort 2>/dev/null || true
            git merge --abort 2>/dev/null || true
            git cherry-pick --abort 2>/dev/null || true
            git reset --hard HEAD
            git clean -fd
            git checkout "${BASE_BRANCH}"
            git checkout -b "${BRANCH_NAME}"
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

    MODELS_LIST="${MODELS:-gemini-3.5-flash gemini-3-flash-preview gemini-3.1-pro-preview gemini-2.5-pro}"
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

setupGit
setupGitRepos
# HACK: Avoid git lock issues
sleep 5
configureGemini
runAgent
