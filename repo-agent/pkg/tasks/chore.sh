#!/bin/bash
set -e
set -x

export REPO_NAME="{{ .RepoName }}"
export PROMPT_FILE="{{ .PromptFile }}"

function runGemini {
    echo "running gemini in yolo mode"
    pushd "/workspaces/${REPO_NAME}" > /dev/null
    set +x
    export GEMINI_API_KEY="${GEMINI_API_KEY}"

    if gemini --yolo < ${PROMPT_FILE}; then
        echo "Gemini execution successful"
    else
        echo "Gemini execution failed"
        exit 1
    fi
    set -x
    popd > /dev/null
}

runGemini
