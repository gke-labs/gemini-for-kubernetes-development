package models

import (
	"github.com/google/go-github/v39/github"
)

// DraftReviewComment defines the structure for a review comment with severity
type DraftReviewComment struct {
	Path      *string `yaml:"path,omitempty" json:"path,omitempty"`
	Position  *int    `yaml:"position,omitempty" json:"position,omitempty"`
	Body      *string `yaml:"body,omitempty" json:"body,omitempty"`
	Line      *int    `yaml:"line,omitempty" json:"line,omitempty"`
	Side      *string `yaml:"side,omitempty" json:"side,omitempty"`
	StartLine *int    `yaml:"start_line,omitempty" json:"start_line,omitempty"`
	StartSide *string `yaml:"start_side,omitempty" json:"start_side,omitempty"`
	Severity  string  `yaml:"severity,omitempty" json:"severity,omitempty"`
}

// PullRequestReviewRequest defines the structure for a review request
type PullRequestReviewRequest struct {
	Body     *string               `yaml:"body,omitempty" json:"body,omitempty"`
	Event    *string               `yaml:"event,omitempty" json:"event,omitempty"`
	Comments []*DraftReviewComment `yaml:"comments,omitempty" json:"comments,omitempty"`
}

// ReviewAgentOutput defines the structure for the agent's YAML output.
type ReviewAgentOutput struct {
	Note   string                    `yaml:"note"`
	Review *PullRequestReviewRequest `yaml:"review"`
	Labels []string                  `yaml:"labels,omitempty"`
}

// ToGitHubReviewRequest converts the internal PullRequestReviewRequest to the GitHub API struct
func (r *PullRequestReviewRequest) ToGitHubReviewRequest() *github.PullRequestReviewRequest {
	if r == nil {
		return nil
	}
	var comments []*github.DraftReviewComment
	for _, c := range r.Comments {
		comments = append(comments, &github.DraftReviewComment{
			Path:      c.Path,
			Position:  c.Position,
			Body:      c.Body,
			Line:      c.Line,
			Side:      c.Side,
			StartLine: c.StartLine,
			StartSide: c.StartSide,
		})
	}
	return &github.PullRequestReviewRequest{
		Body:     r.Body,
		Event:    r.Event,
		Comments: comments,
	}
}

// Board summarizes a RepoBoard for the boards list.
type Board struct {
	Name       string `json:"name"`
	Namespace  string `json:"namespace"`
	RepoURL    string `json:"repoURL"`
	NeedsHuman int    `json:"needsHuman"`
	Active     int    `json:"active"`
	// Role is the viewer's relationship to the repo: "maintainer" (push+)
	// or "read-only". Fix flows are pointless without push — the UI
	// disables them on read-only boards.
	Role string `json:"role,omitempty"`
}

// WorkSandbox is the sandbox chip on a work-item row.
type WorkSandbox struct {
	Name                  string `json:"name"`
	Replicas              string `json:"replicas"`
	TaskState             string `json:"taskState,omitempty"`
	Engine                string `json:"engine,omitempty"`                // stamped at launch; pre-stamp sandboxes ran gemini
	AutoIterate           string `json:"autoIterate,omitempty"`           // effective: "on" | "off"
	AutoIterateOverridden bool   `json:"autoIterateOverridden,omitempty"` // per-PR override set (vs board default)
}

// WorkAction is one action a draft's task output offers, as the row can
// take it: POST /board/:board/issues/:id/actions/:verb {kind, run}.
type WorkAction struct {
	Verb   string `json:"verb"`
	Run    string `json:"run,omitempty"`
	Revise string `json:"revise,omitempty"` // the recipe revise a revise runs
	Label  string `json:"label,omitempty"`
	Field  string `json:"field,omitempty"`  // what an edit edits
	Format string `json:"format,omitempty"` // yaml | markdown, for an edit
	// Inputs are what a revise asks for before it can be filed
	// (Iterate's instruction).
	Inputs []string `json:"inputs,omitempty"`
	// Enabled is false for an action the draft's state rules out just now
	// (done already, or the agent is busy); Reason says why.
	Enabled bool   `json:"enabled"`
	Reason  string `json:"reason,omitempty"`
	// Error is why the last attempt at this write failed, until one
	// succeeds.
	Error string `json:"error,omitempty"`
}

