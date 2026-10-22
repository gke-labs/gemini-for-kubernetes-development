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
	"cmp"
	"context"
	"fmt"
	"slices"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/log"

	boardv1alpha1 "github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/api/repoboard/v1alpha1"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/factorycli"
)

// A recipe Request launches any recipe factory recipe list lists, as
// data: on its issue or PR, in the sandbox factory runs it in, with the
// Request's inputs, finished as the catalog says (onFinish, timeout), and
// its task output, if it declares one, stored on the sandbox as its run's
// (factorycli.KeepOutput), where the board's drafts and applies find it.
// Every recipe runs this one way, a click's and automation's alike
// (auto.go files automation's Requests).

// catalogPeek is how often a board's catalog is checked: a peek at the
// tree SHA of its repository's .agents/recipes (factory peek), one GitHub
// call, and a reload only when that changed. The built-ins do not change
// under the process.
const catalogPeek = 2 * time.Minute

// recipesTree is the folder under .agents/ the repository keeps its
// recipes in.
const recipesTree = "recipes"

// boardCatalog is a board repository's catalog, the tree SHA of its
// .agents/recipes it was read at ("" when it has none, or for the
// built-ins alone), and when it was last checked.
type boardCatalog struct {
	recipes []boardv1alpha1.BoardRecipe
	tree    string
	at      time.Time
}

// recipeCatalog is the recipes the controller's factory runs for work's
// board: the built-ins and its repository's own (.agents/recipes at the
// default branch, read with the board owner's token). Every catalogPeek
// it peeks at the folder's tree SHA, and reads the catalog again only
// when that changed (or the peek failed). A failed read keeps the last
// catalog, else falls back to the built-ins alone; nil when even those
// cannot be read, retried next reconcile.
func (r *Reconciler) recipeCatalog(ctx context.Context, work *workState) []boardv1alpha1.BoardRecipe {
	logger := log.FromContext(ctx)
	key := work.owner + "/" + work.repo
	r.catalogMu.Lock()
	defer r.catalogMu.Unlock()
	last, ok := r.catalogs[key]
	if ok && time.Since(last.at) < catalogPeek {
		return last.recipes
	}
	repoURL := fmt.Sprintf("https://github.com/%s/%s", work.owner, work.repo)
	token, err := r.executorToken(ctx, work.board.Namespace)
	if err != nil {
		logger.Info("listing the built-in recipes only: the board owner has no GitHub token", "board", work.board.Name, "error", err.Error())
		repoURL = ""
	}
	tree := ""
	if repoURL != "" {
		trees, perr := r.Factory.AgentsTrees(ctx, repoURL, token)
		switch {
		case perr != nil:
			logger.Info("peeking at the repository's .agents/ failed; reading the catalog", "repo", key, "error", perr.Error())
		case ok && trees[recipesTree] == last.tree:
			last.at = time.Now()
			r.catalogs[key] = last
			return last.recipes
		default:
			tree = trees[recipesTree]
		}
	}
	recipes, skipped, err := r.Factory.Recipes(ctx, repoURL, token)
	if err != nil && repoURL != "" {
		logger.Error(err, "unable to read the repository's recipes", "repo", key)
		if ok {
			return last.recipes
		}
		recipes, skipped, err = r.Factory.Recipes(ctx, "", "")
	}
	if err != nil {
		logger.Error(err, "unable to read the recipe catalog")
		return nil
	}
	for _, s := range skipped {
		logger.Info("a repository recipe file is not a recipe", "repo", key, "problem", s)
	}
	if r.catalogs == nil {
		r.catalogs = map[string]boardCatalog{}
	}
	r.catalogs[key] = boardCatalog{recipes: recipes, tree: tree, at: time.Now()}
	return recipes
}

// findRecipe is name's recipe in catalog.
func findRecipe(catalog []boardv1alpha1.BoardRecipe, name string) (boardv1alpha1.BoardRecipe, bool) {
	i := slices.IndexFunc(catalog, func(rec boardv1alpha1.BoardRecipe) bool { return rec.Name == name })
	if i < 0 {
		return boardv1alpha1.BoardRecipe{}, false
	}
	return catalog[i], true
}

