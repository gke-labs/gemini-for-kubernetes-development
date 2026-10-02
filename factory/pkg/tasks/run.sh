
set -e
set -o pipefail

# Run task: plan, deploy or tear down one run, in that run's own
# sandbox. A run owns everything it needs — runbook.md is the
# procedure, the scripts are generated from it, the receipts are the
# test results — all under docs-exploration/agent-runs/<name>/ on the
# research/runs branch of the member's fork.
#
# The difference from the runbook task this replaces is that nothing is
# shared. One run reads and writes one directory, so there are no
# siblings to hide, no source document to protect from edits, and no
# stash to restore after a run is killed mid-phase. Each mode is a
# single engine invocation.
#
# The script owns the deterministic parts (branch, commit, push); the
# engine owns authoring, execution and judgment.
#
# It expects the following environment variables to be set:
# - GEMINI_API_KEY or ANTHROPIC_API_KEY (per ENGINE)
# - GITHUB_TOKEN
# - REPO_NAME / CLONE_URL / PROMPT_FILE
# - GITHUB_USER_ID / GITHUB_USER_EMAIL / GITHUB_USER_NAME
# - RUN_NAME (the run's identity, e.g. deploy-gke-k8s1)
# - RUN_MODE (plan | deploy | teardown)
# - RUN_RESOURCE_PREFIX (what this run may name and own in the cloud)
# - RUN_RUNBOOK (plan only, optional: the runbook this run starts from)
# - RUN_TARGET_PR (plan only, optional: the pull request to deploy)
# - RUN_INTENT (plan only, optional: what to build, or what to change)
# - MODELS
# - GOOGLE_CLOUD_PROJECT / CLOUDSDK_* when the member configured a project

# The caller passes the PAT as GITHUB_TOKEN; lib.sh's setupGit reads it
# as GITHUB_USER_TOKEN. Every other task script bridges the two names
# here, and this one did not — so setupGit wrote an empty oauth_token
# into gh's hosts.yml and `https://<user>:@github.com/` as the git
# credential, and every run since was unauthenticated.
#
# It failed a long way from the cause: gh treats a hosts.yml entry with
# no token as a config it must migrate and cannot, so it exits 1 for
# every subcommand — `gh --version` included — and the run died in
# checkoutDefaultBranch's `gh repo view` with a message about dbus.
# Refuse up front instead, where the name of the missing thing is still
# in scope.
export GITHUB_USER_TOKEN="${GITHUB_USER_TOKEN:-${GITHUB_TOKEN}}"
if [ -z "${GITHUB_USER_TOKEN}" ]; then
    echo "No GitHub token: set GITHUB_TOKEN (or GITHUB_USER_TOKEN)." >&2
    exit 1
fi

RUNS_BRANCH="research/runs"
RUN_DIR="docs-exploration/agent-runs/${RUN_NAME}"

function ensureRunsBranch {
    echo "Ensuring runs branch ${RUNS_BRANCH}..."
    pushd "/workspaces/${REPO_NAME}" > /dev/null
    if git fetch origin "${RUNS_BRANCH}" 2>/dev/null; then
        git checkout -B "${RUNS_BRANCH}" "origin/${RUNS_BRANCH}"
    else
        git checkout -B "${RUNS_BRANCH}"
    fi
    # Runs must execute against LATEST code.
    SRC_REMOTE="upstream"
    git remote get-url upstream >/dev/null 2>&1 || SRC_REMOTE="origin"
    DEFAULT_BRANCH=$(gh repo view --json defaultBranchRef --jq .defaultBranchRef.name)
    if [ -n "${DEFAULT_BRANCH}" ] && git fetch "${SRC_REMOTE}" "${DEFAULT_BRANCH}"; then
        RUNBOOK_REF="${SRC_REMOTE}/${DEFAULT_BRANCH}"
        git merge --no-edit -X ours "${SRC_REMOTE}/${DEFAULT_BRANCH}" || {
            git merge --abort 2>/dev/null || true
            echo "WARN: could not refresh the code base; running from the branch as-is."
        }
    fi
    # Idempotent setup beats reliable cleanup: restore this run's
    # directory from the branch before anything reads it, in case a
    # previous invocation was killed mid-write. Scoped to this run, so
    # nothing else in the worktree is touched.
    git checkout -f -- "${RUN_DIR}" 2>/dev/null || true
    adoptLegacyInstance
    mkdir -p "${RUN_DIR}"
    popd > /dev/null
}

