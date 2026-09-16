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
// executor-consent rule) and from the transient request mailbox; execution
// runs through the factory CLI as the consenting member; GitHub and factory
// sandboxes are the state, the CR is near-static config.
//
// Phase 1 scope: personal boards — the executor namespace is the board's
// namespace. Shared boards (executor routed to the assignee's namespace)
// arrive with the shared-board phase.
package repoboard

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
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
	// AnnotationRequests is the transient kickoff mailbox written by the
	// API on a member's click and consumed (cleared) by the controller
	// once the corresponding sandbox exists. JSON map, e.g.
	// {"fix-123": "alice", "review-45": "alice"}.
	AnnotationRequests = "board.gemini.google.com/requests"

	// Draft/claim annotations on factory sandboxes; same wire contract the
	// PR-review flow established (legacy names kept so the API/UI read one
	// shape).
	AnnotationAgentDraft        = "agentDraft"
	AnnotationAgentState        = "agentState"
	AnnotationReviewedAt        = "review.gemini.google.com/reviewed-at"
	AnnotationRereviewRequested = "review.gemini.google.com/rereview-requested-at"
	AnnotationRefixRequested    = "review.gemini.google.com/refix-requested-at"
	AnnotationPreventAutoPause  = "sandbox.gemini.google.com/prevent-auto-shutdown"
	AnnotationUnpausedAt        = "sandbox.gemini.google.com/unpaused-at"
	AnnotationBoard             = "board.gemini.google.com/board"
	agentStateReviewReady       = "review ready"
	defaultRequeue              = time.Minute
	launchRetryBackoff          = 30 * time.Minute
	prWatchRelaunchInterval     = 10 * time.Minute
	draftPRInstruction          = "Open the pull request as a draft pull request."
	discloseInstructionTemplate = "Add a line to the pull request description stating that this change was prepared with AI agent assistance."
)

var sandboxGVK = schema.GroupVersionKind{Group: "agents.x-k8s.io", Version: "v1alpha1", Kind: "Sandbox"}

// Reconciler reconciles RepoBoard objects.
type Reconciler struct {
	client.Client
	Scheme  *runtime.Scheme
	Factory factorycli.Launcher

	// NewGithubClient is injectable for tests; defaults to
	// memberGithubClient.
	NewGithubClient func(ctx context.Context, r *Reconciler, namespace string) (*github.Client, string, error)
}

