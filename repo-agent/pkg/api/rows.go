package api

import (
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	boardv1alpha1 "github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/api/repoboard/v1alpha1"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/models"
)

// A row shows its runs, each with its own status (runStatus), and what
// it offers and asks of the member follows from them by one set of rules,
// the same for every recipe:
//
//	none yet  — the recipe's launch button
//	running   — working; the session to watch
//	ready     — needs you: the draft's actions
//	done      — the recipe again
//	failed    — needs you; the recipe again
//
// GitHub adds the facts no run records: a review requested of the member,
// their pending review, their own draft PR.

// hookedRecipes are the built-ins the controller has passes of its own
// for, in the board's order, as the rows offer them until the controller
// has published its catalog.
var hookedRecipes = []boardv1alpha1.BoardRecipe{
	{Name: "triage", Label: "Triage", On: []string{"issue"}},
	{Name: "plan", Label: "Plan", On: []string{"issue"}},
	{Name: "fix", Label: "Fix", On: []string{"issue"}},
	{Name: "review", Label: "Review", On: []string{"pr"}},
}

// rowStarts reports whether a row of item (issue or pr) offers rec.
func rowStarts(rec boardv1alpha1.BoardRecipe, item string) bool {
	if want, ok := hookedRecipeItems[rec.Name]; ok {
		return item == want
	}
	return recipeStartsOn(rec, item)
}

// recipeLabel is name's label in catalog, else its name.
func recipeLabel(catalog []boardv1alpha1.BoardRecipe, name string) string {
	if i := slices.IndexFunc(catalog, func(rec boardv1alpha1.BoardRecipe) bool { return rec.Name == name }); i >= 0 && catalog[i].Label != "" {
		return catalog[i].Label
	}
	return name
}

// rowRecipes are the launch buttons of a row of item.
func rowRecipes(catalog []boardv1alpha1.BoardRecipe, item string) []models.RowRecipe {
	if len(catalog) == 0 {
		catalog = hookedRecipes
	}
	var out []models.RowRecipe
	for _, rec := range catalog {
		if !rowStarts(rec, item) {
			continue
		}
		button := models.RowRecipe{Name: rec.Name, Label: recipeLabel(catalog, rec.Name)}
		for _, in := range rec.Inputs {
			if in.Required && in.Default == "" && !in.Revise {
				button.Inputs = append(button.Inputs, in.Name)
			}
		}
		out = append(out, button)
	}
	return out
}

// latestRuns is the newest run of each recipe on the item.
func latestRuns(item *models.WorkItem) []models.RunSession {
	var out []models.RunSession
	seen := map[string]bool{}
	// Sessions are newest first.
	for _, s := range item.Sessions {
		if !seen[s.Recipe] {
			seen[s.Recipe] = true
			out = append(out, s)
		}
	}
	return out
}

// applyRowRules gives every item its launch buttons, the clicks on it not
// started yet, and its attention.
func applyRowRules(items map[string]*models.WorkItem, catalog []boardv1alpha1.BoardRecipe, requests []boardv1alpha1.Request, atCapacity bool, now time.Time) {
	for _, item := range items {
		// An issue with an open PR from its fix offers no more recipes:
		// the PR row carries the work.
		if item.Type == "issue" && item.PRURL != "" {
			continue
		}
		item.Recipes = rowRecipes(catalog, item.Type)
		if item.Type == "pr" && item.Mine {
			// GitHub takes no verdict from a PR's author on their own PR.
			item.Recipes = slices.DeleteFunc(item.Recipes, func(rec models.RowRecipe) bool { return rec.Name == "review" })
		}
	}
	for _, req := range requests {
		if req.Spec.Verb != boardv1alpha1.VerbRecipe {
			continue
		}
		item := items[req.Spec.Item+"-"+strconv.Itoa(req.Spec.Number)]
		if item == nil || runSince(item, req.Spec.Recipe, req.CreationTimestamp.Time) {
			continue
		}
		state := "starting"
		if atCapacity && item.Sandbox == nil && req.Spec.Recipe != "triage" {
			// Triage is gated by the board's capacity only, not a slot.
			state = "queued"
		}
		if item.Launching == nil {
			item.Launching = map[string]string{}
		}
		item.Launching[req.Spec.Recipe] = state
	}
	for _, item := range items {
		item.Attention = attentionOf(item, now)
	}
}

// runSince reports whether recipe's newest run on item started since a
// click at: the click has its run.
func runSince(item *models.WorkItem, recipe string, at time.Time) bool {
	for _, s := range latestRuns(item) {
		if s.Recipe != recipe {
			continue
		}
		started, err := time.Parse(time.RFC3339, s.StartedAt)
		return err == nil && !started.Before(at.Truncate(time.Second))
	}
	return false
}

// attentionOf is what the row asks of the member: working while a run of
// it runs or a click on it starts; needs you when a run is ready or
// failed, or GitHub waits on them (a pending review to finalize, a fresh
// review request, their own fresh draft PR); waiting on someone else
// otherwise, for a PR of theirs or one asking for their review.
func attentionOf(item *models.WorkItem, now time.Time) string {
	working, needsYou := false, false
	for _, s := range latestRuns(item) {
		switch s.Status {
		case "running":
			working = true
		case "ready", "failed":
			needsYou = true
		}
	}
	queued := false
	for _, state := range item.Launching {
		if state == "queued" {
			queued = true
		} else {
			working = true
		}
	}
	updated, _ := time.Parse(time.RFC3339, item.UpdatedAt)
	fresh := now.Sub(updated) <= reviewRequestFreshWindow
	switch {
	case working:
		return attentionWorking
	case needsYou || item.ReviewPending || item.Error != "":
		return attentionNeedsYou
	case item.Type == "pr" && item.ReviewRequested && fresh:
		return attentionNeedsYou
	case item.Type == "pr" && item.Mine && item.DraftPR && fresh:
		// Under the draft-PR policy the agent opens drafts for a human to
		// promote; a parked one decays out of the inbox like a request.
		return attentionNeedsYou
	case queued, item.Type == "pr" && (item.ReviewRequested || item.Mine):
		return attentionWaiting
	}
	return ""
}

// bareReviewRequest is a row that needs the member only because their
// review was requested: nothing is prepared for them yet.
func bareReviewRequest(item models.WorkItem) bool {
	if item.Type != "pr" || !item.ReviewRequested || item.ReviewPending || item.Error != "" {
		return false
	}
	for _, s := range latestRuns(&item) {
		if s.Status == "ready" || s.Status == "failed" {
			return false
		}
	}
	return true
}

// sortWork orders the feed by attention, then finished agent work awaiting
// a verdict before a bare review request, then the newest first.
func sortWork(work []models.WorkItem) {
	rank := map[string]int{attentionNeedsYou: 0, attentionWorking: 1, attentionWaiting: 2, "": 3}
	deferred := func(item models.WorkItem) int {
		if bareReviewRequest(item) {
			return 1
		}
		return 0
	}
	sort.SliceStable(work, func(i, j int) bool {
		if rank[work[i].Attention] != rank[work[j].Attention] {
			return rank[work[i].Attention] < rank[work[j].Attention]
		}
		if deferred(work[i]) != deferred(work[j]) {
			return deferred(work[i]) < deferred(work[j])
		}
		return strings.Compare(work[i].UpdatedAt, work[j].UpdatedAt) > 0
	})
}
