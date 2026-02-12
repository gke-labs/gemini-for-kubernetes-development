#!/bin/bash
set -e
set -x

# It expects the following environment variables to be set:
# - GEMINI_API_KEY
# - GITHUB_USER_TOKEN

export REPO_NAME="{{ .Repo.Name }}"
export CLONE_URL={{ .Repo.CloneURL }}
export PROMPT_FILE="{{ .PromptFile }}"
export GITHUB_USER_ID={{ .User.UserID }}
export GITHUB_USER_EMAIL={{ .User.Email }}
export GITHUB_USER_NAME="{{ .User.Name }}"



function runGemini {
    # Only run gemini if a prompt was actually provided in env or prompt file is non-empty
    if [ -s "${PROMPT_FILE}" ]; then
        echo "Running runGemini..."
        echo "running gemini in yolo mode"

        if [ -n "$GITHUB_BOT_NAME" ]; then
            echo "Using bot identity for commits"
            export GIT_AUTHOR_NAME="$GITHUB_BOT_NAME"
            export GIT_AUTHOR_EMAIL="$GITHUB_BOT_EMAIL"
            export GIT_COMMITTER_NAME="$GITHUB_BOT_NAME"
            export GIT_COMMITTER_EMAIL="$GITHUB_BOT_EMAIL"
        fi

        pushd "/workspaces/${REPO_NAME}" > /dev/null
        export GEMINI_API_KEY="${GEMINI_API_KEY}"
        
        if ! gemini --yolo --model {{ .Model }} < ${PROMPT_FILE} 2> gemini_stderr.txt; then
            echo "Gemini failed with model {{ .Model }}. Stderr:"
            cat gemini_stderr.txt
            if grep -qE "quota|429" gemini_stderr.txt; then
                echo "Quota exhausted. Retrying with gemini-3-flash-preview..."
                gemini --yolo --model gemini-3-flash-preview < ${PROMPT_FILE}
            else
                exit 1
            fi
        else
            cat gemini_stderr.txt
        fi
        popd > /dev/null
    else
        echo "No prompt provided, skipping gemini execution."
    fi
}

function commitAndPush {
    echo "Running commitAndPush..."
    pushd "/workspaces/${REPO_NAME}" > /dev/null
    
    # check if there are changes
    if [ -z "$(git status --porcelain)" ]; then 
        echo "No changes to commit."
    else
        echo "Changes detected, committing..."
        git add .
        git commit -m "Agent iteration: Apply changes"
        git push origin HEAD
    fi
    popd > /dev/null
}

# Main execution
# Assumes repo is already cloned/ready in workspace by previous tasks or init
# HACK: Avoid git lock issues
sleep 5
runGemini
commitAndPush
