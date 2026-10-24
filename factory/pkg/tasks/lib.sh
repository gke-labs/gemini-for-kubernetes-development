#!/bin/bash
# lib.sh — shared task-script functions, prepended to every task script by
# getScriptWithDefaults. Definition order matters: a script that needs
# different behavior redefines the function after this prelude (bash: the
# last definition wins). Keep this file engine-agnostic where possible —
# it is the seam where the claude engine lands (design/multi-engine.md).
set -e
set -o pipefail

USER_HOME="${HOME:-/root}"
mkdir -p "${USER_HOME}"

# Go caches belong on the workspace PVC, not the container's ephemeral
# layer: the golang base image sets GOPATH=/go, so a big module graph
# (multi-GiB for cloud SDK monorepos) lands on ephemeral storage and
# evicts the pod. Redirecting also makes the cache survive across task
# runs — second builds are warm. Pinned to /workspaces explicitly:
# USER_HOME's /root fallback would silently put the caches back on
# ephemeral if HOME ever went missing. The cache paths are the sandbox
# env's (GoCachePath, GoModCachePath in pkg/sandbox/manifests.go), which
# recipes use too: a warm snapshot's caches are only warm if both agree.
GO_CACHE_HOME="${USER_HOME}"
if [ -d "/workspaces" ]; then
    GO_CACHE_HOME="/workspaces/.home"
    mkdir -p "${GO_CACHE_HOME}"
    export GOMODCACHE="/workspaces/.cache/mod"
    export GOCACHE="/workspaces/.cache/go-build"
else
    export GOMODCACHE="${GO_CACHE_HOME}/go/pkg/mod"
    export GOCACHE="${GO_CACHE_HOME}/.cache/go-build"
fi
export GOPATH="${GO_CACHE_HOME}/go"

# ensureDevNull recreates /dev/null when it is no longer the null device.
# gemini-cli runs the agent's git with GIT_CONFIG_GLOBAL=/dev/null, so an
# agent's `git config --global` writes /dev/null.lock and renames it over
# /dev/null. From then on every `2>/dev/null` appends to a regular file and
# git rejects it as a config file ("bad config line 1 in file /dev/null").
# The pod outlives the task, so the next task inherits it; repair it before
# anything else runs. mknod needs root, which the sandbox runs as.
function ensureDevNull {
    if [ -c /dev/null ]; then
        return 0
    fi
    echo "/dev/null is not the null device; recreating it"
    rm -f /dev/null && mknod -m 666 /dev/null c 1 3 || echo "Warning: could not recreate /dev/null" >&2
}
ensureDevNull

# removeGitHubURLRewrites <location> removes every url.<base>.insteadOf
# rewrite of https://github.com/ from one git config: --global, --local or
# -f <file>. The section names carry tokens, so it runs with xtrace off.
function removeGitHubURLRewrites {
    local trace=0
    case "$-" in *x*) trace=1 ;; esac
    set +x
    local key
    while read -r key; do
        [ -n "${key}" ] || continue
        git config "$@" --remove-section "${key%.insteadof}" 2>/dev/null || true
    done < <(git config "$@" --get-regexp '^url\..*\.insteadof$' '^https://github\.com/$' 2>/dev/null | cut -d' ' -f1)
    if [ "$trace" = 1 ]; then
        set -x
    fi
}

# setGitHubURLRewrite <user> makes <user>'s credentials (GITHUB_USER_TOKEN)
# the only global url.<base>.insteadOf rewrite of https://github.com/.
# Because the credentials are in the URL, git never asks a credential
# helper for them, so this overrides gh auth git-credential for github.com
# remotes; it isn't a fallback. The token is read from the environment,
# not passed as an argument: xtrace prints a call's arguments before the
# function gets to turn tracing off.
#
# HOME lives on the workspace PVC, and one sandbox runs tasks as different
# identities (review as the reviewer bot, iterate/address as the PR's
# coder bot). The token is part of the rewrite's section name, so a plain
# `git config --global url.<creds>.insteadOf` adds a section per identity
# instead of replacing the last one, and for equal-length matches git
# picks the first — pushes then go out as whoever ran first in that
# sandbox, and a coder bot's fork answers 403 "denied to <reviewer bot>"
# on every rebase from then on. So every existing rewrite of github.com is
# removed first, matched by value so that any rewrite that would shadow
# this one goes too; removing the whole section, not just the value, also
# takes the previous identity's token off the disk.
#
# With no token, no rewrite is written: a passwordless one can't
# authenticate and would hide a GH_TOKEN that gh's helper could use.
#
# The engine never sees this rewrite (engineGitConfigFile).
function setGitHubURLRewrite {
    # The section names carry tokens; keep them out of a set -x trace.
    local trace=0
    case "$-" in *x*) trace=1 ;; esac
    set +x

    local user="$1"

    removeGitHubURLRewrites --global

    if [ -n "${GITHUB_USER_TOKEN}" ]; then
        git config --global url."https://${user}:${GITHUB_USER_TOKEN}@github.com/".insteadOf "https://github.com/"
    else
        echo "GITHUB_USER_TOKEN is empty; not writing a github.com URL rewrite"
    fi

    if [ "$trace" = 1 ]; then
        set -x
    fi
}

function setupGit {
    echo "Running setupGit..."
    echo "creating ${USER_HOME}/.config/gh directory"
    mkdir -p "${USER_HOME}/.config/gh"

    local GH_USER="${GITHUB_USER_ID}"
    if [ -n "${GITHUB_BOT_LOGIN}" ]; then
        GH_USER="${GITHUB_BOT_LOGIN}"
    fi

    echo "writing gh config"
    cat <<EOF > "${USER_HOME}/.config/gh/hosts.yml"
github.com:
    users:
        ${GH_USER}:
            oauth_token: ${GITHUB_USER_TOKEN}
    git_protocol: https
    oauth_token: ${GITHUB_USER_TOKEN}
    user: ${GH_USER}
EOF

    echo "running git config user.email"
    if [ -n "$GITHUB_BOT_EMAIL" ]; then
        git config --global user.email "${GITHUB_BOT_EMAIL}"
    else
        git config --global user.email "${GITHUB_USER_EMAIL}"
    fi

    echo "running git config user.name"
    if [ -n "$GITHUB_BOT_NAME" ]; then
        git config --global user.name "${GITHUB_BOT_NAME}"
    else
        git config --global user.name "${GITHUB_USER_NAME}"
    fi

    echo "running gh auth setup-git"
    gh auth setup-git || true
    echo "configuring git url fallback"
    setGitHubURLRewrite "${GH_USER}"

    echo "Configuring global git ignore"
    git config --global core.excludesfile "${USER_HOME}/.gitignore_global"
    cat <<EOF > "${USER_HOME}/.gitignore_global"
manager
bin/
EOF

    echo "Disabling git hooks"
    git config --global core.hooksPath /dev/null

    echo "Sanitizing workspace (cleaning stale git locks)..."
    find /workspaces -maxdepth 4 -name "*.lock" -path "*/.git/*" -delete 2>/dev/null || true
}

# engineGitConfigFile writes the global git config the engine gets and prints
# its path: setupGit's, without the github.com rewrite. That rewrite carries
# the token in its section name, and agents print their git config when git
# misbehaves — one ran `git config --list` and put the token in the recorded
# trace (fix-k8s-config-connector-13652). Without it the engine's git
# authenticates through gh's credential helper, which setupGit configured
# too and which reads the token from ~/.config/gh/hosts.yml.
function engineGitConfigFile {
    # The script's own global config, not the /dev/null gemini sets for its
    # shells later. If the script itself runs without one there is nothing
    # to hand on.
    local global="${GIT_CONFIG_GLOBAL:-${USER_HOME}/.gitconfig}"
    local engine="${USER_HOME}/.config/factory/engine.gitconfig"
    if [ ! -f "${global}" ]; then
        return 1
    fi
    mkdir -p "$(dirname "${engine}")"
    cp "${global}" "${engine}"
    removeGitHubURLRewrites -f "${engine}"
    echo "${engine}"
}

# engineGitGlobal gives claude and agy the engine's global config.
function engineGitGlobal {
    local config
    config="$(engineGitConfigFile)" || return 0
    export GIT_CONFIG_GLOBAL="${config}"
}

# engineGitConfig does the same for gemini. gemini-cli (0.62+) runs every
# shell command with GIT_CONFIG_GLOBAL=/dev/null and its own overrides
# appended as GIT_CONFIG_KEY_n, so the identity and credentials setupGit
# wrote are gone: pushes fail with "could not read Username", and agents
# improvised by printing the environment and config, pushing with the token
# in the URL, and running `git config --global` (see ensureDevNull). gemini
# keeps the GIT_CONFIG_* it inherits and appends its overrides after them,
# so an include.path here brings the config back while its overrides
# (core.hooksPath, ...) still win.
#
# One of them, credential.helper="", empties the helper list the include
# brought, so gh's helper is named again in GIT_CONFIG_PARAMETERS — git's
# `-c`, read after every GIT_CONFIG_KEY_n and passed through by gemini.
# Another, diff.external="", breaks every plain `git diff`; engineGitShim
# takes that one out.
function engineGitConfig {
    local config gh
    config="$(engineGitConfigFile)" || return 0
    local n="${GIT_CONFIG_COUNT:-0}"
    export "GIT_CONFIG_KEY_${n}=include.path"
    export "GIT_CONFIG_VALUE_${n}=${config}"
    export GIT_CONFIG_COUNT=$((n + 1))
    if gh="$(command -v gh)"; then
        export GIT_CONFIG_PARAMETERS="${GIT_CONFIG_PARAMETERS:+${GIT_CONFIG_PARAMETERS} }'credential.https://github.com.helper'='!${gh} auth git-credential'"
    fi
    engineGitShim
}

