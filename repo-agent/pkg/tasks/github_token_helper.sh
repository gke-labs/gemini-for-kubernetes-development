#!/bin/bash

# Resolve GITHUB_USER_TOKEN based on priority:
# MANUAL_PAT > OAUTH_PAT > GITHUB_TOKEN
# We preserve GITHUB_USER_TOKEN if it's already set.
GITHUB_USER_TOKEN="${GITHUB_USER_TOKEN:-${MANUAL_PAT:-${OAUTH_PAT:-${GITHUB_TOKEN}}}}"

# If a bot is configured, it may override GITHUB_USER_TOKEN
if [ -n "${GITHUB_BOT_LOGIN}" ]; then
    if [ -n "${GITHUB_BOT_MANUAL_PAT}" ] || [ -n "${GITHUB_BOT_OAUTH_PAT}" ] || [ -n "${GITHUB_BOT_TOKEN}" ]; then
        GITHUB_USER_TOKEN="${GITHUB_BOT_MANUAL_PAT:-${GITHUB_BOT_OAUTH_PAT:-${GITHUB_BOT_TOKEN}}}"
    fi
fi

export GITHUB_USER_TOKEN

# Export GH_TOKEN for the gh CLI, but avoid overwriting with empty string
export GH_TOKEN="${GITHUB_USER_TOKEN:-${GH_TOKEN}}"
