package prs

import (
	"context"
	"fmt"
	"time"

	githubv39 "github.com/google/go-github/v39/github"
)

// prCheckAnalysis is the verdict on a commit's CI.
//
// The earliest failure is singled out rather than the latest because CI
// failures cascade: a compile error fails the build, which fails the tests,
// which fails the e2e job. The first thing to go wrong is the one worth
// investigating, and the rest are usually its consequences.
type prCheckAnalysis struct {
	hasFailure                bool
	hasPending                bool
	earliestFailureTime       time.Time
	earliestFailureName       string
	earliestFailureConclusion string
	failedCount               int
	checkRuns                 []*githubv39.CheckRun
}

// evaluateChecks reads the CI verdict for a commit.
//
// Both the Checks API and the older commit status API are consulted, because a
// repository can be using either or both and a failure reported through the one
// that was not read would look like a green build.
//
// A listing error is returned so the caller can abort the evaluation rather
// than mistaking a failed read for a green commit.
func (s *Scanner) evaluateChecks(ctx context.Context, headSHA string) (prCheckAnalysis, error) {
	var analysis prCheckAnalysis

	checkRuns, err := s.gh.ListCheckRuns(ctx, headSHA)
	if err != nil {
		return analysis, fmt.Errorf("listing check runs for %s: %w", headSHA, err)
	}
	analysis.checkRuns = checkRuns
	for _, run := range checkRuns {
		if run.GetStatus() != "completed" {
			analysis.hasPending = true
		}
		c := run.GetConclusion()
		if c == "failure" || c == "timed_out" || c == "cancelled" || c == "action_required" || c == "stale" {
			analysis.hasFailure = true
			analysis.failedCount++
			t := run.GetCompletedAt().Time
			if t.IsZero() {
				t = run.GetStartedAt().Time
			}
			if !t.IsZero() {
				if analysis.earliestFailureTime.IsZero() || t.Before(analysis.earliestFailureTime) {
					analysis.earliestFailureTime = t
					analysis.earliestFailureName = run.GetName()
					analysis.earliestFailureConclusion = c
				}
			} else if analysis.earliestFailureName == "" {
				// A check with no timestamps at all still names the
				// failure, which is better than reporting 'unknown check'.
				analysis.earliestFailureName = run.GetName()
				analysis.earliestFailureConclusion = c
			}
		}
	}

	statuses, err := s.gh.ListStatuses(ctx, headSHA)
	if err != nil {
		return analysis, fmt.Errorf("listing commit statuses for %s: %w", headSHA, err)
	}
	for _, status := range statuses {
		if status.GetState() == "pending" {
			analysis.hasPending = true
		}
		if status.GetState() == "failure" || status.GetState() == "error" {
			analysis.hasFailure = true
			analysis.failedCount++
			t := status.GetUpdatedAt()
			if t.IsZero() {
				t = status.GetCreatedAt()
			}
			if !t.IsZero() {
				if analysis.earliestFailureTime.IsZero() || t.Before(analysis.earliestFailureTime) {
					analysis.earliestFailureTime = t
					analysis.earliestFailureName = status.GetContext()
					analysis.earliestFailureConclusion = status.GetState()
				}
			} else if analysis.earliestFailureName == "" {
				analysis.earliestFailureName = status.GetContext()
				analysis.earliestFailureConclusion = status.GetState()
			}
		}
	}

	return analysis, nil
}