//+kubebuilder:rbac:groups=review.gemini.google.com,resources=repoboards,verbs=get;list;watch;update;patch
//+kubebuilder:rbac:groups=review.gemini.google.com,resources=repoboards/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=agents.x-k8s.io,resources=sandboxes,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch;create;update;patch

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

	// Phase 1: personal boards — member is the single allowed login (or the
	// namespace name, which equals the GitHub login by tenancy convention),
	// and execution happens in the board's own namespace.
	member := board.Namespace
	if board.Spec.Access.Mode == "list" && len(board.Spec.Access.Allow) > 0 {
		member = board.Spec.Access.Allow[0]
	}
	executorNS := board.Namespace

	newClient := r.NewGithubClient
	if newClient == nil {
		newClient = func(ctx context.Context, r *Reconciler, ns string) (*github.Client, string, error) {
			return r.memberGithubClient(ctx, ns)
		}
	}
	ghClient, githubToken, err := newClient(ctx, r, executorNS)
	if err != nil {
		r.setCondition(ctx, board, "Auth", metav1.ConditionFalse, "TokenMissing", err.Error())
		return ctrl.Result{RequeueAfter: defaultRequeue}, nil
	}

	user, _, err := ghClient.Users.Get(ctx, "")
	if err != nil {
		r.setCondition(ctx, board, "Auth", metav1.ConditionFalse, "TokenInvalid", err.Error())
		return ctrl.Result{RequeueAfter: defaultRequeue}, nil
	}
	if err := r.ensureFactoryUserSecret(ctx, executorNS, user); err != nil {
		logger.Error(err, "unable to sync factory-user secret", "namespace", executorNS)
	}
	r.setCondition(ctx, board, "Auth", metav1.ConditionTrue, "Authenticated", "GitHub authentication successful")

	sandboxes, err := r.listBoardSandboxes(ctx, executorNS, owner, repo)
	if err != nil {
		return ctrl.Result{}, err
	}

	work := &workState{
		board:      board,
		owner:      owner,
		repo:       repo,
		member:     member,
		executorNS: executorNS,
		token:      githubToken,
		sandboxes:  sandboxes,
	}

	// Remote tier: trigger-label discovery, gated by the executor-consent
	// rule (assignee must be the member on a personal board).
	if board.Spec.Triggers.Label != "" {
		if err := r.discoverLabeled(ctx, ghClient, work); err != nil {
			logger.Error(err, "trigger-label discovery failed")
		}
	}

	// Resume in-flight reviews: harvest finished results and reattach after
	// controller restarts, independent of how the review was triggered.
	r.resumeReviews(ctx, work)

	// Manual tier: consume the request mailbox (written by the API on the
	// member's own click — consent is the click).
	if err := r.consumeMailbox(ctx, work); err != nil {
		logger.Error(err, "mailbox consumption failed")
	}

	// Follow up factory-created PRs (investigate failures, address
	// comments) unless the board forbids unattended pushes.
	if board.Spec.Policy.AutoIterate == nil || *board.Spec.Policy.AutoIterate {
		r.followUpPRs(ctx, work)
	}

	// Pause finished sandboxes after the idle period.
	idle := time.Duration(board.Spec.Sandbox.IdleMinutes) * time.Minute
	if idle > 0 {
		r.pauseFinished(ctx, work, idle)
	}

	r.updateCounts(ctx, work)
	return ctrl.Result{RequeueAfter: defaultRequeue}, nil
}

type workState struct {
	board      *boardv1alpha1.RepoBoard
	owner      string
	repo       string
	member     string
	executorNS string
	token      string
	sandboxes  map[string]*unstructured.Unstructured
}

func (w *workState) fixSandboxName(issue int) string {
	return factorycli.FixSandboxName(w.repo, issue)
}

func (r *Reconciler) listBoardSandboxes(ctx context.Context, namespace, owner, repo string) (map[string]*unstructured.Unstructured, error) {
	list := &unstructured.UnstructuredList{}
	list.SetGroupVersionKind(sandboxGVK)
	if err := r.List(ctx, list, client.InNamespace(namespace), client.MatchingLabels{factorycli.LabelManaged: "true"}); err != nil {
		return nil, err
	}
	byName := make(map[string]*unstructured.Unstructured)
	repoHint := fmt.Sprintf("github.com/%s/%s/", owner, repo)
	for i := range list.Items {
		sb := &list.Items[i]
		annotations := sb.GetAnnotations()
		if annotations != nil {
			if annoRepo := annotations["repo"]; annoRepo != "" && annoRepo != repo {
				continue
			}
			if htmlURL := annotations["htmlURL"]; htmlURL != "" && !strings.Contains(htmlURL, repoHint) {
				continue
			}
		}
		byName[sb.GetName()] = sb
	}
	return byName, nil
}