# engineGitShim puts a git in front of the real one, for gemini's shells, that
# drops gemini's diff.external="" override. git runs an empty external diff
# command instead of ignoring it, so every plain `git diff` fails with
# "external diff died" — repository scripts that check `git diff` with it —
# and no config value switches the external diff back off (true makes every
# diff empty). Agents that hit it went on to print their environment and
# config looking for the cause.
function engineGitShim {
    local dir="${USER_HOME}/.config/factory/engine-bin"
    local real
    real="$(type -ap git | grep -v "^${dir}/" | head -n 1)"
    if [ -z "${real}" ]; then
        return 0
    fi
    mkdir -p "${dir}"
    cat > "${dir}/git" <<EOF
#!/bin/bash
# Written by factory (lib.sh engineGitShim): drops gemini-cli's empty
# diff.external override, then runs ${real}.
if [ -n "\${GIT_CONFIG_COUNT:-}" ]; then
    keys=() values=()
    for ((i = 0; i < GIT_CONFIG_COUNT; i++)); do
        k="GIT_CONFIG_KEY_\${i}" v="GIT_CONFIG_VALUE_\${i}"
        if [ "\${!k}" != diff.external ] || [ -n "\${!v}" ]; then
            keys+=("\${!k}") values+=("\${!v}")
        fi
        unset "\${k}" "\${v}"
    done
    for i in "\${!keys[@]}"; do
        export "GIT_CONFIG_KEY_\${i}=\${keys[i]}" "GIT_CONFIG_VALUE_\${i}=\${values[i]}"
    done
    export GIT_CONFIG_COUNT="\${#keys[@]}"
fi
exec "${real}" "\$@"
EOF
    chmod +x "${dir}/git"
    export PATH="${dir}:${PATH}"
}

# dropGitHubTokens takes the GitHub tokens out of the engine's environment.
# The engine prints whatever its commands print into a recorded trace, and
# agents have run `env` (k8s-config-connector#13619). Nothing in the engine
# needs them: git and gh both authenticate through ~/.config/gh/hosts.yml,
# written by setupGit (git through gh's credential helper, see
# engineGitConfigFile). The script keeps them for its own steps.
function dropGitHubTokens {
    unset GITHUB_TOKEN GH_TOKEN GITHUB_USER_TOKEN GITHUB_BOT_TOKEN GITHUB_BOT_MANUAL_PAT GITHUB_BOT_OAUTH_PAT MANUAL_PAT OAUTH_PAT
}

# forkRemote makes "origin" the task identity's fork, with the original
# repository as "upstream". A fork call that fails — GitHub answers 429 under
# load — used to be ignored, leaving origin pointing at upstream, and the
# agent then pushed its branch to the upstream repository. So the call is
# retried.
#
# A fork the organisation forbids — private repositories whose policy keeps
# forks out of personal accounts — answers 403 "due to a policy" every
# time, so that stops at once instead of after three minutes of retries.
#
# Returns 0 when origin is the fork, 2 when the organisation's policy
# refuses one, 1 when the fork could not be made for any other reason.
function forkRemote {
    local owner="${GITHUB_BOT_LOGIN:-${GITHUB_USER_ID}}"
    local attempt out
    for attempt in 1 2 3 4; do
        if originIsForkOf "${owner}"; then
            return 0
        fi
        resetForkRemotes
        echo "running gh repo fork --remote (attempt ${attempt})"
        if out="$(cd "/workspaces/${REPO_NAME}" && gh repo fork --remote 2>&1)" && originIsForkOf "${owner}"; then
            echo "${out}"
            return 0
        fi
        echo "${out}" >&2
        case "${out}" in
            *"due to a policy"*) return 2 ;;
        esac
        if [ "${attempt}" -lt 4 ]; then
            sleep $((attempt * 30))
        fi
    done
    return 1
}

# resetForkRemotes points origin back at the repository and removes
# upstream, before each fork call. gh repo fork --remote renames origin to
# upstream, so in a workspace an earlier task left with both - another
# identity's fork as origin (the pool picks a bot per run, and an agent's
# sandbox is reused), or a fork call that failed halfway - it fails every
# time with "remote upstream already exists". From the repository it makes
# both anew; a call that fails leaves origin at the repository, which
# ensureForkRemote refuses to push to. Without CLONE_URL it changes nothing.
function resetForkRemotes {
    [ -n "${CLONE_URL:-}" ] || return 0
    (
        cd "/workspaces/${REPO_NAME}" || exit 1
        git remote remove upstream 2>/dev/null || true
        if git remote get-url origin >/dev/null 2>&1; then
            git remote set-url origin "${CLONE_URL}"
        else
            git remote add origin "${CLONE_URL}"
        fi
    )
}

# ensureForkRemote is forkRemote for tasks that cannot do without the fork:
# the task stops if origin isn't the fork, rather than pushing to the
# upstream repository.
function ensureForkRemote {
    local owner="${GITHUB_BOT_LOGIN:-${GITHUB_USER_ID}}" rc=0
    forkRemote || rc=$?
    case "${rc}" in
        0) return 0 ;;
        2) echo "${REPO_OWNER}/${REPO_NAME} cannot be forked to ${owner}: the organisation's policy forbids it. This task pushes to ${owner}'s fork and never to the upstream repository, so it cannot run here." >&2 ;;
        *) echo "origin is not ${owner}'s fork of ${REPO_OWNER}/${REPO_NAME}; stopping rather than pushing to the upstream repository" >&2 ;;
    esac
    exit 1
}

