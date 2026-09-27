package tasks

// GetSaveNotesScript returns the in-sandbox script that pushes one
// research conversation's notes to the member's fork.
//
// Read raw, without the lib.sh prelude every other task script gets.
// lib.sh's setupGit writes the member's token into
// ${HOME}/.config/gh/hosts.yml and into a global url.insteadOf rewrite
// — both on the PVC, both readable by the agent holding the
// conversation. This script exists to keep the credential out of that
// reach, so it cannot start by putting it there.
func GetSaveNotesScript() ([]byte, error) {
	return scriptsFS.ReadFile("save_notes.sh")
}

// ResearchNotesBranch is the branch on the member's fork that research
// notes are saved to. It must match notesBranch in repo-agent, which
// builds the link members follow to read them.
//
// Runs live on research/runs, deliberately elsewhere: that branch
// carries live deployment state and is pruned when a run is removed,
// while this one is archival and is written by a session that may be
// approving its own tool calls.
const ResearchNotesBranch = "research/notes"

// ResearchNotesDir is where the notes live on that branch: one file
// per conversation, named after it.
const ResearchNotesDir = "docs-exploration/research"
