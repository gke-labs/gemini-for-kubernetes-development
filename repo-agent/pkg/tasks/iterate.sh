#!/bin/bash
set -e
set -o pipefail
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



function setupGit {
    echo "Running setupGit..."
    # Config global user if not set
    if [ -n "$GITHUB_USER_EMAIL" ]; then
        git config --global user.email "$GITHUB_USER_EMAIL"
    fi
    if [ -n "$GITHUB_USER_NAME" ]; then
        git config --global user.name "$GITHUB_USER_NAME"
    fi

    echo "Configuring global git ignore"
    git config --global core.excludesfile /root/.gitignore_global
    cat <<EOF > /root/.gitignore_global
manager
bin/
EOF
}

function configureGemini {
    echo "Running configureGemini..."
    echo "creating /root/.gemini directory"
    mkdir -p /root/.gemini

    echo "writing gemini config"
    cat <<EOF > /root/.gemini/settings.json
{
  "general": {
    "enableAutoUpdate": false,
    "retryFetchErrors": true
  }
}
EOF
}

function installExtensions {
    echo "Installing extensions..."
    {{- range .Extensions }}
    gemini extensions install "{{ .Source }}" {{ if .Ref }}--ref "{{ .Ref }}"{{ end }} --consent
    {{- end }}
}

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

        MODELS=( {{ range .Models }}"{{ . }}" {{ end }} )
        SUCCESS=false
        for MODEL in "${MODELS[@]}"; do
            echo "Trying model: $MODEL"
            if (cd "/workspaces/${REPO_NAME}" && export GEMINI_API_KEY="${GEMINI_API_KEY}" && gemini --yolo --model "$MODEL" --output-format stream-json < ${PROMPT_FILE} | gemini-stream-processor --output "$(dirname "${PROMPT_FILE}")/gemini-output.json"); then
                echo "Gemini execution successful with model: $MODEL"
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
setupGit
configureGemini
installExtensions
runGemini
commitAndPush
