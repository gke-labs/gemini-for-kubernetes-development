#!/bin/bash
set -e
echo "$(date): Bootstrapping workspaces directory..."
mkdir -p /workspaces/prompt
cp -rf /app/prompt/* /workspaces/prompt/
cp -f /app/summarize.sh /workspaces/summarize.sh
cp -f /app/run.sh /workspaces/run.sh
chmod +x /workspaces/run.sh /workspaces/summarize.sh

echo "$(date): Bootstrap complete. Executing /workspaces/run.sh..."
exec /workspaces/run.sh "$@"
