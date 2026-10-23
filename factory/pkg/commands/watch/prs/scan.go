package prs

import (
	"context"

	githubv39 "github.com/google/go-github/v39/github"
	"k8s.io/klog/v2"
)

// scanCandidates lists every pull request the watcher is responsible for: those
// assigned to a bot in the pool, those carrying the trigger label, and those
// carrying a label the rules are listed by (Config.ListingLabels).
//
// Both queries are fully paginated, which is what makes this the sweep's
// listing rather than the fast pass's. The two sets overlap heavily - a pull
// request opened for a labelled issue is usually both assigned and labelled -
// so the result is deduplicated by number. That also guarantees each pull
// request is evaluated at most once per cycle.
//
// Issue-typed items are dropped: GitHub's issue endpoints return issues and
// pull requests together, and the issues belong to the issue scanner.
func (s *Scanner) scanCandidates(ctx context.Context) ([]*githubv39.Issue, error) {
	var all []*githubv39.Issue
	var firstErr error

	if s.listings != nil {
		// The shared snapshot is these listings, kept up to date; on a failed
		// refresh it is the last good one.
		err := s.listings.Refresh(ctx)
		for _, botUser := range s.cfg.BotUsers {
			all = append(all, prsOnly(s.listings.Assigned(botUser))...)
		}
		all = append(all, prsOnly(s.listings.Labelled())...)
		all = append(all, prsOnly(s.listings.RuleLabelled())...)
		return dedupe(all), err
	}

	for _, botUser := range s.cfg.BotUsers {
		opts := &githubv39.IssueListByRepoOptions{
			Assignee:    botUser,
			State:       "open",
			ListOptions: githubv39.ListOptions{PerPage: 100},
		}
		for {
			page, resp, err := s.gh.ListIssues(ctx, opts)
			if err != nil {
				klog.Errorf("Failed to list PR issues for assignee %s: %v", botUser, err)
				if firstErr == nil {
					firstErr = err
				}
				break
			}
			for _, item := range page {
				if item.PullRequestLinks != nil {
					all = append(all, item)
				}
			}
			if resp == nil || resp.NextPage == 0 {
				break
			}
			opts.Page = resp.NextPage
		}
	}

	labelOpts := &githubv39.IssueListByRepoOptions{
		Labels:      []string{s.cfg.TriggerLabel},
		State:       "open",
		ListOptions: githubv39.ListOptions{PerPage: 100},
	}
	for {
		page, resp, err := s.gh.ListIssues(ctx, labelOpts)
		if err != nil {
			klog.Errorf("Failed to list PR issues for label %s: %v", s.cfg.TriggerLabel, err)
			if firstErr == nil {
				firstErr = err
			}
			break
		}
		for _, item := range page {
			if item.PullRequestLinks != nil {
				all = append(all, item)
			}
		}
		if resp == nil || resp.NextPage == 0 {
			break
		}
		labelOpts.Page = resp.NextPage
	}

	// The labels the rules are listed by, in one query (an OR of them).
	if len(s.cfg.ListingLabels) > 0 {
		got, err := s.gh.OpenPRsLabelled(ctx, s.cfg.ListingLabels)
		if err != nil {
			klog.Errorf("Failed to list PRs labelled %v: %v", s.cfg.ListingLabels, err)
			if firstErr == nil {
				firstErr = err
			}
		}
		all = append(all, got...)
	}

	return dedupe(all), firstErr
}

// scanAssigned lists the pull requests currently assigned to the bot pool.
//
// Assignment is how a pull request is claimed, so this set is exactly the one
// with work in flight - the one whose CI results, review feedback and merge
// state change between sweeps. Each query is a single page sorted by update
// time, which is what makes it cheap enough to run every interval; anything
// that falls off the first page has not been touched recently and is picked up
// by the next sweep.
func (s *Scanner) scanAssigned(ctx context.Context) ([]*githubv39.Issue, error) {
	var all []*githubv39.Issue
	var firstErr error

	if s.listings != nil {
		// Each bot's first page, as the listing below makes it, read out of
		// the shared snapshot.
		err := s.listings.Refresh(ctx)
		for _, botUser := range s.cfg.BotUsers {
			all = append(all, prsOnly(s.listings.AssignedFirst(botUser, s.cfg.ScanLimit))...)
		}
		return dedupe(all), err
	}

	for _, botUser := range s.cfg.BotUsers {
		page, _, err := s.gh.ListIssues(ctx, &githubv39.IssueListByRepoOptions{
			Assignee:    botUser,
			State:       "open",
			Sort:        "updated",
			Direction:   "desc",
			ListOptions: githubv39.ListOptions{PerPage: s.cfg.ScanLimit},
		})
		if err != nil {
			klog.Errorf("Failed to list PRs for assignee %s: %v", botUser, err)
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		for _, item := range page {
			if item.PullRequestLinks != nil {
				all = append(all, item)
			}
		}
	}

	return dedupe(all), firstErr
}

// prsOnly drops the issues from a listing.
func prsOnly(items []*githubv39.Issue) []*githubv39.Issue {
	var out []*githubv39.Issue
	for _, item := range items {
		if item.PullRequestLinks != nil {
			out = append(out, item)
		}
	}
	return out
}

// dedupe collapses the overlapping listings into one entry per pull request.
func dedupe(items []*githubv39.Issue) []*githubv39.Issue {
	unique := make(map[int]*githubv39.Issue, len(items))
	var out []*githubv39.Issue
	for _, item := range items {
		num := item.GetNumber()
		if _, seen := unique[num]; seen {
			continue
		}
		unique[num] = item
		out = append(out, item)
	}
	return out
}
