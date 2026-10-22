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
	"slices"
	"strings"
	"time"

	"github.com/google/go-github/v39/github"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/log"

	boardv1alpha1 "github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/api/repoboard/v1alpha1"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/factorycli"
)

// Automation (spec.auto) is board policy: each rule names a recipe and
// the open items of one type it runs on, and discovery files a recipe
// Request for each item a rule matches, as the board's owner (its
// namespace), the way a member's click files one. From there a run is a
// click's: ensureRecipes launches it, settleRecipe settles it, and its
// draft, failure and chip are any run's (generic-board-recipes.md).
//
// An item gets one Request per recipe, ever: not while one for the
// recipe stands or was settled lately, nor once the recipe has a run
// recorded on the item's sandbox, whoever ran it and however it ended (a
// failed run is not retried; a member clicks), nor once its draft was
// rejected (the tombstone every reject writes). A Review recipe is not
// filed over the owner's own review on GitHub, pending or submitted.

// autoRequest is one recipe automation wants run on an item, under a
// rule's daily cap (0: none).
type autoRequest struct {
	recipe    string
	item      string
	number    int
	maxPerDay int
	// updatedAt is when the item last changed, as listed: a PR the owner
	// reviewed is not asked about again until it changes (reviewedPRs).
	updatedAt time.Time
}

// autoItem is an open issue or PR as the rules' conditions read it.
type autoItem struct {
	number    int
	labels    []*github.Label
	assignees []*github.User
	reviewers []*github.User
	updatedAt time.Time
}

// validRules are the board's rules automation can run, and why each other
// is skipped: a recipe the catalog does not have, an item type it does not
// run on (a my-pr recipe does not: a rule's PRs are anyone's), or a
// condition the type does not have.
func validRules(rules []boardv1alpha1.AutoRule, catalog []boardv1alpha1.BoardRecipe) ([]boardv1alpha1.AutoRule, []string) {
	var valid []boardv1alpha1.AutoRule
	var errs []string
	for _, rule := range rules {
		rec, ok := findRecipe(catalog, rule.Recipe)
		switch {
		case !ok:
			errs = append(errs, fmt.Sprintf("%s on %s: no recipe %q", rule.Recipe, rule.On, rule.Recipe))
		case rule.On != "issue" && rule.On != "pr":
			errs = append(errs, fmt.Sprintf("%s on %s: rules run on issues or PRs", rule.Recipe, rule.On))
		case len(rec.On) > 0 && !slices.Contains(rec.On, rule.On):
			// Stricter than startsOn: a my-pr recipe starts on a PR when
			// it is the member's, and a rule's PRs are anyone's.
			errs = append(errs, fmt.Sprintf("%s on %s: %s does not run on every %s", rule.Recipe, rule.On, rule.Recipe, rule.On))
		case rule.On == "issue" && rule.When.ReviewRequestedFromOwner:
			errs = append(errs, fmt.Sprintf("%s on issue: an issue has no review requests", rule.Recipe))
		default:
			valid = append(valid, rule)
		}
	}
	return valid, errs
}

// matches reports whether item meets every condition rule sets; none set
// is every item.
func (rule autoRuleMatch) matches(item autoItem) bool {
	when := rule.When
	if when.Unclaimed && len(item.assignees) > 0 {
		return false
	}
	if when.AssignedToOwner && !hasLogin(item.assignees, rule.owner) {
		return false
	}
	if when.ReviewRequestedFromOwner && !hasLogin(item.reviewers, rule.owner) {
		return false
	}
	if len(when.Labels) > 0 && !hasAnyGithubLabel(item.labels, when.Labels) {
		return false
	}
	return true
}

// autoRuleMatch is a rule with the owner its conditions name.
type autoRuleMatch struct {
	boardv1alpha1.AutoRule
	owner string
}

func hasLogin(users []*github.User, login string) bool {
	for _, u := range users {
		if strings.EqualFold(u.GetLogin(), login) {
			return true
		}
	}
	return false
}

