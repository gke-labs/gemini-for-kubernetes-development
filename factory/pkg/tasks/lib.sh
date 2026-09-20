#!/bin/bash
# lib.sh — shared task-script functions, prepended to every task script by
# getScriptWithDefaults. Definition order matters: a script that needs
# different behavior redefines the function after this prelude (bash: the
# last definition wins). Keep this file engine-agnostic where possible —
# it is the seam where the claude engine lands (design/multi-engine.md).
set -e
set -o pipefail

USER_HOME="${HOME:-/root}"
mkdir -p "${USER_HOME}"

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
}

function checkoutPRBranch {
    echo "Running checkoutPRBranch..."
    echo "checking out PR #${PR_NUMBER}"
    (cd "/workspaces/${REPO_NAME}" && git rebase --abort 2>/dev/null || true)
    (cd "/workspaces/${REPO_NAME}" && git merge --abort 2>/dev/null || true)
    (cd "/workspaces/${REPO_NAME}" && git cherry-pick --abort 2>/dev/null || true)
    (cd "/workspaces/${REPO_NAME}" && git reset --hard HEAD && git clean -fd && /usr/bin/gh pr checkout ${PR_NUMBER} --force && (git reset --hard @{u} 2>/dev/null || git reset --hard FETCH_HEAD))
    OLD_HEAD=$(cd "/workspaces/${REPO_NAME}" && git rev-parse HEAD)
}

