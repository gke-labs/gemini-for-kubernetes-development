/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package repoboard

import (
	"context"
	"fmt"
	"strings"

	"github.com/google/go-github/v39/github"
	"sigs.k8s.io/controller-runtime/pkg/log"

	boardv1alpha1 "github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/api/repoboard/v1alpha1"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/factorycli"
)

// Automation (spec.auto) is board policy: auto.triage, auto.fix and
// auto.review find their items on GitHub and file a recipe Request for
// each, as the board's owner (its namespace), the way a member's click
// files one. From there a run is a click's: ensureRecipes launches it,
// settleRecipe settles it, and its draft, failure and chip are any run's
// (generic-board-recipes.md).
//
// An item gets one Request, ever: not while one for the recipe stands or
// was settled lately, nor once the recipe has a run recorded on the
// item's sandbox, whoever ran it and however it ended (a failed run is
// not retried; a member clicks), nor once its draft was rejected (the
// tombstone every reject writes). A review is not filed over the owner's
// own review on GitHub, pending or submitted.

// autoRequest is one recipe automation wants run on an item.
type autoRequest struct {
	recipe string
	item   string
	number int
}

// discoverAuto is what the board's automation wants run, from GitHub.
func (r *Reconciler) discoverAuto(ctx context.Context, ghClient *github.Client, work *workState) []autoRequest {
	logger := log.FromContext(ctx)
	board := work.board
	var wanted []autoRequest
	add := func(found []autoRequest, err error, what string) {
		if err != nil {
			logger.Error(err, what+" discovery failed")
		}
		wanted = append(wanted, found...)
	}
	switch board.Spec.Auto.Review {
	case "all":
		found, err := r.discoverAllPRs(ctx, ghClient, work)
		add(found, err, "auto-review")
	case "requested":
		found, err := r.discoverRequestedReviews(ctx, ghClient, work)
		add(found, err, "requested-review")
	}
	if board.Spec.Auto.Triage == "unclaimed" || board.Spec.Auto.Triage == "all" {
		found, err := r.discoverTriage(ctx, ghClient, work)
		add(found, err, "triage")
	}
	if board.Spec.Auto.Fix == "assigned" {
		found, err := r.discoverAssigned(ctx, ghClient, work)
		add(found, err, "assigned-issue")
	}
	return wanted
}

// fileAutoRequests files a recipe Request, as the board's owner, for each
// item automation wants run that is owed one (autoOwed). The owner's
// reviews are read under the discovery identity, the owner's: pending
// reviews are visible only to their author.
func (r *Reconciler) fileAutoRequests(ctx context.Context, work *workState, wanted []autoRequest) {
	if len(wanted) == 0 {
		return
	}
	logger := log.FromContext(ctx)
	catalog := r.recipeCatalog(ctx)
	if catalog == nil {
		return
	}
	owner := work.board.Namespace
	seen := map[autoRequest]bool{}
	for _, want := range wanted {
		if seen[want] {
			continue
		}
		seen[want] = true
		rec, ok := findRecipe(catalog, want.recipe)
		if !ok || !startsOn(rec, want.item) {
			continue
		}
		spec := boardv1alpha1.RequestSpec{
			Board:  work.board.Name,
			Verb:   boardv1alpha1.VerbRecipe,
			Recipe: want.recipe,
			Item:   want.item,
			Number: want.number,
			Member: owner,
		}
		if !work.autoOwed(rec, spec) {
			continue
		}
		if rec.Kind == "Review" {
			pending, reviewed, err := executorReviewStates(ctx, newGithubClientFromToken(ctx, work.discToken), work.owner, work.repo, want.number, owner)
			if err != nil {
				logger.Error(err, "unable to check the owner's reviews; not filing", "pr", want.number)
				continue
			}
			if pending || reviewed {
				continue
			}
		}
		if err := r.fileRequest(ctx, work, spec); err != nil {
			logger.Error(err, "unable to file an automation's recipe Request", "recipe", want.recipe, "item", want.item, "number", want.number)
			continue
		}
		logger.Info("automation filed a recipe Request", "recipe", want.recipe, "item", want.item, "number", want.number, "member", owner)
	}
}

// autoOwed reports whether automation still owes spec's item a run of
// rec: no Request for it standing or settled lately (any member's), and
// neither a run of it recorded on the item's sandbox nor its draft
// rejected there.
func (w *workState) autoOwed(rec boardv1alpha1.BoardRecipe, spec boardv1alpha1.RequestSpec) bool {
	for _, req := range w.requests {
		if req.Spec.Verb == boardv1alpha1.VerbRecipe && req.Spec.Recipe == spec.Recipe && req.Spec.Item == spec.Item && req.Spec.Number == spec.Number {
			return false
		}
	}
	_, sb := w.recipeSandbox(rec, spec)
	if sb == nil {
		return true
	}
	annotations := sb.GetAnnotations()
	runKey := factorycli.RunAnnotation(rec.TaskType)
	if _, ran := factorycli.RecordedRunAt(annotations, runKey); ran {
		return false
	}
	return annotations[factorycli.RejectedAnnotation(runKey)] == ""
}