// discoverLabeled scans open issues and PRs carrying the trigger label.
func (r *Reconciler) discoverLabeled(ctx context.Context, ghClient *github.Client, work *workState) error {
	logger := log.FromContext(ctx)
	label := work.board.Spec.Triggers.Label

	opts := &github.IssueListByRepoOptions{
		State:       "open",
		Labels:      []string{label},
		ListOptions: github.ListOptions{PerPage: 100},
	}
	for {
		items, resp, err := ghClient.Issues.ListByRepo(ctx, work.owner, work.repo, opts)
		if err != nil {
			return err
		}
		for _, item := range items {
			if vetoed(item.Labels, work.board.Spec.Intake.Filters.ExcludeLabels) {
				continue
			}
			if item.IsPullRequest() {
				r.ensureReview(ctx, work, item.GetNumber())
				continue
			}
			// Executor-consent rule: on a personal board the executor is
			// the member, so the item must be assigned to them. Unassigned
			// or foreign-assigned labeled items surface as awaiting-go via
			// the feed; they never execute here.
			if !isAssignee(item, work.member) {
				logger.V(4).Info("labeled item awaiting assignee consent", "issue", item.GetNumber())
				continue
			}
			r.ensureFix(ctx, work, item.GetNumber(), item.GetHTMLURL())
		}
		if resp.NextPage == 0 {
			break
		}
		opts.Page = resp.NextPage
	}
	return nil
}

