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
// namespace; reviews always run attributed and land as the member's
// pending review on GitHub. GitHub and factory sandboxes are the state,
// the CR is near-static config.
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
	// AnnotationExecutor records which member's click consented a review;
	// stamped when the mailbox entry is consumed so resume-after-restart
	// keeps the executor identity even when the sandbox lives in the board
	// namespace (personal boards).
	AnnotationExecutor = "board.gemini.google.com/executor"
	// AnnotationReviewState tracks GitHub-side review lifecycle: "pending"
	// (posted as the executor's pending review, finalize on GitHub) or
	// "submitted" (the API published a draft as a pending review).
	AnnotationReviewState = "reviewState"
	// AnnotationReviewAbandoned is stamped by the API when the member
	// deletes their pending review on GitHub; invocation results older
	// than this must not be re-recorded as pending.
	AnnotationReviewAbandoned = "review.gemini.google.com/abandoned-at"
	// AnnotationReviewError parks a failed review invocation: the message
	// renders on the board and its timestamp gates relaunch until the
	// member clicks Review again. Auto-retrying re-runs the whole agent,
	// and failures that need a human (org blocks the token, bad PAT)
	// never fix themselves.
	AnnotationReviewError   = "review.gemini.google.com/error"
	AnnotationReviewErrorAt = "review.gemini.google.com/error-at"
	// Plan-loop annotations live on the issue's FIX sandbox (`factory
	// plan` runs there so the approved plan sits next to the code the fix
	// will touch). The draft is board-only until the member approves;
	// approval publishes it via the fix PR's description.
	AnnotationPlanDraft      = "board.gemini.google.com/plan"
	AnnotationPlannedAt      = "board.gemini.google.com/planned-at"
	AnnotationPlanFeedback   = "board.gemini.google.com/plan-feedback"
	AnnotationPlanFeedbackAt = "board.gemini.google.com/plan-feedback-at"
	AnnotationPlanApproved   = "board.gemini.google.com/plan-approved-at"
	AnnotationPlanRejected   = "board.gemini.google.com/plan-rejected-at"
	// PR follow-up verbs (Iterate / Address comments / Fix CI): the API
	// stamps the request on the fix sandbox — durable consent the
	// controller drives from. No mailbox claim to strand (the #1529
	// lesson): rerunRequested keeps a request standing until a completion
	// newer than it lands.
	AnnotationIterateRequested     = "board.gemini.google.com/iterate-requested-at"
	AnnotationIterateInstruction   = "board.gemini.google.com/iterate-instruction"
	AnnotationAddressRequested     = "board.gemini.google.com/address-requested-at"
	AnnotationInvestigateRequested = "board.gemini.google.com/investigate-requested-at"
	// AnnotationAutoIterate overrides the board's autoIterate policy for
	// one PR's fix sandbox: "on" | "off"; absent = inherit. Stored as an
	// open string so future per-PR auto modes extend it without
	// re-plumbing.
	AnnotationAutoIterate = "board.gemini.google.com/auto-iterate"
	// AnnotationEngine records which agent engine launched into this
	// sandbox — sessions are engine-private, so the chat terminal must
	// resume with the same CLI that ran the task.
	AnnotationEngine            = "board.gemini.google.com/engine"
	reviewStatePending          = "pending"
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
// The factory CLI runs under this ServiceAccount: it creates each sandbox's
// -lb Service, waits on the pod, and execs the task inside it.
//+kubebuilder:rbac:groups="",resources=services,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch
//+kubebuilder:rbac:groups="",resources=pods/exec,verbs=create
//+kubebuilder:rbac:groups="",resources=pods/log,verbs=get

