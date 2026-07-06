package watch

import (
	"context"
	"fmt"
	"strings"
	"time"

	githubv39 "github.com/google/go-github/v39/github"
	"k8s.io/klog/v2"
)

func (w *watchContext) scan(ctx context.Context) {
	now := time.Now()

	// Determine what to run
	runIssueScan := false
	if w.opts.Mode == "all" || w.opts.Mode == "scan" || w.opts.Mode == "scan-issue" {
		if w.state.lastIssueScan.IsZero() || now.Sub(w.state.lastIssueScan) >= 30*time.Second {
			runIssueScan = true
		}
	}

	runPRScan := false
	if w.opts.Mode == "all" || w.opts.Mode == "scan" || w.opts.Mode == "scan-pr" {
		if w.state.lastPRScan.IsZero() || now.Sub(w.state.lastPRScan) >= 5*time.Minute {
			runPRScan = true
		}
	}

	w.state.mu.Lock()
	refIssues := make(map[int]bool)
	for k, v := range w.state.referencedIssues {
		refIssues[k] = v
	}
	hasPRs := len(w.state.openPRs) > 0 || !w.state.lastPRScan.IsZero()
	w.state.mu.Unlock()

	// Populate PR cache once on startup if needed by issue scan
	if !hasPRs && runIssueScan {
		klog.Infof("Populating open PRs cache for referenced issues...")
		prOpts := &githubv39.PullRequestListOptions{
			State:       "open",
			ListOptions: githubv39.ListOptions{PerPage: 100},
		}
		prs, _, err := w.ghClient.PullRequests.List(ctx, w.opts.Owner, w.opts.Repo, prOpts)
		if err == nil {
			w.state.mu.Lock()
			w.state.openPRs = prs
			w.state.referencedIssues = make(map[int]bool)
			for _, pr := range prs {
				for num := range getReferencedIssues(pr) {
					w.state.referencedIssues[num] = true
					refIssues[num] = true
				}
			}
			w.state.mu.Unlock()
		} else {
			klog.Errorf("Failed to populate open PRs cache: %v", err)
		}
	}

	// 1. Slow PR Scan Cycle
	if runPRScan {
		klog.Infof("Running slow PR scan cycle...")
		prOpts := &githubv39.PullRequestListOptions{
			State:       "open",
			ListOptions: githubv39.ListOptions{PerPage: 100},
		}
		prs, _, err := w.ghClient.PullRequests.List(ctx, w.opts.Owner, w.opts.Repo, prOpts)
		if err == nil {
			w.state.mu.Lock()
			w.state.openPRs = prs
			w.state.referencedIssues = make(map[int]bool)
			for _, pr := range prs {
				for num := range getReferencedIssues(pr) {
					w.state.referencedIssues[num] = true
					refIssues[num] = true
				}
			}
			w.state.lastPRScan = now
			w.state.mu.Unlock()
		} else {
			klog.Errorf("Failed to list open PRs: %v", err)
		}

		// Scan issues labeled with triggerLabel (handling pagination)
		var slowIssues []*githubv39.Issue
		opts2 := &githubv39.IssueListByRepoOptions{
			Labels:      []string{w.triggerLabel},
			State:       "open",
			ListOptions: githubv39.ListOptions{PerPage: 100},
		}
		for {
			pageIssues, resp, err := w.ghClient.Issues.ListByRepo(ctx, w.opts.Owner, w.opts.Repo, opts2)
			if err != nil {
				klog.Errorf("Failed to list issues for label %s: %v", w.triggerLabel, err)
				break
			}
			for _, item := range pageIssues {
				if item.PullRequestLinks == nil {
					slowIssues = append(slowIssues, item)
				}
			}
			if resp.NextPage == 0 {
				break
			}
			opts2.Page = resp.NextPage
		}

		// Process slow issues
		if w.opts.IssueMode != "disabled" {
			queueIssueTasks(ctx, w.ghClient, w.kubeClient, w.cfg, w.opts.Owner, w.opts.Repo, slowIssues, w.processedIssues, refIssues, w.targetAssignee, w.allBotUsers, w.incomingDir, w.processingDir, w.processedDir, w.opts.QueueDir, w.opts.DryRun, w.triggerLabel, w.opts.Namespace)
		}

		// Process Pull Requests (Scanner)
		var prIssues []*githubv39.Issue
		var allPRIssues []*githubv39.Issue
		for _, botUser := range w.allBotUsers {
			opts1 := &githubv39.IssueListByRepoOptions{
				Assignee:    botUser,
				State:       "open",
				ListOptions: githubv39.ListOptions{PerPage: 100},
			}
			iss1, _, err := w.ghClient.Issues.ListByRepo(ctx, w.opts.Owner, w.opts.Repo, opts1)
			if err == nil {
				for _, item := range iss1 {
					if item.PullRequestLinks != nil {
						allPRIssues = append(allPRIssues, item)
					}
				}
			}
		}
		opts2PR := &githubv39.IssueListByRepoOptions{
			Labels:      []string{w.triggerLabel},
			State:       "open",
			ListOptions: githubv39.ListOptions{PerPage: 100},
		}
		iss2, _, err := w.ghClient.Issues.ListByRepo(ctx, w.opts.Owner, w.opts.Repo, opts2PR)
		if err == nil {
			for _, item := range iss2 {
				if item.PullRequestLinks != nil {
					allPRIssues = append(allPRIssues, item)
				}
			}
		}

		// Deduplicate allPRIssues
		uniquePRIssues := make(map[int]*githubv39.Issue)
		for _, item := range allPRIssues {
			uniquePRIssues[item.GetNumber()] = item
		}
		for _, item := range uniquePRIssues {
			prIssues = append(prIssues, item)
		}

		w.processPRs(ctx, prIssues)

		// Scan chores
		if (w.opts.Mode == "all" || w.opts.Mode == "scan" || w.opts.Mode == "scan-pr") && w.opts.ChoresMode != "disabled" {
			scanChores(ctx, w.ghClient, w.opts.Owner, w.opts.Repo, w.incomingDir, w.processingDir, w.opts.QueueDir, w.opts.DryRun)
		}

		// Clean up sandboxes of merged or closed PRs
		if err := cleanupClosedPRSandboxes(ctx, w.ghClient, w.kubeClient, w.opts.Owner, w.opts.Repo, w.opts.Namespace, w.opts.DryRun); err != nil {
			klog.Errorf("Failed to clean up closed PR sandboxes: %v", err)
		}

		// Clean up sandboxes of closed issues
		if err := cleanupClosedIssueSandboxes(ctx, w.ghClient, w.kubeClient, w.opts.Owner, w.opts.Repo, w.opts.Namespace, w.opts.DryRun); err != nil {
			klog.Errorf("Failed to clean up closed issue sandboxes: %v", err)
		}
	}

	// 2. Fast Issue Scan Cycle
	if runIssueScan {
		klog.Infof("Running fast issue scan cycle...")
		var allItems []*githubv39.Issue

		limit := w.opts.ScanLimit
		if limit <= 0 {
			limit = 30
		}

		for _, botUser := range w.allBotUsers {
			opts1 := &githubv39.IssueListByRepoOptions{
				Assignee:    botUser,
				State:       "open",
				Sort:        "updated",
				Direction:   "desc",
				ListOptions: githubv39.ListOptions{PerPage: limit},
			}
			issues1, _, err := w.ghClient.Issues.ListByRepo(ctx, w.opts.Owner, w.opts.Repo, opts1)
			if err != nil {
				klog.Errorf("Failed to list issues for assignee %s: %v", botUser, err)
			} else {
				klog.Infof("Fetched %d issues assigned to %s from GitHub API", len(issues1), botUser)
				allItems = append(allItems, issues1...)
			}
		}

		if w.githubLogin != "" {
			optsCreator := &githubv39.IssueListByRepoOptions{
				Creator:     w.githubLogin,
				State:       "open",
				Sort:        "updated",
				Direction:   "desc",
				ListOptions: githubv39.ListOptions{PerPage: limit},
			}
			issuesCreator, _, err := w.ghClient.Issues.ListByRepo(ctx, w.opts.Owner, w.opts.Repo, optsCreator)
			if err != nil {
				klog.Errorf("Failed to list issues created by %s: %v", w.githubLogin, err)
			} else {
				klog.Infof("Fetched %d issues created by %s from GitHub API", len(issuesCreator), w.githubLogin)
				for _, issue := range issuesCreator {
					if issue.PullRequestLinks != nil {
						continue
					}

					hasTriggerLabel := false
					for _, l := range issue.Labels {
						if strings.EqualFold(l.GetName(), w.triggerLabel) {
							hasTriggerLabel = true
							break
						}
					}

					hasAssignee := false
					for _, u := range issue.Assignees {
						for _, bot := range w.allBotUsers {
							if strings.EqualFold(u.GetLogin(), bot) {
								hasAssignee = true
								break
							}
						}
						if hasAssignee {
							break
						}
					}

					if !hasTriggerLabel || !hasAssignee {
						if w.opts.DryRun {
							fmt.Printf("[DRYRUN] Would label issue #%d created by %s with '%s' and assign to %s\n", issue.GetNumber(), w.githubLogin, w.triggerLabel, w.targetAssignee)
						} else {
							fmt.Printf("Labelling issue #%d created by %s with '%s' and assigning to %s...\n", issue.GetNumber(), w.githubLogin, w.triggerLabel, w.targetAssignee)
							if !hasTriggerLabel {
								if _, _, err := w.ghClient.Issues.AddLabelsToIssue(ctx, w.opts.Owner, w.opts.Repo, issue.GetNumber(), []string{w.triggerLabel}); err != nil {
									klog.Errorf("Failed to add label '%s' to issue #%d: %v", w.triggerLabel, issue.GetNumber(), err)
								} else {
									issue.Labels = append(issue.Labels, &githubv39.Label{Name: githubv39.String(w.triggerLabel)})
								}
							}
							if !hasAssignee && w.targetAssignee != "" {
								if _, _, err := w.ghClient.Issues.AddAssignees(ctx, w.opts.Owner, w.opts.Repo, issue.GetNumber(), []string{w.targetAssignee}); err != nil {
									klog.Errorf("Failed to assign %s to issue #%d: %v", w.targetAssignee, issue.GetNumber(), err)
								} else {
									issue.Assignees = append(issue.Assignees, &githubv39.User{Login: githubv39.String(w.targetAssignee)})
								}
							}
						}
					}
					allItems = append(allItems, issue)
				}
			}
		}

		uniqueIssues := make(map[int]*githubv39.Issue)
		for _, item := range allItems {
			uniqueIssues[item.GetNumber()] = item
		}

		var issues []*githubv39.Issue
		var fastPRIssues []*githubv39.Issue
		for _, item := range uniqueIssues {
			if item.PullRequestLinks == nil {
				issues = append(issues, item)
			} else {
				fastPRIssues = append(fastPRIssues, item)
			}
		}

		if w.opts.IssueMode != "disabled" {
			queueIssueTasks(ctx, w.ghClient, w.kubeClient, w.cfg, w.opts.Owner, w.opts.Repo, issues, w.processedIssues, refIssues, w.targetAssignee, w.allBotUsers, w.incomingDir, w.processingDir, w.processedDir, w.opts.QueueDir, w.opts.DryRun, w.triggerLabel, w.opts.Namespace)
		}

		// Process PRs assigned to the bot in the fast cycle
		if len(fastPRIssues) > 0 {
			klog.Infof("Processing %d assigned PRs in fast cycle...", len(fastPRIssues))
			w.processPRs(ctx, fastPRIssues)
		}

		w.state.mu.Lock()
		w.state.lastIssueScan = now
		w.state.mu.Unlock()
	}
}
