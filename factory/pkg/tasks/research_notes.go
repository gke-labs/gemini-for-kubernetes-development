package tasks

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