# originIsForkOf <owner>: origin's URL names <owner>'s repository. GitHub
# owners are case-insensitive. The URL is read as configured: `git remote
# get-url` applies setGitHubURLRewrite's rewrite, which carries the token,
# and the tasks run under set -x.
function originIsForkOf {
    local owner url
    owner="$(printf '%s' "$1" | tr '[:upper:]' '[:lower:]')"
    url="$(cd "/workspaces/${REPO_NAME}" && git config --get remote.origin.url 2>/dev/null)" || return 1
    url="$(printf '%s' "${url}" | tr '[:upper:]' '[:lower:]')"
    case "${url}" in
        *github.com/"${owner}"/*|*github.com:"${owner}"/*) return 0 ;;
    esac
    return 1
}

# resetRepoGitConfig makes the task repository's own config safe to run
# unattended. The workspace is long-lived, so whatever an earlier task or
# agent set at repository level is still there, and repository config
# overrides setupGit's global config:
#   - hooks: tasks commit, rebase and push unattended, and a repository's
#     hooks (husky, pre-commit, lefthook) can block, prompt, rewrite or
#     reformat behind the agent's back. Installers write a repository-level
#     core.hooksPath, so it is pinned here as well as globally.
#   - commands git runs for us: an agent once left diff.external=false, which
#     makes every `git diff` fail; editors, pagers, fsmonitor and ssh commands
#     are the same kind of trap.
#   - credentials: a repository-level helper or github.com rewrite would
#     shadow the task identity's (setGitHubURLRewrite).
function resetRepoGitConfig {
    (
        cd "/workspaces/${REPO_NAME}"
        local key
        for key in diff.external core.editor sequence.editor core.pager core.fsmonitor core.sshCommand core.askPass credential.helper; do
            git config --local --unset-all "${key}" 2>/dev/null || true
        done
        removeGitHubURLRewrites --local
        git config core.hooksPath /dev/null
    )
}

function setupGitRepos {
    echo "Running setupGitRepos..."
    
    # Check if repo already exists and is a valid git repository
    if [ ! -d "/workspaces/${REPO_NAME}/.git" ]; then
        echo "repository does not exist or is invalid, cleaning up destination and cloning..."
        rm -rf "/workspaces/${REPO_NAME}"
        (cd /workspaces/ && git clone ${CLONE_URL})
    else
        echo "repository already exists, cleaning up previous git state..."
        (cd "/workspaces/${REPO_NAME}" && git rebase --abort 2>/dev/null || true)
        (cd "/workspaces/${REPO_NAME}" && git merge --abort 2>/dev/null || true)
        (cd "/workspaces/${REPO_NAME}" && git cherry-pick --abort 2>/dev/null || true)
        (cd "/workspaces/${REPO_NAME}" && git reset --hard HEAD && git clean -fd)
        # Optional: fetch latest changes
        (cd "/workspaces/${REPO_NAME}" && git fetch origin)
    fi

    echo "running gh repo fork"
    (cd "/workspaces/${REPO_NAME}" && gh repo fork --remote || true)

    echo "running gh repo set-default"
    (cd "/workspaces/${REPO_NAME}" && gh repo set-default "${CLONE_URL}" || true)

    resetRepoGitConfig
}

function checkoutPRBranch {
    echo "Running checkoutPRBranch..."
    echo "checking out PR #${PR_NUMBER}"
    (cd "/workspaces/${REPO_NAME}" && git rebase --abort 2>/dev/null || true)
    (cd "/workspaces/${REPO_NAME}" && git merge --abort 2>/dev/null || true)
    (cd "/workspaces/${REPO_NAME}" && git cherry-pick --abort 2>/dev/null || true)
    (cd "/workspaces/${REPO_NAME}" && git reset --hard HEAD && git clean -fd && /usr/bin/gh pr checkout ${PR_NUMBER} --force && (git reset --hard @{u} 2>/dev/null || git reset --hard FETCH_HEAD))
    OLD_HEAD=$(cd "/workspaces/${REPO_NAME}" && git rev-parse HEAD)
}

# checkoutDefaultBranch puts the checkout on the repository's latest default
# branch. After setupGitRepos forks, origin is the member's fork, whose
# default branch is only as new as their last sync — triage read days-old
# code from it — so the branch comes from upstream; a local-only run has
# no fork, and origin is the repository itself.
function checkoutDefaultBranch {
    echo "Running checkoutDefaultBranch..."
    (cd "/workspaces/${REPO_NAME}" && git rebase --abort 2>/dev/null || true)
    (cd "/workspaces/${REPO_NAME}" && git merge --abort 2>/dev/null || true)
    (cd "/workspaces/${REPO_NAME}" && git cherry-pick --abort 2>/dev/null || true)
    (cd "/workspaces/${REPO_NAME}" && SRC_REMOTE=upstream && { git remote get-url upstream >/dev/null 2>&1 || SRC_REMOTE=origin; } && BASE_BRANCH=$(gh repo view --json defaultBranchRef --jq .defaultBranchRef.name) && git reset --hard HEAD && git clean -fd && git checkout "${BASE_BRANCH}" && git fetch "${SRC_REMOTE}" "${BASE_BRANCH}" && git reset --hard "${SRC_REMOTE}/${BASE_BRANCH}")
}

# cloneRepo clones the repository, or fetches it if it is there, for a
# credentials: clone recipe (pkg/recipe): the GitHub token is in this
# step's environment and nowhere else. git is pointed at gh, which reads
# it from there, with -c for these commands only, so it is written into no
# config file and no remote URL; the empty helper before it drops any
# inherited one. Nothing here forks, sets a default repo or configures
# git, as setupGitRepos does: those leave the token's reach behind.
#
# For a PR (PR_NUMBER, PR_BASE) it also fetches the PR's head and base
# branch from CLONE_URL into refs/factory/pr/{head,base} and checks the
# head out, detached: the agent, which has no token for gh, reviews with
# `git diff refs/factory/pr/base...HEAD`, and the runner pins the result
# to refs/factory/pr/head.
function cloneRepo {
    echo "Running cloneRepo..."
    git_auth() { git -c credential.helper= -c 'credential.helper=!gh auth git-credential' "$@"; }
    if [ ! -d "/workspaces/${REPO_NAME}/.git" ]; then
        rm -rf "/workspaces/${REPO_NAME}"
        (cd /workspaces && git_auth clone "${CLONE_URL}")
    else
        (cd "/workspaces/${REPO_NAME}" && git_auth fetch origin)
    fi
    if [ -n "${PR_NUMBER}" ]; then
        (cd "/workspaces/${REPO_NAME}" &&
            git_auth fetch --force "${CLONE_URL}" "+refs/pull/${PR_NUMBER}/head:refs/factory/pr/head" "+refs/heads/${PR_BASE:?PR_BASE is not set}:refs/factory/pr/base" &&
            git reset -q --hard && git clean -fdq && git checkout -q --force --detach refs/factory/pr/head)
    fi
}

# setupForkRepo is setup-repo for a fix recipe (pkg/recipe/recipes/fix.yaml):
# the checkout, with "origin" the task
# identity's fork and "upstream" the repository. It stops if origin cannot
# be made the fork (ensureForkRemote): a fix never pushes to the
# repository.
function setupForkRepo {
    echo "Running setupForkRepo..."
    if [ ! -d "/workspaces/${REPO_NAME}/.git" ]; then
        rm -rf "/workspaces/${REPO_NAME}"
        (cd /workspaces && git clone "${CLONE_URL}")
    else
        (cd "/workspaces/${REPO_NAME}" && git rebase --abort 2>/dev/null || true)
        (cd "/workspaces/${REPO_NAME}" && git merge --abort 2>/dev/null || true)
        (cd "/workspaces/${REPO_NAME}" && git cherry-pick --abort 2>/dev/null || true)
        (cd "/workspaces/${REPO_NAME}" && git reset --hard HEAD && git clean -fd && git fetch origin)
    fi

    ensureForkRemote

    # An origin that was already the fork skipped gh repo fork, which is
    # what adds upstream; checkoutDefaultBranch branches from it.
    (
        cd "/workspaces/${REPO_NAME}"
        if git remote get-url upstream >/dev/null 2>&1; then
            git remote set-url upstream "${CLONE_URL}"
        else
            git remote add upstream "${CLONE_URL}"
        fi
    )
    (cd "/workspaces/${REPO_NAME}" && gh repo set-default "${CLONE_URL}" || true)

    resetRepoGitConfig

    (cd "/workspaces/${REPO_NAME}" && git config user.email "${GITHUB_USER_EMAIL}" && git config user.name "${GITHUB_USER_NAME}")
}

# pushToFork is a fix recipe's push step: it pushes HEAD to the branch the
# task started ($TASK_DIR/branch) on the token's login's fork of the
# repository, and nowhere else. The agent commits and never pushes; this
# is the only way its commits leave the sandbox.
#
#   - origin must be the login's repository, and GitHub must say it is a
#     fork of the repository. The push goes to that repository's URL, not
#     to the remote, so a pushurl left in the checkout cannot redirect it.
#   - It is a lease: against $TASK_DIR/lease, the head this branch was last
#     pushed at, when there is one; else the branch must not exist yet. A
#     commit someone else pushed in between is never overwritten.
#   - Hooks are off and the checkout's config is reset (resetRepoGitConfig).
#   - With no lease (a start), a HEAD still at the base ($TASK_DIR/base)
#     pushes nothing: the fix made no change. push.json then says so
#     (unchanged), and the Change must say why (its comment), which
#     open-pr posts on the issue; without one, the task fails.
#
# What it pushed goes to $TASK_DIR/push.json, which the runner puts on the
# task's Change (pkg/taskoutput).
# checkNothingHalfDone fails, saying why, if what pushToFork would push is
# half done: a rebase, merge, cherry-pick or revert the agent left in
# progress (HEAD is then partway through it), paths still unmerged, or
# lines the commits since $1 (the PR's head, or the base a start branched
# from) add that are conflict markers. ======= is left alone: on its own
# it is also a heading's underline.
function checkNothingHalfDone {
    local from="$1" op what markers failed=0
    for op in rebase-merge:rebase rebase-apply:rebase MERGE_HEAD:merge CHERRY_PICK_HEAD:cherry-pick REVERT_HEAD:revert; do
        what="${op#*:}"
        if [ -e "$(git rev-parse --git-path "${op%%:*}")" ]; then
            echo "a ${what} is still in progress: git ${what} --continue or --abort" >&2
            failed=1
        fi
    done
    if [ -n "$(git ls-files -u)" ]; then
        echo "paths are still unmerged:" >&2
        git ls-files -u | awk '{print $4}' | sort -u >&2
        failed=1
    fi
    if [ -n "${from}" ] && git rev-parse -q --verify "${from}^{commit}" >/dev/null; then
        markers="$(git diff -U0 --no-color --no-ext-diff "${from}" HEAD | awk '
            /^\+\+\+ / { file = substr($0, 7); next }
            /^\+(<<<<<<<|>>>>>>>)( |$)/ { print file ": " substr($0, 2) }')"
        if [ -n "${markers}" ]; then
            echo "the commits add conflict markers:" >&2
            printf '%s\n' "${markers}" >&2
            failed=1
        fi
    fi
    return "${failed}"
}

function pushToFork {
    echo "Running pushToFork..."
    local dir="${TASK_DIR:?TASK_DIR is not set}"
    local branch base head lease="" login url fork upstream parent
    if ! branch="$(cat "${dir}/branch" 2>/dev/null)" || [ -z "${branch}" ]; then
        echo "no branch recorded in ${dir}/branch; nothing to push to" >&2
        return 1
    fi
    base="$(cat "${dir}/base" 2>/dev/null || true)"
    if [ -f "${dir}/lease" ]; then
        lease="$(cat "${dir}/lease")"
    fi

    cd "/workspaces/${REPO_NAME}"
    if ! [[ "${branch}" =~ ^[A-Za-z0-9._/-]+$ ]] || ! git check-ref-format --branch "${branch}" >/dev/null 2>&1; then
        echo "the recorded branch ${branch} is not a branch name" >&2
        return 1
    fi
    resetRepoGitConfig
    head="$(git rev-parse --verify 'HEAD^{commit}')"
    if [ -z "${lease}" ] && [ "${head}" = "${base}" ]; then
        echo "the fix made no change: there is no commit on top of ${base}; nothing to push"
        printf '{"unchanged":true,"base":"%s","head":"%s"}\n' "${base}" "${head}" > "${dir}/push.json"
        return 0
    fi
    if [ -n "$(git status --porcelain)" ]; then
        echo "Warning: uncommitted changes in the checkout are not pushed" >&2
    fi
    if ! checkNothingHalfDone "${lease:-${base}}"; then
        echo "not pushing ${head}" >&2
        return 1
    fi

    login="$(GH_TOKEN="${GITHUB_USER_TOKEN}" gh api user --jq .login)"
    if [ -z "${login}" ] || ! originIsForkOf "${login}"; then
        echo "origin is not ${login}'s repository; a fix pushes to the member's fork only" >&2
        return 1
    fi
    url="$(git config --get remote.origin.url)"
    fork="${url#*github.com[/:]}"
    fork="${fork%/}"
    fork="${fork%.git}"
    upstream="${CLONE_URL#https://github.com/}"
    upstream="${upstream%.git}"
    # GitHub's names for both, so they compare as GitHub sees them.
    parent="$(GH_TOKEN="${GITHUB_USER_TOKEN}" gh api "repos/${fork}" --jq 'if .fork then .parent.full_name else "" end')"
    fork="$(GH_TOKEN="${GITHUB_USER_TOKEN}" gh api "repos/${fork}" --jq .full_name)"
    upstream="$(GH_TOKEN="${GITHUB_USER_TOKEN}" gh api "repos/${upstream}" --jq .full_name)"
    if [ -z "${parent}" ] || [ "$(printf '%s' "${parent}" | tr '[:upper:]' '[:lower:]')" != "$(printf '%s' "${upstream}" | tr '[:upper:]' '[:lower:]')" ]; then
        echo "${fork} is not a fork of ${upstream}; not pushing to it" >&2
        return 1
    fi

    echo "pushing ${head} to ${fork} ${branch}"
    git -c core.hooksPath=/dev/null push --force-with-lease="refs/heads/${branch}:${lease}" \
        "https://github.com/${fork}.git" "${head}:refs/heads/${branch}"

    printf '{"fork":"%s","branch":"%s","base":"%s","head":"%s"}\n' "${fork}" "${branch}" "${base}" "${head}" > "${dir}/push.json"
}

# The logs of the failed GitHub Actions jobs, for an agent that fixes CI.
# GitHub serves a job's log as soon as that job finishes, while
# `gh run view --log-failed` refuses until every job of its run has: the
# watch starts a task on the first failed check, when the run's other jobs
# are usually still running, and the agent got no log at all. `gh api`
# refuses to print a log with terminal escapes in it, so curl fetches
# them. The logs go to a directory of the task's, outside the checkout:
# nothing commits them, and the engine's file tools do not skip them as
# ignored (*.log often is).
#
# ciFailedJobsOfCommit REPO SHA prints "<job id> <job name>" for each
# failed job. A check run of GitHub Actions is a job: its id is the job's.
function ciFailedJobsOfCommit {
    gh api "repos/$1/commits/$2/check-runs?filter=latest&per_page=100" --paginate \
        --jq '.check_runs[] | select(.app.slug == "github-actions") | select(.conclusion == "failure" or .conclusion == "timed_out") | "\(.id) \(.name)"'
}

# ciLogExcerpt LOG prints the part of a job's log that says why it failed:
# the step that failed, whole if short, else its test failures (or errors)
# and its last lines.
function ciLogExcerpt {
    local log="$1" section
    section="$(awk '/##\[group\]Run /{buf=""} {buf=buf $0 "\n"} /##\[error\]/{printf "%s", buf; found=1; exit} END{if(!found) printf "%s", buf}' "${log}")"
    if [ "$(printf '%s\n' "${section}" | wc -l)" -le 150 ]; then
        printf '%s\n' "${section}"
        return
    fi
    # Go's test output: what a failed test printed comes before its
    # --- FAIL line. Error-ish words alone match every log line of a
    # chatty test, so they are only the fallback.
    # No reader here stops early: under pipefail, head or grep -q leaving
    # a pipe SIGPIPEs its writer (exit 141) once the log outgrows the pipe
    # buffer, failing the step; so grep reads a here-string, and sed reads
    # to the end. A grep that matches nothing is not a failure.
    if grep -q -e '--- FAIL' <<< "${section}"; then
        { grep -B25 -A2 -e '--- FAIL' <<< "${section}" || true; } | sed -n '1,200p'
    else
        { grep -E -B5 -A40 -e 'ERROR|Error:|FAIL|panic:' <<< "${section}" || true; } | sed -n '1,200p'
    fi
    echo "[…]"
    tail -n 10 <<< "${section}"
}

# fetchCIJobLogs REPO DIR reads "<job id> <job name>" lines, saves each
# job's log as DIR/<job id>.log, without colours and timestamps, and
# appends its excerpt to DIR/excerpts.md.
function fetchCIJobLogs {
    local repo="$1" dir="$2" id name log token trace=0
    mkdir -p "${dir}"
    : > "${dir}/excerpts.md"
    while read -r id name; do
        [ -n "${id}" ] || continue
        log="${dir}/${id}.log"
        echo "Downloading the log of job ${id} (${name})"
        # The token stays off the command line and out of xtrace.
        case "$-" in *x*) trace=1 ;; esac
        set +x
        token="$(gh auth token 2>/dev/null || true)"
        if printf 'header = "Authorization: Bearer %s"\n' "${token}" |
            curl -fsSL -K - -H "Accept: application/vnd.github+json" \
                "https://api.github.com/repos/${repo}/actions/jobs/${id}/logs" -o "${log}.raw"; then
            token=""
            [ "${trace}" = 1 ] && set -x
            sed -E $'s/\x1b\\[[0-9;]*[A-Za-z]//g; s/^[0-9T:.-]+Z //' "${log}.raw" > "${log}"
            rm -f "${log}.raw"
            {
                printf '### %s\n\nJob %s; its whole log: %s\n\n```\n' "${name}" "${id}" "${log}"
                ciLogExcerpt "${log}"
                printf '```\n\n'
            } >> "${dir}/excerpts.md"
        else
            token=""
            [ "${trace}" = 1 ] && set -x
            rm -f "${log}.raw"
            printf '### %s\n\nJob %s: its log could not be downloaded.\n\n' "${name}" "${id}" >> "${dir}/excerpts.md"
        fi
    done
}

# stepRepo prints owner/name of the task's repository (REPO_OWNER,
# REPO_NAME, which factory sets), or fails: a step that called GitHub
# about "/name" got nothing back and carried on as if there were nothing.
function stepRepo {
    if [ -z "${REPO_OWNER:-}" ] || [ -z "${REPO_NAME:-}" ]; then
        echo "REPO_OWNER and REPO_NAME are not both set; no repository to ask GitHub about" >&2
        return 1
    fi
    printf '%s/%s\n' "${REPO_OWNER}" "${REPO_NAME}"
}

# fetchPRCILogs (recipe step ci-logs) fetches the logs of the jobs that
# failed on the PR's head ($TASK_DIR/pr, $TASK_DIR/lease, which an earlier
# step writes) into $TASK_DIR/ci-logs and their excerpts into
# $TASK_DIR/ci-logs.md, for the ask: {{ file "ci-logs.md" }}.
function fetchPRCILogs {
    echo "Running fetchPRCILogs..."
    local dir="${TASK_DIR:?TASK_DIR is not set}" repo pr head jobs
    repo="$(stepRepo)" || return 1
    pr="$(cat "${dir}/pr" 2>/dev/null || true)"
    head="$(cat "${dir}/lease" 2>/dev/null || true)"
    if [ -z "${head}" ] && [ -n "${pr}" ]; then
        head="$(gh api "repos/${repo}/pulls/${pr}" --jq .head.sha)" || return 1
    fi
    if [ -z "${head}" ]; then
        echo "neither the PR's head nor its number is recorded in ${dir}" >&2
        return 1
    fi
    # Not piped straight in: a failed call must fail the step, not read as
    # "no job failed".
    jobs="$(ciFailedJobsOfCommit "${repo}" "${head}")" || return 1
    printf '%s\n' "${jobs}" | fetchCIJobLogs "${repo}" "${dir}/ci-logs"
    if [ -s "${dir}/ci-logs/excerpts.md" ]; then
        {
            printf 'The logs of the GitHub Actions jobs that failed on %s, whole in %s, outside the repository (read them with grep, sed -n, tail); what each says about why:\n\n' "${head}" "${dir}/ci-logs"
            cat "${dir}/ci-logs/excerpts.md"
        } > "${dir}/ci-logs.md"
    else
        printf 'No GitHub Actions job failed on %s. If other checks failed (Prow, …), `gh pr checks` links to their logs.\n' "${head}" > "${dir}/ci-logs.md"
    fi
}

# fetchPRComments (recipe step pr-comments) writes the PR's feedback
# ($TASK_DIR/pr) to $TASK_DIR/comments.md, for the ask: {{ file
# "comments.md" }}, trusted authors' only (renderTrustedComments). NEW
# marks what came after the head commit ($TASK_DIR/lease).
function fetchPRComments {
    echo "Running fetchPRComments..."
    local dir="${TASK_DIR:?TASK_DIR is not set}" repo pr head since="" raw
    repo="$(stepRepo)" || return 1
    pr="$(cat "${dir}/pr" 2>/dev/null || true)"
    if ! [[ "${pr}" =~ ^[0-9]+$ ]]; then
        echo "no PR number recorded in ${dir}/pr" >&2
        return 1
    fi
    head="$(cat "${dir}/lease" 2>/dev/null || true)"
    if [ -n "${head}" ]; then
        since="$(gh api "repos/${repo}/commits/${head}" --jq .commit.committer.date)" || return 1
    fi
    raw="${dir}/comments"
    mkdir -p "${raw}"
    # Each call must succeed: a failed one read as no comments, and the
    # agent answered nothing.
    gh api "repos/${repo}/pulls/${pr}/reviews" --paginate --jq '.[]' > "${raw}/reviews.jsonl" || return 1
    gh api "repos/${repo}/pulls/${pr}/comments" --paginate --jq '.[]' > "${raw}/review-comments.jsonl" || return 1
    gh api "repos/${repo}/issues/${pr}/comments" --paginate --jq '.[]' > "${raw}/comments.jsonl" || return 1
    renderTrustedComments "${raw}" "${since}" "The feedback on the pull request, from its reviewers, its author and you" > "${dir}/comments.md"
}

# fetchIssueComments (recipe step issue-comments) writes the comments on
# the issue ($INPUT_ISSUE_NUMBER) to $TASK_DIR/comments.md, for the ask:
# {{ file "comments.md" }}, trusted authors' only (renderTrustedComments),
# as the classic factory fix put them in its prompt.
function fetchIssueComments {
    echo "Running fetchIssueComments..."
    local dir="${TASK_DIR:?TASK_DIR is not set}" repo issue="${INPUT_ISSUE_NUMBER:-}" raw
    repo="$(stepRepo)" || return 1
    if ! [[ "${issue}" =~ ^[0-9]+$ ]]; then
        echo "no issue number (INPUT_ISSUE_NUMBER)" >&2
        return 1
    fi
    raw="${dir}/comments"
    mkdir -p "${raw}"
    gh api "repos/${repo}/issues/${issue}/comments" --paginate --jq '.[]' > "${raw}/comments.jsonl" || return 1
    renderTrustedComments "${raw}" "" "The comments on the issue, from the repository's maintainers and you" > "${dir}/comments.md"
}

# fetchKeyedComment (recipe step keyed-comment) writes the item's comment
# of key $WITH_KEY - the one a recipe's task-output.comment.key keeps,
# marked <!-- factory:comment key=<key> v=<n> --> - to $TASK_DIR/$WITH_TO,
# without its heading line, markers and session link, and its version to
# $TASK_DIR/$WITH_TO.version. Only a comment whose author is of a kind in
# $WITH_AUTHORS counts (comma-separated; the watch rules' author kinds,
# rules.AuthorKind: pool - one of $INPUT_POOL_LOGINS - else by GitHub's
# association owner, member - or one of $INPUT_TRUSTED_LOGINS -,
# collaborator, else external): anyone else's carrying the marker is not
# the item's. The newest such comment counts. None fails the step, unless
# $WITH_OPTIONAL is true: then the file is left empty.
function fetchKeyedComment {
    echo "Running fetchKeyedComment..."
    local dir="${TASK_DIR:?TASK_DIR is not set}" repo num="${INPUT_ISSUE_NUMBER:-${INPUT_PR_NUMBER:-}}" raw
    local key="${WITH_KEY:-}" to="${WITH_TO:-}" authors="${WITH_AUTHORS:-}"
    repo="$(stepRepo)" || return 1
    if ! [[ "${num}" =~ ^[0-9]+$ ]]; then
        echo "no issue or PR number (INPUT_ISSUE_NUMBER, INPUT_PR_NUMBER)" >&2
        return 1
    fi
    if ! [[ "${key}" =~ ^[a-z][a-z0-9-]*$ ]] || [[ -z "${to}" || "${to}" == */* ]] || [[ -z "${authors}" ]]; then
        echo "keyed-comment needs with: key (lowercase), to (a file name) and authors (author kinds)" >&2
        return 1
    fi
    raw="${dir}/keyed-comments-${key}.jsonl"
    gh api "repos/${repo}/issues/${num}/comments" --paginate --jq '.[]' > "${raw}" || return 1
    python3 - "${raw}" "${key}" "${authors}" "${INPUT_POOL_LOGINS:-}" "${INPUT_TRUSTED_LOGINS:-}" "${dir}/${to}" "${WITH_OPTIONAL:-}" <<'PY'
