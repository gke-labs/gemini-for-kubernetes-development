#!/bin/bash
set -e
set -o pipefail
set -x

USER_HOME="${HOME:-/root}"
mkdir -p "${USER_HOME}"

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

function setupGit {
    echo "Running setupGit..."

    echo "creating ${USER_HOME}/.config/gh directory"
    mkdir -p "${USER_HOME}/.config/gh"

    local GH_USER="${GITHUB_USER_ID}"
    if [ -n "${GITHUB_BOT_LOGIN}" ]; then
        GH_USER="${GITHUB_BOT_LOGIN}"
    fi

    echo "writing gh config"
    cat <<EOF > "${USER_HOME}/.config/gh/hosts.yml"
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

    echo "Configuring global git ignore"
    git config --global core.excludesfile "${USER_HOME}/.gitignore_global"
    cat <<EOF > "${USER_HOME}/.gitignore_global"
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

    # Try to find a PR by the current user first
    local pr_number=$(gh search prs "${ISSUE_NUMBER}" --state open --repo "${REPO_OWNER}/${REPO_NAME}" --author "${GITHUB_USER_ID}" --json number --jq '.[0] | "\(.number)"' --limit 1)
    local pr_url=$(gh search prs "${ISSUE_NUMBER}" --state open --repo "${REPO_OWNER}/${REPO_NAME}" --author "${GITHUB_USER_ID}" --json url --jq '.[0] | "\(.url)"' --limit 1)

    # If not found, look for any PR
    if [ -z "$pr_number" ] || [ "$pr_number" == "null" ]; then
        pr_number=$(gh search prs "${ISSUE_NUMBER}" --repo "${REPO_OWNER}/${REPO_NAME}" --state open --json number --jq '.[0] | "\(.number)"' --limit 1)
        pr_url=$(gh search prs "${ISSUE_NUMBER}" --repo "${REPO_OWNER}/${REPO_NAME}" --state open --json url --jq '.[0] | "\(.url)"' --limit 1)
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
        exit 0
    fi

    popd > /dev/null
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

function configureGemini {
    echo "Running configureGemini..."
    echo "creating ${USER_HOME}/.gemini directory"
    mkdir -p "${USER_HOME}/.gemini"

    echo "writing gemini config"
    cat <<EOF > "${USER_HOME}/.gemini/settings.json"
{
  "general": {
    "enableAutoUpdate": false,
    "retryFetchErrors": true,
    "previewFeatures": true
  }
}
EOF
}

function installExtensions {
    echo "Installing extensions..."
    if [ -n "$EXTENSIONS" ]; then
        for ext in $EXTENSIONS; do
            gemini extensions install "$ext" --consent
        done
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
        tool_metrics = {"total_tool_calls": 0, "total_tool_duration_sec": 0, "tools": {}, "shell_calls": []}
        pending = {}
        all_shell_calls = []
        with open(latest_session, "r", errors="ignore") as f:
            for line in f:
                try:
                    data = json.loads(line)
                    ts = parse_iso(data.get("timestamp"))
                    if not ts: continue
                    ts_str = data.get("timestamp", "")
                    if "toolCalls" in data and isinstance(data["toolCalls"], list):
                        for fc in data["toolCalls"]:
                            tname = fc.get("name", "unknown")
                            args = fc.get("args", {})
                            cmd = args.get("command", "") or args.get("CommandLine", "") or args.get("TargetFile", "") or str(args)
                            pending[fc.get("id")] = {"name": tname, "start": ts, "ts_str": ts_str, "cmd": str(cmd)}
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
                                    full_cmd = sinfo.get("cmd", "")
                                    trunc_cmd = full_cmd[:300] + ("..." if len(full_cmd) > 300 else "")
                                    tstat = tool_metrics["tools"].setdefault(tname, {"count": 0, "total_sec": 0, "max_sec": 0, "slowest_cmd": ""})
                                    tstat["count"] += 1
                                    tstat["total_sec"] = round(tstat["total_sec"] + dur, 3)
                                    if dur >= tstat["max_sec"]:
                                        tstat["max_sec"] = dur
                                        tstat["slowest_cmd"] = trunc_cmd[:120] + ("..." if len(trunc_cmd) > 120 else "")
                                    tool_metrics["total_tool_calls"] += 1
                                    tool_metrics["total_tool_duration_sec"] = round(tool_metrics["total_tool_duration_sec"] + dur, 3)
                                    if "shell" in tname or "command" in tname or tname in ("run_shell_command", "run_command", "run_shell_commands", "exec", "bash"):
                                        all_shell_calls.append({
                                            "tool": tname,
                                            "cmd": trunc_cmd,
                                            "duration_sec": dur,
                                            "timestamp": sinfo.get("ts_str", ts.isoformat())
                                        })
                except Exception:
                    pass
        all_shell_calls.sort(key=lambda x: x["duration_sec"], reverse=True)
        tool_metrics["shell_calls"] = all_shell_calls[:50]
        with open(os.path.join(task_dir, "tool-telemetry.json"), "w") as tf:
            json.dump(tool_metrics, tf, indent=2)
except Exception:
    pass
' "$output_file" "$task_dir"
    fi
}

function runGemini {
    echo "running gemini in yolo mode"
    pushd "/workspaces/${REPO_NAME}" > /dev/null
    set +x
    export GEMINI_API_KEY="${GEMINI_API_KEY}"

    if [ -n "$GITHUB_BOT_NAME" ]; then
        echo "Using bot identity for commits"
        export GIT_AUTHOR_NAME="$GITHUB_BOT_NAME"
        export GIT_AUTHOR_EMAIL="$GITHUB_BOT_EMAIL"
        export GIT_COMMITTER_NAME="$GITHUB_BOT_NAME"
        export GIT_COMMITTER_EMAIL="$GITHUB_BOT_EMAIL"
    fi

    MODELS_LIST="${MODELS:-__DEFAULT_MODELS__}"
    SUCCESS=false
    for MODEL in $MODELS_LIST; do
        echo "Trying model: $MODEL"
        if gemini --yolo --model "$MODEL" --output-format json < ${PROMPT_FILE} > "$(dirname "${PROMPT_FILE}")/gemini-output.json"; then
            echo "Gemini execution successful with model: $MODEL"
            record_gemini_usage "$(dirname "${PROMPT_FILE}")/gemini-output.json"
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
    set -x
    popd > /dev/null
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

    # If still not found, try searching PRs by issue number and author
    if [ -z "$pr_url" ] || [ "$pr_url" == "null" ]; then
        if [ "${ISSUE_NUMBER:-0}" -gt 0 ]; then
            echo "Searching for PR..."
            pr_url=$(gh search prs "${ISSUE_NUMBER}" --state open --repo "${REPO_OWNER}/${REPO_NAME}" --author "${GITHUB_USER_ID}" --json url --jq '.[0].url // empty' --limit 1)
        fi
    fi

    if [ -n "$pr_url" ] && [ "$pr_url" != "null" ]; then
        echo "Successfully found PR: ${pr_url}"
        echo "${pr_url}" > "$output_file"
        popd > /dev/null
    else
        echo "Could not find PR link automatically." | tee "$output_file"
        popd > /dev/null
        echo "Task finished without creating a pull request." >&2
        exit 1
    fi
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
runGemini
recordPRLink