// fixPlan is one consented fix to ensure: the executor is the member whose
// identity and namespace run the task.
type fixPlan struct {
	issue    int
	issueURL string
	executor string
	// auto marks a launch consented via the member's standing auto-fix
	// opt-in rather than a direct act; safety rails apply (forced draft PR).
	auto bool
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

// reviewPlan is one review to ensure. Reviews are always attributed: the
// executor is the consenting member (click, personal-board intake, or
// review-request + standing opt-in), the run happens in their namespace
// under their identity with --publish draft, and the pending review lands
// on GitHub visible only to them. Plans without an executor never run.
type reviewPlan struct {
	pr       int
	executor string
	// auto marks standing-automation plans: they defer to any review the
	// executor already has on the PR (pending OR submitted — GitHub is
	// the record, so this survives lost breadcrumbs), while a member's
	// click may deliberately review again.
	auto bool
}

// planRequest is one plan (or plan refinement) to ensure: PLAN -> human
// REFINE -> UPDATE_PLAN rounds run in the requesting member's fix sandbox,
// write nothing to GitHub, and end at APPROVE (fix launches --with-plan)
// or REJECT (draft cleared).
type planRequest struct {
	issue  int
	member string
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
	// recency-bounded, verb scopes named by their values) plus the mailbox
	// (clicks). Nothing else launches anything; what the member VIEWS is
	// client-side state the controller never reads.
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
	mailFixes, mailReviews, mailTriages, mailPlans, mailPRTasks := r.mailboxPlans(work)
	fixes = append(fixes, mailFixes...)
	reviews = append(reviews, mailReviews...)
	// A clicked triage needs only number+URL; no GitHub fetch required.
	// Clicks override the rejected-draft tombstone; auto candidates don't.
	clickedTriage := map[int]bool{}
	for _, n := range mailTriages {
		clickedTriage[n] = true
	}
	seenTriage := map[int]bool{}
	for _, issue := range triageCandidates {
		seenTriage[issue.GetNumber()] = true
	}
	for _, n := range mailTriages {
		if seenTriage[n] {
			continue
		}
		num := n
		url := fmt.Sprintf("https://github.com/%s/%s/issues/%d", owner, repo, n)
		triageCandidates = append(triageCandidates, &github.Issue{Number: &num, HTMLURL: &url})
	}

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
	for _, req := range mailPlans {
		namespaces[req.member] = true
	}
	for _, plan := range reviews {
		if plan.executor != "" {
			namespaces[plan.executor] = true
		}
	}
	if err := r.loadSandboxes(ctx, work, namespaces); err != nil {
		return ctrl.Result{}, err
	}

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
	for _, issue := range triageCandidates {
		r.ensureTriage(ctx, work, issue, clickedTriage[issue.GetNumber()])
	}
	for _, req := range mailPlans {
		r.ensurePlan(ctx, work, req)
	}

	// Resume in-flight reviews: harvest finished results and reattach after
	// controller restarts, independent of how the review was triggered.
	r.resumeReviews(ctx, work)
	r.resumeTriages(ctx, work)
	r.resumePlans(ctx, work)
	r.settleSubmittedReviews(ctx, work)

	if err := r.trimMailbox(ctx, work); err != nil {
		logger.Error(err, "mailbox cleanup failed")
	}

	// Explicit PR follow-up clicks run regardless of the auto policy —
	// a click IS the consent. Mailbox claims (hand-made PRs without a
	// sandbox) convert or launch first, then annotation-driven clicks.
	converted := r.ensurePRTaskClaims(ctx, work, mailPRTasks)
	r.ensurePRTaskClicks(ctx, work, converted)

	// Follow up factory-created PRs (investigate failures, address
	// comments): board policy is the default, each PR's fix sandbox may
	// override it either way.
	r.followUpPRs(ctx, work, board.Spec.Policy.AutoIterate == nil || *board.Spec.Policy.AutoIterate)

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
			reviews = append(reviews, reviewPlan{pr: pr.GetNumber(), executor: work.board.Namespace, auto: true})
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
					reviews = append(reviews, reviewPlan{pr: pr.GetNumber(), executor: owner, auto: true})
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
			fixes = append(fixes, fixPlan{issue: item.GetNumber(), issueURL: item.GetHTMLURL(), executor: member, auto: true})
		}
		if resp.NextPage == 0 {
			return fixes, nil
		}
		opts.Page = resp.NextPage
	}
}

// mailboxPlans turns pending UI requests into plans; consent is the click,
// recorded as the requesting member.
func (r *Reconciler) mailboxPlans(work *workState) ([]fixPlan, []reviewPlan, []int, []planRequest, []prTaskClaim) {
	raw := work.board.GetAnnotations()[AnnotationRequests]
	if raw == "" {
		return nil, nil, nil, nil, nil
	}
	requests := map[string]string{}
	if err := json.Unmarshal([]byte(raw), &requests); err != nil {
		return nil, nil, nil, nil, nil
	}
	var fixes []fixPlan
	var reviews []reviewPlan
	var triages []int
	var plans []planRequest
	var prTasks []prTaskClaim
	for key, member := range requests {
		switch {
		case strings.HasPrefix(key, "fix-"):
			if n, err := strconv.Atoi(strings.TrimPrefix(key, "fix-")); err == nil {
				fixes = append(fixes, fixPlan{issue: n, executor: member})
			}
		case strings.HasPrefix(key, "review-"):
			if n, err := strconv.Atoi(strings.TrimPrefix(key, "review-")); err == nil {
				reviews = append(reviews, reviewPlan{pr: n, executor: member})
			}
		case strings.HasPrefix(key, "triage-"):
			if n, err := strconv.Atoi(strings.TrimPrefix(key, "triage-")); err == nil {
				triages = append(triages, n)
			}
		case strings.HasPrefix(key, "plan-"):
			if n, err := strconv.Atoi(strings.TrimPrefix(key, "plan-")); err == nil {
				plans = append(plans, planRequest{issue: n, member: member})
			}
		case strings.HasPrefix(key, "iterate-"), strings.HasPrefix(key, "address-"), strings.HasPrefix(key, "investigate-"):
			kind, numStr, _ := strings.Cut(key, "-")
			if n, err := strconv.Atoi(numStr); err == nil {
				prTasks = append(prTasks, prTaskClaim{pr: n, member: member, kind: kind})
			}
		}
	}
	return fixes, reviews, triages, plans, prTasks
}