// discoverAuto is what the board's rules want run: the open issues and
// PRs, listed once each when a rule runs on that type, within the shared
// filters (autoEligible), each matched against the rules of its type.
// The rules it skips are reported on the board's status.
func (r *Reconciler) discoverAuto(ctx context.Context, ghClient *github.Client, work *workState) []autoRequest {
	logger := log.FromContext(ctx)
	board := work.board
	rules := board.Spec.Auto.EffectiveRules()
	if len(rules) == 0 {
		board.Status.AutoRuleErrors = nil
		return nil
	}
	catalog := r.recipeCatalog(ctx, work)
	if catalog == nil {
		return nil // the catalog is read again next pass
	}
	valid, errs := validRules(rules, catalog)
	board.Status.AutoRuleErrors = errs
	byType := map[string][]autoRuleMatch{}
	for _, rule := range valid {
		byType[rule.On] = append(byType[rule.On], autoRuleMatch{AutoRule: rule, owner: board.Namespace})
	}
	var wanted []autoRequest
	for _, on := range []string{"issue", "pr"} {
		if len(byType[on]) == 0 {
			continue
		}
		items, err := r.listAutoItems(ctx, ghClient, work, on)
		if err != nil {
			logger.Error(err, "auto discovery failed", "on", on)
		}
		for _, item := range items {
			if !autoEligible(board, item.labels, item.updatedAt) {
				continue
			}
			for _, rule := range byType[on] {
				if rule.matches(item) {
					wanted = append(wanted, autoRequest{recipe: rule.Recipe, item: on, number: item.number, maxPerDay: rule.MaxPerDay, updatedAt: item.updatedAt})
				}
			}
		}
	}
	return wanted
}

// listAutoItems lists the repository's open issues (not PRs) or PRs
// updated within the board's recency (autoRecency): nothing older is
// eligible, so it is not listed, and a large repository costs a page or
// two a pass rather than all its open items.
func (r *Reconciler) listAutoItems(ctx context.Context, ghClient *github.Client, work *workState, on string) ([]autoItem, error) {
	var items []autoItem
	cutoff := time.Now().Add(-autoRecency(work.board))
	if on == "pr" {
		// PRs have no since: newest updated first, stopping at the cutoff.
		opts := &github.PullRequestListOptions{State: "open", Sort: "updated", Direction: "desc", ListOptions: github.ListOptions{PerPage: 100}}
		for {
			prs, resp, err := ghClient.PullRequests.List(ctx, work.owner, work.repo, opts)
			if err != nil {
				return items, err
			}
			older := false
			for _, pr := range prs {
				if updated := pr.GetUpdatedAt(); !updated.IsZero() && updated.Before(cutoff) {
					older = true
					break
				}
				items = append(items, autoItem{number: pr.GetNumber(), labels: pr.Labels, assignees: pr.Assignees, reviewers: pr.RequestedReviewers, updatedAt: pr.GetUpdatedAt()})
			}
			if older || resp.NextPage == 0 {
				return items, nil
			}
			opts.Page = resp.NextPage
		}
	}
	opts := &github.IssueListByRepoOptions{State: "open", Since: cutoff, ListOptions: github.ListOptions{PerPage: 100}}
	for {
		issues, resp, err := ghClient.Issues.ListByRepo(ctx, work.owner, work.repo, opts)
		if err != nil {
			return items, err
		}
		for _, issue := range issues {
			if issue.IsPullRequest() {
				continue
			}
			items = append(items, autoItem{number: issue.GetNumber(), labels: issue.Labels, assignees: issue.Assignees, updatedAt: issue.GetUpdatedAt()})
		}
		if resp.NextPage == 0 {
			return items, nil
		}
		opts.Page = resp.NextPage
	}
}