func isAssignee(issue *github.Issue, login string) bool {
	for _, a := range issue.Assignees {
		if strings.EqualFold(a.GetLogin(), login) {
			return true
		}
	}
	return false
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

// ensureFix launches (or reattaches) a factory fix for the issue unless it
// already reached a terminal state without a re-fix request.
func (r *Reconciler) ensureFix(ctx context.Context, work *workState, issue int, issueURL string) {
	logger := log.FromContext(ctx)
	name := work.fixSandboxName(issue)
	sb := work.sandboxes[name]
	key := fmt.Sprintf("%s/fix-%d", work.executorNS, issue)

	if r.Factory.IsRunning(key) {
		return
	}
	state := ""
	if sb != nil {
		state = sb.GetAnnotations()[factorycli.AnnotationTaskState]
	}
	terminal := state == factorycli.TaskStateCompleted || state == factorycli.TaskStateFailed
	if terminal && refixRequested(sb) {
		terminal = false
	}
	if terminal {
		return
	}
	if res, ok := r.Factory.LastResult(key); ok && res.Err != nil && time.Since(res.FinishedAt) < launchRetryBackoff {
		return
	}
	if sb == nil && r.activeCount(work) >= work.board.Spec.Limits.MaxActive {
		return
	}
	if issueURL == "" {
		issueURL = fmt.Sprintf("https://github.com/%s/%s/issues/%d", work.owner, work.repo, issue)
	}

	instruction := ""
	if work.board.Spec.Policy.DraftPR == nil || *work.board.Spec.Policy.DraftPR {
		instruction = draftPRInstruction
	}
	if work.board.Spec.Policy.Disclose {
		instruction = strings.TrimSpace(instruction + " " + discloseInstructionTemplate)
	}

	if r.Factory.StartFix(key, factorycli.FixOptions{
		Namespace:         work.executorNS,
		IssueURL:          issueURL,
		Instruction:       instruction,
		Image:             work.board.Spec.Sandbox.Image,
		WorkspaceDiskSize: work.board.Spec.Sandbox.DiskSize,
		GithubToken:       work.token,
	}) {
		logger.Info("launched factory fix", "issue", issue, "board", work.board.Name)
	}
}

// ensureReview launches (or reattaches) a draft review for the PR and
// harvests the resulting draft onto the sandbox.
func (r *Reconciler) ensureReview(ctx context.Context, work *workState, pr int) {
	logger := log.FromContext(ctx)
	key := fmt.Sprintf("%s/review-pr-%d", work.executorNS, pr)

	sb := r.findPRSandbox(work, pr)
	draft := ""
	if sb != nil {
		draft = sb.GetAnnotations()[AnnotationAgentDraft]
	}
	if draft != "" && !rerunRequested(sb, AnnotationRereviewRequested, AnnotationReviewedAt) {
		return
	}
	if r.Factory.IsRunning(key) {
		return
	}

	// Harvest a finished invocation before considering a launch.
	if res, ok := r.Factory.LastResult(key); ok {
		if res.Err == nil {
			if yaml := factorycli.ExtractReviewYAML(res.Output); yaml != "" && sb != nil {
				if err := r.storeDraft(ctx, sb, work.board.Name, yaml); err != nil {
					logger.Error(err, "unable to store review draft", "pr", pr)
				}
				return
			}
		} else if time.Since(res.FinishedAt) < launchRetryBackoff {
			return
		}
	}

	if sb == nil && r.activeCount(work) >= work.board.Spec.Limits.MaxActive {
		return
	}
	if r.Factory.StartReview(key, factorycli.ReviewOptions{
		Namespace:         work.executorNS,
		PRURL:             fmt.Sprintf("https://github.com/%s/%s/pull/%d", work.owner, work.repo, pr),
		Image:             work.board.Spec.Sandbox.Image,
		WorkspaceDiskSize: work.board.Spec.Sandbox.DiskSize,
		GithubToken:       work.token,
	}) {
		logger.Info("launched factory review", "pr", pr, "board", work.board.Name)
	}
}

// resumeReviews revisits sandboxes that carry (or should carry) a review:
// harvesting a finished invocation's draft, or relaunching an interrupted
// review. Only sandboxes with review activity qualify — a fix sandbox
// aliased to a PR never gets an unrequested review launched on it.
func (r *Reconciler) resumeReviews(ctx context.Context, work *workState) {
	for _, sb := range work.sandboxes {
		prStr := sb.GetLabels()[factorycli.LabelPR]
		if prStr == "" {
			continue
		}
		annotations := sb.GetAnnotations()
		reviewish := annotations[factorycli.AnnotationTaskType] == "review" ||
			strings.HasPrefix(sb.GetName(), "factory-pr-")
		rerun := rerunRequested(sb, AnnotationRereviewRequested, AnnotationReviewedAt)
		if !reviewish && !rerun {
			continue
		}
		if annotations[AnnotationAgentDraft] != "" && !rerun {
			continue
		}
		pr, err := strconv.Atoi(prStr)
		if err != nil {
			continue
		}
		r.ensureReview(ctx, work, pr)
	}
}

func (r *Reconciler) findPRSandbox(work *workState, pr int) *unstructured.Unstructured {
	prStr := strconv.Itoa(pr)
	for _, sb := range work.sandboxes {
		if sb.GetLabels()[factorycli.LabelPR] == prStr {
			return sb
		}
	}
	return nil
}

// storeDraft stamps the draft and board decoration onto a factory sandbox.
func (r *Reconciler) storeDraft(ctx context.Context, sb *unstructured.Unstructured, boardName, draftYAML string) error {
	annotations := sb.GetAnnotations()
	if annotations == nil {
		annotations = map[string]string{}
	}
	annotations[AnnotationAgentDraft] = draftYAML
	annotations[AnnotationAgentState] = agentStateReviewReady
	annotations[AnnotationReviewedAt] = time.Now().UTC().Format(time.RFC3339)
	annotations[AnnotationBoard] = boardName
	sb.SetAnnotations(annotations)
	return r.Update(ctx, sb)
}

// consumeMailbox handles UI-initiated (discreet) kickoffs and clears entries
// once the corresponding sandbox exists.
func (r *Reconciler) consumeMailbox(ctx context.Context, work *workState) error {
	raw := work.board.GetAnnotations()[AnnotationRequests]
	if raw == "" {
		return nil
	}
	requests := map[string]string{}
	if err := json.Unmarshal([]byte(raw), &requests); err != nil {
		return fmt.Errorf("invalid mailbox annotation: %w", err)
	}

	remaining := map[string]string{}
	for req := range requests {
		switch {
		case strings.HasPrefix(req, "fix-"):
			issue, err := strconv.Atoi(strings.TrimPrefix(req, "fix-"))
			if err != nil {
				continue // drop malformed entries
			}
			if work.sandboxes[work.fixSandboxName(issue)] != nil {
				continue // picked up: sandbox is the durable record
			}
			r.ensureFix(ctx, work, issue, "")
			remaining[req] = requests[req]
		case strings.HasPrefix(req, "review-"):
			pr, err := strconv.Atoi(strings.TrimPrefix(req, "review-"))
			if err != nil {
				continue
			}
			if r.findPRSandbox(work, pr) != nil {
				continue
			}
			r.ensureReview(ctx, work, pr)
			remaining[req] = requests[req]
		}
	}

	if len(remaining) == len(requests) {
		return nil
	}
	annotations := work.board.GetAnnotations()
	if len(remaining) == 0 {
		delete(annotations, AnnotationRequests)
	} else {
		b, _ := json.Marshal(remaining)
		annotations[AnnotationRequests] = string(b)
	}
	work.board.SetAnnotations(annotations)
	return r.Update(ctx, work.board)
}

// followUpPRs keeps a factory pr watch running for every fix sandbox aliased
// to an open PR.
func (r *Reconciler) followUpPRs(ctx context.Context, work *workState) {
	logger := log.FromContext(ctx)
	for name, sb := range work.sandboxes {
		if !strings.HasPrefix(name, "fix-") {
			continue
		}
		prNum := sb.GetLabels()[factorycli.LabelPR]
		prURL := sb.GetAnnotations()["htmlURL"]
		if prNum == "" || !strings.Contains(prURL, "/pull/") {
			continue
		}
		key := fmt.Sprintf("%s/prwatch-%s", work.executorNS, prNum)
		if r.Factory.IsRunning(key) {
			continue
		}
		if res, ok := r.Factory.LastResult(key); ok && time.Since(res.FinishedAt) < prWatchRelaunchInterval {
			continue
		}
		if r.Factory.StartPRWatch(key, factorycli.PRWatchOptions{
			Namespace:   work.executorNS,
			PRURL:       prURL,
			GithubToken: work.token,
		}) {
			logger.Info("launched factory pr watch", "pr", prNum, "board", work.board.Name)
		}
	}
}

func (r *Reconciler) pauseFinished(ctx context.Context, work *workState, after time.Duration) {
	logger := log.FromContext(ctx)
	for name, sb := range work.sandboxes {
		annotations := sb.GetAnnotations()
		if annotations[AnnotationPreventAutoPause] == "true" {
			continue
		}
		state := annotations[factorycli.AnnotationTaskState]
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
			logger.Error(err, "unable to pause sandbox", "sandbox", name)
		}
	}
}