// prTaskClaim is a follow-up verb clicked on a PR with no sandbox yet
// (hand-made PRs): the mailbox bridges until factory creates the
// factory-pr sandbox, then the claim converts to the durable sandbox
// annotation the normal click pass drives.
type prTaskClaim struct {
	pr     int
	member string
	kind   string
}

// dedupeReviews keeps one plan per PR, preferring a member's click over a
// standing-automation plan (the click may deliberately re-review where
// automation defers to an existing review).
func dedupeReviews(in []reviewPlan) []reviewPlan {
	byPR := map[int]reviewPlan{}
	var order []int
	for _, plan := range in {
		existing, seen := byPR[plan.pr]
		if !seen {
			order = append(order, plan.pr)
			byPR[plan.pr] = plan
			continue
		}
		if existing.auto && !plan.auto {
			byPR[plan.pr] = plan
		}
	}
	out := make([]reviewPlan, 0, len(order))
	for _, pr := range order {
		out = append(out, byPR[pr])
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

// resumeFixes re-drives approved plans whose fix never started. The
// mailbox claim is consumed at kickoff, so a controller restart between
// consumption and the task landing in the sandbox — or a launch that
// bailed — would otherwise strand the row at "starting" forever. The
// approval annotation is the durable consent, and the runner's
// preflight/single-flight make relaunching idempotent (duplicates from
// the mailbox in the same pass are skipped by IsRunning).
func (r *Reconciler) resumeFixes(work *workState) []fixPlan {
	var out []fixPlan
	prefix := "fix-" + work.repo + "-"
	for _, sb := range work.sandboxes {
		n, err := strconv.Atoi(strings.TrimPrefix(sb.GetName(), prefix))
		if err != nil || !strings.HasPrefix(sb.GetName(), prefix) {
			continue
		}
		annotations := sb.GetAnnotations()
		if annotations[AnnotationPlanApproved] == "" || annotations[AnnotationPlanDraft] == "" {
			continue
		}
		// Once a fix has run (or is stamped running), the normal
		// paths own the sandbox — resume only pre-fix strandings.
		if strings.HasPrefix(annotations[factorycli.AnnotationTaskType], "fix") {
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

// ensureFix launches (or reattaches) a factory fix as the plan's executor,
// in the executor's namespace with the executor's identity.
func (r *Reconciler) ensureFix(ctx context.Context, work *workState, plan fixPlan) {
	logger := log.FromContext(ctx)
	name := work.fixSandboxName(plan.issue)
	sb := work.findSandbox(plan.executor, name)
	// PR/issue numbers repeat across repos, so runner keys carry the repo:
	// two boards in one namespace must never share a single-flight slot.
	key := fmt.Sprintf("%s/fix-%s-%d", plan.executor, work.repo, plan.issue)

	if r.Factory.IsRunning(key) {
		return
	}
	state, taskType := "", ""
	if sb != nil {
		annotations := sb.GetAnnotations()
		state = annotations[factorycli.AnnotationTaskState]
		taskType = annotations[factorycli.AnnotationTaskType]
	}
	// Terminal means THE FIX ran to an end state. The task-state stamps
	// are per-sandbox, not per-type: a completed plan in the same sandbox
	// (plans run in the fix sandbox by design) must not masquerade as a
	// finished fix — that bailed every Approve & Fix after a plan. An
	// absent type keeps the old semantics (pre-type-stamping sandboxes
	// only ever carried fix results).
	fixLike := taskType == "" || strings.HasPrefix(taskType, "fix")
	terminal := (state == factorycli.TaskStateCompleted || state == factorycli.TaskStateFailed) && fixLike
	if terminal && refixRequested(sb) {
		terminal = false
	}
	if terminal {
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
	if issueURL == "" {
		issueURL = fmt.Sprintf("https://github.com/%s/%s/issues/%d", work.owner, work.repo, plan.issue)
	}
	instruction := ""
	if plan.auto || work.board.Spec.Policy.DraftPR == nil || *work.board.Spec.Policy.DraftPR {
		// Auto-started fixes always open draft PRs, regardless of policy —
		// the human promotes.
		instruction = draftPRInstruction
	}
	if work.board.Spec.Policy.Disclose {
		instruction = strings.TrimSpace(instruction + " " + discloseInstructionTemplate)
	}

	// An approved plan on the sandbox rides along: the fix follows it and
	// publishes it as the PR description's Plan section. Only approval
	// consents this — a plain Fix click on a merely drafted (or rejected)
	// plan ignores it.
	withPlan := false
	if sb != nil {
		annotations := sb.GetAnnotations()
		withPlan = annotations[AnnotationPlanDraft] != "" && annotations[AnnotationPlanApproved] != ""
	}
	r.stampUnpaused(ctx, sb)
	r.stampEngine(ctx, sb, boardEngine(work.board))
	if r.Factory.StartFix(key, factorycli.FixOptions{
		Namespace:         plan.executor,
		SandboxName:       name,
		IssueURL:          issueURL,
		Instruction:       instruction,
		Image:             work.board.Spec.Sandbox.Image,
		WorkspaceDiskSize: work.board.Spec.Sandbox.DiskSize,
		GithubToken:       token,
		WithPlan:          withPlan,
		Engine:            boardEngine(work.board),
	}) {
		logger.Info("launched factory fix", "issue", plan.issue, "executor", plan.executor, "board", work.board.Name)
	}
}

// ensureReview launches (or harvests) a draft review. Drafts are
// unattributed prep: they run in the board namespace under the discovery
// identity and write nothing to GitHub.
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
	token, err := r.executorToken(ctx, plan.executor)
	if err != nil {
		logger.Error(err, "review executor has no token", "executor", plan.executor, "pr", plan.pr)
		return
	}
	if err := r.ensureFactoryUserSecret(ctx, plan.executor, plan.executor, ""); err != nil {
		logger.Error(err, "unable to sync factory-user secret", "namespace", plan.executor)
		return
	}
	key := fmt.Sprintf("%s/review-%s-%d", plan.executor, work.repo, plan.pr)

	sb := work.findPRSandbox(plan.pr)
	annotations := map[string]string{}
	if sb != nil && sb.GetAnnotations() != nil {
		annotations = sb.GetAnnotations()
	}
	// Abandoned, errored and legacy draft-bearing sandboxes are terminal:
	// relaunch only on a fresh re-review marker (a new click stamps one).
	done := annotations[AnnotationAgentDraft] != "" || annotations[AnnotationReviewState] != "" ||
		annotations[AnnotationReviewAbandoned] != "" || annotations[AnnotationReviewError] != ""
	if done && !reviewRerunRequested(sb) {
		return
	}
	if r.Factory.IsRunning(key) {
		return
	}

	if res, ok := r.Factory.LastResult(key); ok && !resultSuperseded(sb, res) {
		if res.Err == nil && sb != nil && factorycli.DraftWasPosted(res.Output) {
			// The pending review is already on GitHub; record that so the
			// board points the member there.
			if err := r.markReviewPending(ctx, sb, work.board.Name); err != nil {
				logger.Error(err, "unable to mark review pending", "pr", plan.pr)
			}
			return
		}
		if res.Err != nil && sb != nil {
			// A failed run parks the review until the member clicks again
			// — no auto-retry.
			if err := r.markReviewError(ctx, sb, work.board.Name, reviewErrorLine(res)); err != nil {
				logger.Error(err, "unable to record review error", "pr", plan.pr)
			}
			return
		}
		// Pre-sandbox failure, or a success with nothing recognizable in
		// its output: back off rather than hot-looping the agent.
		if time.Since(res.FinishedAt) < launchRetryBackoff {
			return
		}
	}

	if sb == nil && r.activeCount(work) >= maxActive(work.board) {
		logger.Info("review deferred: board at maxActive", "pr", plan.pr, "limit", maxActive(work.board))
		return
	}
	// GitHub is the only storage for pending reviews and allows one per
	// author per PR: a fresh or re-armed launch must not race a review
	// already parked there (the post step would 422 and burn a full run).
	if sb == nil || reviewRerunRequested(sb) {
		gh := newGithubClientFromToken(ctx, token)
		pending, reviewed, err := executorReviewStates(ctx, gh, work.owner, work.repo, plan.pr, plan.executor)
		if err != nil {
			logger.Error(err, "unable to check for existing reviews; deferring launch", "pr", plan.pr)
			return
		}
		if pending {
			// GitHub allows one pending review per author: launching would
			// 422 at the post step after burning a full run.
			logger.Info("pending review already parked on GitHub; not launching", "pr", plan.pr, "executor", plan.executor)
			return
		}
		if reviewed && plan.auto {
			// GitHub already carries the executor's submitted review: done
			// is done for automation (this survives lost sandbox
			// breadcrumbs). Only a member's explicit click reviews again.
			logger.Info("review already submitted on GitHub; automation defers", "pr", plan.pr, "executor", plan.executor)
			return
		}
	}
	r.stampUnpaused(ctx, sb)
	r.stampEngine(ctx, sb, boardEngine(work.board))
	reviewSandbox := ""
	if sb != nil {
		reviewSandbox = sb.GetName()
	}
	if r.Factory.StartReview(key, factorycli.ReviewOptions{
		Namespace:         plan.executor,
		SandboxName:       reviewSandbox,
		PRURL:             fmt.Sprintf("https://github.com/%s/%s/pull/%d", work.owner, work.repo, plan.pr),
		Image:             work.board.Spec.Sandbox.Image,
		WorkspaceDiskSize: work.board.Spec.Sandbox.DiskSize,
		GithubToken:       token,
		Publish:           "draft",
		Engine:            boardEngine(work.board),
	}) {
		logger.Info("launched factory review", "pr", plan.pr, "board", work.board.Name, "executor", plan.executor)
	}
}

// resumeReviews revisits board-namespace sandboxes that carry (or should
// carry) a review: harvesting a finished invocation's draft or relaunching
// an interrupted review. A fix sandbox aliased to a PR never gets an
// unrequested review launched on it.
func (r *Reconciler) resumeReviews(ctx context.Context, work *workState) {
	for _, sb := range work.sandboxes {
		prStr := sb.GetLabels()[factorycli.LabelPR]
		if prStr == "" {
			continue
		}
		annotations := sb.GetAnnotations()
		reviewish := annotations[factorycli.AnnotationTaskType] == "review" ||
			strings.HasPrefix(sb.GetName(), "factory-pr-")
		rerun := reviewRerunRequested(sb)
		if !reviewish && !rerun {
			continue
		}
		done := annotations[AnnotationAgentDraft] != "" || annotations[AnnotationReviewState] != "" ||
			annotations[AnnotationReviewAbandoned] != "" || annotations[AnnotationReviewError] != ""
		if done && !rerun {
			continue
		}
		pr, err := strconv.Atoi(prStr)
		if err != nil {
			continue
		}
		// The stamped executor (a member's click) survives restarts; a
		// sandbox outside the board namespace belongs to the executor
		// whose namespace hosts it, and on personal boards the member is
		// the only possible executor.
		r.ensureReview(ctx, work, reviewPlan{pr: pr, executor: work.board.Namespace})
	}
}

// settleSubmittedReviews flips reviewState pending→submitted once the
// member has finalized their pending review on GitHub, so the row stops
// demanding attention. Pending reviews are only visible to their author,
// hence the check runs under the executor's own token.
func (r *Reconciler) settleSubmittedReviews(ctx context.Context, work *workState) {
	logger := log.FromContext(ctx)
	for _, sb := range work.sandboxes {
		annotations := sb.GetAnnotations()
		if annotations[AnnotationReviewState] != reviewStatePending {
			continue
		}
		pr, err := strconv.Atoi(sb.GetLabels()[factorycli.LabelPR])
		if err != nil {
			continue
		}
		executor := work.board.Namespace
		if executor == "" {
			continue
		}
		token, err := r.executorToken(ctx, executor)
		if err != nil {
			continue
		}
		gh := newGithubClientFromToken(ctx, token)
		reviews, _, err := gh.PullRequests.ListReviews(ctx, work.owner, work.repo, pr, &github.ListOptions{PerPage: 100})
		if err != nil {
			logger.Info("settle check failed", "pr", pr, "err", err)
			continue
		}
		stillPending := false
		submitted := false
		for _, review := range reviews {
			if !strings.EqualFold(review.GetUser().GetLogin(), executor) {
				continue
			}
			switch strings.ToUpper(review.GetState()) {
			case "PENDING":
				stillPending = true
			case "APPROVED", "CHANGES_REQUESTED", "COMMENTED":
				submitted = true
			}
		}
		if stillPending || !submitted {
			continue
		}
		annotations[AnnotationReviewState] = "submitted"
		sb.SetAnnotations(annotations)
		if err := r.Update(ctx, sb); err != nil {
			logger.Error(err, "unable to settle submitted review", "pr", pr)
		}
	}
}

// markReviewPending records that the review was posted as a pending review
// on GitHub under the executor's identity — the member finalizes it there.
func (r *Reconciler) markReviewPending(ctx context.Context, sb *unstructured.Unstructured, boardName string) error {
	annotations := sb.GetAnnotations()
	if annotations == nil {
		annotations = map[string]string{}
	}
	annotations[AnnotationReviewState] = reviewStatePending
	annotations[AnnotationReviewedAt] = time.Now().UTC().Format(time.RFC3339)
	annotations[AnnotationBoard] = boardName
	delete(annotations, AnnotationReviewError)
	delete(annotations, AnnotationReviewErrorAt)
	sb.SetAnnotations(annotations)
	return r.Update(ctx, sb)
}

// markReviewError parks a failed review invocation on the sandbox: the
// message renders on the board, and its timestamp is the baseline a
// re-review click must beat before the controller launches again.
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
			if sb := work.findPRSandbox(n); sb != nil {
				// Persist the consenting executor on the sandbox before
				// dropping the mailbox entry: it is the only durable record
				// that this review was a member's click (draft-publish).
				if sb.GetAnnotations()[AnnotationExecutor] != member {
					annotations := sb.GetAnnotations()
					if annotations == nil {
						annotations = map[string]string{}
					}
					annotations[AnnotationExecutor] = member
					sb.SetAnnotations(annotations)
					if err := r.Update(ctx, sb); err != nil {
						return fmt.Errorf("stamping executor on %s: %w", sb.GetName(), err)
					}
				}
				continue
			}
		case strings.HasPrefix(key, "triage-"):
			n, err := strconv.Atoi(strings.TrimPrefix(key, "triage-"))
			if err != nil {
				continue
			}
			// The click stands until a draft is stored: the sandbox may be
			// a rejected leftover whose tombstone the click overrides.
			if sb := work.findSandbox(work.board.Namespace, factorycli.TriageSandboxName(work.repo, n)); sb != nil && sb.GetAnnotations()[AnnotationTriagedAt] != "" {
				continue
			}
		case strings.HasPrefix(key, "iterate-"), strings.HasPrefix(key, "address-"), strings.HasPrefix(key, "investigate-"):
			kind, numStr, _ := strings.Cut(key, "-")
			n, err := strconv.Atoi(numStr)
			if err != nil {
				continue
			}
			reqKey := map[string]string{
				"iterate":     AnnotationIterateRequested,
				"address":     AnnotationAddressRequested,
				"investigate": AnnotationInvestigateRequested,
			}[kind]
			// Consumed once the claim converted to the sandbox annotation.
			if sb := work.findPRSandbox(n); sb != nil && sb.GetAnnotations()[reqKey] != "" {
				continue
			}
		case strings.HasPrefix(key, "plan-"):
			n, err := strconv.Atoi(strings.TrimPrefix(key, "plan-"))
			if err != nil {
				continue
			}
			// The plan request stands until a draft is stored: the fix
			// sandbox may predate the click, so existence alone proves
			// nothing.
			if sb := work.findSandbox(member, work.fixSandboxName(n)); sb != nil && sb.GetAnnotations()[AnnotationPlannedAt] != "" {
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
// ensurePRTaskClaims handles follow-up clicks on PRs with no sandbox:
// once any sandbox carries the PR label (factory created or aliased it),
// the claim converts to the durable request annotation the click pass
// drives — and the trim rule drops the claim. Until then the launch runs
// factory directly: `pr <verb>` ensures the factory-pr sandbox itself
// (gh pr checkout attaches the branch), so hand-made PRs work with the
// same machinery as agent PRs.
func (r *Reconciler) ensurePRTaskClaims(ctx context.Context, work *workState, claims []prTaskClaim) map[string]bool {
	logger := log.FromContext(ctx)
	converted := map[string]bool{}
	reqKeys := map[string]string{
		"iterate":     AnnotationIterateRequested,
		"address":     AnnotationAddressRequested,
		"investigate": AnnotationInvestigateRequested,
	}
	for _, claim := range claims {
		reqKey := reqKeys[claim.kind]
		if reqKey == "" {
			continue
		}
		boardInstrKey := fmt.Sprintf("board.gemini.google.com/iterate-instruction-%d", claim.pr)
		if sb := work.findPRSandbox(claim.pr); sb != nil {
			// Convert: the annotation is the durable consent from here on.
			annotations := sb.GetAnnotations()
			if annotations == nil {
				annotations = map[string]string{}
			}
			if annotations[reqKey] != "" {
				continue // already converted; trim drops the claim
			}
			annotations[reqKey] = time.Now().UTC().Format(time.RFC3339)
			annotations[AnnotationExecutor] = claim.member
			if claim.kind == "iterate" {
				if instr := work.board.GetAnnotations()[boardInstrKey]; instr != "" {
					annotations[AnnotationIterateInstruction] = instr
				}
			}
			sb.SetAnnotations(annotations)
			if err := r.Update(ctx, sb); err != nil {
				logger.Error(err, "unable to convert pr-task claim", "pr", claim.pr, "kind", claim.kind)
			} else {
				// The click pass picks it up NEXT reconcile, when the
				// preflight can see the sandbox's true task state.
				converted[sb.GetName()] = true
			}
			continue
		}
		// No sandbox anywhere: launch factory directly; it ensures the
		// factory-pr sandbox and checks the PR branch out.
		key := fmt.Sprintf("%s/%s-%d", claim.member, claim.kind, claim.pr)
		if r.Factory.IsRunning(key) {
			continue
		}
		if res, ok := r.Factory.LastResult(key); ok && res.Err != nil && time.Since(res.FinishedAt) < launchRetryBackoff {
			continue
		}
		token, err := r.executorToken(ctx, claim.member)
		if err != nil {
			continue
		}
		instruction := ""
		if claim.kind == "iterate" {
			instruction = work.board.GetAnnotations()[boardInstrKey]
		}
		starters := map[string]func(string, factorycli.PRTaskOptions) bool{
			"iterate":     r.Factory.StartIterate,
			"address":     r.Factory.StartAddressComments,
			"investigate": r.Factory.StartInvestigate,
		}
		if starters[claim.kind](key, factorycli.PRTaskOptions{
			Namespace:   claim.member,
			SandboxName: factorycli.PRSandboxName(work.repo, claim.pr),
			PRURL:       fmt.Sprintf("https://github.com/%s/%s/pull/%d", work.owner, work.repo, claim.pr),
			Instruction: instruction,
			GithubToken: token,
			Engine:      boardEngine(work.board),
		}) {
			logger.Info("launched factory pr "+claim.kind+" (manual PR attach)", "pr", claim.pr, "board", work.board.Name)
		}
	}
	return converted
}

// ensurePRTaskClicks drives the explicit PR follow-up verbs. Requests
// live as sandbox annotations (durable consent, restart-proof); a
// request is served once any completion newer than it lands — the same
// global-completion semantics refix uses, which also means a click made
// while another task runs is considered absorbed by that run's finish
// (acceptable: the verbs are one click away). One launch per sandbox per
// pass, and any in-flight follow-up defers the others — the tasks share
// one workspace.
func (r *Reconciler) ensurePRTaskClicks(ctx context.Context, work *workState, skip map[string]bool) {
	logger := log.FromContext(ctx)
	kinds := []struct {
		reqKey, kind string
		start        func(string, factorycli.PRTaskOptions) bool
	}{
		{AnnotationIterateRequested, "iterate", r.Factory.StartIterate},
		{AnnotationAddressRequested, "address", r.Factory.StartAddressComments},
		{AnnotationInvestigateRequested, "investigate", r.Factory.StartInvestigate},
	}
	for _, sb := range work.sandboxes {
		if skip[sb.GetName()] {
			continue // converted this pass; next reconcile owns it
		}
		if !strings.HasPrefix(sb.GetName(), "fix-") && !strings.HasPrefix(sb.GetName(), "factory-pr-") {
			continue
		}
		annotations := sb.GetAnnotations()
		prNum := sb.GetLabels()[factorycli.LabelPR]
		prURL := annotations["htmlURL"]
		if prNum == "" || !strings.Contains(prURL, "/pull/") {
			continue
		}
		namespace := sb.GetNamespace()
		busy := false
		for _, k := range kinds {
			if r.Factory.IsRunning(fmt.Sprintf("%s/%s-%s", namespace, k.kind, prNum)) {
				busy = true
			}
		}
		// A review child may own a factory-pr sandbox.
		if r.Factory.IsRunning(fmt.Sprintf("%s/review-%s-%s", namespace, work.repo, prNum)) {
			busy = true
		}
		// A fix or plan child may still be provisioning this sandbox (no
		// task landed yet for the prober's sandbox-wide busy check to
		// see): their runner keys derive from the sandbox name.
		if r.Factory.IsRunning(namespace+"/"+sb.GetName()) ||
			r.Factory.IsRunning(namespace+"/plan-"+strings.TrimPrefix(sb.GetName(), "fix-")) {
			busy = true
		}
		if busy {
			continue
		}
		for _, k := range kinds {
			if !rerunRequested(sb, k.reqKey, factorycli.AnnotationCompletionTime) {
				continue
			}
			key := fmt.Sprintf("%s/%s-%s", namespace, k.kind, prNum)
			if res, ok := r.Factory.LastResult(key); ok && res.Err != nil && time.Since(res.FinishedAt) < launchRetryBackoff {
				continue
			}
			token, err := r.executorToken(ctx, namespace)
			if err != nil {
				continue
			}
			instruction := ""
			if k.kind == "iterate" {
				instruction = annotations[AnnotationIterateInstruction]
			}
			r.stampUnpaused(ctx, sb)
			r.stampEngine(ctx, sb, boardEngine(work.board))
			if k.start(key, factorycli.PRTaskOptions{
				Namespace:   namespace,
				SandboxName: sb.GetName(),
				PRURL:       prURL,
				Instruction: instruction,
				GithubToken: token,
				Engine:      boardEngine(work.board),
			}) {
				logger.Info("launched factory pr "+k.kind, "pr", prNum, "board", work.board.Name)
			}
			break // one launch per sandbox per pass — shared workspace
		}
	}
}

func (r *Reconciler) followUpPRs(ctx context.Context, work *workState, boardDefault bool) {
	logger := log.FromContext(ctx)
	for _, sb := range work.sandboxes {
		if !strings.HasPrefix(sb.GetName(), "fix-") {
			continue
		}
		if !autoIterateEnabled(sb, boardDefault) {
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
			Engine:      boardEngine(work.board),
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
		// A rerun marker newer than the last completion means a relaunch
		// is waking this sandbox (factory patches replicas directly and
		// stamps no unpaused-at) — pausing now would kill the container
		// mid-provision and fail the run.
		restarting := false
		for _, key := range []string{AnnotationRereviewRequested, AnnotationRefixRequested} {
			if t, err := time.Parse(time.RFC3339, annotations[key]); err == nil && t.After(idleSince) {
				restarting = true
			}
		}
		if restarting {
			continue
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

// executorReviewStates reports whether the executor has a pending review
// parked on the PR and whether they have any submitted one. Pending
// reviews are only visible to their author, so the check must run under
// the executor's own token.
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
			pending = true
		case "APPROVED", "CHANGES_REQUESTED", "COMMENTED":
			reviewed = true
		}
	}
	return pending, reviewed, nil
}

// resultSuperseded reports whether a remembered invocation result predates a
// member action (abandon or re-review request) and must not be re-recorded.
func resultSuperseded(sb *unstructured.Unstructured, res factorycli.Result) bool {
	if sb == nil {
		return false
	}
	annotations := sb.GetAnnotations()
	for _, key := range []string{AnnotationReviewAbandoned, AnnotationRereviewRequested} {
		if at, err := time.Parse(time.RFC3339, annotations[key]); err == nil && res.FinishedAt.Before(at) {
			return true
		}
	}
	return false
}

// stampUnpaused marks a paused sandbox we are about to relaunch into.
// pauseFinished treats unpaused-at as the new idle baseline, so the pause
// pass cannot kill the pod mid-boot — the #1423 race, generalized: rerun
// markers only cover review/fix wakes, while triage/plan clicks (and any
// resume) wake sandboxes markerlessly.
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

// autoIterateEnabled resolves a sandbox's effective auto-follow-up:
// the per-PR annotation overrides the board policy in either direction.
func autoIterateEnabled(sb *unstructured.Unstructured, boardDefault bool) bool {
	switch sb.GetAnnotations()[AnnotationAutoIterate] {
	case "on":
		return true
	case "off":
		return false
	}
	return boardDefault
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

// reviewRerunRequested reports whether a re-review click is newer than the
// last closed-out review attempt (posted or errored). Without the error
// baseline, one click would re-arm a permanently failing review forever.
func reviewRerunRequested(sb *unstructured.Unstructured) bool {
	if sb == nil {
		return false
	}
	annotations := sb.GetAnnotations()
	requestedAt, err := time.Parse(time.RFC3339, annotations[AnnotationRereviewRequested])
	if err != nil {
		return false
	}
	baseline := time.Time{}
	for _, key := range []string{AnnotationReviewedAt, AnnotationReviewErrorAt} {
		if t, err := time.Parse(time.RFC3339, annotations[key]); err == nil && t.After(baseline) {
			baseline = t
		}
	}
	return baseline.IsZero() || requestedAt.After(baseline)
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