import json, re, sys

raw, key, authors, pool, trusted, out, optional = sys.argv[1:8]
kinds = {k.strip() for k in authors.split(",") if k.strip()}
unknown = kinds - {"pool", "owner", "member", "collaborator", "external"}
if unknown:
    sys.exit("keyed-comment: authors %s: not author kinds (pool, owner, member, collaborator, external)" % ", ".join(sorted(unknown)))
pool = {l.strip().lower() for l in pool.split(",") if l.strip()}
trusted = {l.strip().lower() for l in trusted.split(",") if l.strip()}
marker = re.compile(r"<!-- factory:comment key=%s v=(\d+) -->" % re.escape(key))

def kind(c):
    login = str((c.get("user") or {}).get("login", "")).lower()
    assoc = str(c.get("author_association", "")).upper()
    if login in pool:
        return "pool"
    if assoc == "OWNER":
        return "owner"
    if assoc == "MEMBER" or login in trusted:
        return "member"
    if assoc == "COLLABORATOR":
        return "collaborator"
    return "external"

found, version, left_out = None, "", 0
with open(raw) as f:
    for line in f:
        if not line.strip():
            continue
        c = json.loads(line)
        m = marker.findall(c.get("body") or "")
        if not m:
            continue
        if kind(c) not in kinds:
            left_out += 1
            continue
        if found is None or (c.get("created_at") or "") >= (found.get("created_at") or ""):
            found, version = c, m[-1]