// startsOn reports whether rec starts on item: it runs on it (no on:
// runs anywhere). A recipe on [my-pr] starts on a PR; factory refuses it
// on one that is not the member's.
func startsOn(rec boardv1alpha1.BoardRecipe, item string) bool {
	return len(rec.On) == 0 || slices.Contains(rec.On, item) ||
		(item == "pr" && slices.Contains(rec.On, "my-pr"))
}

// recipeKey is the single-flight key of a recipe's run on an item.
func recipeKey(work *workState, member, recipe, item string, number int) string {
	return fmt.Sprintf("%s/recipe-%s-%s-%s-%d", member, recipe, work.repo, item, number)
}

// recipeRunName is what a clicked recipe's task is recorded under: one
// run per click, so a restarted controller follows the run the click
// started, and a click again is a run again.
func recipeRunName(req *boardv1alpha1.Request) string {
	return "request/" + string(req.UID)
}

// recipeSandbox is the sandbox a recipe Request's run is in, or will be,
// and the sandbox if it exists.
func (w *workState) recipeSandbox(rec boardv1alpha1.BoardRecipe, spec boardv1alpha1.RequestSpec) (string, *unstructured.Unstructured) {
	switch {
	case spec.Item == "issue":
		if sb := w.issueSandbox(spec.Member, spec.Number); sb != nil {
			return sb.GetName(), sb
		}
	case spec.Item == "pr" && rec.Credentials != "clone":
		if sb := factorycli.PRFixSandbox(w.sandboxesIn(spec.Member), w.repo, spec.Number); sb != nil {
			return sb.GetName(), sb
		}
	}
	name := factorycli.RecipeSandboxName(rec, spec.Item, w.repo, spec.Number)
	return name, w.findSandbox(spec.Member, name)
}

// ensureRecipes drives the standing recipe Requests no pass of its own
// serves.
func (r *Reconciler) ensureRecipes(ctx context.Context, work *workState, reqs []*boardv1alpha1.Request) {
	if len(reqs) == 0 {
		return
	}
	catalog := r.recipeCatalog(ctx, work)
	for _, req := range reqs {
		// One settle fails is not launched.
		if rec, ok := findRecipe(catalog, req.Spec.Recipe); ok && startsOn(rec, req.Spec.Item) {
			r.ensureRecipe(ctx, work, req, rec)
		}
	}
}

