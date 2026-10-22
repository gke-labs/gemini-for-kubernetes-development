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
	"crypto/sha256"
	"encoding/hex"
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
	// Sandbox annotations.
	AnnotationPreventAutoPause = "sandbox.gemini.google.com/prevent-auto-shutdown"
	AnnotationUnpausedAt       = "sandbox.gemini.google.com/unpaused-at"
	AnnotationBoard            = "board.gemini.google.com/board"
	// AnnotationExecutor records which member a run on the sandbox ran
	// as (keepRecipeOutput).
	AnnotationExecutor = "board.gemini.google.com/executor"
	// AnnotationEngine records which agent engine launched into this
	// sandbox — sessions are engine-private, so the chat terminal must
	// resume with the same CLI that ran the task.
	AnnotationEngine = "board.gemini.google.com/engine"
	// AnnotationViewedAt is when someone last read the board's feed (the
	// API stamps it, at most every two minutes, while a tab polls it).
	AnnotationViewedAt = "board.gemini.google.com/viewed-at"
	// defaultRequeue paces a board someone is looking at (viewed within
	// viewedWithin) or one with work standing; idleRequeue every other
	// board: its automation picks new items up within ten minutes, and
	// GitHub is listed a tenth as often (github-load.md).
	defaultRequeue          = time.Minute
	idleRequeue             = 10 * time.Minute
	viewedWithin            = 5 * time.Minute
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

	// catalogs are the recipes factory runs for each board's repository
	// (recipeCatalog), by owner/repo.
	catalogMu sync.Mutex
	catalogs  map[string]boardCatalog

	// refusals are the recipe Requests ensureRecipe refused to launch, by
	// UID, with why: settleRecipe fails them with it (refuseRecipe).
	refusals sync.Map

	// DiscoveryEvery is the least time between two GitHub discoveries of
	// one board (the owner's identity and automation's listings): a
	// Request's every status write wakes the board, and those reconciles
	// settle and launch without listing GitHub again. Zero discovers on
	// every reconcile.
	DiscoveryEvery time.Duration
	// discoveredAt is when each board (namespace/name) was last
	// discovered.
	discoveredAt sync.Map
	// identities are the logins GET /user answered, by token hash
	// (cachedIdentity), for identityTTL: a token's owner does not change
	// between discoveries.
	identities sync.Map
	// reviewedPRs are the PRs a Review rule found the owner had reviewed,
	// by board and PR, with the PR's updated_at then: not asked again until
	// it changes (ownerReviewed).
	reviewedPRs sync.Map
}

// identityTTL is how long a token's login is taken as known.
const identityTTL = 15 * time.Minute

// cachedIdentity is a token's login and email, and when GitHub said so.
type cachedIdentity struct {
	login, email string
	at           time.Time
}

// tokenKey names a token without holding it.
func tokenKey(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:8])
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