if left_out:
    print("keyed-comment: %d comment(s) of key %s by authors not of %s left out" % (left_out, key, ", ".join(sorted(kinds))))
if found is None:
    open(out, "w").close()
    open(out + ".version", "w").close()
    if optional.lower() == "true":
        print("keyed-comment: no comment of key %s" % key)
        sys.exit(0)
    sys.exit("keyed-comment: no comment of key %s by %s" % (key, ", ".join(sorted(kinds))))
lines = (found.get("body") or "").split("\n")
if lines and lines[0].startswith("**"):
    lines = lines[1:]  # its heading: "**<title>** · v<n> · updated <time>"
keep = [l for l in lines if not l.startswith("<!-- factory:") and not l.startswith("<sub>🤖 [Agent session]")]
with open(out, "w") as f:
    f.write("\n".join(keep).strip() + "\n")
with open(out + ".version", "w") as f:
    f.write(version)
print("keyed-comment: %s v%s by @%s" % (key, version, (found.get("user") or {}).get("login", "")))
PY
}

# renderTrustedComments prints, as markdown, the reviews, review comments
# and comments in $1 (reviews.jsonl, review-comments.jsonl, comments.jsonl:
# GitHub's objects, one per line; a missing file is none) by trusted
# authors (github.IsTrustedAuthor): an OWNER, MEMBER or COLLABORATOR of
# the repository, a GitHub App bot, or one of $INPUT_TRUSTED_LOGINS
# (comma-separated; factory sets it to the factory config's trusted
# logins and the task's login, and a PR's author). Anyone else's are only
# counted: their text never reaches the agent. $2, a time, marks what came
# after it NEW; $3 starts the introduction.
function renderTrustedComments {
    python3 - "$1" "$2" "${INPUT_TRUSTED_LOGINS:-}" "$3" <<'PY'
import json, os, sys

raw, since, logins, intro = sys.argv[1], sys.argv[2], sys.argv[3], sys.argv[4]
trusted_logins = {l.strip().lower() for l in logins.split(",") if l.strip()}
trusted_associations = {"OWNER", "MEMBER", "COLLABORATOR"}

def load(name):
    path = os.path.join(raw, name)
    if not os.path.exists(path):
        return []  # an issue has no reviews
    with open(path) as f:
        return [json.loads(line) for line in f if line.strip()]

left_out = 0
def trusted(item):
    global left_out
    user = item.get("user") or {}
    ok = (str(item.get("author_association", "")).upper() in trusted_associations
          or str(user.get("type", "")).lower() == "bot"
          or str(user.get("login", "")).lower() in trusted_logins)
    if not ok:
        left_out += 1
    return ok

def heading(kind, item, when, extra=""):
    login = (item.get("user") or {}).get("login", "")
    new = " NEW" if since and when and when > since else ""
    return "### %s %d by @%s, %s%s%s" % (kind, item["id"], login, when or "", extra, new)

reviews = [r for r in load("reviews.jsonl") if trusted(r)]
review_comments = [c for c in load("review-comments.jsonl") if trusted(c)]
comments = [c for c in load("comments.jsonl") if trusted(c)]

out = []
for r in reviews:
    body = (r.get("body") or "").strip()
    if not body:
        continue  # its inline comments are below
    out.append(heading("review", r, r.get("submitted_at"), ", " + str(r.get("state", "")).lower()))
    out.append(body + "\n")
for c in review_comments:
    where = ", %s:%s" % (c.get("path", ""), c.get("line") or c.get("original_line") or "")
    if c.get("in_reply_to_id"):
        where += ", in reply to %d" % c["in_reply_to_id"]
    out.append(heading("review comment", c, c.get("created_at"), where))
    hunk = (c.get("diff_hunk") or "").splitlines()[-15:]
    if hunk:
        out.append("```diff\n" + "\n".join(hunk) + "\n```")
    out.append((c.get("body") or "").strip() + "\n")
for c in comments:
    out.append(heading("comment", c, c.get("created_at")))
    out.append((c.get("body") or "").strip() + "\n")

if since:
    intro += "; NEW marks what came after its head commit (%s)" % since
intro += "."
if left_out:
    intro += " %d comment(s) by accounts that are not trusted are left out; do not look for them." % left_out
print(intro)
print()
print("\n".join(out) if out else "There is none.")
PY
}

function configureGemini {
    echo "Running configureGemini..."
    echo "creating ${USER_HOME}/.gemini directory"
    mkdir -p "${USER_HOME}/.gemini"

    # sessionRetention is on by default, and its cleanup at every gemini
    # start deletes a resumed conversation: session/load leaves an empty
    # chat file under the same short id, the cleanup counts it corrupt and
    # unlinks every file with that id, the real one too, so the next
    # session/load finds "No previous sessions found for this project".
    # A sandbox's chats go with the sandbox anyway.
    #
    # A recipe's result is a file the agent writes in its task directory,
    # which is outside the checkout gemini works in: includeDirectories
    # lets its file tools write there.
    echo "writing gemini config"
    cat <<EOF > "${USER_HOME}/.gemini/settings.json"
{
  "general": {
    "enableAutoUpdate": false,
    "retryFetchErrors": true,
    "previewFeatures": true,
    "sessionRetention": {
      "enabled": false
    }
  },
  "context": {
    "includeDirectories": ["/workspaces/tasks"]
  }
}
EOF
}

function record_gemini_usage {
    local output_file="$1"
    local task_dir="$(dirname "${PROMPT_FILE}")"
    if [ -f "$output_file" ]; then
        python3 -c '
import json, os, sys

output_file = sys.argv[1]
task_dir = sys.argv[2]

try:
    with open(output_file, "r") as f:
        data = json.load(f)
except Exception:
    sys.exit(0)

stats = data.get("stats", {})
models = stats.get("models", {})
if not models and ("total_tokens" in stats or "total" in stats or "totalRequests" in stats):
    models = {data.get("model", "gemini-cli"): {
        "api": {"totalRequests": stats.get("totalRequests", stats.get("tool_calls", 0) + 1), "totalErrors": stats.get("totalErrors", 0), "totalLatencyMs": stats.get("totalLatencyMs", stats.get("duration_ms", 0))},
        "tokens": {"input": stats.get("input", stats.get("input_tokens", 0)), "output": stats.get("candidates", stats.get("output", stats.get("output_tokens", 0))), "total": stats.get("total", stats.get("total_tokens", 0)), "cached": stats.get("cached", 0), "thoughts": stats.get("thoughts", 0)}
    }}

if not models:
    sys.exit(0)

usage_path = os.path.join(task_dir, "llm-usage.json")
token_path = os.path.join(task_dir, "token-usage.json")

existing = {"models": {}}
if os.path.exists(usage_path):
    try:
        with open(usage_path, "r") as f:
            existing = json.load(f)
    except Exception:
        pass
elif os.path.exists(token_path):
    try:
        with open(token_path, "r") as f:
            existing = json.load(f)
    except Exception:
        pass

for model_name, model_data in models.items():
    api = model_data.get("api", {})
    tokens = model_data.get("tokens", {})
    
    cur_model = existing.get("models", {}).get(model_name, {
        "api": {"totalRequests": 0, "totalErrors": 0, "totalLatencyMs": 0},
        "tokens": {"input": 0, "output": 0, "total": 0, "cached": 0, "thoughts": 0}
    })
    
    cur_model["api"]["totalRequests"] += api.get("totalRequests", api.get("total_requests", 1))
    cur_model["api"]["totalErrors"] += api.get("totalErrors", api.get("total_errors", 0))
    cur_model["api"]["totalLatencyMs"] += api.get("totalLatencyMs", api.get("total_latency_ms", 0))
    
    cur_model["tokens"]["input"] += tokens.get("input", 0)
    cur_model["tokens"]["output"] += tokens.get("candidates", tokens.get("output", 0))
    cur_model["tokens"]["total"] += tokens.get("total", 0)
    cur_model["tokens"]["cached"] += tokens.get("cached", 0)
    cur_model["tokens"]["thoughts"] += tokens.get("thoughts", 0)
    
    if "models" not in existing:
        existing["models"] = {}
    existing["models"][model_name] = cur_model

try:
    with open(usage_path, "w") as f:
        json.dump(existing, f, indent=2)
    with open(token_path, "w") as f:
        json.dump(existing, f, indent=2)
except Exception:
    pass

try:
    import glob as gb
    from datetime import datetime
    def parse_iso(ts):
        if not ts: return None
        try: return datetime.fromisoformat(ts.replace("Z", "+00:00"))
        except Exception: return None
    session_files = gb.glob("/root/.gemini/tmp/*/chats/session-*.jsonl") + gb.glob("/workspaces/.home/.gemini/tmp/*/chats/session-*.jsonl")
    if session_files:
        latest_session = max(session_files, key=os.path.getmtime)
        tool_metrics = {"total_tool_calls": 0, "total_tool_duration_sec": 0, "tools": {}, "shell_calls": []}
        pending = {}
        all_shell_calls = []
        with open(latest_session, "r", errors="ignore") as f:
            for line in f:
                try:
                    data = json.loads(line)
                    ts = parse_iso(data.get("timestamp"))
                    if not ts: continue
                    ts_str = data.get("timestamp", "")
                    if "toolCalls" in data and isinstance(data["toolCalls"], list):
                        for fc in data["toolCalls"]:
                            tname = fc.get("name", "unknown")
                            args = fc.get("args", {})
                            cmd = args.get("command", "") or args.get("CommandLine", "") or args.get("TargetFile", "") or str(args)
                            pending[fc.get("id")] = {"name": tname, "start": ts, "ts_str": ts_str, "cmd": str(cmd)}
                    c = data.get("content", "")
                    if isinstance(c, list):
                        for part in c:
                            if "functionResponse" in part:
                                fr = part["functionResponse"]
                                cid = fr.get("id")
                                if cid in pending:
                                    sinfo = pending.pop(cid)
                                    dur = round((ts - sinfo["start"]).total_seconds(), 3)
                                    tname = sinfo["name"]
                                    full_cmd = sinfo.get("cmd", "")
                                    trunc_cmd = full_cmd[:300] + ("..." if len(full_cmd) > 300 else "")
                                    tstat = tool_metrics["tools"].setdefault(tname, {"count": 0, "total_sec": 0, "max_sec": 0, "slowest_cmd": ""})
                                    tstat["count"] += 1
                                    tstat["total_sec"] = round(tstat["total_sec"] + dur, 3)
                                    if dur >= tstat["max_sec"]:
                                        tstat["max_sec"] = dur
                                        tstat["slowest_cmd"] = trunc_cmd[:120] + ("..." if len(trunc_cmd) > 120 else "")
                                    tool_metrics["total_tool_calls"] += 1
                                    tool_metrics["total_tool_duration_sec"] = round(tool_metrics["total_tool_duration_sec"] + dur, 3)
                                    if "shell" in tname or "command" in tname or tname in ("run_shell_command", "run_command", "run_shell_commands", "exec", "bash"):
                                        all_shell_calls.append({
                                            "tool": tname,
                                            "cmd": trunc_cmd,
                                            "duration_sec": dur,
                                            "timestamp": sinfo.get("ts_str", ts.isoformat())
                                        })
                except Exception:
                    pass
        all_shell_calls.sort(key=lambda x: x["duration_sec"], reverse=True)
        tool_metrics["shell_calls"] = all_shell_calls[:50]
        with open(os.path.join(task_dir, "tool-telemetry.json"), "w") as tf:
            json.dump(tool_metrics, tf, indent=2)
except Exception:
    pass
' "$output_file" "$task_dir"
    fi
}

