#!/bin/bash
set -e
set -o pipefail

# Exploration task: build and maintain a personal understanding of a repo
# in the member's FORK, on the exploration/notes branch (docs-exploration/
# tree + the exploration skill, so interactive chat sessions inherit the
# same contract). The script owns the deterministic parts — branch, skill
# materialization, commit, push — the engine owns the understanding.
#
# It expects the following environment variables to be set:
# - GEMINI_API_KEY or ANTHROPIC_API_KEY (per ENGINE)
# - GITHUB_TOKEN
# - REPO_NAME
# - CLONE_URL
# - PROMPT_FILE
# - GITHUB_USER_ID / GITHUB_USER_EMAIL / GITHUB_USER_NAME
# - EXPLORE_KIND (onboard | activity | topic)
# - MODELS

export GITHUB_USER_TOKEN="${GITHUB_USER_TOKEN:-${GITHUB_TOKEN}}"

NOTES_BRANCH="exploration/notes"

function ensureNotesBranch {
    echo "Ensuring notes branch ${NOTES_BRANCH}..."
    pushd "/workspaces/${REPO_NAME}" > /dev/null
    # origin is the member's fork after `gh repo fork --remote` (the same
    # remote layout every fix push relies on).
    if git fetch origin "${NOTES_BRANCH}" 2>/dev/null; then
        git checkout -B "${NOTES_BRANCH}" "origin/${NOTES_BRANCH}"
    else
        git checkout -B "${NOTES_BRANCH}"
    fi
    mkdir -p docs-exploration
    popd > /dev/null
}

function materializeSkills {
    echo "Materializing the exploration skill into the checkout..."
    pushd "/workspaces/${REPO_NAME}" > /dev/null
    # One canonical, engine-neutral definition, living with the docs it
    # governs; the engine-specific discovery mechanisms point at it —
    # claude needs the .claude/skills/ layout (a copy), gemini imports
    # the canonical path from GEMINI.md, and the prompts reference the
    # canonical path directly (no per-engine templating).
    mkdir -p docs-exploration .claude/skills/exploration
    cp "$(dirname "${PROMPT_FILE}")/SKILL.md" docs-exploration/SKILL.md
    cp docs-exploration/SKILL.md .claude/skills/exploration/SKILL.md
    if ! grep -q "docs-exploration/SKILL.md" GEMINI.md 2>/dev/null; then
        printf '\n@./docs-exploration/SKILL.md\n' >> GEMINI.md
    fi
    popd > /dev/null
}

function commitAndPushNotes {
    echo "Committing and pushing exploration notes..."
    pushd "/workspaces/${REPO_NAME}" > /dev/null
    git add docs-exploration .claude/skills/exploration GEMINI.md 2>/dev/null || true
    if git commit -m "exploration(${EXPLORE_KIND}): notes update"; then
        git push origin "${NOTES_BRANCH}"
        echo "Notes pushed to origin/${NOTES_BRANCH}"
    else
        echo "No note changes to commit."
    fi
    popd > /dev/null
}

# Main execution
setupGit
setupGitRepos
# HACK: Avoid git lock issues
sleep 5
checkoutDefaultBranch
ensureNotesBranch
materializeSkills
configureGemini
runEngine
commitAndPushNotes
