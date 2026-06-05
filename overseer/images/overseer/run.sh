#!/bin/bash
set -e

function writeFactoryConfig {
    echo "$(date): Generating /workspaces/.factory.cfg..."
    
    CFG_FILE="/workspaces/.factory.cfg"
    rm -f "$CFG_FILE"
    touch "$CFG_FILE"
    
    if [ -n "$MAX_ACTIVE_REVIEWS" ]; then
        echo "maxActiveReviews: $MAX_ACTIVE_REVIEWS" >> "$CFG_FILE"
    fi
    if [ -n "$MAX_ACTIVE_ISSUES" ]; then
        echo "maxActiveIssues: $MAX_ACTIVE_ISSUES" >> "$CFG_FILE"
    fi
    if [ -n "$CHORES_MODE" ]; then
        echo "chores:" >> "$CFG_FILE"
        echo "  mode: $CHORES_MODE" >> "$CFG_FILE"
    fi
    if [ -n "$EPHEMERAL_STORAGE" ]; then
        echo "ephemeralStorage: $EPHEMERAL_STORAGE" >> "$CFG_FILE"
    fi
    if [ -n "$FACTORY_IMAGE" ]; then
        echo "image: $FACTORY_IMAGE" >> "$CFG_FILE"
    fi
    if [ -n "$WORKSPACE_DISK_SIZE" ]; then
        echo "workspaceDiskSize: $WORKSPACE_DISK_SIZE" >> "$CFG_FILE"
    fi
    
    if [ -n "$FACTORY_SECRETS" ]; then
        echo "secrets:" >> "$CFG_FILE"
        echo "$FACTORY_SECRETS" | jq -r '.[] | "  - name: \(.name)\n    mountPath: \(.mountPath)"' >> "$CFG_FILE"
    fi
    
    if [ -n "$FACTORY_ENV" ]; then
        echo "env:" >> "$CFG_FILE"
        echo "$FACTORY_ENV" | jq -r '.[] | "  - name: \(.name)\n    value: \(.value)"' >> "$CFG_FILE"
    fi
    
    export FACTORY_CONFIG="$CFG_FILE"
    echo "$(date): FACTORY_CONFIG set to $FACTORY_CONFIG"
}

function constructPrompt {
    if [ -d "/workspaces/prompt" ]; then
        echo "$(date): Constructing prompt from /workspaces/prompt templates into /workspaces/current_prompt.txt..."
        PROMPT_FILE="/workspaces/current_prompt.txt"
        rm -f "$PROMPT_FILE"
        cat /workspaces/prompt/01-header.txt >> "$PROMPT_FILE"
        if [ "$PR_MODE" != "disabled" ]; then
            cat /workspaces/prompt/03-pr-handling.txt >> "$PROMPT_FILE"
        fi
        if [ "$REVIEW_MODE" != "disabled" ]; then
            cat /workspaces/prompt/03a-pr-review-handling.txt >> "$PROMPT_FILE"
        fi
        if [ "$PR_MODE" != "disabled" ]; then
            cat /workspaces/prompt/06a-examples-prs.txt >> "$PROMPT_FILE"
        fi
        if [ "$REVIEW_MODE" != "disabled" ]; then
            cat /workspaces/prompt/06b-examples-prs-review.txt >> "$PROMPT_FILE"
        fi
        cat /workspaces/prompt/08-footer.txt >> "$PROMPT_FILE"
        PROMPT=$(cat "$PROMPT_FILE")
    else
        PROMPT="${AGENT_PROMPT:-You are the Overseer. Monitor the repository and orchestrate agents.}"
    fi
}

if [ -z "$REPO_URL" ]; then
  echo "REPO_URL environment variable is not set"
  exit 1
fi

