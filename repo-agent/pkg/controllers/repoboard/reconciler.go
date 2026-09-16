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
// executor-consent rule) and from the transient request mailbox; attributed
// execution runs through the factory CLI in the consenting member's own
// namespace; draft-only reviews run in the board namespace. GitHub and
// factory sandboxes are the state, the CR is near-static config.
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
	// memberGithubClient (token from the namespace's github-pat secret).
	NewGithubClient func(ctx context.Context, r *Reconciler, namespace string) (*github.Client, string, error)
}

//+kubebuilder:rbac:groups=board.gemini.google.com,resources=repoboards,verbs=get;list;watch;update;patch
//+kubebuilder:rbac:groups=board.gemini.google.com,resources=repoboards/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=agents.x-k8s.io,resources=sandboxes,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch;create;update;patch

// fixPlan is one consented fix to ensure: the executor is the member whose
// identity and namespace run the task.
type fixPlan struct {
	issue    int
	issueURL string
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

	// Build the work plan from GitHub (remote tier) and the mailbox
	// (manual tier), applying the executor-consent rule.
	var fixes []fixPlan
	var reviews []int
	if board.Spec.Triggers.Label != "" {
		f, rv, err := r.discoverLabeled(ctx, ghClient, work)
		if err != nil {
			logger.Error(err, "trigger-label discovery failed")
		}
		fixes, reviews = f, rv
	}
	mailFixes, mailReviews := r.mailboxPlans(work)
	fixes = append(fixes, mailFixes...)
	reviews = append(reviews, mailReviews...)

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
	if err := r.loadSandboxes(ctx, work, namespaces); err != nil {
		return ctrl.Result{}, err
	}

	for _, plan := range fixes {
		r.ensureFix(ctx, work, plan)
	}
	for _, pr := range dedupeInts(reviews) {
		r.ensureReview(ctx, work, pr)
	}

	// Resume in-flight reviews: harvest finished results and reattach after
	// controller restarts, independent of how the review was triggered.
	r.resumeReviews(ctx, work)

	if err := r.trimMailbox(ctx, work); err != nil {
		logger.Error(err, "mailbox cleanup failed")
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
	board     *boardv1alpha1.RepoBoard
	owner     string
	repo      string
	personal  bool   // board namespace is a member namespace
	discToken string // discovery-identity token (reads only)
	sandboxes []*unstructured.Unstructured
}

func (w *workState) fixSandboxName(issue int) string {
	return factorycli.FixSandboxName(w.repo, issue)
}

func (w *workState) findSandbox(namespace, name string) *unstructured.Unstructured {
	for _, sb := range w.sandboxes {
		if sb.GetNamespace() == namespace && sb.GetName() == name {
			return sb
		}
	}
	return nil
}

func (w *workState) findPRSandbox(pr int) *unstructured.Unstructured {
	prStr := strconv.Itoa(pr)
	for _, sb := range w.sandboxes {
		if sb.GetLabels()[factorycli.LabelPR] == prStr {
			return sb
		}
	}
	return nil
}

// discoveryClient resolves the identity used for GitHub reads and, for
// personal boards, syncs the member's factory-user secret.
func (r *Reconciler) discoveryClient(ctx context.Context, work *workState) (*github.Client, error) {
	newClient := r.NewGithubClient
	if newClient == nil {
		newClient = func(ctx context.Context, r *Reconciler, ns string) (*github.Client, string, error) {
			return r.memberGithubClient(ctx, ns)
		}
	}

	if ghClient, token, err := newClient(ctx, r, work.board.Namespace); err == nil {
		work.personal = true
		work.discToken = token
		user, _, err := ghClient.Users.Get(ctx, "")
		if err != nil {
			return nil, fmt.Errorf("github token invalid: %w", err)
		}
		if err := r.ensureFactoryUserSecret(ctx, work.board.Namespace, user.GetLogin(), user.GetEmail()); err != nil {
			log.FromContext(ctx).Error(err, "unable to sync factory-user secret", "namespace", work.board.Namespace)
		}
		return ghClient, nil
	}

	// Shared board: prep identity holds a factory-user-format secret in the
	// board namespace. Intake and review drafts never write to GitHub, so a
	// bot identity is acceptable here.
	secretName := work.board.Spec.PrepIdentity.SecretName
	if secretName == "" {
		return nil, fmt.Errorf("shared board requires spec.prepIdentity.secretName (no github-pat in namespace %s)", work.board.Namespace)
	}
	token, err := r.prepToken(ctx, work.board.Namespace, secretName)
	if err != nil {
		return nil, err
	}
	work.discToken = token
	return newGithubClientFromToken(ctx, token), nil
}

// newGithubClientFromToken is injectable for tests.
var newGithubClientFromToken = githubClientFromToken

// discoverLabeled scans open items carrying the trigger label and returns
// consented fix plans plus review candidates.
func (r *Reconciler) discoverLabeled(ctx context.Context, ghClient *github.Client, work *workState) ([]fixPlan, []int, error) {
	logger := log.FromContext(ctx)
	label := work.board.Spec.Triggers.Label

	var fixes []fixPlan
	var reviews []int

	opts := &github.IssueListByRepoOptions{
		State:       "open",
		Labels:      []string{label},
		ListOptions: github.ListOptions{PerPage: 100},
	}
	for {
		items, resp, err := ghClient.Issues.ListByRepo(ctx, work.owner, work.repo, opts)
		if err != nil {
			return fixes, reviews, err
		}
		for _, item := range items {
			if vetoed(item.Labels, work.board.Spec.Intake.Filters.ExcludeLabels) {
				continue
			}
			if item.IsPullRequest() {
				// Review drafts are unattributed prep — no consent needed.
				reviews = append(reviews, item.GetNumber())
				continue
			}
			executor := r.consentedAssignee(ctx, ghClient, work, item)
			if executor == "" {
				logger.V(4).Info("labeled item awaiting assignee consent", "issue", item.GetNumber())
				continue
			}
			fixes = append(fixes, fixPlan{issue: item.GetNumber(), issueURL: item.GetHTMLURL(), executor: executor})
		}
		if resp.NextPage == 0 {
			break
		}
		opts.Page = resp.NextPage
	}
	return fixes, reviews, nil
}

// consentedAssignee applies the executor-consent rule: an assignee may
// execute if they applied the trigger label themselves (verified against the
// issue's labeled events), or — personal-board fast path — the board lives
// in the assignee's own namespace. Standing consent (auto-fix opt-in)
// arrives with the intake phase.
func (r *Reconciler) consentedAssignee(ctx context.Context, ghClient *github.Client, work *workState, issue *github.Issue) string {
	var assignees []string
	for _, a := range issue.Assignees {
		assignees = append(assignees, a.GetLogin())
	}
	if len(assignees) == 0 {
		return ""
	}

	if work.personal {
		for _, login := range assignees {
			if strings.EqualFold(login, work.board.Namespace) {
				return login
			}
		}
		return ""
	}

	// Skip the events call when the work is already running or terminal:
	// consent was established at launch time.
	if sb := work.findAnySandboxNamed(work.fixSandboxName(issue.GetNumber())); sb != nil {
		for _, login := range assignees {
			if strings.EqualFold(login, sb.GetNamespace()) {
				return login
			}
		}
	}

	labeler := latestLabelerOf(ctx, ghClient, work, issue.GetNumber())
	for _, login := range assignees {
		if strings.EqualFold(login, labeler) {
			return login
		}
	}
	return ""
}

func (w *workState) findAnySandboxNamed(name string) *unstructured.Unstructured {
	for _, sb := range w.sandboxes {
		if sb.GetName() == name {
			return sb
		}
	}
	return nil
}

// latestLabelerOf returns the actor of the most recent trigger-label
// "labeled" event on the issue.
func latestLabelerOf(ctx context.Context, ghClient *github.Client, work *workState, issue int) string {
	label := work.board.Spec.Triggers.Label
	labeler := ""
	opts := &github.ListOptions{PerPage: 100}
	for {
		events, resp, err := ghClient.Issues.ListIssueEvents(ctx, work.owner, work.repo, issue, opts)
		if err != nil {
			return ""
		}
		for _, ev := range events {
			if ev.GetEvent() == "labeled" && strings.EqualFold(ev.GetLabel().GetName(), label) {
				labeler = ev.GetActor().GetLogin()
			}
		}
		if resp.NextPage == 0 {
			return labeler
		}
		opts.Page = resp.NextPage
	}
}

// mailboxPlans turns pending UI requests into plans; consent is the click,
// recorded as the requesting member.
func (r *Reconciler) mailboxPlans(work *workState) ([]fixPlan, []int) {
	raw := work.board.GetAnnotations()[AnnotationRequests]
	if raw == "" {
		return nil, nil
	}
	requests := map[string]string{}
	if err := json.Unmarshal([]byte(raw), &requests); err != nil {
		return nil, nil
	}
	var fixes []fixPlan
	var reviews []int
	for key, member := range requests {
		switch {
		case strings.HasPrefix(key, "fix-"):
			if n, err := strconv.Atoi(strings.TrimPrefix(key, "fix-")); err == nil {
				fixes = append(fixes, fixPlan{issue: n, executor: member})
			}
		case strings.HasPrefix(key, "review-"):
			if n, err := strconv.Atoi(strings.TrimPrefix(key, "review-")); err == nil {
				reviews = append(reviews, n)
			}
		}
	}
	return fixes, reviews
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
			annotations := sb.GetAnnotations()
			if annotations != nil {
				if annoRepo := annotations["repo"]; annoRepo != "" && annoRepo != work.repo {
					continue
				}
				if htmlURL := annotations["htmlURL"]; htmlURL != "" && !strings.Contains(htmlURL, repoHint) {
					continue
				}
			}
			work.sandboxes = append(work.sandboxes, sb)
		}
	}
	return nil
}

