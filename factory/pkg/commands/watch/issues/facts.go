package issues

import (
	"context"
	"fmt"
	"slices"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/commands/common"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/watch/rules"
)

// Facts reads one issue's facts, every one the rules read, for a decision
// outside a scan: the dispatcher's at start, or factory watch rules
// explain. It writes nothing - no trigger label, no stop, no cache - and,
// unlike a scan, does not stop at an active task or a cooldown: the caller
// decides as if its own task were not there. It stops where nothing more
// can decide: a gate, or a fan-out parent.
//
// Each fact comes from the function the scan records it with, so the two
// read the same. A linked PR is the scan's open-PR cache's; a connected PR,
// the issue's timeline's, read only for a rule that asks.
func (s *Scanner) Facts(ctx context.Context, num int) (rules.Item, rules.Env, error) {
	env := s.rulesEnv()
	issue, err := s.gh.GetIssue(ctx, num)
	if err != nil {
		return rules.Item{}, env, err
	}
	if issue.PullRequestLinks != nil {
		return rules.Item{}, env, fmt.Errorf("#%d is a pull request, not an issue", num)
	}
	it := s.issueItemOf(issue)
	if _, gated := rules.GateOf(it, env); gated || it.FanoutParent {
		return it, env, nil
	}

	// As a scan: the facts the listing and the queue give, then each fact
	// a rule that could decide needs (Set.Needs) - asked as if no task of
	// the issue were there, as the caller decides.
	wf := identifyWorkflow(ctx, s.gh, issue)
	it.Workflow = wf.Name
	s.fillIssueRuns(&it, num, wf.Name)
	it.TaskActive = s.queue.HasActiveIssueTask(num)
	if s.entities.GetReferencedIssuesMap()[num] {
		it.LinkedPR = "open"
	}
	it.Unknown = []string{rules.FactConnectedPR}
	if wf.Name != "" {
		it.Unknown = []string{rules.FactWorkflowCooldown, rules.FactWorkflowDefinition, rules.FactConnectedPR}
	}
	for {
		asked := it
		asked.TaskActive, asked.Queued = false, nil
		needs := s.needsSet().Needs(asked, env)
		if len(needs) == 0 {
			break
		}
		switch needs[0] {
		case rules.FactWorkflowCooldown:
			if it.WorkflowCooldown, err = common.GetWorkflowCooldown(ctx, s.gh, wf.Path); err != nil {
				return it, env, err
			}
		case rules.FactWorkflowDefinition:
			switch wf = wf.check(ctx, s.gh); wf.Status {
			case common.WorkflowMissing, common.WorkflowUnusable:
				it.WorkflowMissing = true
			case common.WorkflowUnresolved:
				return it, env, fmt.Errorf("fetching its workflow %s: %v", wf.Path, wf.Err)
			}
		case rules.FactConnectedPR:
			connected, err := s.gh.HasLinkedPR(ctx, num)
			if err != nil {
				return it, env, fmt.Errorf("checking the linked PR: %w", err)
			}
			if connected {
				it.ConnectedPR = "open"
			}
		}
		it.Unknown = slices.DeleteFunc(it.Unknown, func(f string) bool { return f == needs[0] })
	}
	if err := s.fillSpoken(ctx, &it, num); err != nil {
		return it, env, fmt.Errorf("reading the comments: %w", err)
	}
	return it, env, nil
}
