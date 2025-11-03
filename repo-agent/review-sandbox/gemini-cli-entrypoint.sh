#!/bin/bash

# if .gemini directory exists in /workspaces copy it to home directory
if [ -d /workspaces/.gemini ]; then
  echo ".gemini directory exists in /workspaces, copying to repo directory"
  # if desitation .gemini directory exists move it to .gemini.bak
  if [ -d .gemini ]; then
    echo ".gemini directory exists in repo directory, moving to .gemini.bak"
    mv .gemini .gemini.bak
  fi
  cp -R /workspaces/.gemini .gemini
else
  echo ".gemini directory does not exist in /workspaces"
fi

export GEMINI_API_KEY=`cat /tokens/gemini`
set -x
# protection against running gemini on an unpause
# if gemini-promt.txt dont run gemini else create and run it
if [ -f ../agent-prompt.txt ]; then
  echo "agent-prompt.txt exists, skipping gemini generation"
else
  echo "agent-prompt.txt does not exist, running gemini"
  echo "$AGENT_PROMPT" > ../agent-prompt.txt
  gemini -y  -p  "$AGENT_PROMPT" > ../agent-output.txt || true
fi
#/usr/local/bin/code-server-entrypoint
/usr/bin/code-server --auth=none --bind-addr=0.0.0.0:13337