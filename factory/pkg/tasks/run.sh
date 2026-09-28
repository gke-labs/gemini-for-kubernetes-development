
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
# - RUN_INTENT (plan only, optional: set when the owner asked for changes)
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

# RUNBOOK_FILES are what a runbook is: the procedure and what it
# transcribes to. Receipts are deliberately not among them — they are
# the test results of the run they came from, not of this one.
RUNBOOK_FILES="runbook.md params.env deploy.sh teardown.sh"

# resolveRunbook finds RUN_RUNBOOK and copies its files into $1. It
# looks in the repository's .agents/runbooks/ first, where a runbook is
# reviewed like code, then among the member's own runs — any run is a
# runbook, since its directory holds the same files — including runs
# still under a legacy path. runbook.md is what makes a directory a
# runbook: without the procedure there is nothing to review.
#
# Prints where it came from, for the receipt; prints nothing and copies
# nothing when there is no such runbook.
function resolveRunbook {
    local out="$1" root="/workspaces/${REPO_NAME}"
    local repoPath=".agents/runbooks/${RUN_RUNBOOK}" f base
    if [ -n "${RUNBOOK_REF}" ] && git -C "${root}" cat-file -e "${RUNBOOK_REF}:${repoPath}/runbook.md" 2>/dev/null; then
        for f in ${RUNBOOK_FILES}; do
            if git -C "${root}" cat-file -e "${RUNBOOK_REF}:${repoPath}/${f}" 2>/dev/null; then
                git -C "${root}" show "${RUNBOOK_REF}:${repoPath}/${f}" > "${out}/${f}"
            fi
        done
        echo "${repoPath} at ${RUNBOOK_REF} $(git -C "${root}" rev-parse --short "${RUNBOOK_REF}")"
        return 0
    fi
    for base in "${repoPath%/*}" "$(dirname "${RUN_DIR}")" ${LEGACY_RUN_DIRS}; do
        # The repository path again, from the worktree: only reached
        # when the ref could not be read.
        if [ "${base}" = "${repoPath%/*}" ] && [ -n "${RUNBOOK_REF}" ]; then
            continue
        fi
        if [ -f "${root}/${base}/${RUN_RUNBOOK}/runbook.md" ]; then
            for f in ${RUNBOOK_FILES}; do
                if [ -f "${root}/${base}/${RUN_RUNBOOK}/${f}" ]; then
                    cp "${root}/${base}/${RUN_RUNBOOK}/${f}" "${out}/${f}"
                fi
            done
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

# paramValue prints the value of KEY in a params.env line's right-hand
# side: a quoted string up to its closing quote, or a bare word up to
# the first blank. Enough for the literal-values-only files the plan
# prompt writes; anything cleverer is shell, and shell is not rewritten.
function paramValue {
    local v="$1"
    case "${v}" in
    \"*) v="${v#\"}"; echo "${v%%\"*}" ;;
    \'*) v="${v#\'}"; echo "${v%%\'*}" ;;
    *) echo "${v%%[[:space:]]*}" ;;
    esac
}

# rewriteParams makes a copied params.env this run's own. A params.env
# holds literal values, and some of them belong to wherever it came
# from: RESOURCE_PREFIX names the source run's cloud resources, so a
# copy that kept it would deploy onto that run's resources — and its
# teardown would delete them. The project and region are whoever wrote
# the runbook's.
#
# So RESOURCE_PREFIX becomes RUN_RESOURCE_PREFIX, the project and region
# keys become the member's settings, and the old prefix is replaced
# wherever else it appears (CLUSTER="<prefix>-gke" and friends). A key
# left empty is appended, one per line, to $2. The old prefix and
# project are left in OLD_PREFIX / OLD_PROJECT for the script check —
# which is why this is called directly and never as $(rewriteParams),
# where they would die with the subshell.
function rewriteParams {
    local file="$1" missing="$2" tmp="$1.new" line key raw val re
    re='^(export[[:space:]]+)?([A-Za-z_][A-Za-z0-9_]*)=(.*)$'
    OLD_PREFIX="" OLD_PROJECT=""
    while IFS= read -r line || [ -n "${line}" ]; do
        if [[ "${line}" =~ ${re} ]]; then
            case "${BASH_REMATCH[2]}" in
            RESOURCE_PREFIX) OLD_PREFIX="$(paramValue "${BASH_REMATCH[3]}")" ;;
            PROJECT | PROJECT_ID | GCP_PROJECT | GOOGLE_CLOUD_PROJECT | CLOUDSDK_CORE_PROJECT)
                OLD_PROJECT="$(paramValue "${BASH_REMATCH[3]}")" ;;
            esac
        fi
    done < "${file}"
    : > "${tmp}"
    while IFS= read -r line || [ -n "${line}" ]; do
        if [[ ! "${line}" =~ ${re} ]]; then
            printf '%s\n' "${line}" >> "${tmp}"
            continue
        fi
        key="${BASH_REMATCH[2]}"
        raw="${BASH_REMATCH[3]}"
        val="$(paramValue "${raw}")"
        case "${key}" in
        RESOURCE_PREFIX) val="${RUN_RESOURCE_PREFIX}" ;;
        PROJECT | PROJECT_ID | GCP_PROJECT | GOOGLE_CLOUD_PROJECT | CLOUDSDK_CORE_PROJECT)
            val="${GOOGLE_CLOUD_PROJECT:-}" ;;
        REGION | GCP_REGION | CLOUDSDK_COMPUTE_REGION) val="${CLOUDSDK_COMPUTE_REGION:-}" ;;
        *)
            if [ -n "${OLD_PREFIX}" ]; then
                # The replacement is unquoted on purpose: quotes there
                # are literal to bash 3.2 and read differently since
                # 5.2's patsub_replacement. A prefix is [a-z0-9-] only,
                # so there is nothing in it to protect.
                val="${val//"${OLD_PREFIX}"/${RUN_RESOURCE_PREFIX}}"
            fi
            ;;
        esac
        if [ -z "${val}" ]; then
            echo "${key}" >> "${missing}"
        fi
        if [ "${val}" = "$(paramValue "${raw}")" ]; then
            printf '%s\n' "${line}" >> "${tmp}"
        else
            printf '%s%s="%s"  # rewritten for this run\n' "${BASH_REMATCH[1]}" "${key}" "${val}" >> "${tmp}"
        fi
    done < "${file}"
    mv "${tmp}" "${file}"
}