// discoverTriage finds the issues auto-triage wants triaged: not PRs,
// eligible under the universal auto filters (recency, labels, veto), and
// — for the "unclaimed" scope — carrying no assignees (anyone's
// assignment is a claim; "all" is for repos where assignment does not
// imply triaged).
func (r *Reconciler) discoverTriage(ctx context.Context, ghClient *github.Client, work *workState) ([]autoRequest, error) {
	unclaimedOnly := work.board.Spec.Auto.Triage != "all"
	var wanted []autoRequest
	opts := &github.IssueListByRepoOptions{State: "open", ListOptions: github.ListOptions{PerPage: 100}}
	for {
		items, resp, err := ghClient.Issues.ListByRepo(ctx, work.owner, work.repo, opts)
		if err != nil {
			return wanted, err
		}
		for _, item := range items {
			if item.IsPullRequest() || (unclaimedOnly && len(item.Assignees) > 0) || !autoEligible(work.board, item.Labels, item.GetUpdatedAt()) {
				continue
			}
			wanted = append(wanted, autoRequest{recipe: "triage", item: "issue", number: item.GetNumber()})
		}
		if resp.NextPage == 0 {
			return wanted, nil
		}
		opts.Page = resp.NextPage
	}
}

// discoverAllPRs finds every eligible open PR for auto-review ("all"):
// enabling the verb was the owner's consent to review as them.
func (r *Reconciler) discoverAllPRs(ctx context.Context, ghClient *github.Client, work *workState) ([]autoRequest, error) {
	return r.discoverPRs(ctx, ghClient, work, func(*github.PullRequest) bool { return true })
}

// discoverRequestedReviews finds the open PRs that request the owner's
// review (auto-review "requested").
func (r *Reconciler) discoverRequestedReviews(ctx context.Context, ghClient *github.Client, work *workState) ([]autoRequest, error) {
	owner := work.board.Namespace
	return r.discoverPRs(ctx, ghClient, work, func(pr *github.PullRequest) bool {
		for _, reviewer := range pr.RequestedReviewers {
			if strings.EqualFold(reviewer.GetLogin(), owner) {
				return true
			}
		}
		return false
	})
}

// discoverPRs finds the eligible open PRs that want a review.
func (r *Reconciler) discoverPRs(ctx context.Context, ghClient *github.Client, work *workState, wants func(*github.PullRequest) bool) ([]autoRequest, error) {
	var wanted []autoRequest
	opts := &github.PullRequestListOptions{State: "open", ListOptions: github.ListOptions{PerPage: 100}}
	for {
		prs, resp, err := ghClient.PullRequests.List(ctx, work.owner, work.repo, opts)
		if err != nil {
			return wanted, err
		}
		for _, pr := range prs {
			if autoEligible(work.board, pr.Labels, pr.GetUpdatedAt()) && wants(pr) {
				wanted = append(wanted, autoRequest{recipe: "review", item: "pr", number: pr.GetNumber()})
			}
		}
		if resp.NextPage == 0 {
			return wanted, nil
		}
		opts.Page = resp.NextPage
	}
}

// discoverAssigned finds the open issues assigned to the board's owner
// (auto-fix "assigned").
func (r *Reconciler) discoverAssigned(ctx context.Context, ghClient *github.Client, work *workState) ([]autoRequest, error) {
	var wanted []autoRequest
	opts := &github.IssueListByRepoOptions{State: "open", Assignee: work.board.Namespace, ListOptions: github.ListOptions{PerPage: 100}}
	for {
		items, resp, err := ghClient.Issues.ListByRepo(ctx, work.owner, work.repo, opts)
		if err != nil {
			return wanted, err
		}
		for _, item := range items {
			if item.IsPullRequest() || !autoEligible(work.board, item.Labels, item.GetUpdatedAt()) {
				continue
			}
			wanted = append(wanted, autoRequest{recipe: "fix", item: "issue", number: item.GetNumber()})
		}
		if resp.NextPage == 0 {
			return wanted, nil
		}
		opts.Page = resp.NextPage
	}
}

// requestPrefix names a Request the controller files after what it is
// for, as the board's API names a click's.
func requestPrefix(spec boardv1alpha1.RequestSpec) string {
	switch spec.Verb {
	case boardv1alpha1.VerbRecipe:
		return fmt.Sprintf("recipe-%s-%s-%d-", spec.Recipe, spec.Item, spec.Number)
	default:
		return fmt.Sprintf("%s-%s-%d-", spec.Verb, spec.Item, spec.Number)
	}
}