// ensureRecipe launches one click's run, or stores what came of it.
func (r *Reconciler) ensureRecipe(ctx context.Context, work *workState, req *boardv1alpha1.Request, rec boardv1alpha1.BoardRecipe) {
	logger := log.FromContext(ctx)
	spec := req.Spec
	name, sb := work.recipeSandbox(rec, spec)
	key := recipeKey(work, spec.Member, rec.Name, spec.Item, spec.Number)
	if r.Factory.IsRunning(key) {
		return
	}
	if res, ok := r.Factory.LastResult(key); ok && !res.FinishedAt.Before(req.CreationTimestamp.Time) {
		if sb != nil {
			r.keepRecipeOutput(ctx, work, sb, rec, spec.Member, res)
		}
		// settleRecipe reads the rest of the result.
		return
	}

	// Another run at work in the sandbox (a plan, a care run): this one
	// waits for it rather than being refused by factory. settleRecipe says
	// so on the Request.
	if busy, ok := busyRun(sb, recipeRunName(req)); ok {
		logger.Info("recipe waits: its sandbox is busy", "recipe", rec.Name, "number", spec.Number, "sandbox", name, "running", busy.Recipe)
		return
	}
	if sb == nil && r.activeCount(work) >= maxActive(work.board) {
		logger.Info("recipe deferred: board at maxActive", "recipe", rec.Name, "number", spec.Number, "limit", maxActive(work.board))
		return
	}
	token, err := r.executorToken(ctx, spec.Member)
	if err != nil {
		logger.Error(err, "recipe executor has no token", "executor", spec.Member, "recipe", rec.Name)
		return
	}
	// GitHub keeps one pending review per person, and factory's post
	// replaces only one it posted: over the member's own, a Review could
	// never be posted. A submitted review does not stop a click.
	if rec.Kind == "Review" && spec.Item == "pr" {
		pending, _, err := executorReviewStates(ctx, newGithubClientFromToken(ctx, token), work.owner, work.repo, spec.Number, spec.Member)
		if err != nil {
			logger.Error(err, "unable to check for the member's pending review; deferring", "recipe", rec.Name, "pr", spec.Number)
			return
		}
		if pending {
			r.refuseRecipe(req, requestOutcome{phase: boardv1alpha1.RequestFailed, reason: "OwnPendingReview",
				message: fmt.Sprintf("you have a pending review on #%d that factory did not post; submit or discard it on GitHub first", spec.Number)})
			return
		}
	}
	if err := r.ensureFactoryUserSecret(ctx, spec.Member, spec.Member, ""); err != nil {
		logger.Error(err, "unable to sync factory-user secret", "namespace", spec.Member)
		return
	}
	r.stampUnpaused(ctx, sb)
	r.stampEngine(ctx, sb, boardEngine(work.board))
	opts := r.recipeOptions(work, rec.Name, spec.Member, name, itemURL(work, spec.Item, spec.Number), token, recipeRunName(req))
	opts.RepoRecipe = !rec.IsBuiltin()
	opts.Inputs = spec.Inputs
	opts.NewSession = spec.NewSession && rec.Session != ""
	// What the recipe declares the board applies when the run ends (care's
	// post-replies: its commits are pushed by the run, so the replies that
	// answer for them go with them), and how long a run may take.
	opts.Apply = rec.OnFinish
	if d, err := time.ParseDuration(rec.Timeout); err == nil && d > 0 {
		opts.Timeout = d
	}
	if r.Factory.StartRecipe(key, opts) {
		logger.Info("launched factory recipe", "recipe", rec.Name, "item", spec.Item, "number", spec.Number, "board", work.board.Name, "executor", spec.Member)
	}
}

// busyRun is the run at work in sb other than the one named runName, if
// there is one: factory refuses a sandbox that is running a task, so a
// recipe waits for it.
func busyRun(sb *unstructured.Unstructured, runName string) (factorycli.RecordedRun, bool) {
	if sb == nil {
		return factorycli.RecordedRun{}, false
	}
	for _, run := range factorycli.Runs(sb.GetAnnotations()) {
		if run.State == factorycli.TaskStateRunning && run.Name != runName {
			return run, true
		}
	}
	return factorycli.RecordedRun{}, false
}

// refuseRecipe records why req is not launched, for settleRecipe to fail
// it with.
func (r *Reconciler) refuseRecipe(req *boardv1alpha1.Request, out requestOutcome) {
	r.refusals.Store(req.UID, out)
}

// keepRecipeOutput stores on sb the output of rec's run there, which
// ended with res, with what its runner applied stamped: nothing if the
// apply failed, so the draft waits to be applied again. The board and
// executor are stamped even on a run that left no output, so its
// session's draft, written later (research's notes), is filed on it.
func (r *Reconciler) keepRecipeOutput(ctx context.Context, work *workState, sb *unstructured.Unstructured, rec boardv1alpha1.BoardRecipe, member string, res factorycli.Result) {
	annotations := sb.GetAnnotations()
	if annotations == nil {
		annotations = map[string]string{}
	}
	var applied []string
	if rec.OnFinish != "" && res.Err == nil {
		applied = []string{rec.OnFinish}
	}
	kept := rec.Kind != "" && factorycli.KeepOutput(annotations, factorycli.RunAnnotation(rec.TaskType), factorycli.HarvestedOutput(rec.Kind, res.Output), res.FinishedAt, applied...)
	if !kept && annotations[AnnotationBoard] == work.board.Name && annotations[AnnotationExecutor] == member {
		return
	}
	annotations[AnnotationBoard] = work.board.Name
	annotations[AnnotationExecutor] = member
	sb.SetAnnotations(annotations)
	if err := r.Update(ctx, sb); err != nil {
		log.FromContext(ctx).Error(err, "unable to store a recipe's output", "recipe", rec.Name, "sandbox", sb.GetName())
	}
}