# LEGACY_RUN_DIRS are the paths a run has lived under before now:
# runbook-deployments/<name>/ from `factory runbook`, and a short-lived
# runs/<name>/ that had to be renamed — "runs/" is a bare .gitignore
# entry in a great many repositories (TensorBoard and friends write
# there), and a bare pattern matches at any depth, so
# docs-exploration/runs/ was ignored wherever that appeared.
LEGACY_RUN_DIRS="docs-exploration/runbook-deployments docs-exploration/runs"

# adoptLegacyInstance moves a run from wherever it used to live into
# RUN_DIR, lazily, and only for a run someone actually touches: a
# big-bang migration of live deployments is a worse risk than a git mv
# at the moment of use. The sandbox is already the same one — naming
# is unchanged — so the PVC, the cluster state and the teardown script
# all come along.
#
# runbook.md is deliberately NOT synthesised for a deployment that
# never had one: the old shared runbook described a scenario, not what
# this particular deployment did, and inventing prose about live
# infrastructure is how a teardown removes the wrong thing. The first
# deploy or teardown reconciles one from the scripts, which is the
# only honest source.
function adoptLegacyInstance {
    # An empty name would make the legacy path the whole tree and move
    # every run at once.
    if [ -z "${RUN_NAME}" ] || [ -d "${RUN_DIR}" ]; then
        return 0
    fi
    local legacy
    for base in ${LEGACY_RUN_DIRS}; do
        legacy="${base}/${RUN_NAME}"
        [ -d "${legacy}" ] || continue
        echo "Adopting ${legacy} into ${RUN_DIR}..."
        mkdir -p "$(dirname "${RUN_DIR}")"
        if ! git mv "${legacy}" "${RUN_DIR}" 2>/dev/null; then
            mv "${legacy}" "${RUN_DIR}"
        fi
        # The commit stages RUN_DIR with --ignore-removal, which by
        # design will not record the old path's disappearance. Stage
        # that removal here, or the branch keeps both copies.
        git add -A "${legacy}" 2>/dev/null || true
        return 0
    done
}

# RUNBOOK_REF is the commit a repository runbook is read at: the
# default branch as ensureRunsBranch just fetched it. Read from the
# commit, not the worktree — research/runs merges the default branch
# with -X ours, so a runbook edited on both sides would come out as the
# fork's copy, not the repository's. Empty when that fetch failed, and
# then the worktree is the best there is.
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