func (r *Reconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	board := &boardv1alpha1.RepoBoard{}
	if err := r.Get(ctx, req.NamespacedName, board); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// The deprecated auto.triage, auto.fix and auto.review become rules,
	// once (generic-board-recipes.md, "Auto for any recipe").
	if board.Spec.Auto.ConvertLegacy() {
		if err := r.Update(ctx, board); err != nil {
			return ctrl.Result{}, err
		}
		logger.Info("converted the board's auto settings into rules", "rules", len(board.Spec.Auto.Rules))
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
	var wanted []autoRequest
	if r.discoveryDue(req.NamespacedName.String(), time.Now()) {
		ghClient, err := r.discoveryClient(ctx, work)
		if err != nil {
			r.setCondition(ctx, board, "Auth", metav1.ConditionFalse, "DiscoveryIdentityUnavailable", err.Error())
			return ctrl.Result{RequeueAfter: defaultRequeue}, nil
		}
		r.setCondition(ctx, board, "Auth", metav1.ConditionTrue, "Authenticated", "GitHub discovery identity available")
		// The work: standing automation (spec.auto — deliberate,
		// recency-bounded), which files recipe Requests as the board's
		// owner.
		wanted = r.discoverAuto(ctx, ghClient, work)
	}

	// Then the standing Requests (clicks and automation's alike: a
	// recipe's, an apply, a watch, a run or a research session). Nothing
	// else launches anything; that a member is viewing the board only
	// paces the reconcile (requeueAfter).
	// Requests are objects in this namespace, so a failure to list them
	// is a failure to reconcile: carrying on would read as "nobody
	// clicked anything" and quietly settle nothing.
	if err := r.loadRequests(ctx, work); err != nil {
		return ctrl.Result{}, err
	}
	mail := r.requestMailbox(work)

	// Aggregate sandboxes across every involved namespace: the board's
	// own, plus each Request's member's. A research claim is served by
	// its sandbox existing, so the member's namespace has to be one the
	// sandbox load covers, or the claim would be relaunched on every
	// reconcile.
	namespaces := map[string]bool{board.Namespace: true}
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
	if err := r.loadSandboxes(ctx, work, namespaces); err != nil {
		return ctrl.Result{}, err
	}

	// What automation is owed is filed now and launched as the Requests
	// it files wake the board, like a click's.
	r.fileAutoRequests(ctx, work, wanted)
	r.ensureApplies(ctx, work, mail.applies)
	r.ensureRecipes(ctx, work, mail.recipes)
	r.ensureWatches(ctx, work, mail.watches)
	r.ensureRunbookClaims(ctx, work, mail.runbooks)
	r.ensureResearchClaims(ctx, work, mail.research)

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
	busy := len(mail.recipes)+len(mail.runbooks)+len(mail.research) > 0
	return ctrl.Result{RequeueAfter: requeueAfter(board, busy, time.Now())}, nil
}

// requeueAfter paces a board: every minute while someone is viewing it or
// while work stands (a recipe, run or research Request), which nothing
// else wakes it to settle — a run's end is the runner's, not an event —
// and every ten minutes otherwise.
func requeueAfter(board *boardv1alpha1.RepoBoard, busy bool, now time.Time) time.Duration {
	if busy {
		return defaultRequeue
	}
	if at, err := time.Parse(time.RFC3339, board.GetAnnotations()[AnnotationViewedAt]); err == nil && now.Sub(at) < viewedWithin {
		return defaultRequeue
	}
	return idleRequeue
}

// discoveryDue reports whether board is due a GitHub discovery, and if so
// records it as discovered now.
func (r *Reconciler) discoveryDue(board string, now time.Time) bool {
	if r.DiscoveryEvery <= 0 {
		return true
	}
	if at, ok := r.discoveredAt.Load(board); ok && now.Sub(at.(time.Time)) < r.DiscoveryEvery {
		return false
	}
	r.discoveredAt.Store(board, now)
	return true
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

// issueSandbox is issue's sandbox in namespace (factorycli.IssueSandbox),
// whatever factory named it.
func (w *workState) issueSandbox(namespace string, issue int) *unstructured.Unstructured {
	return factorycli.IssueSandbox(w.sandboxesIn(namespace), w.repo, issue)
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
	if id, ok := r.identities.Load(tokenKey(token)); ok && time.Since(id.(cachedIdentity).at) < identityTTL {
		login, email = id.(cachedIdentity).login, id.(cachedIdentity).email
	} else if user, _, userErr := ghClient.Users.Get(ctx, ""); userErr == nil {
		login, email = user.GetLogin(), user.GetEmail()
		r.identities.Store(tokenKey(token), cachedIdentity{login: login, email: email, at: time.Now()})
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

// recipeOptions are the board's options for a run of recipe on url, in
// sandbox (empty: factory's for url), as member, named runName: the
// board's sandbox image, disk and size, its engine and its disclosure.
func (r *Reconciler) recipeOptions(work *workState, recipe, member, sandbox, url, token, runName string) factorycli.RecipeOptions {
	return factorycli.RecipeOptions{
		Recipe:            recipe,
		URL:               url,
		SandboxName:       sandbox,
		Namespace:         member,
		Image:             work.board.Spec.Sandbox.Image,
		WorkspaceDiskSize: work.board.Spec.Sandbox.DiskSize,
		Sandbox:           boardSandboxSize(work.board.Spec.Sandbox),
		GithubToken:       token,
		Engine:            boardEngine(work.board),
		Disclose:          work.board.Spec.Policy.Disclose,
		RunName:           runName,
	}
}

// boardSandboxSize is the size the board's sandboxes are made with.
func boardSandboxSize(s boardv1alpha1.SandboxSpec) factorycli.SandboxSize {
	return factorycli.SandboxSize{
		StorageClass:     s.StorageClass,
		EphemeralStorage: s.EphemeralStorage,
		CPURequest:       s.CPURequest,
		CPULimit:         s.CPULimit,
		MemoryRequest:    s.MemoryRequest,
		MemoryLimit:      s.MemoryLimit,
	}
}

// runErrorLine digs the most useful line out of a failed run's output:
// factory prints "Error: ..." on its way out.
func runErrorLine(res factorycli.Result) string {
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
	return "the run failed"
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
	catalog := r.recipeCatalog(ctx, work)
	work.board.Status.Counts = boardv1alpha1.BoardCounts{
		NeedsHuman: needsHuman(work.sandboxes, catalog),
		Active:     r.activeCount(work),
	}
	// What the board's rows offer to launch, for the API to read.
	if catalog != nil {
		work.board.Status.Recipes = catalog
	}
	if err := r.Status().Update(ctx, work.board); err != nil {
		log.FromContext(ctx).Error(err, "unable to update board status")
	}
}

// needsHuman counts the runs waiting on someone, whichever recipe: per
// sandbox, the newest run of each group (a session's recipes are one, as
// a row shows them) when it is ready (a draft to judge) or failed.
func needsHuman(sandboxes []*unstructured.Unstructured, catalog []boardv1alpha1.BoardRecipe) int {
	n := 0
	for _, sb := range sandboxes {
		annotations := sb.GetAnnotations()
		seen := map[string]bool{}
		for _, run := range factorycli.Runs(annotations) { // newest first
			group := run.Recipe
			if rec, ok := findRecipe(catalog, run.Recipe); ok && rec.Session != "" {
				group = rec.Session
			}
			if seen[group] {
				continue
			}
			seen[group] = true
			if status := run.Status(annotations); status == "ready" || status == "failed" {
				n++
			}
		}
	}
	return n
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
