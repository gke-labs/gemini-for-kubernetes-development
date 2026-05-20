#!/bin/bash
set -e

# Default prompt from files
if [ -d "/workspaces/prompt" ]; then
    PROMPT_FILE=$(mktemp)
    cat /workspaces/prompt/01-header.txt >> "$PROMPT_FILE"
    if [ "$ISSUE_MODE" != "disabled" ]; then
        cat /workspaces/prompt/02-issue-handling.txt >> "$PROMPT_FILE"
    fi
    if [ "$PR_MODE" != "disabled" ]; then
        cat /workspaces/prompt/03-pr-handling.txt >> "$PROMPT_FILE"
    fi
    if [ "$REVIEW_MODE" != "disabled" ]; then
        cat /workspaces/prompt/03a-pr-review-handling.txt >> "$PROMPT_FILE"
    fi
    if [ "$CHORES_MODE" != "disabled" ]; then
        cat /workspaces/prompt/04-chores.txt >> "$PROMPT_FILE"
    fi
    cat /workspaces/prompt/05-examples-header.txt >> "$PROMPT_FILE"
    if [ "$ISSUE_MODE" != "disabled" ]; then
        cat /workspaces/prompt/06-examples-issues.txt >> "$PROMPT_FILE"
    fi
    if [ "$PR_MODE" != "disabled" ]; then
        cat /workspaces/prompt/06a-examples-prs.txt >> "$PROMPT_FILE"
    fi
    if [ "$REVIEW_MODE" != "disabled" ]; then
        cat /workspaces/prompt/06b-examples-prs-review.txt >> "$PROMPT_FILE"
    fi
    if [ "$CHORES_MODE" != "disabled" ]; then
        cat /workspaces/prompt/07-examples-chores.txt >> "$PROMPT_FILE"
    fi
    cat /workspaces/prompt/08-footer.txt >> "$PROMPT_FILE"
    PROMPT=$(cat "$PROMPT_FILE")
    rm -f "$PROMPT_FILE"
else
    PROMPT="${AGENT_PROMPT:-You are the Overseer. Monitor the repository and orchestrate agents.}"
fi

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

    # Hierarchy: MANUAL_PAT > OAUTH_PAT > GITHUB_USER_TOKEN
    GITHUB_USER_TOKEN="${MANUAL_PAT:-${OAUTH_PAT:-$GITHUB_USER_TOKEN}}"

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

# Clone the repo if it doesn't exist
# We are in /workspaces because of WORKDIR in Dockerfile
REPO_NAME=$(basename "$REPO_URL" .git)

if [ ! -d "$REPO_NAME" ]; then
  echo "Cloning $REPO_URL into /workspaces/$REPO_NAME..."
  gh repo clone "$REPO_URL" "$REPO_NAME"
fi

cd "$REPO_NAME"

if [ -d "/configdir" ] && [ "$(ls -A /configdir)" ]; then
  echo "Injecting configdir files into repository..."
  shopt -s dotglob
  cp -R /configdir/* .
  shopt -u dotglob
fi

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

  SLEEP_TIME=${POLL_INTERVAL:-300}
  
  {
    echo "$(date): Running Overseer cycle..."
    
    # Refresh LLM token
    refreshLLMToken

    # Update the repo
    git pull || true

    # Reconcile chores if enabled
    if [ "$CHORES_MODE" != "disabled" ]; then
      echo "$(date): running overseer-cli admin chore reconcile ..."
      overseer-cli admin chore reconcile || true
    fi

    # Run gemini
    GEMINI_ERR=$(mktemp)
    if ! gemini --yolo "$PROMPT" 2> "$GEMINI_ERR"; then
      cat "$GEMINI_ERR" >&2
      if grep -iq "TerminalQuotaError\|Quota exceeded" "$GEMINI_ERR"; then
        echo "$(date): Quota exhausted. Setting sleep to 1 hour..."
        SLEEP_TIME=3600
      else
        echo "$(date): Gemini failed with non-quota error."
      fi
    else
      echo "$(date): Cycle complete."
    fi
    rm -f "$GEMINI_ERR"
  } > "$LOG_FILE" 2>&1 || {
    EXIT_CODE=$?
    cat "$LOG_FILE"
    exit $EXIT_CODE
  }
  
  # Print log to stdout
  cat "$LOG_FILE"
  
  echo "$(date): Sleeping for $SLEEP_TIME seconds..."
  sleep "$SLEEP_TIME"
done
