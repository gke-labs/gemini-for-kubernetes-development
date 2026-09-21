package prs

import (
	"context"

	githubv39 "github.com/google/go-github/v39/github"
	"k8s.io/klog/v2"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/commands/common"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/github"
)

// refIssues resolves the issues a pull request closes, fetching each of them at
// most once.
//
// Three separate parts of one evaluation want those issues - the label sync,
// the review opt-in check, and the review task's prompt - and each of them used
// to fetch them itself. They ran within milliseconds of each other against the
// same pull request and got the same answer every time, so the second and third
// fetches bought nothing but rate limit consumption.
//
// The lifetime is deliberately one evaluation. A scanner-wide cache would be
// cheaper still, but it would also mean a label added to a parent issue took
// effect only once the entry expired, and the whole point of the label sync is
// that 'overseer/stop' on an issue stops work on its pull request promptly.
type refIssues struct {
	gh *github.Client
	pr *githubv39.PullRequest

	// resolved is the fetched issues, and loaded records that the fetch has
	// happened - a pull request that references nothing resolves to an empty
	// slice, which must not be mistaken for "not looked yet".
	resolved []*githubv39.Issue
	loaded   bool
}

// newRefIssues returns a resolver for the issues the pull request closes.
func newRefIssues(gh *github.Client, pr *githubv39.PullRequest) *refIssues {
	return &refIssues{gh: gh, pr: pr}
}

// all returns the issues the pull request closes, fetching them on first use.
//
// An issue that cannot be fetched is reported and left out rather than failing
// the lot: the callers each degrade sensibly on a partial answer, and a single
// unreadable parent issue is not a reason to stop evaluating the pull request.
// It is not retried within the evaluation, so one failure costs one request.
func (r *refIssues) all(ctx context.Context) []*githubv39.Issue {
	if r.loaded {
		return r.resolved
	}
	r.loaded = true

	if r.gh == nil || r.pr == nil {
		return r.resolved
	}
	for num := range common.GetReferencedIssues(r.pr) {
		issue, err := r.gh.GetIssue(ctx, num)
		if err != nil {
			klog.Warningf("Failed to fetch referenced parent issue #%d for PR #%d: %v", num, r.pr.GetNumber(), err)
			continue
		}
		r.resolved = append(r.resolved, issue)
	}
	return r.resolved
}
