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
  
  gemini prompt "$PROMPT"
  
  echo "$(date): Cycle complete. Sleeping..."
  sleep ${POLL_INTERVAL:-300}
done