# record_antigravity_usage: agy's -p JSON carries one usage block and no
# model name, so it is booked under the model asked for ($MODEL,
# "default" when agy chose), in the shape record_gemini_usage writes.
function record_antigravity_usage {
    local output_file="$1"
    local task_dir="$(dirname "${PROMPT_FILE}")"
    if [ -f "$output_file" ]; then
        python3 -c '
import json, os, sys

output_file, task_dir, model = sys.argv[1], sys.argv[2], sys.argv[3]
try:
    with open(output_file) as f:
        data = json.load(f)
except Exception:
    sys.exit(0)
u = data.get("usage")
if not u:
    sys.exit(0)

usage_path = os.path.join(task_dir, "llm-usage.json")
existing = {"models": {}}
for path in (usage_path, os.path.join(task_dir, "token-usage.json")):
    if os.path.exists(path):
        try:
            with open(path) as f:
                existing = json.load(f)
        except Exception:
            pass
        break

cur = existing.setdefault("models", {}).setdefault(model, {
    "api": {"totalRequests": 0, "totalErrors": 0, "totalLatencyMs": 0},
    "tokens": {"input": 0, "output": 0, "total": 0, "cached": 0, "thoughts": 0},
})
cur["api"]["totalRequests"] += data.get("num_turns", 1)
cur["api"]["totalErrors"] += 0 if data.get("status", "SUCCESS") == "SUCCESS" else 1
cur["api"]["totalLatencyMs"] += int(data.get("duration_seconds", 0) * 1000)
cur["tokens"]["input"] += u.get("input_tokens", 0)
cur["tokens"]["output"] += u.get("output_tokens", 0)
cur["tokens"]["total"] += u.get("total_tokens", u.get("input_tokens", 0) + u.get("output_tokens", 0))
cur["tokens"]["cached"] += u.get("cache_read_tokens", 0)
cur["tokens"]["thoughts"] = cur["tokens"].get("thoughts", 0) + u.get("thinking_tokens", 0)

for path in (usage_path, os.path.join(task_dir, "token-usage.json")):
    with open(path, "w") as f:
        json.dump(existing, f, indent=2)
' "$output_file" "$task_dir" "${MODEL:-default}" || echo "record_antigravity_usage failed (non-fatal)"
    fi
}

# ---- Runs -------------------------------------------------------------
#
# The run recipes' steps (recipe/recipes/run-plan.yaml, run-deploy.yaml,
# run-teardown.yaml): plan, deploy or tear down one run, in that run's
# own sandbox (factory/design/run-branches.md). A run owns everything it
# needs — runbook.md is the procedure, the scripts are generated from
# it, the receipts are the test results — on a branch of its own,
# runs/<run>, which holds those files and nothing else, at its root.
# Every run branch starts from runs/root, one empty commit (runsRoot).
#
# The run's branch is checked out as a worktree of the sandbox's clone,
# RUN_DIR (.factory-run, inside the checkout: an engine's file tools are
# confined to its workspace). The checkout itself stays on the code the
# phase runs: the default branch, or the commit a pull request run pins.
# The run's branch is always in the sandbox; with a fork it is pushed
# there too, after every phase, and without one (an organisation's
# policy keeps forks of its private repositories out of personal
# accounts) it is local-only: never pushed. See enterLocalOnly.
#
# These functions own the deterministic parts (branch, commit, push); the
# agent owns authoring, execution and judgment. The recipes run them as
# named steps, each a process of its own (recipe_step.sh): runs-remote
# (setupRunsRemote) before the checkout, run-prepare (runPrepare) after
# it, and run-finish (runFinish) after the ask, which reads what
# run-prepare left in $TASK_DIR (saveRunState).
#
# The step reads its with: phase (plan | deploy | teardown) and the
# recipe's inputs (INPUT_*), set below as the RUN_* names the functions
# use:
# - run (the run's identity, e.g. deploy-gke-k8s1)
# - runbook (run-plan, optional: the runbook this run starts from)
# - target_pr (run-plan, optional: the pull request to deploy)
# and the task's environment: GITHUB_TOKEN, REPO_NAME, CLONE_URL,
# GITHUB_USER_ID / GITHUB_USER_EMAIL / GITHUB_USER_NAME, TASK_DIR.
RUN_NAME="${RUN_NAME:-${INPUT_RUN:-}}"
RUN_MODE="${RUN_MODE:-${WITH_PHASE:-}}"
RUN_RUNBOOK="${RUN_RUNBOOK:-${INPUT_RUNBOOK:-}}"
RUN_TARGET_PR="${RUN_TARGET_PR:-${INPUT_TARGET_PR:-}}"

# RUN_REPO is the sandbox's clone; RUN_DIR, relative to it, the run's
# worktree; RUN_BRANCH, the run's branch; RUNS_ROOT_BRANCH, the commit
# every run branch starts from.
RUN_REPO="${RUN_REPO:-/workspaces/${REPO_NAME}}"
RUN_DIR=".factory-run"
RUN_BRANCH="runs/${RUN_NAME}"
RUNS_ROOT_BRANCH="runs/root"

# RUNS_LOCAL_ONLY is set when this run's records stay in the sandbox;
# see enterLocalOnly.
RUNS_LOCAL_ONLY=""

# LOCAL_ONLY_PUSH_URL is what origin pushes to in a local-only run:
# not a URL, so any push fails before it reaches the network.
LOCAL_ONLY_PUSH_URL="local-only-run:never-push"

# setupRunsRemote makes origin the member's fork, which is where runs
# are pushed and where the UI reads them. setupGitRepos ignores a
# failed fork, which left origin on the upstream repository: the push
# then failed with a 403 after the engine had done all its work — or,
# for a member with write access, would have put the run on the upstream
# repository.
function setupRunsRemote {
    local rc=0
    forkRemote || rc=$?
    case "${rc}" in
        0) return 0 ;;
        2) enterLocalOnly ;;
        *)
            echo "origin is not ${GITHUB_USER_ID}'s fork of ${REPO_OWNER}/${REPO_NAME}; stopping rather than pushing to the upstream repository" >&2
            exit 1
            ;;
    esac
}

# enterLocalOnly keeps the run's records in this sandbox when the
# repository cannot be forked. Cloning only needs read access, and a
# run's sandbox and its PVC outlive every phase — plan, deploy and
# teardown all run here — so the branch can live on the PVC instead of
# the fork. The cost is that the records are only as durable as the
# PVC: deleting the sandbox deletes the teardown script with it.
#
# origin is still the upstream repository, so its push URL is replaced
# with something that is not one: a push from these functions, or from
# the engine, fails instead of landing upstream. It is repository config,
# which persists on the PVC and which resetRepoGitConfig leaves alone.
# If a fork becomes possible later, `gh repo fork --remote` renames
# this remote to upstream and the guard goes with it.
function enterLocalOnly {
    RUNS_LOCAL_ONLY=1
    git -C "${RUN_REPO}" remote set-url --push origin "${LOCAL_ONLY_PUSH_URL}"
    echo "${REPO_OWNER}/${REPO_NAME} cannot be forked to ${GITHUB_USER_ID} (the organisation's policy forbids it)."
    echo "This run is local-only: its branch is committed in this sandbox and never pushed."
    echo "Deleting the sandbox deletes the run's records, its teardown script included."
}

# runsRoot is the commit every run branch starts from: empty, and the
# same in every sandbox — the empty tree, a fixed author, committer and
# date — so two sandboxes that create it at once create the same commit,
# and a fork's runs/root never conflicts. It is what keeps a pull request
# per run possible: GitHub opens none between branches with no common
# history.
function runsRoot {
    local tree
    tree="$(git -C "${RUN_REPO}" hash-object -t tree /dev/null)"
    GIT_AUTHOR_NAME=factory GIT_AUTHOR_EMAIL=factory@users.noreply.github.com GIT_AUTHOR_DATE="2026-01-01T00:00:00Z" \
    GIT_COMMITTER_NAME=factory GIT_COMMITTER_EMAIL=factory@users.noreply.github.com GIT_COMMITTER_DATE="2026-01-01T00:00:00Z" \
        git -C "${RUN_REPO}" -c commit.gpgsign=false commit-tree "${tree}" -m "runs root" < /dev/null
}