func (r *Reconciler) activeCount(work *workState) int {
	active := 0
	for _, sb := range work.sandboxes {
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
		if annotations[AnnotationAgentDraft] != "" && annotations["reviewState"] != "submitted" {
			needsHuman++
			continue
		}
		if annotations[factorycli.AnnotationTaskState] == factorycli.TaskStateCompleted &&
			strings.Contains(annotations["htmlURL"], "/pull/") &&
			strings.HasPrefix(sb.GetName(), "fix-") {
			needsHuman++
		}
	}
	work.board.Status.Counts = boardv1alpha1.BoardCounts{
		NeedsHuman: needsHuman,
		Active:     r.activeCount(work),
	}
	if err := r.Status().Update(ctx, work.board); err != nil {
		log.FromContext(ctx).Error(err, "unable to update board status")
	}
}

func rerunRequested(sb *unstructured.Unstructured, requestKey, completedKey string) bool {
	if sb == nil {
		return false
	}
	annotations := sb.GetAnnotations()
	requestedAt, err := time.Parse(time.RFC3339, annotations[requestKey])
	if err != nil {
		return false
	}
	completedAt, err := time.Parse(time.RFC3339, annotations[completedKey])
	if err != nil {
		return true
	}
	return requestedAt.After(completedAt)
}

func refixRequested(sb *unstructured.Unstructured) bool {
	return rerunRequested(sb, AnnotationRefixRequested, factorycli.AnnotationCompletionTime)
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
		WithOptions(controller.Options{MaxConcurrentReconciles: concurrency}).
		Complete(r)
}
