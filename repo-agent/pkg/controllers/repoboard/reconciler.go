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

// Package repoboard reconciles RepoBoard boards (docs/design/repoboard.md):
// work is discovered from GitHub (trigger label + assignee, under the
// executor-consent rule) and from the Requests members file by clicking;
// attributed execution runs through the factory CLI in the consenting
// member's own namespace; reviews always run attributed and land as the
// member's pending review on GitHub. GitHub and factory sandboxes are
// the state, the CR is near-static config.
package repoboard

import (
	"context"
	"fmt"
	"iter"
	"strings"
	"sync"
	"time"

	"github.com/google/go-github/v39/github"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/log"

	boardv1alpha1 "github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/api/repoboard/v1alpha1"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/factorycli"
)

const (
	// Review and sandbox annotations; the review names are the PR-review
	// flow's wire contract, kept so the API/UI read one shape.
	AnnotationReviewedAt       = "review.gemini.google.com/reviewed-at"
	AnnotationPreventAutoPause = "sandbox.gemini.google.com/prevent-auto-shutdown"
	AnnotationUnpausedAt       = "sandbox.gemini.google.com/unpaused-at"
	AnnotationBoard            = "board.gemini.google.com/board"
	// AnnotationExecutor records which member a run on the sandbox ran
	// as (keepRecipeOutput), so a resumed fix keeps the executor identity
	// even when the sandbox lives in the board namespace (personal
	// boards).
	AnnotationExecutor = "board.gemini.google.com/executor"
	// AnnotationReviewAbandoned is stamped by the API when the member
	// deletes their pending review on GitHub; invocation results older
	// than this must not be re-recorded as pending.
	AnnotationReviewAbandoned = "review.gemini.google.com/abandoned-at"
	// AnnotationReviewError parks a failed auto-review: the message
	// renders on the board, and automation does not try again. Retrying
	// re-runs the whole agent, and failures that need a human (org blocks
	// the token, bad PAT) never fix themselves; a member clicks Review.
	AnnotationReviewError   = "review.gemini.google.com/error"
	AnnotationReviewErrorAt = "review.gemini.google.com/error-at"
	// AnnotationFixHarvestedAt is when the controller last read a fix
	// run's result (the PR it opened, or why it failed): a fix run
	// recorded after it has not been read yet, and a restarted controller
	// follows it by its run name to open its PR. AnnotationFixError is
	// why the last one failed, until one succeeds.
	AnnotationFixHarvestedAt = "board.gemini.google.com/fix-harvested-at"
	AnnotationFixError       = "board.gemini.google.com/fix-error"
	// AnnotationEngine records which agent engine launched into this
	// sandbox — sessions are engine-private, so the chat terminal must
	// resume with the same CLI that ran the task.
	AnnotationEngine        = "board.gemini.google.com/engine"
	defaultRequeue          = time.Minute
	applyRequeue            = 5 * time.Second
	launchRetryBackoff      = 30 * time.Minute
	prWatchRelaunchInterval = 10 * time.Minute
)

var sandboxGVK = schema.GroupVersionKind{Group: "agents.x-k8s.io", Version: "v1alpha1", Kind: "Sandbox"}

// Reconciler reconciles RepoBoard objects.
type Reconciler struct {
	client.Client
	Scheme  *runtime.Scheme
	Factory factorycli.Launcher

	// NewGithubClient is injectable for tests; defaults to
	// memberGithubClient (token from the namespace's github-pat secret).
	NewGithubClient func(ctx context.Context, r *Reconciler, namespace string) (*github.Client, string, error)

	// catalog is the recipes factory runs (recipeCatalog).
	catalogMu sync.Mutex
	catalog   []boardv1alpha1.BoardRecipe

	// refusals are the recipe Requests ensureRecipe refused to launch, by
	// UID, with why: settleRecipe fails them with it (refuseRecipe).
	refusals sync.Map
}

//+kubebuilder:rbac:groups=board.gemini.google.com,resources=repoboards,verbs=get;list;watch;update;patch
//+kubebuilder:rbac:groups=board.gemini.google.com,resources=repoboards/status,verbs=get;update;patch
// Requests are the clicks. The controller reads them, writes their phase,
// and deletes them once they have been terminal long enough to be read.
//+kubebuilder:rbac:groups=board.gemini.google.com,resources=requests,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=board.gemini.google.com,resources=requests/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=agents.x-k8s.io,resources=sandboxes,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch;create;update;patch
// The factory CLI runs under this ServiceAccount: it creates each sandbox's
// -lb Service, waits on the pod, and execs the task inside it, or reaches
// a recipe task through the daemon's task server over a port-forward.
//+kubebuilder:rbac:groups="",resources=services,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups="",resources=serviceaccounts,verbs=get;create
// serviceaccounts: factory ensures the per-namespace factory-deployer KSA
// (the Workload Identity principal) when creating explore sandboxes.
//+kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch
//+kubebuilder:rbac:groups="",resources=pods/exec,verbs=create
//+kubebuilder:rbac:groups="",resources=pods/log,verbs=get
//+kubebuilder:rbac:groups="",resources=pods/portforward,verbs=create

// fixPlan is one auto-fix to ensure (spec.auto.fix): the executor is the
// member whose identity and namespace run the task. A Fix click is a
// recipe Request (ensureRecipes).
type fixPlan struct {
	issue    int
	issueURL string
	executor string
}

// maxActive mirrors the CRD default for specs that omit the limits block
// entirely (API-server defaulting only fires when the parent object
// exists, so a UI-created board carries no limits at all — zero must mean
// "default", not "block every launch").
func maxActive(board *boardv1alpha1.RepoBoard) int {
	if board.Spec.Limits.MaxActive <= 0 {
		return 5
	}
	return board.Spec.Limits.MaxActive
}

// reviewPlan is one auto-review to ensure (spec.auto.review). Reviews are
// always attributed: the executor is the board's owner, the run happens in
// their namespace under their identity, and the pending review lands on
// GitHub visible only to them. Plans without an executor never run. A
// Review click is a recipe Request (ensureRecipes).
type reviewPlan struct {
	pr       int
	executor string
}