// WorkItem is one row of the board work feed: an issue or PR merged with
// its agent/sandbox state.
type WorkItem struct {
	Type            string       `json:"type"`            // issue | pr
	Group           string       `json:"group,omitempty"` // issues | prs
	Number          int          `json:"number"`
	Title           string       `json:"title"`
	HTMLURL         string       `json:"htmlURL"`
	Attention       string       `json:"attention,omitempty"` // needs-you | working | waiting
	Assignee        string       `json:"assignee,omitempty"`
	Author          string       `json:"author,omitempty"`          // PR author (review rows show it as a chip)
	ReviewRequested bool         `json:"reviewRequested,omitempty"` // PR asks for the viewer's review (client-side scope)
	PRURL           string       `json:"prURL,omitempty"`
	Labels          []string     `json:"labels,omitempty"`
	Draft           string       `json:"draft,omitempty"`           // triage/review draft, when ready
	TriagePublished bool         `json:"triagePublished,omitempty"` // published triage rides the row as a done-state chip
	Error           string       `json:"error,omitempty"`           // why the last agent run failed, human-readable
	Plan            string       `json:"plan,omitempty"`            // implementation-plan draft awaiting refine/approve
	PlanApproved    bool         `json:"planApproved,omitempty"`    // approved plan rides the row as a done-state receipt
	TriageActions   []WorkAction `json:"triageActions,omitempty"`   // what can be done with Draft, from its task output
	PlanActions     []WorkAction `json:"planActions,omitempty"`     // what can be done with Plan, from its task output
	DraftPR         bool         `json:"draftPR,omitempty"`         // PR is a GitHub draft (promotable)
	Fixes           []int        `json:"fixes,omitempty"`           // issue numbers this PR closes
	Sandbox         *WorkSandbox `json:"sandbox,omitempty"`
	UpdatedAt       string       `json:"updatedAt,omitempty"`
	// PlanSession and TriageSession are the agent sessions of the last
	// plan and triage runs, to watch while they run and continue after.
	PlanSession   *TaskSession `json:"planSession,omitempty"`
	TriageSession *TaskSession `json:"triageSession,omitempty"`
	// ReviewSession is a PR's review session (the review recipe's), for
	// Continue session and Update review.
	ReviewSession *TaskSession `json:"reviewSession,omitempty"`
	// FixSession is the fix's session (the fix recipe's), on the issue's
	// row and on the PR it opened: watch while it runs, Continue session
	// after, and its revises (Iterate, Address comments, Fix CI).
	FixSession *TaskSession `json:"fixSession,omitempty"`
	// FixRevises are the fix run's revises, the PR row's follow-ups.
	FixRevises []WorkAction `json:"fixRevises,omitempty"`
	// Mine is a PR the member authored; MyPR one they authored whose head
	// is on their fork, which a recipe may push to.
	Mine bool `json:"mine,omitempty"`
	MyPR bool `json:"myPR,omitempty"`
	// Sessions are the runs recorded on the item's sandboxes, whichever
	// recipes they are, newest first: what the row's chips, attention and
	// session links follow.
	Sessions []RunSession `json:"sessions,omitempty"`
	// Recipes are the recipes the row starts, in the board's order: its
	// launch buttons. An issue with an open PR from its fix has none; the
	// PR row carries the work from there.
	Recipes []RowRecipe `json:"recipes,omitempty"`
	// Launching are the recipes clicked on the row whose runs have not
	// started yet: recipe → starting, or queued behind the board's limit.
	Launching map[string]string `json:"launching,omitempty"`
	// ReviewPending is a pending review of the member's on the PR, on
	// GitHub, to finalize there; Reviewed one they submitted, with no new
	// request for another.
	ReviewPending bool `json:"reviewPending,omitempty"`
	Reviewed      bool `json:"reviewed,omitempty"`
}

// RowRecipe is a recipe a row starts: its launch button.
type RowRecipe struct {
	Name  string `json:"name"`
	Label string `json:"label"`
	// Inputs are the inputs a launch must be given: required, with no
	// default.
	Inputs []string `json:"inputs,omitempty"`
}

// RunSession is one recipe run on an item: its recipe, the session its
// revises go into, its state, and its stored task output's applied
// actions.
type RunSession struct {
	Recipe string `json:"recipe,omitempty"`
	// Label is the recipe's, from the board's catalog.
	Label   string `json:"label,omitempty"`
	Sandbox string `json:"sandbox"`
	// Task is the run's agent session (a revise's is the one it revised
	// in), for #/task-session/<sandbox>/<task>.
	Task string `json:"task"`
	// Run is the annotation the run is recorded under.
	Run   string `json:"run"`
	Kind  string `json:"kind,omitempty"`
	State string `json:"state,omitempty"`
	// Status is what the row makes of the run, the same for every recipe:
	// running; ready (ended with a draft nothing but edit was applied to:
	// the member's move); done (ended, its draft applied, or none); or
	// failed.
	Status    string `json:"status"`
	StartedAt string `json:"startedAt,omitempty"`
	EndedAt   string `json:"endedAt,omitempty"`
	// Output is whether the board stores a task output of the run.
	Output bool `json:"output,omitempty"`
	// Applied is the actions applied to the output: action → RFC3339.
	Applied map[string]string `json:"applied,omitempty"`
	// Revises are the revise ids the run's recipe offers into its session.
	Revises []string `json:"revises,omitempty"`
}

// TaskSession names a factory task's agent session: the sandbox it ran in
// and the task, whose id is the session's.
type TaskSession struct {
	Sandbox string `json:"sandbox"`
	Task    string `json:"task"`
}