// ensureFix launches (or reattaches) a factory fix as the plan's executor,
// in the executor's namespace with the executor's identity.
func (r *Reconciler) ensureFix(ctx context.Context, work *workState, plan fixPlan) {
	logger := log.FromContext(ctx)
	name := work.fixSandboxName(plan.issue)
	sb := work.findSandbox(plan.executor, name)
	key := fmt.Sprintf("%s/fix-%d", plan.executor, plan.issue)

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
	if r.activeForExecutor(work, plan.executor) >= work.board.Spec.Limits.MaxActivePerUser && sb == nil {
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
	if issueURL == "" {
		issueURL = fmt.Sprintf("https://github.com/%s/%s/issues/%d", work.owner, work.repo, plan.issue)
	}
	instruction := ""
	if work.board.Spec.Policy.DraftPR == nil || *work.board.Spec.Policy.DraftPR {
		instruction = draftPRInstruction
	}
	if work.board.Spec.Policy.Disclose {
		instruction = strings.TrimSpace(instruction + " " + discloseInstructionTemplate)
	}

	if r.Factory.StartFix(key, factorycli.FixOptions{
		Namespace:         plan.executor,
		IssueURL:          issueURL,
		Instruction:       instruction,
		Image:             work.board.Spec.Sandbox.Image,
		WorkspaceDiskSize: work.board.Spec.Sandbox.DiskSize,
		GithubToken:       token,
	}) {
		logger.Info("launched factory fix", "issue", plan.issue, "executor", plan.executor, "board", work.board.Name)
	}
}

// ensureReview launches (or harvests) a draft review. Drafts are
// unattributed prep: they run in the board namespace under the discovery
// identity and write nothing to GitHub.
func (r *Reconciler) ensureReview(ctx context.Context, work *workState, pr int) {
	logger := log.FromContext(ctx)
	key := fmt.Sprintf("%s/review-pr-%d", work.board.Namespace, pr)

	sb := work.findPRSandbox(pr)
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
		Namespace:         work.board.Namespace,
		PRURL:             fmt.Sprintf("https://github.com/%s/%s/pull/%d", work.owner, work.repo, pr),
		Image:             work.board.Spec.Sandbox.Image,
		WorkspaceDiskSize: work.board.Spec.Sandbox.DiskSize,
		GithubToken:       work.discToken,
	}) {
		logger.Info("launched factory review", "pr", pr, "board", work.board.Name)
	}
}

