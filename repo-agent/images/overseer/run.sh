#!/bin/bash
set -e

# Default prompt from file
if [ -f "/system_prompt.txt" ]; then
    PROMPT=$(cat /system_prompt.txt)
else
    PROMPT="${AGENT_PROMPT:-You are the Overseer. Monitor the repository and orchestrate agents.}"
fi

# Loop
while true; do
  echo "$(date): Running Overseer cycle..."
  
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
