package prs

import (
	"context"
	"fmt"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/watch/rules"
)

// Facts reads one pull request's facts, every one the rules read, for a
// decision outside a scan: the dispatcher's at start, or factory watch
// rules explain. It writes nothing - no label sync, no pause, no state -
// and, unlike a scan, does not stop at an active task: the caller decides
// as if its own task were not there. It stops where nothing more can
// decide: a gate, or a PR whose author no rule admits (factory's need
// author: pool).
//
// Each fact comes from the function the scan records it with, so the two
// read the same; a scan only reads them lazily, in the order it decides.
func (s *Scanner) Facts(ctx context.Context, num int) (rules.Item, rules.Env, error) {
	env := s.rulesEnv()
	prIssue, err := s.gh.GetIssue(ctx, num)
	if err != nil {
		return rules.Item{}, env, err
	}
	if prIssue.PullRequestLinks == nil {
		return rules.Item{}, env, fmt.Errorf("#%d is an issue, not a pull request", num)
	}
	it := prItemOf(prIssue)
	if _, gated := rules.GateOf(it, env); gated {
		return it, env, nil
	}
	pr, err := s.gh.GetPullRequest(ctx, num)
	if err != nil {
		return it, env, fmt.Errorf("reading the PR: %w", err)
	}
	fillPR(&it, pr)
	if it.MergeQueue, err = s.gh.IsInMergeQueue(ctx, num); err != nil {
		return it, env, fmt.Errorf("checking the merge queue: %w", err)
	}
	if it.MergeQueue || !s.rules.Set().AdmitsAuthor(it, env) {
		return it, env, nil
	}
	it.Closes = closesOf(ctx, newRefIssues(s.gh, pr))
	s.fillRuns(&it, num)
	it.TaskActive = s.queue.HasActivePRTask(num)

	history, err := s.fetchHistory(ctx, num)
	if err != nil {
		return it, env, fmt.Errorf("reading the PR's history: %w", err)
	}
	head := pr.GetHead().GetSHA()
	s.fillHistory(&it, pr, history, head)
	it.NewTrustedFeedback = s.evaluateComments(ctx, pr, history, history.lastCommitTime, s.state.get(num).lastCommentAddressedTime).hasNewComments
	// As a scan: the checks and approval of a PR that does not merge are
	// not read (nothing but conflict decides on it), and so unknown.
	if pr.Mergeable == nil || *pr.Mergeable {
		checks, err := s.evaluateChecks(ctx, head)
		if err != nil {
			return it, env, fmt.Errorf("reading the checks: %w", err)
		}
		fillChecks(&it, checks, isPRApprovedOrLGTM(pr, prIssue, history.reviews))
	} else {
		it.Unknown = []string{rules.FactChecks}
	}
	return it, env, nil
}