# instantiateRunbook starts this run from RUN_RUNBOOK: the runbook's
# files copied into RUN_DIR, params.env rewritten for this run, and —
# when nobody asked for changes — a receipt, so the run lands PLANNED
# (or BLOCKED, saying what is missing) without an engine being asked
# anything. The procedure was reviewed where it came from; what is left
# is the owner reading this run's parameters and clicking Deploy.
#
# The scripts are copied, never rewritten. One that names the source's
# prefix or project literally cannot be made this run's by a text
# substitution anyone should trust with a teardown, so it BLOCKs the
# run instead, and Refine hands it to the engine.
#
# Every step is written to survive set -e: a false test that ends a
# loop or a function is enough to kill the plan.
function instantiateRunbook {
    local root="/workspaces/${REPO_NAME}"
    local dst="${root}/${RUN_DIR}" stage origin needs="" f verdict
    if [ -e "${dst}/runbook.md" ]; then
        echo "ERROR: run ${RUN_NAME} already has a runbook.md; --runbook starts a new run." >&2
        echo "       Re-plan it to change it, or pick a new name." >&2
        exit 1
    fi
    stage="$(mktemp -d)"
    origin="$(resolveRunbook "${stage}")"
    if [ -z "${origin}" ]; then
        echo "ERROR: no runbook named '${RUN_RUNBOOK}': looked in .agents/runbooks/ and among your runs on ${RUNS_BRANCH}." >&2
        exit 1
    fi
    echo "Instantiating ${RUN_NAME} from ${origin}..."
    mkdir -p "${dst}"
    cp "${stage}"/* "${dst}/"

    if [ -f "${dst}/params.env" ]; then
        : > "${stage}/missing"
        rewriteParams "${dst}/params.env" "${stage}/missing"
        for f in $(cat "${stage}/missing"); do
            needs="${needs}- ${f} is empty in params.env. Set it in Settings if it is the GCP project or region; otherwise Refine with the value."$'\n'
        done
        for f in deploy.sh teardown.sh; do
            if [ -n "${OLD_PREFIX}" ] && [ "${OLD_PREFIX}" != "${RUN_RESOURCE_PREFIX}" ] && [ -f "${dst}/${f}" ] && grep -qF -- "${OLD_PREFIX}" "${dst}/${f}"; then
                needs="${needs}- ${f} names the source's resource prefix \`${OLD_PREFIX}\` literally instead of \${RESOURCE_PREFIX}. Refine to have it parameterised."$'\n'
            fi
            if [ -n "${OLD_PROJECT}" ] && [ "${OLD_PROJECT}" != "${GOOGLE_CLOUD_PROJECT:-}" ] && [ -f "${dst}/${f}" ] && grep -qF -- "${OLD_PROJECT}" "${dst}/${f}"; then
                needs="${needs}- ${f} names the source's project \`${OLD_PROJECT}\` literally. Refine to have it parameterised."$'\n'
            fi
        done
    else
        needs="${needs}- The runbook has no params.env, so nothing says what makes this run this run. Refine to have the parameters resolved."$'\n'
    fi
    rm -rf "${stage}"
    for f in deploy.sh teardown.sh; do
        if [ ! -f "${dst}/${f}" ]; then
            needs="${needs}- The runbook has no ${f}. Refine to have it written from runbook.md."$'\n'
        fi
    done

    # Asked for changes: the engine plans them as an amendment to the
    # procedure just copied, and writes the receipt itself.
    if [ -n "${RUN_INTENT:-}" ]; then
        return 0
    fi
    verdict="PLANNED"
    if [ -n "${needs}" ]; then
        verdict="BLOCKED"
    fi
    {
        echo "${verdict} — instantiated from runbook ${RUN_RUNBOOK}"
        echo
        echo "Source: ${origin}"
        echo
        echo "Nothing was executed and no agent ran. runbook.md, deploy.sh and"
        echo "teardown.sh are the source's, unchanged. params.env was rewritten"
        echo "for this run: RESOURCE_PREFIX=${RUN_RESOURCE_PREFIX}, and the project"
        echo "and region from Settings."
        if [ -n "${needs}" ]; then
            echo
            echo "## Needs from owner"
            echo
            printf '%s' "${needs}"
        else
            echo
            echo "Read params.env — it is what makes this run this run — then Deploy."
        fi
    } > "${dst}/receipt-$(date -u +%Y%m%d-%H%M).md"
}

function commitAndPushRun {
    local what="$1"
    echo "Committing and pushing ${what}..."
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
        git checkout -f -- . 2>/dev/null || true
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
    else
        echo "No changes to commit for ${what}."
    fi
    popd > /dev/null
}

# Main execution
setupGit
setupGitRepos
# HACK: Avoid git lock issues
sleep 5
checkoutDefaultBranch
ensureRunsBranch
configureGemini

case "${RUN_MODE}" in
plan)
    # Authors (or revises) runbook.md, then generates the scripts from
    # it, then writes a PLANNED receipt. Nothing executes: the owner
    # reviews the prose before anything spends money.
    #
    # From a runbook with nothing to change, there is nothing to plan:
    # the procedure was reviewed where it came from, and the copy
    # already wrote this run's receipt.
    if [ -n "${RUN_RUNBOOK:-}" ]; then
        instantiateRunbook
        if [ -z "${RUN_INTENT:-}" ]; then
            commitAndPushRun "plan (instantiated from runbook ${RUN_RUNBOOK} — nothing executed, no agent ran)"
            exit 0
        fi
    fi
    runEngine
    commitAndPushRun "plan (runbook.md, scripts, PLANNED receipt — nothing executed)"
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
