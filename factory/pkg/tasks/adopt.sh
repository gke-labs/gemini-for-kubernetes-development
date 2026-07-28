#!/bin/bash
set -e
set -o pipefail
set -x

USER_HOME="${HOME:-/root}"
mkdir -p "${USER_HOME}"

export GITHUB_USER_TOKEN="${GITHUB_USER_TOKEN:-${GITHUB_TOKEN}}"
if [ -z "$GITHUB_USER_TOKEN" ]; then
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

setupGit

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
configureGemini

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
                                    if tname == "run_shell_command":
                                        all_shell_calls.append({
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

# Fork the repository if it doesn't already exist under the bot user account
GH_USER="${GITHUB_USER_ID}"
if [ -n "${GITHUB_BOT_LOGIN}" ]; then
    GH_USER="${GITHUB_BOT_LOGIN}"
fi

echo "Ensuring fork of ${REPO_OWNER}/${REPO_NAME} for user ${GH_USER}..."
gh repo fork "${REPO_OWNER}/${REPO_NAME}" --clone=false || true

# Clone the repository if it doesn't exist under /workspaces
if [ ! -d "/workspaces/${REPO_NAME}" ]; then
    echo "Cloning repository ${CLONE_URL}..."
    (cd /workspaces/ && git clone "${CLONE_URL}")
fi

cd "/workspaces/${REPO_NAME}"
FORK_URL="https://github.com/${GH_USER}/${REPO_NAME}.git"
git remote add fork "${FORK_URL}" || true
git fetch origin

if [ "$STRATEGY" = "reuse" ]; then
    echo "Executing 'reuse' strategy (git-based history preservation)..."
    
    echo "Fetching PR head commit..."
    git fetch origin "pull/${PR_NUMBER}/head:adopt-pr-${PR_NUMBER}"
    
    echo "Pushing branch adopt-pr-${PR_NUMBER} to fork..."
    git push -f fork "adopt-pr-${PR_NUMBER}"
    
    git checkout "adopt-pr-${PR_NUMBER}"

elif [ "$STRATEGY" = "reimplement" ]; then
    echo "Executing 'reimplement' strategy (LLM-based re-implementation)..."
    
    BASE_BRANCH=$(gh repo view --json defaultBranchRef --jq .defaultBranchRef.name)
    
    git rebase --abort 2>/dev/null || true
    git merge --abort 2>/dev/null || true
    git cherry-pick --abort 2>/dev/null || true
    git reset --hard HEAD
    git clean -fd
    git checkout "${BASE_BRANCH}"
    
    BRANCH_NAME="adopt-reimplement-pr-${PR_NUMBER}"
    git checkout -B "${BRANCH_NAME}"
    
    echo "Running Gemini in YOLO mode..."
    set +x
    export GEMINI_API_KEY="${GEMINI_API_KEY}"
    
    MODELS_LIST="${MODELS:-gemini-3.5-flash gemini-3-flash-preview gemini-3.1-pro-preview gemini-2.5-pro}"
    SUCCESS=false
    for MODEL in $MODELS_LIST; do
        echo "Trying model: $MODEL"
        if gemini --yolo --model "$MODEL" --output-format json < "${PROMPT_FILE}" > "$(dirname "${PROMPT_FILE}")/gemini-output.json"; then
            echo "Gemini execution successful with model: $MODEL"
            record_gemini_usage "$(dirname "${PROMPT_FILE}")/gemini-output.json"
            SUCCESS=true
            break
        else
            echo "Gemini execution encountered errors with model: $MODEL. Retrying next model..."
        fi
    done
    
    if [ "$SUCCESS" = false ]; then
        echo "All models failed to implement changes."
        exit 1
    fi
    set -x
    
    if [ -n "$(git status --porcelain)" ]; then
        echo "Committing reimplemented changes..."
        git add .
        git commit -m "chore: adopt PR #${PR_NUMBER} by reimplementing changes on latest ${BASE_BRANCH}"
        git push -f fork "${BRANCH_NAME}"
    else
        echo "Error: No changes were implemented by the model."
        exit 1
    fi
    
else
    echo "Unknown strategy: $STRATEGY"
    exit 1
fi

# ----------------- Create New Adopted PR -----------------
cd "/workspaces"
if [ -d "/workspaces/${REPO_NAME}" ]; then
    cd "/workspaces/${REPO_NAME}"
fi

NEW_PR_TITLE=$(gh pr view "${PR_URL}" --json title --jq .title)
ORIGINAL_PR_BODY=$(gh pr view "${PR_URL}" --json body --jq .body)
NEW_PR_BODY="This Pull Request was adopted from original PR ${PR_URL}

---
### Original Description:
${ORIGINAL_PR_BODY}"

if [ "$STRATEGY" = "reuse" ]; then
    HEAD_BRANCH="${GH_USER}:adopt-pr-${PR_NUMBER}"
else
    HEAD_BRANCH="${GH_USER}:adopt-reimplement-pr-${PR_NUMBER}"
fi

BASE_BRANCH=$(gh repo view "${CLONE_URL}" --json defaultBranchRef --jq .defaultBranchRef.name)

echo "Creating adopted PR on GitHub..."
CREATED_PR_URL=$(gh pr create --title "adopt: ${NEW_PR_TITLE}" --body "${NEW_PR_BODY}" --head "${HEAD_BRANCH}" --base "${BASE_BRANCH}" || true)

if [ -n "${CREATED_PR_URL}" ] && [[ "${CREATED_PR_URL}" == http* ]]; then
    echo "PR successfully created: ${CREATED_PR_URL}"
    # Write to agent output so host can extract it
    echo "${CREATED_PR_URL}" > "$(dirname "${PROMPT_FILE}")/agent-output.txt"
    
    # ----------------- Comment & Close Original PR -----------------
    if [ "$ADOPT_FLAG" = "close" ]; then
        COMMENT_BODY="This PR has been adopted/forked here: ${CREATED_PR_URL} and closed."
        gh pr comment "${PR_URL}" --body "${COMMENT_BODY}" || true
        gh pr close "${PR_URL}" || true
    else
        COMMENT_BODY="This PR has been adopted/forked here: ${CREATED_PR_URL}"
        gh pr comment "${PR_URL}" --body "${COMMENT_BODY}" || true
    fi
else
    echo "Failed to create PR"
    exit 1
fi