// itemURL is the GitHub URL of an issue or PR of the board's repository.
func itemURL(work *workState, item string, number int) string {
	path := "issues"
	if item == "pr" {
		path = "pull"
	}
	return fmt.Sprintf("https://github.com/%s/%s/%s/%d", work.owner, work.repo, path, number)
}

// settleRecipe is what came of a recipe Request no pass of its own
// serves: running while its run is, done once its output is stored (or
// it ended, for a recipe with none), failed if it failed. A click buys
// one run; another is another click.
func (r *Reconciler) settleRecipe(work *workState, req *boardv1alpha1.Request, now time.Time) requestOutcome {
	spec := req.Spec
	catalog := r.catalogCopy(work)
	if catalog == nil {
		// Not read yet: nothing can be said about the recipe.
		return pendingOutcome(req, now)
	}
	rec, ok := findRecipe(catalog, spec.Recipe)
	if !ok {
		return requestOutcome{phase: boardv1alpha1.RequestFailed, reason: "UnknownRecipe",
			message: fmt.Sprintf("factory has no recipe %q", spec.Recipe)}
	}
	if !startsOn(rec, spec.Item) {
		return requestOutcome{phase: boardv1alpha1.RequestFailed, reason: "NotLaunchable",
			message: fmt.Sprintf("recipe %s does not start on an %s", rec.Name, spec.Item)}
	}
	name, sb := work.recipeSandbox(rec, spec)
	key := recipeKey(work, spec.Member, rec.Name, spec.Item, spec.Number)
	if r.Factory.IsRunning(key) {
		return requestOutcome{phase: boardv1alpha1.RequestRunning, reason: "Running", sandbox: name}
	}
	if out, refused := r.refusals.LoadAndDelete(req.UID); refused {
		return out.(requestOutcome)
	}
	res, ok := r.Factory.LastResult(key)
	if !ok || res.FinishedAt.Before(req.CreationTimestamp.Time) {
		if busy, ok := busyRun(sb, recipeRunName(req)); ok {
			if out := pendingOutcome(req, now); out.phase != "" {
				return out
			}
			return requestOutcome{phase: boardv1alpha1.RequestPending, reason: "Waiting", sandbox: name,
				message: fmt.Sprintf("waiting for %s to finish in %s", cmp.Or(busy.Recipe, "another task"), name)}
		}
		return pendingOutcome(req, now)
	}
	if res.Err != nil {
		return requestOutcome{phase: boardv1alpha1.RequestFailed, reason: "RunFailed", sandbox: name,
			message: runErrorLine(res)}
	}
	if rec.Kind == "" {
		return served(name)
	}
	doc := factorycli.HarvestedOutput(rec.Kind, res.Output)
	if doc == "" {
		return requestOutcome{phase: boardv1alpha1.RequestFailed, reason: "NoOutput", sandbox: name,
			message: fmt.Sprintf("the run ended without a %s", rec.Kind)}
	}
	if sb != nil && sb.GetAnnotations()[factorycli.OutputAnnotation(factorycli.RunAnnotation(rec.TaskType))] == doc {
		return served(name)
	}
	// Ended, its output not stored yet: ensureRecipe stores it.
	return requestOutcome{phase: boardv1alpha1.RequestRunning, reason: "Storing", sandbox: name}
}

// catalogCopy is work's board's catalog as last read, without reading it.
func (r *Reconciler) catalogCopy(work *workState) []boardv1alpha1.BoardRecipe {
	r.catalogMu.Lock()
	defer r.catalogMu.Unlock()
	return r.catalogs[work.owner+"/"+work.repo].recipes
}
