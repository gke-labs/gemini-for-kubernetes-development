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
while true; do
  echo "$(date): Running Overseer cycle..."
  
  # Refresh LLM token
  refreshLLMToken

  # Update the repo
  git pull

  # Reconcile chores if enabled
  if [ "$CHORES_MODE" != "disabled" ]; then
    echo "$(date): running overseer-cli reconcile ..."
    overseer-cli reconcile
  fi

  # Run gemini
  # We assume gemini is in PATH
  # We use --prompt to pass the instruction
  # We rely on environment variables for auth (GEMINI_API_KEY, GITHUB_TOKEN, etc.)
  
  # Note: If LLM_PROVIDER is set, we might need to adapt.
  # But for now we assume gemini-cli handles what it handles.
  
  # Capture stderr to a file so we can inspect it for quota errors
  GEMINI_ERR=$(mktemp)
  if ! gemini --yolo "$PROMPT" 2> "$GEMINI_ERR"; then
    cat "$GEMINI_ERR" >&2
    if grep -iq "TerminalQuotaError\|Quota exceeded" "$GEMINI_ERR"; then
      echo "$(date): Quota exhausted. Sleeping for 1 hour..."
      sleep 3600
    else
      echo "$(date): Gemini failed with non-quota error. Sleeping for normal interval..."
      sleep ${POLL_INTERVAL:-300}
    fi
  else
    echo "$(date): Cycle complete. Sleeping..."
    sleep ${POLL_INTERVAL:-300}
  fi
  rm -f "$GEMINI_ERR"
done