# resolveRunbook finds RUN_RUNBOOK and copies it into $1. It looks in
# the repository's .agents/runbooks/ first, where a runbook is reviewed
# like code, then among the member's own runs — any run is a runbook —
# including runs still under a legacy path.
#
# A runbook has no required shape. It is the plan's starting point, not
# something this script executes: prose, scripts, a run's full set of
# files — the engine makes this run's files out of whatever is there.
# So everything is copied, subdirectories included, except receipts:
# they are the test results of the run they came from, not of this one.
# Nor its target.env: that pins the pull request the other run deploys,
# and a copy of it would have this run deploy that pull request too.
#
# The repository's copy is read at RUNBOOK_REF — with a pull request
# target, its head, so a runbook the pull request changes is the one it
# is deployed with — and then at RUNBOOK_FALLBACK_REF.
#
# Prints where it came from; prints nothing and copies nothing when
# there is no such runbook.
function resolveRunbook {
    local out="$1" root="/workspaces/${REPO_NAME}"
    local repoPath=".agents/runbooks/${RUN_RUNBOOK}" f base rel src ref
    for ref in "${RUNBOOK_REF}" "${RUNBOOK_FALLBACK_REF}"; do
        if [ -z "${ref}" ] || [ -z "$(git -C "${root}" ls-tree -r --name-only "${ref}" -- "${repoPath}/" 2>/dev/null)" ]; then
            continue
        fi
        while IFS= read -r f; do
            rel="${f#"${repoPath}"/}"
            case "${rel##*/}" in receipt-*) continue ;; esac
            [ "${rel}" != "${TARGET_FILE}" ] || continue
            mkdir -p "$(dirname "${out}/${rel}")"
            git -C "${root}" show "${ref}:${f}" > "${out}/${rel}"
        done < <(git -C "${root}" ls-tree -r --name-only "${ref}" -- "${repoPath}/")
        echo "${repoPath} at ${ref} $(git -C "${root}" rev-parse --short "${ref}")"
        return 0
    done
    for base in "${repoPath%/*}" "$(dirname "${RUN_DIR}")" ${LEGACY_RUN_DIRS}; do
        # The repository path again, from the worktree: only reached
        # when the ref could not be read.
        if [ "${base}" = "${repoPath%/*}" ] && [ -n "${RUNBOOK_REF}" ]; then
            continue
        fi
        src="${root}/${base}/${RUN_RUNBOOK}"
        if [ -d "${src}" ] && [ -n "$(ls -A "${src}")" ]; then
            cp -R "${src}/." "${out}/"
            find "${out}" -name 'receipt-*' -type f -exec rm -f {} +
            rm -f "${out}/${TARGET_FILE}"
            if [ "${base}" = "${repoPath%/*}" ]; then
                echo "${repoPath} (working tree)"
            else
                echo "run ${RUN_RUNBOOK} on ${RUNS_BRANCH} (${base}/${RUN_RUNBOOK})"
            fi
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
#
# Every step is written to survive set -e: a false test that ends a
# loop or a function is enough to kill the plan.
function instantiateRunbook {
    local root="/workspaces/${REPO_NAME}"
    local dst="${root}/${RUN_DIR}" stage
    if [ -e "${dst}/runbook.md" ]; then
        echo "ERROR: run ${RUN_NAME} already has a runbook.md; --runbook starts a new run." >&2
        echo "       Re-plan it to change it, or pick a new name." >&2
        exit 1
    fi
    stage="$(mktemp -d)"
    RUNBOOK_ORIGIN="$(resolveRunbook "${stage}")"
    if [ -z "${RUNBOOK_ORIGIN}" ]; then
        echo "ERROR: no runbook named '${RUN_RUNBOOK}': looked in .agents/runbooks/ and among your runs on ${RUNS_BRANCH}." >&2
        exit 1
    fi
    echo "Starting ${RUN_NAME} from ${RUNBOOK_ORIGIN}..."
    mkdir -p "${dst}"
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
# re-plan with --target re-pins to the new head.
#
# The pull request's files are laid over the worktree, not merged:
# research/runs is pushed, and a merge would carry the pull request's
# commits into every later run on the branch. docs-exploration/ is
# left alone — it is where the runs live. clearTarget puts the
# worktree back before anything is committed.
#
# No target.env and no --target: the default branch, as always.
function resolveTarget {
    local root="/workspaces/${REPO_NAME}" pin
    pin="${root}/${RUN_DIR}/${TARGET_FILE}"
    if [ "${RUN_MODE}" = "plan" ] && [ -n "${RUN_TARGET_PR:-}" ]; then
        if ! git -C "${root}" fetch -q "${SRC_REMOTE}" "pull/${RUN_TARGET_PR}/head"; then
            echo "ERROR: could not fetch pull request #${RUN_TARGET_PR} from ${SRC_REMOTE}." >&2
            exit 1
        fi
        TARGET_PR="${RUN_TARGET_PR}"
        TARGET_SHA="$(git -C "${root}" rev-parse FETCH_HEAD)"
        mkdir -p "$(dirname "${pin}")"
        printf 'TARGET_PR=%s\nTARGET_SHA=%s\n' "${TARGET_PR}" "${TARGET_SHA}" > "${pin}"
    elif [ -f "${pin}" ]; then
        # Read, never sourced: it is a file on a branch, and all it may
        # say is a number and a commit.
        TARGET_PR="$(sed -n 's/^TARGET_PR=//p' "${pin}")"
        TARGET_SHA="$(sed -n 's/^TARGET_SHA=//p' "${pin}")"
        if ! [[ "${TARGET_PR}" =~ ^[0-9]+$ && "${TARGET_SHA}" =~ ^[0-9a-f]{40}$ ]]; then
            echo "ERROR: ${RUN_DIR}/${TARGET_FILE} does not pin a pull request and a commit." >&2
            exit 1
        fi
        # The pinned commit is usually here already. A force-push can
        # take it out of the pull request, so ask for it by name too.
        if ! git -C "${root}" cat-file -e "${TARGET_SHA}^{commit}" 2>/dev/null; then
            git -C "${root}" fetch -q "${SRC_REMOTE}" "pull/${TARGET_PR}/head" 2>/dev/null || true
            git -C "${root}" fetch -q "${SRC_REMOTE}" "${TARGET_SHA}" 2>/dev/null || true
        fi
        if ! git -C "${root}" cat-file -e "${TARGET_SHA}^{commit}" 2>/dev/null; then
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
    git -C "${root}" restore --source="${TARGET_SHA}" --worktree --no-overlay -- . ':(exclude)docs-exploration'
}

# clearTarget takes the pull request's files back off the worktree,
# listing whatever the engine changed in them first: that is collateral,
# exactly as it is on the default branch, and it is never committed.
function clearTarget {
    local root="/workspaces/${REPO_NAME}" changed idx
    if [ -z "${TARGET_SHA}" ]; then
        return 0
    fi
    # Against an index of the pull request's own tree: the real index
    # does not know the files it adds, and diffing against the commit
    # would list every one of them as changed.
    idx="$(mktemp -d)"
    GIT_INDEX_FILE="${idx}/index" git -C "${root}" read-tree "${TARGET_SHA}"
    changed="$(GIT_INDEX_FILE="${idx}/index" git -C "${root}" diff --name-only -- . ':(exclude)docs-exploration'
        GIT_INDEX_FILE="${idx}/index" git -C "${root}" ls-files --others --exclude-standard -- . ':(exclude)docs-exploration')"
    rm -rf "${idx}"
    if [ -n "${changed}" ]; then
        echo "Discarding changes to pull request #${TARGET_PR}'s files:"
        echo "${changed}" | sed "s/^/  /"
        addDiscarded "${changed}"
    fi
    git -C "${root}" restore --source=HEAD --worktree --no-overlay -- . ':(exclude)docs-exploration'
    git -C "${root}" clean -fdq -- . ':(exclude)docs-exploration'
}