func (r *Reconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	board := &boardv1alpha1.RepoBoard{}
	if err := r.Get(ctx, req.NamespacedName, board); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	owner, repo, err := parseRepoURL(board.Spec.RepoURL)
	if err != nil {
		r.setCondition(ctx, board, "Config", metav1.ConditionFalse, "InvalidRepoURL", err.Error())
		return ctrl.Result{}, nil
	}

	// Discovery identity: a personal board (its namespace is a member
	// namespace holding github-pat) reads with the member's token; a shared
	// board reads with the prep identity. Discovery only reads GitHub —
	// attributed writes always use the executor member's token.
	work := &workState{board: board, owner: owner, repo: repo}
	ghClient, err := r.discoveryClient(ctx, work)
	if err != nil {
		r.setCondition(ctx, board, "Auth", metav1.ConditionFalse, "DiscoveryIdentityUnavailable", err.Error())
		return ctrl.Result{RequeueAfter: defaultRequeue}, nil
	}
	r.setCondition(ctx, board, "Auth", metav1.ConditionTrue, "Authenticated", "GitHub discovery identity available")

	// Build the work plan: standing automation (spec.auto — deliberate,
	// recency-bounded, verb scopes named by their values) plus the
	// standing Requests (clicks, every one of them a recipe's, an apply,
	// a watch, a run or a research session). Nothing else launches
	// anything; what the member VIEWS is client-side state the controller
	// never reads.
	var fixes []fixPlan
	var reviews []reviewPlan
	switch board.Spec.Auto.Review {
	case "all":
		rv, err := r.discoverAllPRs(ctx, ghClient, work)
		if err != nil {
			logger.Error(err, "auto-review discovery failed")
		}
		reviews = append(reviews, rv...)
	case "requested":
		rv, err := r.discoverRequestedReviews(ctx, ghClient, work)
		if err != nil {
			logger.Error(err, "requested-review discovery failed")
		}
		reviews = append(reviews, rv...)
	}
	var triageCandidates []*github.Issue
	if board.Spec.Auto.Triage == "unclaimed" || board.Spec.Auto.Triage == "all" {
		tc, err := r.discoverTriage(ctx, ghClient, work)
		if err != nil {
			logger.Error(err, "triage discovery failed")
		}
		triageCandidates = tc
	}
	if board.Spec.Auto.Fix == "assigned" {
		f, err := r.discoverAssigned(ctx, ghClient, work)
		if err != nil {
			logger.Error(err, "assigned-issue discovery failed")
		}
		fixes = append(fixes, f...)
	}
	// The clicks. Requests are objects in this namespace, so a failure to
	// list them is a failure to reconcile: carrying on would read as
	// "nobody clicked anything" and quietly settle nothing.
	if err := r.loadRequests(ctx, work); err != nil {
		return ctrl.Result{}, err
	}
	mail := r.requestMailbox(work)

	// Drop plans whose executor never onboarded (no namespace/token — we
	// could not execute as them anyway).
	fixes = r.filterOnboarded(ctx, logger, fixes)

	// Aggregate sandboxes across every involved namespace: the board's own,
	// plus each planned executor's. (Sandboxes of past executors surface as
	// long as their claim — the GitHub assignment — stands.)
	namespaces := map[string]bool{board.Namespace: true}
	for _, plan := range fixes {
		namespaces[plan.executor] = true
	}
	// A research claim is served by its sandbox existing, so the
	// member's namespace has to be one the sandbox load covers. Boards
	// are personal today, which makes that the board's own namespace —
	// listed already. This is here for the same reason the fix plans
	// above list theirs: a claim from elsewhere would otherwise be
	// relaunched on every reconcile.
	for _, claim := range mail.research {
		namespaces[claim.member] = true
	}
	for _, req := range mail.applies {
		namespaces[req.Spec.Member] = true
	}
	for _, req := range mail.recipes {
		namespaces[req.Spec.Member] = true
	}
	for _, req := range mail.watches {
		namespaces[req.Spec.Member] = true
	}
	for _, plan := range reviews {
		if plan.executor != "" {
			namespaces[plan.executor] = true
		}
	}
	if err := r.loadSandboxes(ctx, work, namespaces); err != nil {
		return ctrl.Result{}, err
	}

	// One pass per fix.
	seenFix := map[string]bool{}
	for _, plan := range append(fixes, r.resumeFixes(work)...) {
		k := fmt.Sprintf("%s/%d", plan.executor, plan.issue)
		if seenFix[k] {
			continue
		}
		seenFix[k] = true
		r.ensureFix(ctx, work, plan)
	}
	for _, plan := range dedupeReviews(reviews) {
		r.ensureReview(ctx, work, plan)
	}
	// Auto-triage, which has no clicker, runs in the board owner's
	// namespace.
	for _, issue := range triageCandidates {
		r.ensureTriage(ctx, work, issue, board.Namespace, triageRunName(board.Name, issue.GetNumber()))
	}
	r.ensureApplies(ctx, work, mail.applies)
	r.ensureRecipes(ctx, work, mail.recipes)
	r.ensureWatches(ctx, work, mail.watches)
	r.ensureRunbookClaims(ctx, work, mail.runbooks)
	r.ensureResearchClaims(ctx, work, mail.research)

	// Resume in-flight auto-reviews and auto-triages: harvest finished
	// results and reattach after controller restarts.
	r.resumeReviews(ctx, work)
	r.resumeTriages(ctx, work)

	// Settle every standing click against what the passes above left
	// behind, and collect the ones that have been settled long enough.
	if err := r.reapRequests(ctx, work); err != nil {
		logger.Error(err, "request cleanup failed")
	}

	// A fix's PR is watched (auto on) from when the fix opens it, under
	// the board's autoIterate policy.
	r.fileFixWatches(ctx, work, board.Spec.Policy.AutoIterate == nil || *board.Spec.Policy.AutoIterate)

	// Pause finished sandboxes after the idle period.
	idle := time.Duration(board.Spec.Sandbox.IdleMinutes) * time.Minute
	if idle > 0 {
		r.pauseFinished(ctx, work, idle)
	}

	r.updateCounts(ctx, work)
	// A write is a GitHub call or two, and nothing wakes the board when
	// the runner finishes it: come back for its result in seconds rather
	// than leave the button saying "posting" for a minute.
	if len(mail.applies) > 0 {
		return ctrl.Result{RequeueAfter: applyRequeue}, nil
	}
	return ctrl.Result{RequeueAfter: defaultRequeue}, nil
}

type workState struct {
	board     *boardv1alpha1.RepoBoard
	owner     string
	repo      string
	discToken string // discovery-identity token (reads only)
	sandboxes []*unstructured.Unstructured
	// requests are every click filed against this board, settled and
	// not, oldest first. The standing ones drive the launch passes; the
	// settled ones are receipts waiting to be collected.
	requests []*boardv1alpha1.Request
}

func (w *workState) fixSandboxName(issue int) string {
	return factorycli.FixSandboxName(w.repo, issue)
}