// fileAutoRequests files a recipe Request, as the board's owner, for each
// item automation wants run that is owed one (autoOwed), while the rule's
// recipe is under its daily cap. The owner's reviews are read under the
// discovery identity, the owner's: pending reviews are visible only to
// their author. What it files is recorded on the board's status
// (AutoFiled, the last day's), for the caps.
func (r *Reconciler) fileAutoRequests(ctx context.Context, work *workState, wanted []autoRequest) {
	work.board.Status.AutoFiled = lastDay(work.board.Status.AutoFiled, time.Now())
	if len(wanted) == 0 {
		return
	}
	logger := log.FromContext(ctx)
	catalog := r.recipeCatalog(ctx, work)
	if catalog == nil {
		return
	}
	owner := work.board.Namespace
	defer r.pruneReviewedPRs(work, wanted)
	type key struct {
		recipe, item string
		number       int
	}
	seen := map[key]bool{}
	for _, want := range wanted {
		k := key{want.recipe, want.item, want.number}
		if seen[k] {
			continue
		}
		seen[k] = true
		rec, ok := findRecipe(catalog, want.recipe)
		if !ok || !startsOn(rec, want.item) {
			continue
		}
		if want.maxPerDay > 0 && filedFor(work.board.Status.AutoFiled, want.recipe) >= want.maxPerDay {
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
			if r.ownerReviewed(work, want) {
				continue
			}
			pending, reviewed, err := executorReviewStates(ctx, newGithubClientFromToken(ctx, work.discToken), work.owner, work.repo, want.number, owner)
			if err != nil {
				logger.Error(err, "unable to check the owner's reviews; not filing", "pr", want.number)
				continue
			}
			if pending || reviewed {
				r.rememberOwnerReviewed(work, want)
				continue
			}
		}
		if err := r.fileRequest(ctx, work, spec); err != nil {
			logger.Error(err, "unable to file an automation's recipe Request", "recipe", want.recipe, "item", want.item, "number", want.number)
			continue
		}
		work.board.Status.AutoFiled = append(work.board.Status.AutoFiled, boardv1alpha1.AutoFiling{Recipe: want.recipe, At: metav1.Now()})
		logger.Info("automation filed a recipe Request", "recipe", want.recipe, "item", want.item, "number", want.number, "member", owner)
	}
}

// reviewedKey names a PR of a board in reviewedPRs.
func reviewedKey(work *workState, pr int) string {
	return fmt.Sprintf("%s/%s#%d", work.board.Namespace, work.board.Name, pr)
}

// ownerReviewed reports whether the owner was found to have reviewed the
// PR as it is now (unchanged since: the same updated_at), so GitHub need
// not be asked again.
func (r *Reconciler) ownerReviewed(work *workState, want autoRequest) bool {
	at, ok := r.reviewedPRs.Load(reviewedKey(work, want.number))
	return ok && !want.updatedAt.IsZero() && at.(time.Time).Equal(want.updatedAt)
}

// rememberOwnerReviewed records that the owner has reviewed the PR as it
// is now.
func (r *Reconciler) rememberOwnerReviewed(work *workState, want autoRequest) {
	if !want.updatedAt.IsZero() {
		r.reviewedPRs.Store(reviewedKey(work, want.number), want.updatedAt)
	}
}

// pruneReviewedPRs forgets the board's PRs discovery no longer lists, so
// the memo holds at most what discovery does.
func (r *Reconciler) pruneReviewedPRs(work *workState, wanted []autoRequest) {
	listed := map[string]bool{}
	for _, want := range wanted {
		if want.item == "pr" {
			listed[reviewedKey(work, want.number)] = true
		}
	}
	prefix := fmt.Sprintf("%s/%s#", work.board.Namespace, work.board.Name)
	r.reviewedPRs.Range(func(k, _ any) bool {
		if key := k.(string); strings.HasPrefix(key, prefix) && !listed[key] {
			r.reviewedPRs.Delete(key)
		}
		return true
	})
}

// lastDay are the filings of the day before now.
func lastDay(filed []boardv1alpha1.AutoFiling, now time.Time) []boardv1alpha1.AutoFiling {
	var kept []boardv1alpha1.AutoFiling
	for _, f := range filed {
		if now.Sub(f.At.Time) < 24*time.Hour {
			kept = append(kept, f)
		}
	}
	return kept
}

// filedFor counts the filings of recipe.
func filedFor(filed []boardv1alpha1.AutoFiling, recipe string) int {
	n := 0
	for _, f := range filed {
		if f.Recipe == recipe {
			n++
		}
	}
	return n
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