# ensureRunBranch puts the run's branch in RUN_DIR: the fork's, when it
# has one, or the sandbox's own, or a new one from runs/root.
#
# The sandbox's copy may be ahead of the fork's — a phase whose push
# failed — and then it is kept, for this phase's push to carry; behind,
# it catches up. Diverged, it stops: one of the two would be lost.
#
# The worktree is made anew every phase: a phase killed mid-write leaves
# nothing for the next one to read but what was committed.
function ensureRunBranch {
    local remote=""
    pushd "${RUN_REPO}" > /dev/null
    git worktree remove --force "${RUN_DIR}" 2>/dev/null || true
    rm -rf "${RUN_DIR}"
    git worktree prune
    if [ -z "${RUNS_LOCAL_ONLY}" ] && git fetch -q origin "+refs/heads/${RUN_BRANCH}:refs/remotes/origin/${RUN_BRANCH}" 2>/dev/null; then
        remote="refs/remotes/origin/${RUN_BRANCH}"
    fi
    if git show-ref --verify --quiet "refs/heads/${RUN_BRANCH}"; then
        if [ -n "${remote}" ]; then
            if git merge-base --is-ancestor "refs/heads/${RUN_BRANCH}" "${remote}"; then
                git update-ref "refs/heads/${RUN_BRANCH}" "${remote}"
            elif git merge-base --is-ancestor "${remote}" "refs/heads/${RUN_BRANCH}"; then
                echo "${RUN_BRANCH} has commits the fork does not yet; this phase pushes them."
            else
                echo "ERROR: ${RUN_BRANCH} in this sandbox and on the fork have diverged; not choosing one." >&2
                exit 1
            fi
        fi
    elif [ -n "${remote}" ]; then
        git branch -q "${RUN_BRANCH}" "${remote}"
    else
        git show-ref --verify --quiet "refs/heads/${RUNS_ROOT_BRANCH}" || git update-ref "refs/heads/${RUNS_ROOT_BRANCH}" "$(runsRoot)"
        git branch -q "${RUN_BRANCH}" "${RUNS_ROOT_BRANCH}"
    fi
    git worktree add -q "${RUN_DIR}" "${RUN_BRANCH}"
    popd > /dev/null
}

# RUNBOOK_REF is the commit a repository runbook is read at: the default
# branch, as checkout-default-branch left the checkout. Read from the
# commit, not the worktree.
RUNBOOK_REF=""

# RUNBOOK_FALLBACK_REF is where a runbook is read when RUNBOOK_REF does
# not have it: the default branch, once a pull request target has taken
# RUNBOOK_REF over. A pull request opened before a runbook merged does
# not carry it, and the runbooks offered are the default branch's.
RUNBOOK_FALLBACK_REF=""

# RUNBOOK_ORIGIN says where instantiateRunbook found the runbook, for
# the commit that records the plan.
RUNBOOK_ORIGIN=""

# TARGET_FILE is where a run pins the pull request it deploys; see
# resolveTarget.
TARGET_FILE="target.env"

# copyRunFiles copies the files under path (or, with an empty path,
# every file) at ref into dir, keeping the executable bit, except
# receipts — the test results of the run they came from, not of this one
# — and target.env, which pins the pull request another run deploys.
# Reports whether there was anything there.
function copyRunFiles {
    local ref="$1" path="$2" dir="$3" mode type sha f rel found=1
    while IFS=$'\t' read -r meta f; do
        read -r mode type sha <<< "${meta}"
        [ "${type}" = blob ] || continue
        found=0
        rel="${f#"${path:+${path}/}"}"
        case "${rel##*/}" in receipt-*) continue ;; esac
        [ "${rel}" != "${TARGET_FILE}" ] || continue
        mkdir -p "$(dirname "${dir}/${rel}")"
        git -C "${RUN_REPO}" cat-file blob "${sha}" > "${dir}/${rel}"
        [ "${mode}" != 100755 ] || chmod +x "${dir}/${rel}"
    done < <(git -C "${RUN_REPO}" ls-tree -r "${ref}" -- ${path:+"${path}/"} 2>/dev/null)
    return "${found}"
}

# resolveRunbook finds RUN_RUNBOOK and copies it into $1. It looks in
# the repository's .agents/runbooks/ first, where a runbook is reviewed
# like code — at RUNBOOK_REF, then RUNBOOK_FALLBACK_REF — then among the
# member's own runs: any run's branch is a runbook.
#
# A runbook has no required shape. It is the plan's starting point, not
# something these functions execute: prose, scripts, a run's full set of
# files — the engine makes this run's files out of whatever is there.
#
# Prints where it came from; prints nothing and copies nothing when
# there is no such runbook.
function resolveRunbook {
    local out="$1" repoPath=".agents/runbooks/${RUN_RUNBOOK}" ref other="runs/${RUN_RUNBOOK}"
    for ref in "${RUNBOOK_REF}" "${RUNBOOK_FALLBACK_REF}"; do
        if [ -n "${ref}" ] && copyRunFiles "${ref}" "${repoPath}" "${out}"; then
            echo "${repoPath} at $(git -C "${RUN_REPO}" rev-parse --short "${ref}")"
            return 0
        fi
    done
    if [ -z "${RUNS_LOCAL_ONLY}" ]; then
        git -C "${RUN_REPO}" fetch -q origin "+refs/heads/${other}:refs/remotes/origin/${other}" 2>/dev/null || true
    fi
    for ref in "refs/remotes/origin/${other}" "refs/heads/${other}"; do
        if git -C "${RUN_REPO}" rev-parse -q --verify "${ref}" > /dev/null && copyRunFiles "${ref}" "" "${out}"; then
            echo "run ${RUN_RUNBOOK} (${other})"
            return 0
        fi
    done
    return 0
}

# instantiateRunbook starts this run from RUN_RUNBOOK by copying it into
# RUN_DIR. That is all it does: the plan that follows makes the copy
# this run's — its resource prefix, project and region, and whatever the
# owner asked to change — and verifies it against this run's
# environment. Nothing here rewrites a file; a text substitution is not
# something to trust with what a teardown will delete.
function instantiateRunbook {
    local dst="${RUN_REPO}/${RUN_DIR}" stage
    if [ -e "${dst}/runbook.md" ]; then
        echo "ERROR: run ${RUN_NAME} already has a runbook.md; --runbook starts a new run." >&2
        echo "       Re-plan it to change it, or pick a new name." >&2
        exit 1
    fi
    if [ "${RUN_RUNBOOK}" = "${RUN_NAME}" ]; then
        echo "ERROR: --runbook ${RUN_RUNBOOK} names this run itself; a run is started from a different one." >&2
        exit 1
    fi
    stage="$(mktemp -d)"
    RUNBOOK_ORIGIN="$(resolveRunbook "${stage}")"
    if [ -z "${RUNBOOK_ORIGIN}" ]; then
        echo "ERROR: no runbook named '${RUN_RUNBOOK}': looked in .agents/runbooks/ and among your runs." >&2
        exit 1
    fi
    echo "Starting ${RUN_NAME} from ${RUNBOOK_ORIGIN}..."
    cp -R "${stage}/." "${dst}/"
    rm -rf "${stage}"
}

# TARGET_PR / TARGET_SHA are the pull request this run deploys instead
# of the default branch, and the commit of it the plan was made
# against. Empty for a run of the default branch.
TARGET_PR=""
TARGET_SHA=""

# resolveTarget decides what code this invocation executes. `plan
# --target N` pins pull request N's head commit in the run's
# target.env; every later invocation — deploy, teardown, a re-plan
# without --target — reads the pin back, so what executes is what the
# owner reviewed, however far the pull request has moved since. A
# re-plan with --target re-pins to the new head. The checkout is put
# on the pinned commit, detached: the run's files are in a worktree of
# their own, so nothing has to be laid over them or taken back off.
#
# No target.env and no --target: the default branch, as always.
function resolveTarget {
    local pin="${RUN_REPO}/${RUN_DIR}/${TARGET_FILE}" src=upstream
    git -C "${RUN_REPO}" remote get-url upstream > /dev/null 2>&1 || src=origin
    if [ "${RUN_MODE}" = "plan" ] && [ -n "${RUN_TARGET_PR:-}" ]; then
        if ! [[ "${RUN_TARGET_PR}" =~ ^[0-9]+$ ]]; then
            echo "ERROR: target_pr '${RUN_TARGET_PR}' is not a pull request number." >&2
            exit 1
        fi
        if ! git -C "${RUN_REPO}" fetch -q "${src}" "pull/${RUN_TARGET_PR}/head"; then
            echo "ERROR: could not fetch pull request #${RUN_TARGET_PR} from ${src}." >&2
            exit 1
        fi
        TARGET_PR="${RUN_TARGET_PR}"
        TARGET_SHA="$(git -C "${RUN_REPO}" rev-parse FETCH_HEAD)"
        printf 'TARGET_PR=%s\nTARGET_SHA=%s\n' "${TARGET_PR}" "${TARGET_SHA}" > "${pin}"
    elif [ -f "${pin}" ]; then
        # Read, never sourced: it is a file on a branch, and all it may
        # say is a number and a commit.
        TARGET_PR="$(sed -n 's/^TARGET_PR=//p' "${pin}")"
        TARGET_SHA="$(sed -n 's/^TARGET_SHA=//p' "${pin}")"
        if ! [[ "${TARGET_PR}" =~ ^[0-9]+$ && "${TARGET_SHA}" =~ ^[0-9a-f]{40}$ ]]; then
            echo "ERROR: ${TARGET_FILE} does not pin a pull request and a commit." >&2
            exit 1
        fi
        # The pinned commit is usually here already. A force-push can
        # take it out of the pull request, so ask for it by name too.
        if ! git -C "${RUN_REPO}" cat-file -e "${TARGET_SHA}^{commit}" 2>/dev/null; then
            git -C "${RUN_REPO}" fetch -q "${src}" "pull/${TARGET_PR}/head" 2>/dev/null || true
            git -C "${RUN_REPO}" fetch -q "${src}" "${TARGET_SHA}" 2>/dev/null || true
        fi
        if ! git -C "${RUN_REPO}" cat-file -e "${TARGET_SHA}^{commit}" 2>/dev/null; then
            echo "ERROR: commit ${TARGET_SHA} of pull request #${TARGET_PR} is no longer available." >&2
            echo "       Re-plan with --target ${TARGET_PR} to pin its current head." >&2
            exit 1
        fi
    else
        return 0
    fi
    # A runbook the pull request adds or changes is the one it is
    # deployed with; one it does not carry is read from the default
    # branch, where the runbooks on offer come from.
    RUNBOOK_FALLBACK_REF="${RUNBOOK_REF}"
    RUNBOOK_REF="${TARGET_SHA}"
    echo "Target: pull request #${TARGET_PR} at ${TARGET_SHA}"
    git -C "${RUN_REPO}" checkout -q --detach "${TARGET_SHA}"
}