// issueSandbox is issue's sandbox in namespace (factorycli.IssueSandbox),
// whatever factory named it.
func (w *workState) issueSandbox(namespace string, issue int) *unstructured.Unstructured {
	return factorycli.IssueSandbox(w.sandboxesIn(namespace), w.repo, issue)
}

// triageSandbox is the sandbox in namespace holding issue's triage
// (factorycli.TriageSandbox).
func (w *workState) triageSandbox(namespace string, issue int) *unstructured.Unstructured {
	return factorycli.TriageSandbox(w.sandboxesIn(namespace), w.repo, issue)
}

func (w *workState) sandboxesIn(namespace string) iter.Seq[*unstructured.Unstructured] {
	return func(yield func(*unstructured.Unstructured) bool) {
		for _, sb := range w.sandboxes {
			if sb.GetNamespace() == namespace && !yield(sb) {
				return
			}
		}
	}
}

func (w *workState) findSandbox(namespace, name string) *unstructured.Unstructured {
	for _, sb := range w.sandboxes {
		if sb.GetNamespace() == namespace && sb.GetName() == name {
			return sb
		}
	}
	return nil
}

// reviewSandbox is the member's sandbox of pr's review
// (factorycli.ReviewSandboxName), or nil.
func (w *workState) reviewSandbox(namespace string, pr int) *unstructured.Unstructured {
	return w.findSandbox(namespace, factorycli.ReviewSandboxName(w.repo, pr))
}

// discoveryClient resolves the owner's identity: boards are personal, so
// the board namespace holds the member's github-pat, and discovery reads,
// automation and the factory-user secret all belong to them.
func (r *Reconciler) discoveryClient(ctx context.Context, work *workState) (*github.Client, error) {
	newClient := r.NewGithubClient
	if newClient == nil {
		newClient = func(ctx context.Context, r *Reconciler, ns string) (*github.Client, string, error) {
			return r.memberGithubClient(ctx, ns)
		}
	}

	ghClient, token, err := newClient(ctx, r, work.board.Namespace)
	if err != nil {
		return nil, fmt.Errorf("board owner has no github token: %w", err)
	}
	work.discToken = token
	var login, email string
	if user, _, userErr := ghClient.Users.Get(ctx, ""); userErr == nil {
		login, email = user.GetLogin(), user.GetEmail()
	} else if fbLogin, fbEmail, ok := r.identityFromSecret(ctx, work.board.Namespace); ok {
		// Tokens that cannot answer GET /user (e.g. CI installation
		// tokens) fall back to the identity recorded alongside the PAT.
		login, email = fbLogin, fbEmail
	} else {
		return nil, fmt.Errorf("github token invalid: %w", userErr)
	}
	if err := r.ensureFactoryUserSecret(ctx, work.board.Namespace, login, email); err != nil {
		log.FromContext(ctx).Error(err, "unable to sync factory-user secret", "namespace", work.board.Namespace)
	}
	return ghClient, nil
}

// newGithubClientFromToken is injectable for tests.
var newGithubClientFromToken = githubClientFromToken

// discoverLabeled scans open items carrying the trigger label and returns
// consented fix plans plus review candidates.
// autoEligible applies the universal automation filters: the recency
// window (cold-start protection — enabling auto on an old repo processes
// the live edge, not the archive), the labels allowlist, and the
// excludeLabels veto.
func autoEligible(board *boardv1alpha1.RepoBoard, labels []*github.Label, updatedAt time.Time) bool {
	if !updatedAt.IsZero() && time.Since(updatedAt) > autoRecency(board) {
		return false
	}
	if vetoed(labels, board.Spec.Auto.ExcludeLabels) {
		return false
	}
	if len(board.Spec.Auto.Labels) > 0 && !hasAnyGithubLabel(labels, board.Spec.Auto.Labels) {
		return false
	}
	return true
}

func autoRecency(board *boardv1alpha1.RepoBoard) time.Duration {
	days := board.Spec.Auto.RecencyDays
	if days <= 0 {
		days = 7
	}
	return time.Duration(days) * 24 * time.Hour
}

func hasAnyGithubLabel(labels []*github.Label, names []string) bool {
	for _, l := range labels {
		for _, name := range names {
			if strings.EqualFold(l.GetName(), name) {
				return true
			}
		}
	}
	return false
}

// discoverAllPRs plans auto reviews for every eligible open PR (auto.review
// "all"). Reviews run as the owner — enabling the verb was their consent.
func (r *Reconciler) discoverAllPRs(ctx context.Context, ghClient *github.Client, work *workState) ([]reviewPlan, error) {
	var reviews []reviewPlan
	opts := &github.PullRequestListOptions{State: "open", ListOptions: github.ListOptions{PerPage: 100}}
	for {
		prs, resp, err := ghClient.PullRequests.List(ctx, work.owner, work.repo, opts)
		if err != nil {
			return reviews, err
		}
		for _, pr := range prs {
			if !autoEligible(work.board, pr.Labels, pr.GetUpdatedAt()) {
				continue
			}
			reviews = append(reviews, reviewPlan{pr: pr.GetNumber(), executor: work.board.Namespace})
		}
		if resp.NextPage == 0 {
			return reviews, nil
		}
		opts.Page = resp.NextPage
	}
}

// discoverRequestedReviews plans reviews for open PRs that explicitly
// request the owner's review (the standing auto-review opt-in tier).
func (r *Reconciler) discoverRequestedReviews(ctx context.Context, ghClient *github.Client, work *workState) ([]reviewPlan, error) {
	var reviews []reviewPlan
	owner := work.board.Namespace
	opts := &github.PullRequestListOptions{State: "open", ListOptions: github.ListOptions{PerPage: 100}}
	for {
		prs, resp, err := ghClient.PullRequests.List(ctx, work.owner, work.repo, opts)
		if err != nil {
			return reviews, err
		}
		for _, pr := range prs {
			if !autoEligible(work.board, pr.Labels, pr.GetUpdatedAt()) {
				continue
			}
			for _, reviewer := range pr.RequestedReviewers {
				if strings.EqualFold(reviewer.GetLogin(), owner) {
					reviews = append(reviews, reviewPlan{pr: pr.GetNumber(), executor: owner})
					break
				}
			}
		}
		if resp.NextPage == 0 {
			return reviews, nil
		}
		opts.Page = resp.NextPage
	}
}

