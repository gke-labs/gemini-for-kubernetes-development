package github

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	githubv39 "github.com/google/go-github/v39/github"
)

type PullRequestReview struct {
	review *githubv39.PullRequestReview

	PullRequestComments []PullRequestComment
}

func (r *PullRequestReview) ID() int64 {
	return r.review.GetID()
}

func (r *PullRequestReview) NodeID() string {
	return r.review.GetNodeID()
}

func (r *PullRequestReview) UserLogin() string {
	return r.review.GetUser().GetLogin()
}

func (r *PullRequestReview) Body() string {
	return r.review.GetBody()
}

func (r *PullRequestReview) SubmittedAt() time.Time {
	return r.review.GetSubmittedAt()
}

func (r *PullRequestReview) AuthorAssociation() string {
	return r.review.GetAuthorAssociation()
}

type PullRequestComment struct {
	comment *githubv39.PullRequestComment
}

func (c *PullRequestComment) ID() int64 {
	return c.comment.GetID()
}

func (c *PullRequestComment) NodeID() string {
	return c.comment.GetNodeID()
}

func (c *PullRequestComment) PullRequestReviewID() int64 {
	return c.comment.GetPullRequestReviewID()
}

func (c *PullRequestComment) UserLogin() string {
	return c.comment.GetUser().GetLogin()
}

func (c *PullRequestComment) AuthorAssociation() string {
	return c.comment.GetAuthorAssociation()
}

func (c *PullRequestComment) Body() string {
	return c.comment.GetBody()
}

func (c *PullRequestComment) Path() string {
	return c.comment.GetPath()
}

func (c *PullRequestComment) DiffHunk() string {
	return AnnotateDiffHunk(c.comment.GetDiffHunk())
}

func AnnotateDiffHunk(diffHunk string) string {
	lines := strings.Split(diffHunk, "\n")
	var annotatedLines []string

	currentOldLine := 0
	currentNewLine := 0
	// Regex to parse the hunk header: @@ -oldStart,oldLen +newStart,newLen @@
	// This is how we get line numbers
	headerRegex := regexp.MustCompile(`^@@ \-(\d+)(?:,\d+)? \+(\d+)(?:,\d+)? @@`)

	for _, line := range lines {
		if strings.HasPrefix(line, "@@") {
			matches := headerRegex.FindStringSubmatch(line)
			if len(matches) == 3 {
				oldStart, err := strconv.Atoi(matches[1])
				if err == nil {
					currentOldLine = oldStart
				}
				newStart, err := strconv.Atoi(matches[2])
				if err == nil {
					currentNewLine = newStart
				}
			}
			continue
		}

		if currentNewLine == 0 && currentOldLine == 0 {
			// If we haven't seen a header yet, just print the line
			annotatedLines = append(annotatedLines, line)
			continue
		}

		if strings.HasPrefix(line, " ") {
			// Context line
			annotatedLines = append(annotatedLines, fmt.Sprintf("%4d: %s", currentNewLine, line))
			currentOldLine++
			currentNewLine++
		} else if strings.HasPrefix(line, "+") {
			// Added line
			annotatedLines = append(annotatedLines, fmt.Sprintf("%4d: %s", currentNewLine, line))
			currentNewLine++
		} else if strings.HasPrefix(line, "-") {
			// Deleted line - show placeholder for line number
			annotatedLines = append(annotatedLines, fmt.Sprintf("....: %s", line))
			currentOldLine++
		} else {
			// Other (e.g. \ No newline at end of file)
			annotatedLines = append(annotatedLines, fmt.Sprintf(".... : %s", line))
		}
	}

	if len(annotatedLines) > 4 {
		// Only show last 4 lines to avoid too much verbosity - github seems to end the chunk at the "right spot"
		annotatedLines = annotatedLines[len(annotatedLines)-4:]
	}
	return strings.Join(annotatedLines, "\n")
}
