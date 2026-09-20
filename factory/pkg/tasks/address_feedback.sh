#!/bin/bash
set -e
set -o pipefail


# It expects the following environment variables to be set:
# - GEMINI_API_KEY
# - GITHUB_USER_TOKEN
# - REPO_NAME
# - CLONE_URL
# - PROMPT_FILE
# - GITHUB_USER_ID
# - GITHUB_USER_EMAIL
# - GITHUB_USER_NAME
# - PR_NUMBER
# - MODELS
# - EXTENSIONS

export GITHUB_USER_TOKEN="${GITHUB_USER_TOKEN:-${GITHUB_TOKEN}}"
if [ -z "$GITHUB_USER_TOKEN" ]; then
    # Try other common names
    GITHUB_USER_TOKEN="${MANUAL_PAT:-${OAUTH_PAT}}"
fi

if [ -n "${GITHUB_BOT_LOGIN}" ]; then
    if [ -n "${GITHUB_BOT_TOKEN}" ] || [ -n "${GITHUB_BOT_OAUTH_PAT}" ] || [ -n "${GITHUB_BOT_MANUAL_PAT}" ]; then
        GITHUB_USER_TOKEN="${GITHUB_BOT_TOKEN:-${GITHUB_BOT_MANUAL_PAT:-${GITHUB_BOT_OAUTH_PAT}}}"
    fi
fi

function runGemini {
    echo "Running runGemini..."
    echo "running gemini in yolo mode"

    if [ -n "$GITHUB_BOT_NAME" ]; then
        echo "Using bot identity for commits"
        export GIT_AUTHOR_NAME="$GITHUB_BOT_NAME"
        export GIT_AUTHOR_EMAIL="$GITHUB_BOT_EMAIL"
        export GIT_COMMITTER_NAME="$GITHUB_BOT_NAME"
        export GIT_COMMITTER_EMAIL="$GITHUB_BOT_EMAIL"
    fi

    MODELS_LIST="${MODELS:-__DEFAULT_MODELS__}"
    SUCCESS=false
    for MODEL in $MODELS_LIST; do
        echo "Trying model: $MODEL"
        GEMINI_ARGS=("--yolo" "--model" "$MODEL" "--output-format" "json")
        if [ "$GEMINI_CONTINUE_SESSION" = "true" ]; then
            GEMINI_ARGS+=("--resume" "latest")
        fi
        if (cd "/workspaces/${REPO_NAME}" && export GEMINI_API_KEY="${GEMINI_API_KEY}" && gemini "${GEMINI_ARGS[@]}" < ${PROMPT_FILE} > "$(dirname "${PROMPT_FILE}")/gemini-output.json"); then
             echo "Gemini execution successful with model: $MODEL"
             record_gemini_usage "$(dirname "${PROMPT_FILE}")/gemini-output.json"
             SUCCESS=true
             break
        else
             echo "Gemini execution failed with model: $MODEL. Retrying with next model..."
        fi
    done
    
    if [ "$SUCCESS" = false ]; then
        echo "All models failed."
        exit 1
    fi
}

function commitAndPush {
    echo "Running commitAndPush..."
    pushd "/workspaces/${REPO_NAME}" > /dev/null
    
    NEW_HEAD=$(git rev-parse HEAD)

    # check if there are changes
    if [ -z "$(git status --porcelain)" ]; then 
        if [ "$OLD_HEAD" != "$NEW_HEAD" ]; then
            echo "HEAD has changed (committed or rebased by agent). Pushing changes..."
            git push --force origin HEAD
        else
            echo "No changes to commit."
        fi
    else
        echo "Changes detected in working directory, committing..."
        git add .
        git commit -m "Address review feedback: Apply changes"
        if [ "$OLD_HEAD" != "$NEW_HEAD" ]; then
            echo "HEAD has changed and working directory has changes. Pushing changes..."
            git push --force origin HEAD
        else
            git push origin HEAD
        fi
    fi
    popd > /dev/null
}

# Main execution
setupGit
setupGitRepos
# HACK: Avoid git lock issues
sleep 5
checkoutPRBranch
configureGemini
installExtensions
runGemini
commitAndPush