# result.json is what this invocation concluded, in the task directory
# beside exit_code, for whoever launched it to read without GitHub and
# without the engine's own output format.
#
# This script writes it, never the engine, so it is the same file
# whichever engine ran. The one thing taken from the engine is the
# receipt's first line, which every run prompt already makes the
# verdict; a first word that is not a known verdict is recorded as
# "unknown", not guessed at. Written on every exit, failures included:
# a run that dies before its receipt is the one worth reading about.
RESULT_FILE=""
[ -n "${PROMPT_FILE:-}" ] && RESULT_FILE="$(dirname "${PROMPT_FILE}")/result.json"
RESULT_VERDICTS=" PLANNED VERIFIED DEPLOYED-UNVERIFIED FAILED BLOCKED TORN-DOWN PARTIAL "
RESULT_RECEIPTS_BEFORE=""
RESULT_COMMIT=""
# Newline-separated paths this run changed or created outside its own
# directory, all of them thrown away.
RESULT_DISCARDED=""

function addDiscarded {
    [ -n "$1" ] && RESULT_DISCARDED="${RESULT_DISCARDED:+${RESULT_DISCARDED}$'\n'}$1"
    return 0
}

# snapshotReceipts records the receipts already on the branch, so the
# one this invocation writes is the one that is new — by name, since
# restoring the run's directory rewrites every file's mtime.
function snapshotReceipts {
    RESULT_RECEIPTS_BEFORE="$(ls "/workspaces/${REPO_NAME}/${RUN_DIR}" 2>/dev/null | grep '^receipt-' || true)"
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
    local rc="$1" root="/workspaces/${REPO_NAME}" receipt="" verdict="" name line word target="null" discarded="" first=1 p
    [ -n "${RESULT_FILE}" ] || return 0
    for name in $(ls "${root}/${RUN_DIR}" 2>/dev/null | grep '^receipt-' | sort || true); do
        if ! printf '%s\n' "${RESULT_RECEIPTS_BEFORE}" | grep -qxF "${name}"; then
            receipt="${RUN_DIR}/${name}"
        fi
    done
    if [ -n "${receipt}" ]; then
        line="$(grep -m1 -v '^[[:space:]]*$' "${root}/${receipt}" 2>/dev/null || true)"
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
        printf '  "version": 1,\n'
        printf '  "run": %s,\n' "$(jsonString "${RUN_NAME}")"
        printf '  "mode": %s,\n' "$(jsonString "${RUN_MODE}")"
        printf '  "exitCode": %d,\n' "${rc}"
        printf '  "verdict": %s,\n' "$(jsonOrNull "${verdict}")"
        printf '  "receipt": %s,\n' "$(jsonOrNull "${receipt}")"
        printf '  "branch": %s,\n' "$(jsonString "${RUNS_BRANCH}")"
        printf '  "commit": %s,\n' "$(jsonOrNull "${RESULT_COMMIT}")"
        printf '  "target": %s,\n' "${target}"
        printf '  "discarded": [%s]\n' "${discarded}"
        printf '}\n'
    } > "${RESULT_FILE}.tmp" && mv "${RESULT_FILE}.tmp" "${RESULT_FILE}"
}


