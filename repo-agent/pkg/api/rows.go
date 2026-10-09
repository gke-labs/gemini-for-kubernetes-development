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

// recipeLabel is name's label in catalog, else its name.
func recipeLabel(catalog []boardv1alpha1.BoardRecipe, name string) string {
	if i := slices.IndexFunc(catalog, func(rec boardv1alpha1.BoardRecipe) bool { return rec.Name == name }); i >= 0 && catalog[i].Label != "" {
		return catalog[i].Label
	}
	return name
}

// launchInputs are the inputs a launch of rec must be given: required,
// with no default.
func launchInputs(rec boardv1alpha1.BoardRecipe) []string {
	var out []string
	for _, in := range rec.Inputs {
		if in.Required && in.Default == "" && !in.Revise {
			out = append(out, in.Name)
		}
	}
	return out
}

// recipeGroup is name's group in catalog: its session tag, else itself.
// A group's runs are one conversation and one chip on the row, and it is
// one button.
func recipeGroup(catalog []boardv1alpha1.BoardRecipe, name string) string {
	if tag := recipeSessionTag(catalog, name); tag != "" {
		return tag
	}
	return name
}

// recipeSessionTag is name's session tag in catalog, "" for a recipe in
// none.
func recipeSessionTag(catalog []boardv1alpha1.BoardRecipe, name string) string {
	if i := slices.IndexFunc(catalog, func(rec boardv1alpha1.BoardRecipe) bool { return rec.Name == name }); i >= 0 {
		return catalog[i].Session
	}
	return ""
}

// runGroup is a run's group: its recipe's session tag, else its recipe.
func runGroup(s models.RunSession) string {
	if s.Session != "" {
		return s.Session
	}
	return s.Recipe
}

// rowRecipes are the launch buttons of a row of item, a PR of the
// member's from their fork if myPR, labelled labels: the recipes the
// controller published that run there and are offered there, none before
// it has.
func rowRecipes(catalog []boardv1alpha1.BoardRecipe, item string, myPR bool, labels []string) []models.RowRecipe {
	var out []models.RowRecipe
	for _, rec := range catalog {
		if !recipeRunsOn(rec, item, myPR) || !recipeOffered(rec, labels) {
			continue
		}
		out = append(out, models.RowRecipe{Name: rec.Name, Label: recipeLabel(catalog, rec.Name), Session: rec.Session, Inputs: launchInputs(rec)})
	}
	return out
}

// recipeOffered reports whether a row labelled labels offers rec as a
// button: unless its applicableWhen names labels the row has none of.
func recipeOffered(rec boardv1alpha1.BoardRecipe, labels []string) bool {
	if rec.ApplicableWhen == nil {
		return true
	}
	return slices.ContainsFunc(rec.ApplicableWhen.Labels, func(want string) bool {
		// GitHub labels are case-insensitive.
		return slices.ContainsFunc(labels, func(l string) bool { return strings.EqualFold(l, want) })
	})
}

// latestRuns is the newest run of each group (runGroup) on the item.
func latestRuns(item *models.WorkItem) []models.RunSession {
	var out []models.RunSession
	seen := map[string]bool{}
	// Sessions are newest first.
	for _, s := range item.Sessions {
		if g := runGroup(s); !seen[g] {
			seen[g] = true
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
		// A PR of the member's offers Review too; the UI hides it unless
		// they turn self-review on, a view preference of theirs.
		item.Recipes = rowRecipes(catalog, item.Type, item.MyPR, item.Labels)
	}
	for _, req := range requests {
		if req.Spec.Verb != boardv1alpha1.VerbRecipe {
			continue
		}
		item := items[req.Spec.Item+"-"+strconv.Itoa(req.Spec.Number)]
		if item == nil || runSince(item, recipeGroup(catalog, req.Spec.Recipe), req.CreationTimestamp.Time) {
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

// markAutos gives each PR row of the member's its auto: on while their
// watch Request for it stands.
func markAutos(items map[string]*models.WorkItem, requests []boardv1alpha1.Request, member string) {
	for _, item := range items {
		if item.Type == "pr" && item.MyPR {
			item.Auto = &models.WorkAuto{}
		}
	}
	for _, req := range requests {
		if req.Spec.Verb != boardv1alpha1.VerbWatch || req.Spec.Member != member || !req.Active() {
			continue
		}
		item := items["pr-"+strconv.Itoa(req.Spec.Number)]
		if item == nil {
			continue
		}
		item.Auto = &models.WorkAuto{On: true, Since: req.CreationTimestamp.UTC().Format(time.RFC3339), Message: req.Status.Message}
	}
}

// runSince reports whether group's newest run on item started since a
// click at: the click has its run. A group runs one at a time, so its
// newest run is the click's, whichever of its recipes it ran.
func runSince(item *models.WorkItem, group string, at time.Time) bool {
	for _, s := range latestRuns(item) {
		if runGroup(s) != group {
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
	doneAt := finishedAt(item, updated)
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
	case queued:
		return attentionWaiting
	case !doneAt.IsZero() && now.Sub(doneAt) <= doneFreshWindow:
		return attentionDone
	case item.Type == "pr" && (item.ReviewRequested || item.Mine):
		return attentionWaiting
	}
	return ""
}

// finishedAt is when the item's work last finished, zero if none did: its
// newest done run's end or last applied action, else, for a review
// submitted or a fix's PR open (no run left to time), the item's last
// update.
func finishedAt(item *models.WorkItem, updated time.Time) time.Time {
	var at time.Time
	later := func(s string) {
		if t, err := time.Parse(time.RFC3339, s); err == nil && t.After(at) {
			at = t
		}
	}
	for _, s := range latestRuns(item) {
		if s.Status != "done" {
			continue
		}
		later(s.EndedAt)
		for _, t := range s.Applied {
			later(t)
		}
	}
	if at.IsZero() && (item.Reviewed || (item.Type == "issue" && item.PRURL != "")) {
		at = updated
	}
	return at
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
	rank := map[string]int{attentionNeedsYou: 0, attentionWorking: 1, attentionDone: 2, attentionWaiting: 3, "": 4}
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