# result.json is what this invocation concluded, in the task directory
# beside exit_code, for whoever launched it to read without GitHub and
# without the engine's own output format.
#
# These functions write it, never the engine, so it is the same file
# whichever engine ran. The one thing taken from the engine is the
# receipt's first line, which every run prompt already makes the
# verdict; a first word that is not a known verdict is recorded as
# "unknown", not guessed at. Written on every exit, failures included:
# a run that dies before its receipt is the one worth reading about.
RESULT_FILE=""
[ -n "${TASK_DIR:-}" ] && RESULT_FILE="${TASK_DIR}/result.json"
RESULT_VERDICTS=" PLANNED VERIFIED DEPLOYED-UNVERIFIED FAILED BLOCKED TORN-DOWN PARTIAL "
RESULT_RECEIPTS_BEFORE=""
RESULT_COMMIT=""
# Newline-separated paths this run changed or created in the checkout,
# outside its own worktree: not kept.
RESULT_DISCARDED=""

function addDiscarded {
    [ -n "$1" ] && RESULT_DISCARDED="${RESULT_DISCARDED:+${RESULT_DISCARDED}$'\n'}$1"
    return 0
}

# snapshotReceipts records the receipts already on the branch, so the
# one this invocation writes is the one that is new.
function snapshotReceipts {
    RESULT_RECEIPTS_BEFORE="$(ls "${RUN_REPO}/${RUN_DIR}" 2>/dev/null | grep '^receipt-' || true)"
}

function jsonString {
    local s="$1"
    s="${s//\\/\\\\}"
    s="${s//\"/\\\"}"
    s="${s//$'\t'/\\t}"
    s="${s//$'\r'/\\r}"
    s="${s//$'\n'/\\n}"
    printf '"%s"' "${s}"
}

function jsonOrNull {
    if [ -n "$1" ]; then jsonString "$1"; else printf 'null'; fi
}

function writeResult {
    local rc="$1" dir="${RUN_REPO}/${RUN_DIR}" receipt="" verdict="" name line word target="null" discarded="" first=1 p
    [ -n "${RESULT_FILE}" ] || return 0
    for name in $(ls "${dir}" 2>/dev/null | grep '^receipt-' | sort || true); do
        if ! printf '%s\n' "${RESULT_RECEIPTS_BEFORE}" | grep -qxF "${name}"; then
            receipt="${name}"
        fi
    done
    if [ -n "${receipt}" ]; then
        line="$(grep -m1 -v '^[[:space:]]*$' "${dir}/${receipt}" 2>/dev/null || true)"
        word="$(printf '%s' "${line}" | sed -E 's/^[^A-Za-z]*//; s/[^A-Z-].*$//')"
        verdict="unknown"
        if [ -n "${word}" ] && [[ "${RESULT_VERDICTS}" == *" ${word} "* ]]; then
            verdict="${word}"
        fi
    fi
    if [ -n "${TARGET_PR:-}" ]; then
        target="{\"pr\": ${TARGET_PR}, \"sha\": $(jsonString "${TARGET_SHA}")}"
    fi
    while IFS= read -r p; do
        [ -n "${p}" ] || continue
        [ "${first}" = 1 ] || discarded="${discarded}, "
        discarded="${discarded}$(jsonString "${p}")"
        first=0
    done <<< "$(printf '%s\n' "${RESULT_DISCARDED}" | sort -u)"
    {
        printf '{\n'
        printf '  "version": 2,\n'
        printf '  "run": %s,\n' "$(jsonString "${RUN_NAME}")"
        printf '  "mode": %s,\n' "$(jsonString "${RUN_MODE}")"
        printf '  "exitCode": %d,\n' "${rc}"
        printf '  "verdict": %s,\n' "$(jsonOrNull "${verdict}")"
        printf '  "receipt": %s,\n' "$(jsonOrNull "${receipt}")"
        printf '  "branch": %s,\n' "$(jsonString "${RUN_BRANCH}")"
        printf '  "localOnly": %s,\n' "$([ -n "${RUNS_LOCAL_ONLY}" ] && echo true || echo false)"
        printf '  "commit": %s,\n' "$(jsonOrNull "${RESULT_COMMIT}")"
        printf '  "target": %s,\n' "${target}"
        printf '  "discarded": [%s]\n' "${discarded}"
        printf '}\n'
    } > "${RESULT_FILE}.tmp" && mv "${RESULT_FILE}.tmp" "${RESULT_FILE}"
}

# commitAndPushRun commits everything in the run's worktree — scripts,
# prose and receipt together, in one commit, so a partial push cannot
# split the story — and pushes the run's branch, and runs/root, to the
# fork (not in a local-only run). Only this run writes its branch: a
# rejected push fails the phase, and the commit waits in the sandbox for
# the next phase to push.
#
# What the agent changed in the checkout itself is not the run's and is
# never committed; it is listed, so a fix that depended on it (a
# .gcloudignore at the repository root) does not go unrecorded.
function commitAndPushRun {
    local what="$1" changed
    echo "Committing ${what}..."
    changed="$(git -C "${RUN_REPO}" status --porcelain --untracked-files=all -- . ":(exclude)${RUN_DIR}" | cut -c4-)"
    if [ -n "${changed}" ]; then
        echo "Not kept (changed in the checkout, outside ${RUN_DIR}):"
        echo "${changed}" | sed "s/^/  /"
        addDiscarded "${changed}"
    fi
    git -C "${RUN_REPO}/${RUN_DIR}" add -A
    if ! git -C "${RUN_REPO}/${RUN_DIR}" commit -q -m "run(${RUN_NAME}): ${what}"; then
        echo "No changes to commit for ${what}."
    fi
    RESULT_COMMIT="$(git -C "${RUN_REPO}/${RUN_DIR}" rev-parse HEAD)"
    if [ -n "${RUNS_LOCAL_ONLY}" ]; then
        echo "Committed to ${RUN_BRANCH} in this sandbox; local-only, so nothing is pushed."
        return 0
    fi
    if ! git -C "${RUN_REPO}" push -q origin "${RUN_BRANCH}"; then
        echo "ERROR: could not push ${RUN_BRANCH} to the fork; the commit is kept in this sandbox for the next phase to push." >&2
        exit 1
    fi
    # runs/root too, when this sandbox made it: the fork has the commit
    # already (it is the run branch's first), and the branch names it.
    if git -C "${RUN_REPO}" show-ref --verify --quiet "refs/heads/${RUNS_ROOT_BRANCH}"; then
        git -C "${RUN_REPO}" push -q origin "${RUNS_ROOT_BRANCH}" 2>/dev/null || true
    fi
    echo "Pushed ${RUN_BRANCH} to the fork."
}

# Main execution: the recipes' run-prepare and run-finish steps.

# RUN_STATE is what runPrepare leaves runFinish, which runs in a process
# of its own after the ask: whether the run is local-only, the runbook it
# started from, the target it pinned and the receipts there were.
RUN_STATE_VARS="RUNS_LOCAL_ONLY RUNBOOK_ORIGIN TARGET_PR TARGET_SHA RESULT_RECEIPTS_BEFORE"

# Plain assignments, not declare -p's output: sourced inside
# loadRunState, declare would make each one local to it.
function saveRunState {
    local v
    for v in ${RUN_STATE_VARS}; do
        printf '%s=%q\n' "${v}" "${!v}"
    done > "${TASK_DIR:?TASK_DIR is not set}/run-state.sh"
}

function loadRunState {
    # shellcheck disable=SC1091
    source "${TASK_DIR:?TASK_DIR is not set}/run-state.sh"
}

# runPrepare is the run recipes' run-prepare step, after runs-remote and
# checkout-default-branch: the run's branch in its worktree, the target
# pinned and checked out, the receipts noted, and, for a plan from a
# runbook, the runbook copied in. Its result.json, written until
# runFinish writes the real one, says the phase never finished
# (exitCode -1): a failed ask ends the recipe before runFinish.
function runPrepare {
    set -e
    set -o pipefail
    # The token lib.sh's setupGit reads. Without it setupGit writes an
    # empty oauth_token into gh's hosts.yml, and gh then exits 1 for every
    # subcommand, a long way from the cause.
    if [ -z "${GITHUB_USER_TOKEN:-}" ]; then
        echo "No GitHub token: set GITHUB_TOKEN (or GITHUB_USER_TOKEN)." >&2
        exit 1
    fi
    case "${RUN_MODE}" in plan|deploy|teardown) ;; *) echo "phase ${RUN_MODE} is not plan, deploy or teardown" >&2; exit 1 ;; esac
    [[ "${RUN_NAME}" =~ ^[a-z0-9]([a-z0-9-]*[a-z0-9])?$ ]] && [ "${RUN_NAME}" != root ] || { echo "run '${RUN_NAME}' is not a run name" >&2; exit 1; }
    # runs-remote, a step of its own, made origin the fork or the run
    # local-only; which it was is in the checkout's config.
    if [ "$(git -C "${RUN_REPO}" remote get-url --push origin 2>/dev/null)" = "${LOCAL_ONLY_PUSH_URL}" ]; then
        RUNS_LOCAL_ONLY=1
    fi
    RUNBOOK_REF="$(git -C "${RUN_REPO}" rev-parse HEAD)"
    ensureRunBranch
    resolveTarget
    snapshotReceipts
    # From a runbook, the copy is what the agent plans from: it makes the
    # runbook this run's, and verifies it here, like any plan.
    if [ "${RUN_MODE}" = "plan" ] && [ -n "${RUN_RUNBOOK:-}" ]; then
        instantiateRunbook
    fi
    saveRunState
    writeResult -1
}

# runFinish is the run recipes' run-finish step, after the ask:
# everything the phase left in the run's worktree is committed and
# pushed (commitAndPushRun), and result.json says what it concluded.
function runFinish {
    set -e
    set -o pipefail
    loadRunState
    trap 'rc=$?; writeResult "${rc}" || echo "WARN: could not write result.json" >&2; exit "${rc}"' EXIT
    case "${RUN_MODE}" in
    plan)
        if [ -n "${RUN_RUNBOOK:-}" ]; then
            commitAndPushRun "plan from runbook ${RUNBOOK_ORIGIN}${TARGET_PR:+ for pull request #${TARGET_PR}} (nothing executed)"
        else
            commitAndPushRun "plan${TARGET_PR:+ for pull request #${TARGET_PR}} (runbook.md, scripts, PLANNED receipt — nothing executed)"
        fi
        ;;
    deploy)
        commitAndPushRun "deploy (execution receipt; runbook.md reconciled)"
        ;;
    *)
        commitAndPushRun "${RUN_MODE} receipt"
        ;;
    esac
}