function commitAndPushRun {
    local what="$1"
    echo "Committing and pushing ${what}..."
    clearTarget
    pushd "/workspaces/${REPO_NAME}" > /dev/null
    # Stage only this run's directory. --ignore-removal keeps the
    # harness from recording a deletion as a side effect: whatever a
    # killed phase left missing from the worktree, the branch keeps.
    #
    # Errors are NOT swallowed. git refuses to add a path the repo
    # ignores, and suppressing that turned a whole run into a silent
    # "nothing to commit" — the work done, the receipt written, and
    # nothing on the branch to show for it.
    if ! git add --ignore-removal "${RUN_DIR}"; then
        echo "ERROR: could not stage ${RUN_DIR}. If the repository ignores this path," >&2
        echo "       nothing would be committed and the run would vanish silently." >&2
        exit 1
    fi
    # A run owns its own directory and nothing else. Anything it
    # changed outside is collateral — a .gitignore it edited to get at
    # its own files, a shared doc it wandered into — and committing it
    # is not this run's business. Restoring also leaves the worktree
    # clean, which matters: a stray modification made the replay below
    # fail with "cannot rebase: You have unstaged changes", turning a
    # recoverable push race into a lost receipt.
    if ! git diff --quiet; then
        echo "Discarding changes outside ${RUN_DIR}:"
        git diff --name-only | sed "s/^/  /"
        addDiscarded "$(git diff --name-only)"
        git checkout -f -- . 2>/dev/null || true
    fi
    # A file the engine created outside the run is not committed
    # either, and used to go unmentioned: a fix that depended on one (a
    # .gcloudignore at the repository root) worked in that sandbox and
    # nowhere else, and nothing recorded that it had ever existed.
    local untracked
    untracked="$(git ls-files --others --exclude-standard)"
    if [ -n "${untracked}" ]; then
        echo "Not committed (created outside ${RUN_DIR}):"
        echo "${untracked}" | sed "s/^/  /"
        addDiscarded "${untracked}"
    fi
    if git commit -m "run(${RUN_NAME}): ${what}"; then
        # A dropped connection after a successful server-side push makes
        # the retry fail with 'cannot lock ref … is at <our sha>'. If the
        # remote is already at our commit, the push succeeded.
        if ! git push origin "${RUNS_BRANCH}"; then
            git fetch origin "${RUNS_BRANCH}"
            if [ "$(git rev-parse HEAD)" = "$(git rev-parse "origin/${RUNS_BRANCH}")" ]; then
                echo "Remote already at our commit; push had succeeded."
            # The remote moved: another run pushed to this branch while
            # we worked. Replay onto it rather than dying — losing a
            # completed plan to a race is how a run silently vanishes.
            elif git rebase --autostash "origin/${RUNS_BRANCH}"; then
                echo "Remote had moved; replayed onto it."
                git push origin "${RUNS_BRANCH}"
            else
                git rebase --abort 2>/dev/null || true
                echo "ERROR: push failed and the branch could not be replayed." >&2
                exit 1
            fi
        fi
        echo "Pushed ${what} to origin/${RUNS_BRANCH}"
        RESULT_COMMIT="$(git rev-parse HEAD)"
    else
        echo "No changes to commit for ${what}."
    fi
    popd > /dev/null
}