// discoverAssigned lists open issues assigned to the personal-board member
// for the require=[assigned] auto tier.
func (r *Reconciler) discoverAssigned(ctx context.Context, ghClient *github.Client, work *workState) ([]fixPlan, error) {
	member := work.board.Namespace
	var fixes []fixPlan
	opts := &github.IssueListByRepoOptions{State: "open", Assignee: member, ListOptions: github.ListOptions{PerPage: 100}}
	for {
		items, resp, err := ghClient.Issues.ListByRepo(ctx, work.owner, work.repo, opts)
		if err != nil {
			return fixes, err
		}
		for _, item := range items {
			if item.IsPullRequest() || !autoEligible(work.board, item.Labels, item.GetUpdatedAt()) {
				continue
			}
			fixes = append(fixes, fixPlan{issue: item.GetNumber(), issueURL: item.GetHTMLURL(), executor: member})
		}
		if resp.NextPage == 0 {
			return fixes, nil
		}
		opts.Page = resp.NextPage
	}
}

// mailbox is the standing Requests, sorted into a slice per verb the UI
// can click. A struct rather than a return list because there are seven
// of them, and a reader had to count commas to tell which was which.
//
// The name is older than the Requests: this used to be one parse of a
// JSON map in a board annotation. Everything downstream of here still
// sees exactly what it saw then — requestMailbox in requests.go is the
// only thing that knows a click is now an object.
type mailbox struct {
	runbooks []runbookClaim
	research []researchClaim
	applies  []*boardv1alpha1.Request
	// watches are the PRs' autos.
	watches []*boardv1alpha1.Request
	// recipes are the recipe Requests: every click of a recipe.
	recipes []*boardv1alpha1.Request
}

// dedupeReviews keeps one plan per PR: discovery may find a PR twice.
func dedupeReviews(in []reviewPlan) []reviewPlan {
	seen := map[int]bool{}
	var out []reviewPlan
	for _, plan := range in {
		if !seen[plan.pr] {
			seen[plan.pr] = true
			out = append(out, plan)
		}
	}
	return out
}

func (r *Reconciler) filterOnboarded(ctx context.Context, logger interface{ Info(string, ...interface{}) }, fixes []fixPlan) []fixPlan {
	kept := fixes[:0]
	for _, plan := range fixes {
		if plan.executor == "" {
			continue
		}
		if _, err := r.executorToken(ctx, plan.executor); err != nil {
			logger.Info("skipping fix: executor not onboarded", "issue", plan.issue, "executor", plan.executor)
			continue
		}
		kept = append(kept, plan)
	}
	return kept
}

func (r *Reconciler) loadSandboxes(ctx context.Context, work *workState, namespaces map[string]bool) error {
	repoHint := fmt.Sprintf("github.com/%s/%s/", work.owner, work.repo)
	work.sandboxes = nil
	for namespace := range namespaces {
		list := &unstructured.UnstructuredList{}
		list.SetGroupVersionKind(sandboxGVK)
		if err := r.List(ctx, list, client.InNamespace(namespace), client.MatchingLabels{factorycli.LabelManaged: "true"}); err != nil {
			return err
		}
		for i := range list.Items {
			sb := &list.Items[i]
			if factorycli.LaunchedElsewhere(sb.GetLabels()) {
				continue
			}
			annotations := sb.GetAnnotations()
			if annotations != nil {
				if annoRepo := annotations["repo"]; annoRepo != "" && annoRepo != work.repo {
					continue
				}
				// The hint keeps its trailing slash so that open-rl does
				// not match open-rl-extra, and the URL is given one so
				// that a bare repo URL still matches. Review and fix
				// sandboxes carry a PR URL, which has a path after the
				// repo; a research sandbox is about the repo itself and
				// carries nothing after it.
				if htmlURL := annotations["htmlURL"]; htmlURL != "" && !strings.Contains(htmlURL+"/", repoHint) {
					continue
				}
			}
			work.sandboxes = append(work.sandboxes, sb)
		}
	}
	return nil
}

// resumeFixes revisits the issue sandboxes whose auto-fix is owed
// something: a fix run whose result the controller has not read, still
// running when it restarted, or just ended. Its PR is opened from it. (A
// Fix click's run is its Request's, which stands until its output is
// stored.)
//
// The runner's single-flight and the run name make relaunching
// idempotent.
func (r *Reconciler) resumeFixes(work *workState) []fixPlan {
	var out []fixPlan
	for _, sb := range work.sandboxes {
		n, ok := factorycli.IssueOf(sb, work.repo)
		if !ok {
			continue
		}
		annotations := sb.GetAnnotations()
		_, unread := fixRunUnread(annotations, work.board.Name, n)
		if !unread {
			continue
		}
		executor := annotations[AnnotationExecutor]
		if executor == "" {
			executor = sb.GetNamespace()
		}
		out = append(out, fixPlan{issue: n, issueURL: annotations["htmlURL"], executor: executor})
	}
	return out
}

// fixLike reports whether a sandbox's last task type is a fix's. An
// absent type is one: sandboxes from before type stamping only ever
// carried fixes.
func fixLike(taskType string) bool {
	return taskType == "" || strings.HasPrefix(taskType, "fix")
}

// ensureFix launches an auto-fix as the plan's executor, in the
// executor's namespace with the executor's identity, or follows one to
// its end: `factory recipe fix` in the issue's sandbox, whose pushed
// branch the runner opens as a draft PR once the run ends (StartRecipe,
// Apply open-pr). The PR is GitHub's, and the sandbox is aliased to it.
func (r *Reconciler) ensureFix(ctx context.Context, work *workState, plan fixPlan) {
	logger := log.FromContext(ctx)
	sb := work.issueSandbox(plan.executor, plan.issue)
	name := work.fixSandboxName(plan.issue)
	if sb != nil {
		name = sb.GetName()
	}
	key := fixKey(work, plan.executor, plan.issue)

	if r.Factory.IsRunning(key) {
		return
	}
	annotations := map[string]string{}
	if sb != nil {
		r.recordFixResult(ctx, sb, key)
		if sb.GetAnnotations() != nil {
			annotations = sb.GetAnnotations()
		}
	}
	state := annotations[factorycli.AnnotationTaskState]
	// A fix run recorded and not read, by a controller that did not see
	// it end: followed by its name, for the PR it pushed.
	followed, follow := "", false
	if _, ok := r.Factory.LastResult(key); !ok {
		followed, follow = fixRunUnread(annotations, work.board.Name, plan.issue)
	}
	// Terminal means a fix ran to an end state, the automation's or a
	// member's click: automation does not fix an issue again. The
	// task-state stamps are per-sandbox, not per-type: a completed plan
	// in the same sandbox must not masquerade as a finished fix.
	terminal := (state == factorycli.TaskStateCompleted || state == factorycli.TaskStateFailed) &&
		fixLike(annotations[factorycli.AnnotationTaskType])
	if terminal && !follow {
		return
	}
	// Something else is at work in the sandbox (a plan, a follow-up the
	// watch runs): the fix waits for it rather than being refused.
	if state == factorycli.TaskStateRunning && !follow {
		return
	}
	if res, ok := r.Factory.LastResult(key); ok && res.Err != nil && time.Since(res.FinishedAt) < launchRetryBackoff {
		return
	}
	if sb == nil && r.activeCount(work) >= maxActive(work.board) {
		log.FromContext(ctx).Info("fix deferred: board at maxActive", "issue", plan.issue, "limit", maxActive(work.board))
		return
	}
	token, err := r.executorToken(ctx, plan.executor)
	if err != nil {
		logger.Error(err, "executor token unavailable", "executor", plan.executor)
		return
	}
	if err := r.ensureFactoryUserSecret(ctx, plan.executor, plan.executor, ""); err != nil {
		logger.Error(err, "unable to sync executor factory-user secret", "executor", plan.executor)
	}

	issueURL := plan.issueURL
	if issueURL == "" || strings.Contains(issueURL, "/pull/") {
		// A fix sandbox's htmlURL is its PR's once it has one.
		issueURL = fmt.Sprintf("https://github.com/%s/%s/issues/%d", work.owner, work.repo, plan.issue)
	}
	runName := fixRunName(work.board.Name, plan.issue)
	if follow {
		runName = followed
	}
	r.stampUnpaused(ctx, sb)
	r.stampEngine(ctx, sb, boardEngine(work.board))
	// The PR is always a draft: open-pr opens nothing else.
	opts := r.recipeOptions(work, "fix", plan.executor, name, issueURL, token, runName)
	opts.Timeout, opts.Apply = 3*time.Hour, "open-pr"
	if r.Factory.StartRecipe(key, opts) {
		logger.Info("launched factory recipe fix", "issue", plan.issue, "executor", plan.executor, "board", work.board.Name, "run", runName)
	}
}

