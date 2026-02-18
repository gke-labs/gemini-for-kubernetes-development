#!/bin/bash
set -e

# Default prompt from file
if [ -f "/workspaces/system_prompt.txt" ]; then
    PROMPT=$(cat /workspaces/system_prompt.txt)
else
    PROMPT="${AGENT_PROMPT:-You are the Overseer. Monitor the repository and orchestrate agents.}"
fi

if [ -z "$REPO_URL" ]; then
  echo "REPO_URL environment variable is not set"
  exit 1
fi

function setupGit {
    echo "Running setupGit..."
    
    # Hierarchy: MANUAL_PAT > OAUTH_PAT > GITHUB_TOKEN
    TOKEN="${MANUAL_PAT:-${OAUTH_PAT:-$GITHUB_TOKEN}}"
    
    # Use TOKEN if GITHUB_USER_TOKEN is not set
    GITHUB_USER_TOKEN="${GITHUB_USER_TOKEN:-$TOKEN}"

    # Also ensure GITHUB_TOKEN is set for tools that specifically look for it
    if [ -n "$TOKEN" ]; then
        export GITHUB_TOKEN="$TOKEN"
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

# Loop
while true; do
  echo "$(date): Running Overseer cycle..."
  
  # Update the repo
  git pull

  # Run gemini
  # We assume gemini is in PATH
  # We use --prompt to pass the instruction
  # We rely on environment variables for auth (GEMINI_API_KEY, GITHUB_TOKEN, etc.)
  
  # Note: If LLM_PROVIDER is set, we might need to adapt.
  # But for now we assume gemini-cli handles what it handles.
  
  gemini --yolo "$PROMPT"
  
  echo "$(date): Cycle complete. Sleeping..."
  sleep ${POLL_INTERVAL:-300}
done