function checkoutDefaultBranch {
    echo "Running checkoutDefaultBranch..."
    (cd "/workspaces/${REPO_NAME}" && git rebase --abort 2>/dev/null || true)
    (cd "/workspaces/${REPO_NAME}" && git merge --abort 2>/dev/null || true)
    (cd "/workspaces/${REPO_NAME}" && git cherry-pick --abort 2>/dev/null || true)
    (cd "/workspaces/${REPO_NAME}" && BASE_BRANCH=$(gh repo view --json defaultBranchRef --jq .defaultBranchRef.name) && git reset --hard HEAD && git clean -fd && git checkout "${BASE_BRANCH}" && git fetch origin "${BASE_BRANCH}" && git reset --hard "origin/${BASE_BRANCH}")
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

# record_claude_usage: map Claude Code's -p JSON (modelUsage / usage /
# total_cost_usd) into the same llm-usage.json / token-usage.json shape
# record_gemini_usage writes, so the token daemon and TokenUsage UI flow
# through unchanged. Transcript-derived tool telemetry is gemini-only for
# now (multi-engine design, phase 4).
function record_claude_usage {
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

models = {}
for name, mu in data.get("modelUsage", {}).items():
    models[name] = {
        "api": {"totalRequests": data.get("num_turns", 1), "totalErrors": 1 if data.get("is_error") else 0, "totalLatencyMs": data.get("duration_api_ms", data.get("duration_ms", 0))},
        "tokens": {"input": mu.get("inputTokens", 0), "output": mu.get("outputTokens", 0), "total": mu.get("inputTokens", 0) + mu.get("outputTokens", 0), "cached": mu.get("cacheReadInputTokens", 0), "thoughts": 0},
    }
if not models and "usage" in data:
    u = data.get("usage", {})
    models = {"claude": {
        "api": {"totalRequests": data.get("num_turns", 1), "totalErrors": 1 if data.get("is_error") else 0, "totalLatencyMs": data.get("duration_api_ms", data.get("duration_ms", 0))},
        "tokens": {"input": u.get("input_tokens", 0), "output": u.get("output_tokens", 0), "total": u.get("input_tokens", 0) + u.get("output_tokens", 0), "cached": u.get("cache_read_input_tokens", 0), "thoughts": 0},
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

    cur_model["api"]["totalRequests"] += api.get("totalRequests", 1)
    cur_model["api"]["totalErrors"] += api.get("totalErrors", 0)
    cur_model["api"]["totalLatencyMs"] += api.get("totalLatencyMs", 0)

    cur_model["tokens"]["input"] += tokens.get("input", 0)
    cur_model["tokens"]["output"] += tokens.get("output", 0)
    cur_model["tokens"]["total"] += tokens.get("total", 0)
    cur_model["tokens"]["cached"] += tokens.get("cached", 0)

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

# Tool telemetry from the Claude Code transcript: assistant tool_use
# entries pair with user tool_result entries by id; the output shape is
# identical to the gemini miner so downstream readers need nothing.
try:
    import glob as gb
    from datetime import datetime
    def parse_iso(ts):
        if not ts: return None
        try: return datetime.fromisoformat(ts.replace("Z", "+00:00"))
        except Exception: return None
    session_files = gb.glob(os.path.join(os.environ.get("HOME", "/workspaces/.home"), ".claude", "projects", "*", "*.jsonl"))
    if session_files:
        latest = max(session_files, key=os.path.getmtime)
        tool_metrics = {"total_tool_calls": 0, "total_tool_duration_sec": 0, "tools": {}, "shell_calls": []}
        pending = {}
        all_shell_calls = []
        with open(latest, "r", errors="ignore") as f:
            for line in f:
                try:
                    d = json.loads(line)
                    ts = parse_iso(d.get("timestamp"))
                    if not ts: continue
                    content = (d.get("message") or {}).get("content")
                    if not isinstance(content, list): continue
                    if d.get("type") == "assistant":
                        for c in content:
                            if isinstance(c, dict) and c.get("type") == "tool_use":
                                inp = c.get("input", {}) or {}
                                cmd = inp.get("command", "") or inp.get("file_path", "") or inp.get("pattern", "") or str(inp)
                                pending[c.get("id")] = {"name": c.get("name", "unknown"), "start": ts, "ts_str": d.get("timestamp", ""), "cmd": str(cmd)}
                    elif d.get("type") == "user":
                        for c in content:
                            if isinstance(c, dict) and c.get("type") == "tool_result":
                                cid = c.get("tool_use_id")
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
                                    if tname in ("Bash", "BashOutput") or "bash" in tname.lower():
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
' "$output_file" "$task_dir" || echo "record_claude_usage failed (non-fatal)"
    fi
}

# record_engine_usage: the per-engine dispatch — each engine keeps its own
# recorder, this is the only place that knows which is which.
function record_engine_usage {
    case "${ENGINE:-gemini}" in
      claude) record_claude_usage "$1" ;;
      *) record_gemini_usage "$1" ;;
    esac
}

# runEngine: the model-fallback loop for the selected agent engine — the
# one place engine invocation lives (design/multi-engine.md).
#   $1 (optional): output basename (e.g. plan-output.txt); when set, the
#      engine's final response is extracted next to the prompt file.
# Env: ENGINE (gemini|claude, default gemini); GEMINI_CONTINUE_SESSION
#   ("true" resumes the engine's latest session); SKIP_EMPTY_PROMPT
#   ("true": no-op when the prompt file is empty — iterate's contract).
function runEngine {
    local extract_to="${1:-}"
    local task_dir
    task_dir="$(dirname "${PROMPT_FILE}")"
    if [ "${SKIP_EMPTY_PROMPT:-false}" = "true" ] && [ ! -s "${PROMPT_FILE}" ]; then
        echo "No prompt provided, skipping engine execution."
        return 0
    fi
    echo "Running runEngine (${ENGINE:-gemini})..."

    if [ -n "$GITHUB_BOT_NAME" ]; then
        echo "Using bot identity for commits"
        export GIT_AUTHOR_NAME="$GITHUB_BOT_NAME"
        export GIT_AUTHOR_EMAIL="$GITHUB_BOT_EMAIL"
        export GIT_COMMITTER_NAME="$GITHUB_BOT_NAME"
        export GIT_COMMITTER_EMAIL="$GITHUB_BOT_EMAIL"
    fi

    # API keys must not leak into a trace (fix_issue.sh runs under set -x).
    local trace=0
    case "$-" in *x*) trace=1 ;; esac
    set +x

    local resume="${GEMINI_CONTINUE_SESSION:-false}"
    local out_json="" response_field=""
    MODELS_LIST="${MODELS:-__DEFAULT_MODELS__}"
    SUCCESS=false
    for MODEL in $MODELS_LIST; do
        echo "Trying model: $MODEL"
        case "${ENGINE:-gemini}" in
          claude)
            out_json="${task_dir}/claude-output.json"
            response_field="result"
            CLAUDE_ARGS=("-p" "--dangerously-skip-permissions" "--model" "$MODEL" "--output-format" "json")
            if [ "$resume" = "true" ]; then
                CLAUDE_ARGS+=("--continue")
            fi
            # Claude Code refuses --dangerously-skip-permissions as root
            # unless IS_SANDBOX=1 declares the disposable-container
            # context — which this pod is (same trust model as --yolo).
            if (cd "/workspaces/${REPO_NAME}" && export ANTHROPIC_API_KEY="${ANTHROPIC_API_KEY}" && export IS_SANDBOX=1 && claude "${CLAUDE_ARGS[@]}" < ${PROMPT_FILE} > "$out_json"); then
                SUCCESS=true
            fi
            ;;
          *)
            out_json="${task_dir}/gemini-output.json"
            response_field="response"
            GEMINI_ARGS=("--yolo" "--model" "$MODEL" "--output-format" "json")
            if [ "$resume" = "true" ]; then
                GEMINI_ARGS+=("--resume" "latest")
            fi
            if (cd "/workspaces/${REPO_NAME}" && export GEMINI_API_KEY="${GEMINI_API_KEY}" && gemini "${GEMINI_ARGS[@]}" < ${PROMPT_FILE} > "$out_json"); then
                SUCCESS=true
            fi
            ;;
        esac
        if [ "$SUCCESS" = true ]; then
            echo "Engine execution successful with model: $MODEL"
            record_engine_usage "$out_json"
            if [ -n "$extract_to" ]; then
                python3 -c '
import json, sys
try:
    with open(sys.argv[1]) as f:
        data = json.load(f)
        resp = data.get(sys.argv[2], "")
        if resp:
            print(resp)
        else:
            print(data)
except Exception:
    with open(sys.argv[1]) as f:
        print(f.read())
' "$out_json" "$response_field" > "${task_dir}/${extract_to}"
            fi
            break
        else
            echo "Engine execution failed with model: $MODEL. Retrying with next model..."
        fi
    done

    if [ "$trace" = 1 ]; then
        set -x
    fi
    if [ "$SUCCESS" = false ]; then
        echo "All models failed."
        exit 1
    fi
}
