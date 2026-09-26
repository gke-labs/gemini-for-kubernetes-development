
set -e
set -o pipefail

# Run task: plan, deploy or tear down one run, in that run's own
# sandbox. A run owns everything it needs — runbook.md is the
# procedure, the scripts are generated from it, the receipts are the
# test results — all under docs-exploration/agent-runs/<name>/ on the
# exploration/notes branch of the member's fork.
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
# - MODELS
# - GOOGLE_CLOUD_PROJECT / CLOUDSDK_* when the member configured a project

NOTES_BRANCH="exploration/notes"
RUN_DIR="docs-exploration/agent-runs/${RUN_NAME}"

function ensureNotesBranch {
    echo "Ensuring notes branch ${NOTES_BRANCH}..."
    pushd "/workspaces/${REPO_NAME}" > /dev/null
    if git fetch origin "${NOTES_BRANCH}" 2>/dev/null; then
        git checkout -B "${NOTES_BRANCH}" "origin/${NOTES_BRANCH}"
    else
        git checkout -B "${NOTES_BRANCH}"
    fi
    # Runs must execute against LATEST code.
    SRC_REMOTE="upstream"
    git remote get-url upstream >/dev/null 2>&1 || SRC_REMOTE="origin"
    DEFAULT_BRANCH=$(gh repo view --json defaultBranchRef --jq .defaultBranchRef.name)
    if [ -n "${DEFAULT_BRANCH}" ] && git fetch "${SRC_REMOTE}" "${DEFAULT_BRANCH}"; then
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

# seedFromRun copies an existing run's procedure as the starting point.
# It carries a runbook.md already corrected by a real deployment, which
# is the whole reason deriving beats starting from the intent again.
# The receipts are deliberately left behind: they belong to that run's
# executions, not this one's.
function seedFromRun {
    [ -n "${RUN_FROM}" ] || return 0
    local src="/workspaces/${REPO_NAME}/docs-exploration/runs/${RUN_FROM}"
    local dst="/workspaces/${REPO_NAME}/${RUN_DIR}"
    if [ ! -d "${src}" ]; then
        echo "WARN: --from run '${RUN_FROM}' not found on the branch; planning from the intent instead."
        return 0
    fi
    if [ -e "${dst}/runbook.md" ]; then
        echo "Run already has a runbook.md; ignoring --from ${RUN_FROM}."
        return 0
    fi
    echo "Seeding ${RUN_NAME} from ${RUN_FROM}..."
    for f in runbook.md params.env deploy.sh teardown.sh; do
        [ -f "${src}/${f}" ] && cp "${src}/${f}" "${dst}/${f}"
    done
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
        if ! git push origin "${NOTES_BRANCH}"; then
            git fetch origin "${NOTES_BRANCH}"
            if [ "$(git rev-parse HEAD)" = "$(git rev-parse "origin/${NOTES_BRANCH}")" ]; then
                echo "Remote already at our commit; push had succeeded."
            # The remote moved: another run pushed to this branch while
            # we worked. Replay onto it rather than dying — losing a
            # completed plan to a race is how a run silently vanishes.
            elif git rebase --autostash "origin/${NOTES_BRANCH}"; then
                echo "Remote had moved; replayed onto it."
                git push origin "${NOTES_BRANCH}"
            else
                git rebase --abort 2>/dev/null || true
                echo "ERROR: push failed and the branch could not be replayed." >&2
                exit 1
            fi
        fi
        echo "Pushed ${what} to origin/${NOTES_BRANCH}"
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
ensureNotesBranch
configureGemini

case "${RUN_MODE}" in
plan)
    # Authors (or revises) runbook.md, then generates the scripts from
    # it, then writes a PLANNED receipt. Nothing executes: the owner
    # reviews the prose before anything spends money.
    seedFromRun
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