// recipeOptions are the board's options for a run of recipe on url, in
// sandbox (empty: factory's for url), as member, named runName: the
// board's sandbox image and disk, its engine and its disclosure.
func (r *Reconciler) recipeOptions(work *workState, recipe, member, sandbox, url, token, runName string) factorycli.RecipeOptions {
	return factorycli.RecipeOptions{
		Recipe:            recipe,
		URL:               url,
		SandboxName:       sandbox,
		Namespace:         member,
		Image:             work.board.Spec.Sandbox.Image,
		WorkspaceDiskSize: work.board.Spec.Sandbox.DiskSize,
		GithubToken:       token,
		Engine:            boardEngine(work.board),
		Disclose:          work.board.Spec.Policy.Disclose,
		RunName:           runName,
	}
}

// ensureReview launches an auto-review, or reads what came of one:
// `factory recipe review` in the executor's review sandbox, whose Review
// the runner posts as the executor's pending review on the PR once the
// run ends (StartRecipe, Apply post-review). The draft is GitHub's.
func (r *Reconciler) ensureReview(ctx context.Context, work *workState, plan reviewPlan) {
	logger := log.FromContext(ctx)

	// Every review is attributed: it runs in the consenting member's
	// namespace under their identity and posts a pending review on GitHub
	// (visible only to them — saved work, not a public act). Plans without
	// an executor never run; anonymous reviews are tokens spent on a
	// review nobody asked for.
	if plan.executor == "" {
		return
	}
	key := reviewKey(work, plan.executor, plan.pr)
	name := factorycli.ReviewSandboxName(work.repo, plan.pr)
	sb := work.reviewSandbox(plan.executor, plan.pr)
	annotations := map[string]string{}
	if sb != nil && sb.GetAnnotations() != nil {
		annotations = sb.GetAnnotations()
	}
	// Posted, abandoned and errored reviews are terminal for automation,
	// and so is a review a member ran: a member clicks Review again.
	if reviewDone(annotations, work.board.Name, plan.pr) {
		return
	}
	if r.Factory.IsRunning(key) {
		return
	}

	if res, ok := r.Factory.LastResult(key); ok && !resultSuperseded(sb, res) {
		if res.Err == nil && sb != nil {
			// The pending review is on GitHub; record that so the board
			// points the member there.
			if err := r.markReviewPending(ctx, sb, work.board.Name, res); err != nil {
				logger.Error(err, "unable to mark review pending", "pr", plan.pr)
			}
			return
		}
		if res.Err != nil && sb != nil {
			// A failed run parks the review: no auto-retry.
			if err := r.markReviewError(ctx, sb, work.board.Name, reviewErrorLine(res)); err != nil {
				logger.Error(err, "unable to record review error", "pr", plan.pr)
			}
			return
		}
		// A failure before the sandbox existed: back off rather than
		// hot-looping the agent.
		if time.Since(res.FinishedAt) < launchRetryBackoff {
			return
		}
	}

	if sb == nil && r.activeCount(work) >= maxActive(work.board) {
		logger.Info("review deferred: board at maxActive", "pr", plan.pr, "limit", maxActive(work.board))
		return
	}
	token, err := r.executorToken(ctx, plan.executor)
	if err != nil {
		logger.Error(err, "review executor has no token", "executor", plan.executor, "pr", plan.pr)
		return
	}
	if err := r.ensureFactoryUserSecret(ctx, plan.executor, plan.executor, ""); err != nil {
		logger.Error(err, "unable to sync factory-user secret", "namespace", plan.executor)
		return
	}
	// GitHub allows one pending review per author per PR. factory's post
	// replaces a pending review it posted, and refuses one the member
	// wrote: a fresh launch must not race the member's own (the post
	// would fail after burning a full run).
	if sb == nil {
		gh := newGithubClientFromToken(ctx, token)
		pending, reviewed, err := executorReviewStates(ctx, gh, work.owner, work.repo, plan.pr, plan.executor)
		if err != nil {
			logger.Error(err, "unable to check for existing reviews; deferring launch", "pr", plan.pr)
			return
		}
		if pending {
			logger.Info("the member's own pending review is parked on GitHub; not launching", "pr", plan.pr, "executor", plan.executor)
			return
		}
		if reviewed {
			// GitHub already carries the executor's submitted review: done
			// is done for automation (this survives lost sandbox
			// breadcrumbs). Only a member's explicit click reviews again.
			logger.Info("review already submitted on GitHub; automation defers", "pr", plan.pr, "executor", plan.executor)
			return
		}
	}
	// A run this controller did not see end (a restart's) is followed by
	// its recorded name; another run recorded under the same key (one
	// named otherwise) is not the review's.
	runName := r.resumableRun(key, annotations, factorycli.AnnotationReviewRun, reviewRunName(work.board.Name, plan.pr),
		AnnotationReviewedAt, AnnotationReviewErrorAt, AnnotationReviewAbandoned)
	if !strings.HasPrefix(runName, reviewRunPrefix(work.board.Name, plan.pr)) {
		runName = reviewRunName(work.board.Name, plan.pr)
	}
	r.stampUnpaused(ctx, sb)
	r.stampEngine(ctx, sb, boardEngine(work.board))
	opts := r.recipeOptions(work, "review", plan.executor, name, fmt.Sprintf("https://github.com/%s/%s/pull/%d", work.owner, work.repo, plan.pr), token, runName)
	opts.Timeout, opts.Apply = 45*time.Minute, "post-review"
	if r.Factory.StartRecipe(key, opts) {
		logger.Info("launched factory recipe review", "pr", plan.pr, "board", work.board.Name, "executor", plan.executor)
	}
}

