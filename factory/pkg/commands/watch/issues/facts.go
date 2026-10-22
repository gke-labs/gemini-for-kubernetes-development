package issues

import (
	"context"
	"fmt"

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
// read the same. A linked PR is the scan's open-PR cache's, or the
// issue's timeline's (a PR opened since the cache was filled).
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

	// As a scan: the workflow the issue names is identified first (by
	// its path's convention), its task and rule named after it, and then
	// checked.
	wf := identifyWorkflow(ctx, s.gh, issue)
	rule := "fix"
	if wf.Name != "" {
		rule = "workflow"
	}
	it.Workflow = wf.Name
	filename := issueTaskFilename(num, wf.Name)
	s.fillRuns(&it, rule, filename)
	if s.queue.TaskExists(filename) {
		it.Queued = append(it.Queued, rule)
	}
	s.fillRuleTaskRuns(&it, num)
	switch wf = wf.check(ctx, s.gh); wf.Status {
	case common.WorkflowMissing, common.WorkflowUnusable:
		it.WorkflowMissing = true
	case common.WorkflowUnresolved:
		return it, env, fmt.Errorf("fetching its workflow %s: %v", wf.Path, wf.Err)
	case common.WorkflowFound:
		it.WorkflowCooldown = common.GetWorkflowCooldown(ctx, s.gh, wf.Path)
	}
	it.TaskActive = s.queue.HasActiveIssueTask(num)

	linked := s.entities.GetReferencedIssuesMap()[num]
	if !linked && rule == "fix" {
		if linked, err = s.gh.HasLinkedPR(ctx, num); err != nil {
			return it, env, fmt.Errorf("checking the linked PR: %w", err)
		}
	}
	if linked {
		it.LinkedPR = "open"
	}
	if err := s.fillSpoken(ctx, &it, num); err != nil {
		return it, env, fmt.Errorf("reading the comments: %w", err)
	}
	return it, env, nil
}
