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
	Name      string `json:"name"`
	Replicas  string `json:"replicas"`
	TaskState string `json:"taskState,omitempty"`
}

// WorkItem is one row of the board work feed: an issue or PR merged with
// its agent/sandbox state.
type WorkItem struct {
	Type      string       `json:"type"`            // issue | pr
	Group     string       `json:"group,omitempty"` // review | fix | mine-pr | mine-issue
	Number    int          `json:"number"`
	Title     string       `json:"title"`
	HTMLURL   string       `json:"htmlURL"`
	Stage     string       `json:"stage"`
	Attention string       `json:"attention,omitempty"` // needs-you | working | waiting
	Assignee  string       `json:"assignee,omitempty"`
	Author    string       `json:"author,omitempty"` // PR author (review rows show it as a chip)
	PRURL     string       `json:"prURL,omitempty"`
	Labels    []string     `json:"labels,omitempty"`
	Draft     string       `json:"draft,omitempty"`   // triage/review draft, when ready
	Error     string       `json:"error,omitempty"`   // why the last agent run failed, human-readable
	Plan      string       `json:"plan,omitempty"`    // implementation-plan draft awaiting refine/approve
	DraftPR   bool         `json:"draftPR,omitempty"` // PR is a GitHub draft (promotable)
	Fixes     []int        `json:"fixes,omitempty"`   // issue numbers this PR closes
	Sandbox   *WorkSandbox `json:"sandbox,omitempty"`
	UpdatedAt string       `json:"updatedAt,omitempty"`
}