// resumableRun is the run name to invoke factory with for key: the run
// recorded on the sandbox under runKey, when this controller has no
// result for key (it restarted, or another replica started the run) and
// the run started after every marker — a run older than the last harvest
// or reject is not the one wanted. Invoking factory with it
// follows the run to its end, or reads its result if it has one; a run
// that failed fails again at once, and the retry after the backoff, with
// a result now in memory, gets fresh. Otherwise, fresh.
func (r *Reconciler) resumableRun(key string, annotations map[string]string, runKey, fresh string, markers ...string) string {
	if _, ok := r.Factory.LastResult(key); ok {
		return fresh
	}
	var since time.Time
	for _, m := range markers {
		if at, err := time.Parse(time.RFC3339, annotations[m]); err == nil && at.After(since) {
			since = at
		}
	}
	if name, ok := factorycli.RecordedRunName(annotations, runKey, since); ok {
		return name
	}
	return fresh
}

// reviewKey is the runner key of a PR's reviews.
func reviewKey(work *workState, member string, pr int) string {
	return fmt.Sprintf("%s/review-%s-%d", member, work.repo, pr)
}

// reviewRunName is what an auto-review's task is recorded under in its
// sandbox, to read its result by. factory runs a name once, so it names
// one launch.
func reviewRunName(board string, pr int) string {
	return fmt.Sprintf("%s%d", reviewRunPrefix(board, pr), time.Now().Unix())
}

func reviewRunPrefix(board string, pr int) string {
	return fmt.Sprintf("review/%s/%d/", board, pr)
}

// reviewDone reports whether automation is done with a PR's review
// sandbox: a review was posted, abandoned or failed there, or a member ran
// one (a click's run, recorded under any name but an auto-review's).
func reviewDone(annotations map[string]string, board string, pr int) bool {
	return annotations[AnnotationReviewedAt] != "" || annotations[AnnotationReviewAbandoned] != "" ||
		annotations[AnnotationReviewError] != "" || memberRun(annotations, factorycli.AnnotationReviewRun, reviewRunPrefix(board, pr))
}

// memberRun reports whether the run recorded under runKey is a member's:
// named otherwise than automation names its runs (autoPrefix). A click's
// run is its Request's (request/<uid>).
func memberRun(annotations map[string]string, runKey, autoPrefix string) bool {
	run, ok := factorycli.RecordedRunAt(annotations, runKey)
	return ok && run.Name != "" && !strings.HasPrefix(run.Name, autoPrefix)
}

// resumeReviews revisits the review sandboxes whose auto-review has not
// ended: following a run a restart lost sight of, by its recorded name.
func (r *Reconciler) resumeReviews(ctx context.Context, work *workState) {
	for _, sb := range work.sandboxes {
		pr, ok := factorycli.ReviewPROf(sb, work.repo)
		if !ok || reviewDone(sb.GetAnnotations(), work.board.Name, pr) {
			continue
		}
		// The sandbox is in its executor's namespace.
		r.ensureReview(ctx, work, reviewPlan{pr: pr, executor: sb.GetNamespace()})
	}
}

// markReviewPending records that the auto-review was posted as a pending
// review on GitHub under the executor's identity — the member finalizes it
// there — and keeps the Review it posted (res's) as its run's output.
func (r *Reconciler) markReviewPending(ctx context.Context, sb *unstructured.Unstructured, boardName string, res factorycli.Result) error {
	annotations := sb.GetAnnotations()
	if annotations == nil {
		annotations = map[string]string{}
	}
	factorycli.KeepOutput(annotations, factorycli.AnnotationReviewRun, factorycli.HarvestedTaskOutput(res.Output), res.FinishedAt, "post-review")
	annotations[AnnotationReviewedAt] = time.Now().UTC().Format(time.RFC3339)
	annotations[AnnotationBoard] = boardName
	delete(annotations, AnnotationReviewError)
	delete(annotations, AnnotationReviewErrorAt)
	sb.SetAnnotations(annotations)
	return r.Update(ctx, sb)
}

// markReviewError parks a failed auto-review on the sandbox: the message
// renders on the board, and automation does not try again.
func (r *Reconciler) markReviewError(ctx context.Context, sb *unstructured.Unstructured, boardName, msg string) error {
	annotations := sb.GetAnnotations()
	if annotations == nil {
		annotations = map[string]string{}
	}
	annotations[AnnotationReviewError] = msg
	annotations[AnnotationReviewErrorAt] = time.Now().UTC().Format(time.RFC3339)
	annotations[AnnotationBoard] = boardName
	sb.SetAnnotations(annotations)
	return r.Update(ctx, sb)
}

// reviewErrorLine digs the most useful line out of a failed invocation's
// output — factory prints "Error: ..." on its way out.
func reviewErrorLine(res factorycli.Result) string {
	lines := strings.Split(strings.TrimSpace(res.Output), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if strings.HasPrefix(line, "Error:") {
			return clipMessage(strings.TrimSpace(strings.TrimPrefix(line, "Error:")), 300)
		}
	}
	if res.Err != nil {
		return clipMessage(res.Err.Error(), 300)
	}
	return "review failed"
}

