#!/bin/bash
set -e
echo "$(date): Bootstrapping workspaces directory..."
mkdir -p /workspaces/prompt
cp -rf /app/prompt/* /workspaces/prompt/
cp -f /app/summarize.sh /workspaces/summarize.sh
cp -f /app/run.sh /workspaces/run.sh
chmod +x /workspaces/run.sh /workspaces/summarize.sh

# Clean up any lingering DO NOT PROCESS / drain marker files from previous instances before starting
rm -f /workspaces/.do_not_process /workspaces/do_not_process /workspaces/.drain /workspaces/drain /workspaces/queues/.do_not_process /workspaces/queues/do_not_process /workspaces/queues/.drain /workspaces/queues/drain || true

echo "$(date): Bootstrap complete. Executing /workspaces/run.sh..."
exec /workspaces/run.sh "$@"