// resumeReviews revisits board-namespace sandboxes that carry (or should
// carry) a review: harvesting a finished invocation's draft or relaunching
// an interrupted review. A fix sandbox aliased to a PR never gets an
// unrequested review launched on it.
func (r *Reconciler) resumeReviews(ctx context.Context, work *workState) {
	for _, sb := range work.sandboxes {
		if sb.GetNamespace() != work.board.Namespace {
			continue
		}
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

// trimMailbox clears request entries whose sandbox now exists.
func (r *Reconciler) trimMailbox(ctx context.Context, work *workState) error {
	raw := work.board.GetAnnotations()[AnnotationRequests]
	if raw == "" {
		return nil
	}
	requests := map[string]string{}
	if err := json.Unmarshal([]byte(raw), &requests); err != nil {
		return fmt.Errorf("invalid mailbox annotation: %w", err)
	}

	remaining := map[string]string{}
	for key, member := range requests {
		switch {
		case strings.HasPrefix(key, "fix-"):
			n, err := strconv.Atoi(strings.TrimPrefix(key, "fix-"))
			if err != nil {
				continue
			}
			if work.findSandbox(member, work.fixSandboxName(n)) != nil {
				continue
			}
		case strings.HasPrefix(key, "review-"):
			n, err := strconv.Atoi(strings.TrimPrefix(key, "review-"))
			if err != nil {
				continue
			}
			if work.findPRSandbox(n) != nil {
				continue
			}
		default:
			continue
		}
		remaining[key] = member
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
// to an open PR, in the sandbox owner's namespace with their identity.
func (r *Reconciler) followUpPRs(ctx context.Context, work *workState) {
	logger := log.FromContext(ctx)
	for _, sb := range work.sandboxes {
		if !strings.HasPrefix(sb.GetName(), "fix-") {
			continue
		}
		prNum := sb.GetLabels()[factorycli.LabelPR]
		prURL := sb.GetAnnotations()["htmlURL"]
		if prNum == "" || !strings.Contains(prURL, "/pull/") {
			continue
		}
		namespace := sb.GetNamespace()
		key := fmt.Sprintf("%s/prwatch-%s", namespace, prNum)
		if r.Factory.IsRunning(key) {
			continue
		}
		if res, ok := r.Factory.LastResult(key); ok && time.Since(res.FinishedAt) < prWatchRelaunchInterval {
			continue
		}
		token, err := r.executorToken(ctx, namespace)
		if err != nil {
			continue
		}
		if r.Factory.StartPRWatch(key, factorycli.PRWatchOptions{
			Namespace:   namespace,
			PRURL:       prURL,
			GithubToken: token,
		}) {
			logger.Info("launched factory pr watch", "pr", prNum, "namespace", namespace, "board", work.board.Name)
		}
	}
}

func (r *Reconciler) pauseFinished(ctx context.Context, work *workState, after time.Duration) {
	logger := log.FromContext(ctx)
	for _, sb := range work.sandboxes {
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
			logger.Error(err, "unable to pause sandbox", "sandbox", sb.GetName(), "namespace", sb.GetNamespace())
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

func (r *Reconciler) activeForExecutor(work *workState, namespace string) int {
	active := 0
	for _, sb := range work.sandboxes {
		if sb.GetNamespace() != namespace {
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

func dedupeInts(in []int) []int {
	seen := map[int]bool{}
	out := in[:0]
	for _, n := range in {
		if !seen[n] {
			seen[n] = true
			out = append(out, n)
		}
	}
	return out
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
		WithOptions(controller.Options{MaxConcurrentReconciles: concurrency}).
		Complete(r)
}
