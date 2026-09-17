package issues

import (
	"context"
	"fmt"
	"strings"

	githubv39 "github.com/google/go-github/v39/github"
	"k8s.io/klog/v2"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/commands/watch/conventions"
)

// scanLabelled lists every open issue carrying the trigger label, following
// pagination. It is the authoritative view of the work the repository has asked
// for, and the expensive half of scanning, which is why it runs on the sweep
// interval rather than on every cycle.
//
// Pull requests are dropped: GitHub's issue endpoints return them alongside
// issues, and they belong to the pull request scanner.
func (s *Scanner) scanLabelled(ctx context.Context) ([]*githubv39.Issue, error) {
	var labelled []*githubv39.Issue
	opts := &githubv39.IssueListByRepoOptions{
		Labels:      []string{s.cfg.TriggerLabel},
		State:       "open",
		ListOptions: githubv39.ListOptions{PerPage: 100},
	}
	for {
		pageIssues, resp, err := s.gh.ListIssues(ctx, opts)
		if err != nil {
			// Whatever was paged in so far is still returned: the caller queues
			// from it but must not publish it as the complete open set.
			return labelled, err
		}
		for _, item := range pageIssues {
			if item.PullRequestLinks == nil {
				labelled = append(labelled, item)
			}
		}
		if resp == nil || resp.NextPage == 0 {
			break
		}
		opts.Page = resp.NextPage
	}
	return labelled, nil
}

// scanAssigned lists the issues that are the watcher's business by ownership
// rather than by label: those assigned to a bot in the pool, and those the
// operator filed themselves.
//
// Each query is a single page sorted by update time, which is what makes this
// cheap enough to run every interval. An issue that falls off the first page
// has not been touched recently and is picked up by the labelled sweep.
func (s *Scanner) scanAssigned(ctx context.Context) ([]*githubv39.Issue, error) {
	var allItems []*githubv39.Issue

	for _, botUser := range s.cfg.BotUsers {
		assigned, _, err := s.gh.ListIssues(ctx, &githubv39.IssueListByRepoOptions{
			Assignee:    botUser,
			State:       "open",
			Sort:        "updated",
			Direction:   "desc",
			ListOptions: githubv39.ListOptions{PerPage: s.cfg.ScanLimit},
		})
		if err != nil {
			klog.Errorf("Failed to list issues for assignee %s: %v", botUser, err)
			continue
		}
		klog.Infof("Fetched %d issues assigned to %s from GitHub API", len(assigned), botUser)
		allItems = append(allItems, assigned...)
	}

	if s.cfg.GitHubLogin != "" {
		created, _, err := s.gh.ListIssues(ctx, &githubv39.IssueListByRepoOptions{
			Creator:     s.cfg.GitHubLogin,
			State:       "open",
			Sort:        "updated",
			Direction:   "desc",
			ListOptions: githubv39.ListOptions{PerPage: s.cfg.ScanLimit},
		})
		if err != nil {
			klog.Errorf("Failed to list issues created by %s: %v", s.cfg.GitHubLogin, err)
		} else {
			klog.Infof("Fetched %d issues created by %s from GitHub API", len(created), s.cfg.GitHubLogin)
			allItems = append(allItems, s.adoptCreatedIssues(ctx, created)...)
		}
	}

	unique := make(map[int]*githubv39.Issue)
	for _, item := range allItems {
		unique[item.GetNumber()] = item
	}

	issues := make([]*githubv39.Issue, 0, len(unique))
	for _, item := range unique {
		// Pull requests belong to the pull request scanner, which lists them
		// itself. Handing them over would couple the two subcontrollers for the
		// sake of an interval's latency.
		if item.PullRequestLinks == nil {
			issues = append(issues, item)
		}
	}
	return issues, nil
}

// adoptCreatedIssues labels and assigns the issues the operator filed, so that
// an issue they created is worked on without them having to label it, and
// returns the ones eligible for queueing.
//
// The local copy is updated to match what was written, so the caller's
// subsequent label and assignee checks see the adoption that just happened
// rather than the state GitHub returned before it.
func (s *Scanner) adoptCreatedIssues(ctx context.Context, created []*githubv39.Issue) []*githubv39.Issue {
	var adopted []*githubv39.Issue
	for _, issue := range created {
		if issue.PullRequestLinks != nil {
			continue
		}
		num := issue.GetNumber()
		if conventions.HasStopLabel(issue.Labels, s.cfg.TriggerLabel) {
			klog.Infof("Skipping auto labeling/assigning issue #%d because it has the stop label ('overseer/stop' or '%s/stop')", num, s.cfg.TriggerLabel)
			continue
		}

		hasTriggerLabel := conventions.HasTriggerLabel(issue.Labels, s.cfg.TriggerLabel)
		hasAssignee := s.assignedToPool(issue)

		if !hasTriggerLabel || !hasAssignee {
			if s.cfg.DryRun {
				fmt.Printf("[DRYRUN] Would label issue #%d created by %s with '%s' and assign to %s\n", num, s.cfg.GitHubLogin, s.cfg.TriggerLabel, s.cfg.TargetAssignee)
			} else {
				fmt.Printf("Labelling issue #%d created by %s with '%s' and assigning to %s...\n", num, s.cfg.GitHubLogin, s.cfg.TriggerLabel, s.cfg.TargetAssignee)
				if !hasTriggerLabel {
					if err := s.gh.AddLabels(ctx, num, []string{s.cfg.TriggerLabel}); err != nil {
						klog.Errorf("Failed to add label '%s' to issue #%d: %v", s.cfg.TriggerLabel, num, err)
					} else {
						issue.Labels = append(issue.Labels, &githubv39.Label{Name: githubv39.String(s.cfg.TriggerLabel)})
					}
				}
				if !hasAssignee && s.cfg.TargetAssignee != "" {
					if err := s.gh.AddAssignees(ctx, num, []string{s.cfg.TargetAssignee}); err != nil {
						klog.Errorf("Failed to assign %s to issue #%d: %v", s.cfg.TargetAssignee, num, err)
					} else {
						issue.Assignees = append(issue.Assignees, &githubv39.User{Login: githubv39.String(s.cfg.TargetAssignee)})
					}
				}
			}
		}
		adopted = append(adopted, issue)
	}
	return adopted
}

// assignedToPool reports whether any bot account is already assigned to the issue.
func (s *Scanner) assignedToPool(issue *githubv39.Issue) bool {
	for _, u := range issue.Assignees {
		for _, bot := range s.cfg.BotUsers {
			if strings.EqualFold(u.GetLogin(), bot) {
				return true
			}
		}
	}
	return false
}