func clipMessage(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// runbookClaim is a runbook execution click from the Try tab.
type runbookClaim struct {
	mode      string // run | teardown
	scenario  string // the runbook name: deploy-gcp, upgrade-gcp, …
	instance  string // default: the runbook name
	member    string
	claimedAt time.Time
	// intent is this run's brief, carried on the claim so it dies with
	// it rather than outliving every other run on the board.
	intent string
	// runbook is what a new run is copied from before it is planned.
	runbook string
	// target is the pull request a plan pins the run to.
	target int
	// request is the click this claim came from. Run is the only verb
	// that keeps it: its "already done this" receipt is the runner's
	// in-memory result, so it is the only one that has to write down
	// that it is about to spend money. See markLaunching.
	request *boardv1alpha1.Request
}

func runbookKey(member, repo string, c runbookClaim) string {
	instance := factorycli.RunbookInstance(c.scenario, c.instance)
	return fmt.Sprintf("%s/%s-%s", member, factorycli.RunSandboxName(repo, instance), c.mode)
}

// runbookClaimConsumed: runbook claims are ONE-SHOT — a click buys
// exactly one launch attempt, whatever its outcome. Deploys have cloud
// side effects, so a failure is terminal (the ❌/🔒 badge prompts the
// owner to Re-deploy deliberately), and a watcher timeout must not
// duplicate work the pod is still doing. Any result newer than the
// click consumes the claim.
func (r *Reconciler) runbookClaimConsumed(claim runbookClaim, work *workState) bool {
	res, ok := r.Factory.LastResult(runbookKey(claim.member, work.repo, claim))
	return ok && res.FinishedAt.After(claim.claimedAt)
}

// ensureRunbookClaims: the Request is the click, the runner result is
// the receipt, and factory ensures the run sandbox itself. run and
// teardown for the same path share a sandbox, so the sandbox-wide
// preflight serializes them.
func (r *Reconciler) ensureRunbookClaims(ctx context.Context, work *workState, claims []runbookClaim) {
	logger := log.FromContext(ctx)
	for _, claim := range claims {
		key := runbookKey(claim.member, work.repo, claim)
		if r.Factory.IsRunning(key) {
			continue
		}
		// Launched once already by a process that is not running it any
		// more: this controller restarted, and the deploy it started may
		// well have landed — or may still be going, in the pod, which
		// outlives us. Never a second attempt. The reap pass reads the
		// outcome off the sandbox, and strands the click if it cannot.
		//
		// The test is launchedAt and not the phase: the phase is Running
		// for the whole length of the run, which is where a restart
		// actually lands.
		if claim.request != nil && claim.request.Status.LaunchedAt != nil {
			continue
		}
		if r.runbookClaimConsumed(claim, work) {
			continue // the trim pass drops it — no retry for runbook work
		}
		token, err := r.executorToken(ctx, claim.member)
		if err != nil {
			continue
		}
		instance := factorycli.RunbookInstance(claim.scenario, claim.instance)
		name := factorycli.RunSandboxName(work.repo, instance)
		if sb := work.findSandbox(claim.member, name); sb != nil {
			r.stampUnpaused(ctx, sb)
			r.stampEngine(ctx, sb, boardEngine(work.board))
		}
		mode := runMode(claim.mode)
		// Before, not after. A controller killed between here and the
		// result has no memory that it deployed; the Request does, and
		// the reap pass strands it rather than deploying twice.
		r.markLaunching(ctx, claim.request, name)
		launched := r.Factory.StartRun(key, factorycli.RunOptions{
			Namespace:   claim.member,
			SandboxName: name,
			Mode:        mode,
			// The run's name is the old instance name. Adoption of a
			// legacy deployment matches on it, and so does the
			// sandbox, so an existing deployment keeps its workspace.
			Name:        instance,
			Intent:      runIntent(claim.scenario, instance, claim.intent),
			Runbook:     claim.runbook,
			Target:      claim.target,
			RepoURL:     fmt.Sprintf("https://github.com/%s/%s", work.owner, work.repo),
			GithubToken: token,
			Engine:      boardEngine(work.board),
		})
		if launched {
			logger.Info("launched factory run", "mode", mode, "name", instance, "board", work.board.Name)
			continue
		}
		// Refused, not spent: the sandbox is busy with a task this
		// process did not start, or the slot was taken between the
		// check above and here. Take the stamp back so the click is
		// still a click, and try again next reconcile.
		logger.V(1).Info("run launch refused; sandbox busy", "mode", mode, "name", instance, "board", work.board.Name)
		r.unmarkLaunching(ctx, claim.request)
	}
}

// runMode maps a claim's mode onto what `factory run` offers. The
// combined plan-and-execute pass is gone: a claim asking for it stops
// at the plan gate instead, which is what the owner would see anyway
// and cannot spend money without their say-so.
func runMode(claimMode string) string {
	switch claimMode {
	case "deploy", "teardown":
		return claimMode
	default:
		return "plan"
	}
}

// runIntent carries the scenario forward when it still says something.
// A runbook used to be a shared document the scenario pointed at;
// there is no such document now, so the shape it named survives only
// as words in the brief — and only when the run's own name does not
// already carry it.
func runIntent(scenario, name, guidance string) string {
	guidance = strings.TrimSpace(guidance)
	scenario = strings.TrimSpace(scenario)
	if scenario == "" || scenario == name {
		return guidance
	}
	shape := "This run is a " + scenario + "."
	if guidance == "" {
		return shape
	}
	return shape + " " + guidance
}

func (r *Reconciler) pauseFinished(ctx context.Context, work *workState, after time.Duration) {
	logger := log.FromContext(ctx)
	for _, sb := range work.sandboxes {
		// Run sandboxes (type=run) may be SERVING something — idle
		// pause would kill the deployment. Excluded for now; lifecycle
		// is the Tear down button.
		if sb.GetLabels()["sandbox.gemini.google.com/type"] == factorycli.RunSandboxType {
			continue
		}
		annotations := sb.GetAnnotations()
		if annotations[AnnotationPreventAutoPause] == "true" {
			continue
		}
		// Triage in an issue's sandbox keeps its own state: running, the
		// sandbox is busy whatever the fix's says; alone, it is the state.
		state := annotations[factorycli.AnnotationTaskState]
		triage := factorycli.TriageTaskState(annotations)
		if triage == "Running" || state == "" {
			state = triage
		}
		if state != factorycli.TaskStateCompleted && state != factorycli.TaskStateFailed {
			continue
		}
		idleSince, err := time.Parse(time.RFC3339, annotations[factorycli.AnnotationCompletionTime])
		if err != nil {
			continue
		}
		if unpausedAt, err := time.Parse(time.RFC3339, annotations[AnnotationUnpausedAt]); err == nil && unpausedAt.After(idleSince) {
			idleSince = unpausedAt
		}
		if time.Since(idleSince) < after {
			continue
		}
		replicas, found, err := unstructured.NestedInt64(sb.Object, "spec", "replicas")
		if err != nil || (found && replicas == 0) {
			continue
		}
		if err := unstructured.SetNestedField(sb.Object, int64(0), "spec", "replicas"); err != nil {
			continue
		}
		if err := r.Update(ctx, sb); err != nil {
			logger.Error(err, "unable to pause sandbox", "sandbox", sb.GetName(), "namespace", sb.GetNamespace())
		}
	}
}

func (r *Reconciler) activeCount(work *workState) int {
	active := 0
	for _, sb := range work.sandboxes {
		// Run sandboxes (type=run) host living deployments — they
		// are not task slots and never count against maxActive.
		if sb.GetLabels()["sandbox.gemini.google.com/type"] == factorycli.RunSandboxType {
			continue
		}
		replicas, found, err := unstructured.NestedInt64(sb.Object, "spec", "replicas")
		if err == nil && found && replicas > 0 {
			active++
		}
	}
	return active
}

func (r *Reconciler) updateCounts(ctx context.Context, work *workState) {
	needsHuman := 0
	for _, sb := range work.sandboxes {
		annotations := sb.GetAnnotations()
		if annotations[factorycli.AnnotationTriageOutput] != "" && !factorycli.IsApplied(annotations, factorycli.AnnotationTriageApplied, "comment") {
			needsHuman++
			continue
		}
		_, isIssueSB := factorycli.IssueOf(sb, work.repo)
		if annotations[factorycli.AnnotationTaskState] == factorycli.TaskStateCompleted &&
			strings.Contains(annotations["htmlURL"], "/pull/") && isIssueSB {
			needsHuman++
		}
	}
	work.board.Status.Counts = boardv1alpha1.BoardCounts{
		NeedsHuman: needsHuman,
		Active:     r.activeCount(work),
	}
	// What the board's rows offer to launch, for the API to read.
	if catalog := r.recipeCatalog(ctx); catalog != nil {
		work.board.Status.Recipes = catalog
	}
	if err := r.Status().Update(ctx, work.board); err != nil {
		log.FromContext(ctx).Error(err, "unable to update board status")
	}
}

// factoryReviewMarker opens the marker factory ends a review it posts
// with (factory/pkg/taskoutput).
const factoryReviewMarker = "<!-- factory:task-output kind=Review "

// executorReviewStates reports whether the executor has a pending review
// of their own parked on the PR and whether they have any submitted one.
// A pending review factory posted (its body carries factory's Review
// marker) is not the executor's own: posting a new review replaces it.
// Pending reviews are only visible to their author, so the check must run
// under the executor's own token.
func executorReviewStates(ctx context.Context, gh *github.Client, owner, repo string, pr int, executor string) (pending, reviewed bool, err error) {
	reviews, _, err := gh.PullRequests.ListReviews(ctx, owner, repo, pr, &github.ListOptions{PerPage: 100})
	if err != nil {
		return false, false, err
	}
	for _, rv := range reviews {
		if !strings.EqualFold(rv.GetUser().GetLogin(), executor) {
			continue
		}
		switch strings.ToUpper(rv.GetState()) {
		case "PENDING":
			pending = pending || !strings.Contains(rv.GetBody(), factoryReviewMarker)
		case "APPROVED", "CHANGES_REQUESTED", "COMMENTED":
			reviewed = true
		}
	}
	return pending, reviewed, nil
}

// resultSuperseded reports whether a remembered invocation result predates
// the member abandoning the review and must not be re-recorded.
func resultSuperseded(sb *unstructured.Unstructured, res factorycli.Result) bool {
	if sb == nil {
		return false
	}
	at, err := time.Parse(time.RFC3339, sb.GetAnnotations()[AnnotationReviewAbandoned])
	return err == nil && res.FinishedAt.Before(at)
}

// stampUnpaused marks a paused sandbox we are about to relaunch into.
// pauseFinished treats unpaused-at as the new idle baseline, so the pause
// pass cannot kill the pod mid-boot (the #1423 race).
func (r *Reconciler) stampUnpaused(ctx context.Context, sb *unstructured.Unstructured) {
	if sb == nil {
		return
	}
	replicas, found, err := unstructured.NestedInt64(sb.Object, "spec", "replicas")
	if err != nil || !found || replicas != 0 {
		return
	}
	annotations := sb.GetAnnotations()
	if annotations == nil {
		annotations = map[string]string{}
	}
	annotations[AnnotationUnpausedAt] = time.Now().UTC().Format(time.RFC3339)
	sb.SetAnnotations(annotations)
	if err := r.Update(ctx, sb); err != nil {
		log.FromContext(ctx).Error(err, "unable to stamp wake on paused sandbox", "sandbox", sb.GetName())
	}
}

// boardEngine resolves the board's engine choice (default gemini).
func boardEngine(board *boardv1alpha1.RepoBoard) string {
	if board.Spec.Sandbox.Engine != "" {
		return board.Spec.Sandbox.Engine
	}
	return "gemini"
}

// stampEngine records the launching engine on the sandbox so the chat
// terminal resumes the conversation with the same CLI. Stamped at launch:
// an engine flip on the board affects the next launch only.
func (r *Reconciler) stampEngine(ctx context.Context, sb *unstructured.Unstructured, engine string) {
	if sb == nil {
		return
	}
	annotations := sb.GetAnnotations()
	if annotations[AnnotationEngine] == engine {
		return
	}
	if annotations == nil {
		annotations = map[string]string{}
	}
	annotations[AnnotationEngine] = engine
	sb.SetAnnotations(annotations)
	if err := r.Update(ctx, sb); err != nil {
		log.FromContext(ctx).Error(err, "unable to stamp engine on sandbox", "sandbox", sb.GetName())
	}
}

func vetoed(labels []*github.Label, excluded []string) bool {
	for _, l := range labels {
		for _, e := range excluded {
			if strings.EqualFold(l.GetName(), e) {
				return true
			}
		}
	}
	return false
}

func (r *Reconciler) setCondition(ctx context.Context, board *boardv1alpha1.RepoBoard, condType string, status metav1.ConditionStatus, reason, message string) {
	apimeta.SetStatusCondition(&board.Status.Conditions, metav1.Condition{
		Type:               condType,
		Status:             status,
		Reason:             reason,
		Message:            message,
		ObservedGeneration: board.Generation,
	})
	if err := r.Status().Update(ctx, board); err != nil {
		log.FromContext(ctx).Error(err, "unable to update board condition")
	}
}

// SetupWithManager sets up the controller with the Manager.
func (r *Reconciler) SetupWithManager(mgr ctrl.Manager, concurrency int) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&boardv1alpha1.RepoBoard{}).
		// A click used to be an annotation on the board, so filing one
		// woke this loop for free. A Request is its own object, and
		// owned by the board — so this is what keeps the launch latency
		// where it was instead of up to a requeue interval behind.
		Owns(&boardv1alpha1.Request{}).
		WithOptions(controller.Options{MaxConcurrentReconciles: concurrency}).
		Complete(r)
}