function refreshLLMToken {
    if [ -n "$TOKENSCRIPT_DIR" ] && [ -d "$TOKENSCRIPT_DIR" ]; then
        for script in "$TOKENSCRIPT_DIR"/*; do
            if [ -f "$script" ]; then
                echo "Running tokenscript $script"
                SCRIPT_TOKEN=$("$script")
                if [ -n "$SCRIPT_TOKEN" ]; then
                    export GEMINI_API_KEY="$SCRIPT_TOKEN"
                    break
                fi
            fi
        done
    fi
}

function setupGit {
    echo "Running setupGit..."
    
    echo "creating gh wrapper script"
    cat <<'EOF' > /usr/local/bin/gh
#!/bin/bash
HTTPS_PROXY=http://github-portal.overseer-system.svc.cluster.local:80 SSL_CERT_FILE=/etc/github-portal/ca/tls.crt /usr/bin/gh "$@"
EOF
    chmod +x /usr/local/bin/gh

    # Map GITHUB_BOT_* env variables if set
    GITHUB_USER_ID="${GITHUB_BOT_LOGIN:-$GITHUB_USER_ID}"
    GITHUB_USER_NAME="${GITHUB_BOT_NAME:-$GITHUB_BOT_NAME}"
    GITHUB_USER_EMAIL="${GITHUB_BOT_EMAIL:-$GITHUB_BOT_EMAIL}"
    GITHUB_USER_TOKEN="${GITHUB_BOT_MANUAL_PAT:-${GITHUB_BOT_OAUTH_PAT:-${GITHUB_BOT_TOKEN:-${MANUAL_PAT:-${OAUTH_PAT:-$GITHUB_USER_TOKEN}}}}}"

    # Also ensure GITHUB_TOKEN is set for tools that specifically look for it
    if [ -n "$GITHUB_USER_TOKEN" ]; then
        export GITHUB_TOKEN="$GITHUB_USER_TOKEN"
    fi

    if [ -n "${GITHUB_USER_TOKEN}" ] && [ -n "${GITHUB_USER_ID}" ]; then
        echo "creating /root/.config/gh directory"
        mkdir -p /root/.config/gh

        echo "writing gh config"
        cat <<EOF > /root/.config/gh/hosts.yml
github.com:
    users:
        ${GITHUB_USER_ID}:
            oauth_token: ${GITHUB_USER_TOKEN}
    git_protocol: https
    oauth_token: ${GITHUB_USER_TOKEN}
    user: ${GITHUB_USER_ID}
EOF
    fi

    if [ -n "${GITHUB_USER_EMAIL}" ]; then
        echo "running git config user.email"
        git config --global user.email "${GITHUB_USER_EMAIL}"
    fi

    if [ -n "${GITHUB_USER_NAME}" ]; then
        echo "running git config user.name"
        git config --global user.name "${GITHUB_USER_NAME}"
    fi

    echo "running gh auth setup-git"
    gh auth setup-git

    echo "Configuring git sslCAInfo"
    git config --global http.sslCAInfo /etc/github-portal/ca/tls.crt

    echo "Configuring global git ignore"
    git config --global core.excludesfile /root/.gitignore_global
    cat <<EOF > /root/.gitignore_global
manager
bin/
EOF
}

# Setup git and gh
setupGit
writeFactoryConfig

# Clone the repo if it doesn't exist
# We are in /workspaces because of WORKDIR in Dockerfile
REPO_NAME=$(basename "$REPO_URL" .git)

if [ ! -d "$REPO_NAME" ]; then
  echo "Cloning $REPO_URL into /workspaces/$REPO_NAME..."
  gh repo clone "$REPO_URL" "$REPO_NAME"
fi

cd "$REPO_NAME"

echo "Ensuring fork is configured..."
gh repo fork --remote || true

if [ -d "/configdir" ] && [ "$(ls -A /configdir)" ]; then
  echo "Injecting configdir files into repository..."
  shopt -s dotglob
  cp -R /configdir/* .
  shopt -u dotglob
fi

function runGeminiOrchestrator {
    # Run Gemini LLM (Non-deterministic Scanner/Orchestrator)
    constructPrompt
    GEMINI_ERR=$(mktemp)
    if ! gemini --yolo "$PROMPT" 2> "$GEMINI_ERR"; then
      cat "$GEMINI_ERR" >&2
      if grep -iq "TerminalQuotaError\|Quota exceeded" "$GEMINI_ERR"; then
        echo "$(date): Gemini quota exhausted. Continuing to run queued tasks..."
      else
        echo "$(date): Gemini failed with non-quota error. Continuing to run queued tasks..."
      fi
    else
      echo "$(date): Gemini orchestration cycle complete."
    fi
    rm -f "$GEMINI_ERR"
}

function runWatchCycle {
    echo "$(date): Running deterministic watch cycle..."
    
    # 1. Parse repo owner/name from REPO_URL
    REPO_PATH=$(echo "$REPO_URL" | sed -E 's|https://github.com/([^/]+/[^/.]+)(\.git)?|\1|')
    
    # 2. Update main branch
    REMOTE_MAIN="origin"
    if git remote | grep -q "^upstream$"; then
        REMOTE_MAIN="upstream"
    fi
    git checkout main || git checkout -b main
    git fetch $REMOTE_MAIN
    git reset --hard $REMOTE_MAIN/main
    
    # 3. Switch to overseer branch and rebase onto main
    git checkout overseer || git checkout -b overseer
    git rebase main || {
        echo "$(date): Rebase failed. Resetting overseer branch to main..."
        git rebase --abort || true
        git reset --hard main
    }
    
    # 4. Run Watch Daemon for POLL_INTERVAL duration (default 300s/5m)
    TIMEOUT_DURATION=${POLL_INTERVAL:-300s}
    if [[ "$TIMEOUT_DURATION" =~ ^[0-9]+$ ]]; then
        TIMEOUT_DURATION="${TIMEOUT_DURATION}s"
    fi
    factory watch \
        --mode all \
        --watch-timeout "${TIMEOUT_DURATION}" \
        --queue-dir ./overseer/queues \
        --repo "$REPO_PATH"
        
    # 5. Run Gemini LLM (Non-deterministic Scanner/Orchestrator)
    if [ "${ALLOW_GEMINI_ORCHESTRATION}" = "true" ]; then
        runGeminiOrchestrator
    fi

    # 6. Push queue and state changes back to fork/origin
    if [ -d "./overseer/queues" ]; then
        git add ./overseer/queues
        if [ -f "./overseer/queues/journal.jsonl" ]; then
            git add ./overseer/queues/journal.jsonl
        fi
        if [ -f "./overseer/queues/chores_state.json" ]; then
            git add ./overseer/queues/chores_state.json
        fi
        
        if ! git diff --cached --quiet; then
            echo "$(date): Committing and pushing queue updates to overseer branch..."
            git commit -m "chore(watch): sync queue state at $(date -u +%Y-%m-%dT%H:%M:%SZ)"
            git push origin overseer --force
        else
            echo "$(date): No queue changes to push."
        fi
    fi
}

# Loop
# Create logs directory
mkdir -p /workspaces/logs

LAST_DAY=$(date +%F)
LAST_WEEK=$(date +%V)

while true; do
  CURRENT_DAY=$(date +%F)
  CURRENT_WEEK=$(date +%V)
  TIMESTAMP=$(date +%Y%m%d-%H%M%S)
  LOG_FILE="/workspaces/logs/run-$TIMESTAMP.log"

  # Daily Summary and Cleanup
  if [ "$CURRENT_DAY" != "$LAST_DAY" ]; then
    echo "$(date): Day changed from $LAST_DAY to $CURRENT_DAY. Running daily summary..."
    /workspaces/summarize.sh --daily "$LAST_DAY" || true
    
    echo "$(date): Cleaning up old logs..."
    find /workspaces/logs -type f -name "run-*.log" -mtime +15 -delete || true
    
    LAST_DAY="$CURRENT_DAY"
  fi

  # Weekly Summary
  if [ "$CURRENT_WEEK" != "$LAST_WEEK" ]; then
    echo "$(date): Week changed from $LAST_WEEK to $CURRENT_WEEK. Running weekly summary..."
    /workspaces/summarize.sh --weekly "$LAST_WEEK" || true
    LAST_WEEK="$CURRENT_WEEK"
  fi

  # Refresh LLM token
  refreshLLMToken

  {
    echo "$(date): Running Overseer cycle..."
    runWatchCycle
    echo "$(date): Cycle complete."
  } > "$LOG_FILE" 2>&1 || {
    EXIT_CODE=$?
    cat "$LOG_FILE"
    exit $EXIT_CODE
  }
  
  # Print log to stdout
  cat "$LOG_FILE"
  
  echo "$(date): Sleeping for 10 seconds before next cycle..."
  sleep 10
done