# Main execution
trap 'rc=$?; writeResult "${rc}" || echo "WARN: could not write result.json" >&2; exit "${rc}"' EXIT
setupGit
setupGitRepos
# Runs are pushed to research/runs on origin, and the UI reads them from
# the member's fork. setupGitRepos ignores a failed fork, which left
# origin on the upstream repository: the push then failed with a 403
# after the engine had done all its work — or, for a member with write
# access, would have put research/runs on the upstream repository.
ensureForkRemote
# HACK: Avoid git lock issues
sleep 5
checkoutDefaultBranch
ensureRunsBranch
resolveTarget
snapshotReceipts
configureGemini

case "${RUN_MODE}" in
plan)
    # Authors (or revises) runbook.md, then generates the scripts from
    # it, then writes a PLANNED receipt. Nothing executes: the owner
    # reviews the prose before anything spends money.
    #
    # From a runbook, the copy is what the engine plans from: it makes
    # the runbook this run's, and verifies it here, like any plan.
    if [ -n "${RUN_RUNBOOK:-}" ]; then
        instantiateRunbook
        runEngine
        commitAndPushRun "plan from runbook ${RUNBOOK_ORIGIN}${TARGET_PR:+ for pull request #${TARGET_PR}} (nothing executed)"
        exit 0
    fi
    runEngine
    commitAndPushRun "plan${TARGET_PR:+ for pull request #${TARGET_PR}} (runbook.md, scripts, PLANNED receipt — nothing executed)"
    ;;
deploy)
    # The owner reviewed the plan and clicked Deploy. The engine repairs
    # the scripts as it goes and, once, at the end, brings runbook.md
    # back in line with them — one commit carrying scripts, prose and
    # receipt together, so a partial push cannot split the story.
    runEngine
    commitAndPushRun "deploy (execution receipt; runbook.md reconciled)"
    ;;
*)
    runEngine
    commitAndPushRun "${RUN_MODE} receipt"
    ;;
esac
