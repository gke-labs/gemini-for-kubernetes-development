set -e
set -o pipefail

# Save one research conversation's notes to the member's fork.
#
# The conversation writes prose into its own checkout; this script is
# the other half, and the split is the whole point. A research session
# can be running with approvals turned off, so the credential is held
# here, for the length of one command, and never put anywhere the
# conversation can read it back: no gh hosts.yml, no global
# credential.helper, no token in a remote URL. It arrives as an
# environment variable on this process and leaves with it.
#
# Unlike run.sh this script is NOT prepended with lib.sh. setupGit
# writes the token into ${HOME}/.config/gh/hosts.yml and into a global
# url.insteadOf rewrite, both of which live on the PVC the agent reads
# and writes — exactly what this script exists to avoid.
#
# It expects the following environment variables to be set:
# - GH_TOKEN (the member's; transient)
# - REPO_NAME (the checkout under /workspaces)
# - UPSTREAM_REPO (owner/repo the member's fork is of)
# - SESSION_ID (names the directory the notes land in)
# - GITHUB_USER_NAME / GITHUB_USER_EMAIL (commit identity)

NOTES_BRANCH="research/notes"
NOTES_DIR="docs-exploration/research/${SESSION_ID}"
SRC="/workspaces/${REPO_NAME}/${NOTES_DIR}"

# Nothing to save is an error, not a quiet success. The caller asked
# the conversation to write notes and then asked for them to be saved;
# an empty directory means the first half did not happen, and reporting
# "saved" would send someone to a branch that has nothing on it.
if [ ! -d "${SRC}" ] || [ -z "$(ls -A "${SRC}" 2>/dev/null)" ]; then
    echo "ERROR: ${NOTES_DIR} is missing or empty in the checkout." >&2
    echo "       The conversation has not written its notes there yet." >&2
    exit 1
fi

# Which fork to push to is the token's own answer, not a flag: a
# mistyped owner would push a member's notes into somebody else's
# repository, and the token is the only thing that actually knows whose
# it is.
FORK_OWNER="$(gh api user --jq .login)"
if [ -z "${FORK_OWNER}" ]; then
    echo "ERROR: could not resolve the token's GitHub login." >&2
    exit 1
fi

# A member who has only ever held research conversations has no fork
# yet — `factory run` is what usually makes one, via setupGitRepos.
# Idempotent: gh reports an existing fork and exits 0.
echo "Ensuring ${FORK_OWNER} has a fork of ${UPSTREAM_REPO}..."
gh repo fork "${UPSTREAM_REPO}" --clone=false

FORK_URL="https://github.com/${FORK_OWNER}/${REPO_NAME}.git"
# gh reads the token from the environment, so the helper config carries
# no secret and dies with the temporary clone below.
GIT_CRED='!gh auth git-credential'

# Everything happens in a throwaway clone, never in the conversation's
# checkout. Switching that checkout's branch under a live session would
# change files the agent is in the middle of reading, and a session is
# long-lived by design.
WORK="$(mktemp -d)"
trap 'rm -rf "${WORK}"' EXIT

if git -c credential.helper="${GIT_CRED}" clone --quiet --branch "${NOTES_BRANCH}" \
    --single-branch "${FORK_URL}" "${WORK}/notes" 2>/dev/null; then
    echo "Adding to the existing ${NOTES_BRANCH} branch..."
else
    # The first notes on this fork. The branch is built from an empty
    # repository rather than off the default branch: it carries prose
    # and nothing else, so cloning it stays cheap however large the
    # repository is, and no code change can ever conflict with a note.
    echo "Starting ${NOTES_BRANCH} on ${FORK_OWNER}/${REPO_NAME}..."
    git init --quiet "${WORK}/notes"
    # symbolic-ref rather than `checkout -b`, which needs a commit to
    # exist, or `init -b`, which needs a git new enough to have it.
    git -C "${WORK}/notes" symbolic-ref HEAD "refs/heads/${NOTES_BRANCH}"
    git -C "${WORK}/notes" remote add origin "${FORK_URL}"
fi

git -C "${WORK}/notes" config user.name "${GITHUB_USER_NAME}"
git -C "${WORK}/notes" config user.email "${GITHUB_USER_EMAIL}"
git -C "${WORK}/notes" config credential.helper "${GIT_CRED}"

# Replace this session's directory wholesale rather than copying over
# it, so a file the conversation deleted does not survive on the branch
# as a stale note. Scoped to this session: no other session's notes are
# in reach.
mkdir -p "${WORK}/notes/$(dirname "${NOTES_DIR}")"
rm -rf "${WORK}/notes/${NOTES_DIR}"
cp -R "${SRC}" "${WORK}/notes/${NOTES_DIR}"

cd "${WORK}/notes"

# Errors are NOT swallowed here. git refuses to add an ignored path,
# and suppressing that is how a run once turned into a silent "nothing
# to commit" with the work already done — see commitAndPushRun in
# run.sh, which learned this the hard way.
if ! git add --all "${NOTES_DIR}"; then
    echo "ERROR: could not stage ${NOTES_DIR}." >&2
    exit 1
fi
if git diff --cached --quiet; then
    echo "No change: ${NOTES_BRANCH} already has these notes."
    exit 0
fi
git commit --quiet -m "research(${SESSION_ID}): notes"

# A dropped connection after a successful server-side push makes the
# retry fail with 'cannot lock ref … is at <our sha>'. If the remote is
# already at our commit, the push succeeded.
if ! git push origin "${NOTES_BRANCH}"; then
    git fetch origin "${NOTES_BRANCH}"
    if [ "$(git rev-parse HEAD)" = "$(git rev-parse "origin/${NOTES_BRANCH}")" ]; then
        echo "Remote already at our commit; the push had succeeded."
    # The remote moved: another session saved notes while we worked.
    # Replay onto it rather than dying — losing a summary the member
    # waited for to a race is how notes silently vanish.
    elif git rebase "origin/${NOTES_BRANCH}"; then
        echo "Remote had moved; replayed onto it."
        git push origin "${NOTES_BRANCH}"
    else
        git rebase --abort 2>/dev/null || true
        echo "ERROR: push failed and the branch could not be replayed." >&2
        exit 1
    fi
fi

echo "Saved ${NOTES_DIR} to ${FORK_OWNER}/${REPO_NAME} on ${NOTES_BRANCH}"
