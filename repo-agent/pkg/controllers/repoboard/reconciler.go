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
	AnnotationReviewAbandoned   = "review.gemini.google.com/abandoned-at"
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
//+kubebuilder:rbac:groups="",resources=configmaps,verbs=get;list;watch
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

// maxActive/maxActivePerUser mirror the CRD defaults for specs that omit
// the limits block entirely (API-server defaulting only fires when the
// parent object exists, so a UI-created board carries no limits at all —
// zero must mean "default", not "block every launch").
func maxActive(board *boardv1alpha1.RepoBoard) int {
	if board.Spec.Limits.MaxActive <= 0 {
		return 5
	}
	return board.Spec.Limits.MaxActive
}

func maxActivePerUser(board *boardv1alpha1.RepoBoard) int {
	if board.Spec.Limits.MaxActivePerUser <= 0 {
		return 2
	}
	return board.Spec.Limits.MaxActivePerUser
}

// reviewPlan is one review to ensure. Reviews are always attributed: the
// executor is the consenting member (click, personal-board intake, or
// review-request + standing opt-in), the run happens in their namespace
// under their identity with --publish draft, and the pending review lands
// on GitHub visible only to them. Plans without an executor never run.
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

	// Build the work plan from GitHub (remote tier) and the mailbox
	// (manual tier), applying the executor-consent rule.
	var fixes []fixPlan
	var reviews []reviewPlan
	if board.Spec.Triggers.Label != "" {
		f, rv, err := r.discoverLabeled(ctx, ghClient, work)
		if err != nil {
			logger.Error(err, "trigger-label discovery failed")
		}
		fixes = f
		// Labeled PRs review as the personal-board member; on shared
		// boards a label alone names nobody, so nothing runs without a
		// click or a review request to an opted-in member.
		if work.personal {
			for _, pr := range rv {
				reviews = append(reviews, reviewPlan{pr: pr, executor: board.Namespace})
			}
		}
	}
	if board.Spec.Intake.DraftReviews {
		rv, err := r.discoverIntakePRs(ctx, ghClient, work)
		if err != nil {
			logger.Error(err, "review intake discovery failed")
		}
		reviews = append(reviews, rv...)
	}
	var triageCandidates []*github.Issue
	if board.Spec.Intake.TriageIssues {
		tc, err := r.discoverTriage(ctx, ghClient, work)
		if err != nil {
			logger.Error(err, "triage discovery failed")
		}
		triageCandidates = tc
	}
	if autoFixWithoutLabel(board) && work.personal {
		// Aggressive personal variant: every issue assigned to the member
		// is a candidate, gated by their own standing opt-in.
		f, err := r.discoverAssigned(ctx, ghClient, work)
		if err != nil {
			logger.Error(err, "assigned-issue discovery failed")
		}
		fixes = append(fixes, f...)
	}
	mailFixes, mailReviews, mailTriages := r.mailboxPlans(work)
	fixes = append(fixes, mailFixes...)
	reviews = append(reviews, mailReviews...)
	// A clicked triage needs only number+URL; no GitHub fetch required.
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
	for _, plan := range reviews {
		if plan.executor != "" {
			namespaces[plan.executor] = true
		}
	}
	if err := r.loadSandboxes(ctx, work, namespaces); err != nil {
		return ctrl.Result{}, err
	}

	for _, plan := range fixes {
		r.ensureFix(ctx, work, plan)
	}
	for _, plan := range dedupeReviews(reviews) {
		r.ensureReview(ctx, work, plan)
	}
	for _, issue := range triageCandidates {
		r.ensureTriage(ctx, work, issue)
	}

	// Resume in-flight reviews: harvest finished results and reattach after
	// controller restarts, independent of how the review was triggered.
	r.resumeReviews(ctx, work)
	r.settleSubmittedReviews(ctx, work)

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
			auto := false
			if executor == "" {
				// Standing consent (two-key auto-fix) can substitute for a
				// direct act when the require gate holds; the label
				// requirement is satisfied here by construction.
				executor = r.autoConsentedAssignee(ctx, work, item)
				auto = executor != ""
			}
			if executor == "" {
				logger.V(4).Info("labeled item awaiting assignee consent", "issue", item.GetNumber())
				continue
			}
			fixes = append(fixes, fixPlan{issue: item.GetNumber(), issueURL: item.GetHTMLURL(), executor: executor, auto: auto})
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

// autoConsentedAssignee returns an assignee holding standing auto-fix
// consent (two-key: board intake.autoFix.enabled AND the member's own
// opt-in), for items satisfying the require gate.
func (r *Reconciler) autoConsentedAssignee(ctx context.Context, work *workState, issue *github.Issue) string {
	autoFix := work.board.Spec.Intake.AutoFix
	if !autoFix.Enabled {
		return ""
	}
	for _, a := range issue.Assignees {
		login := a.GetLogin()
		if r.memberOptedInAutoFix(ctx, login, work.board) {
			return login
		}
	}
	return ""
}

func autoFixWithoutLabel(board *boardv1alpha1.RepoBoard) bool {
	autoFix := board.Spec.Intake.AutoFix
	if !autoFix.Enabled {
		return false
	}
	for _, req := range autoFix.Require {
		if req == "label" {
			return false
		}
	}
	return true
}

// discoverIntakePRs lists every open PR for draft-review intake.
// discoverIntakePRs plans intake reviews. Every review needs a named,
// consenting executor — anonymous prep-identity reviews never run (tokens
// spent on a review nobody asked for, invisible to everyone):
//   - personal board: the member IS the board; every open PR is reviewed
//     as them (enabling intake was their consent).
//   - shared board: only PRs whose review is explicitly requested from a
//     member with the standing auto-review opt-in (two-key, like auto-fix).
func (r *Reconciler) discoverIntakePRs(ctx context.Context, ghClient *github.Client, work *workState) ([]reviewPlan, error) {
	var reviews []reviewPlan
	optedIn := map[string]bool{}
	opts := &github.PullRequestListOptions{State: "open", ListOptions: github.ListOptions{PerPage: 100}}
	for {
		prs, resp, err := ghClient.PullRequests.List(ctx, work.owner, work.repo, opts)
		if err != nil {
			return reviews, err
		}
		for _, pr := range prs {
			if vetoed(pr.Labels, work.board.Spec.Intake.Filters.ExcludeLabels) {
				continue
			}
			if work.personal {
				reviews = append(reviews, reviewPlan{pr: pr.GetNumber(), executor: work.board.Namespace})
				continue
			}
			for _, reviewer := range pr.RequestedReviewers {
				member := strings.ToLower(reviewer.GetLogin())
				opted, ok := optedIn[member]
				if !ok {
					opted = r.memberOptedInAutoReview(ctx, member, work.board)
					optedIn[member] = opted
				}
				if opted {
					reviews = append(reviews, reviewPlan{pr: pr.GetNumber(), executor: member})
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
	if !r.memberOptedInAutoFix(ctx, member, work.board) {
		return nil, nil
	}
	var fixes []fixPlan
	opts := &github.IssueListByRepoOptions{State: "open", Assignee: member, ListOptions: github.ListOptions{PerPage: 100}}
	for {
		items, resp, err := ghClient.Issues.ListByRepo(ctx, work.owner, work.repo, opts)
		if err != nil {
			return fixes, err
		}
		for _, item := range items {
			if item.IsPullRequest() || vetoed(item.Labels, work.board.Spec.Intake.Filters.ExcludeLabels) {
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
func (r *Reconciler) mailboxPlans(work *workState) ([]fixPlan, []reviewPlan, []int) {
	raw := work.board.GetAnnotations()[AnnotationRequests]
	if raw == "" {
		return nil, nil, nil
	}
	requests := map[string]string{}
	if err := json.Unmarshal([]byte(raw), &requests); err != nil {
		return nil, nil, nil
	}
	var fixes []fixPlan
	var reviews []reviewPlan
	var triages []int
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
		}
	}
	return fixes, reviews, triages
}

// dedupeReviews keeps one plan per PR, preferring a consented executor
// (member click) over an anonymous discovery plan.
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
		if existing.executor == "" && plan.executor != "" {
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
	if sb == nil && r.activeCount(work) >= maxActive(work.board) {
		log.FromContext(ctx).Info("fix deferred: board at maxActive", "issue", plan.issue, "limit", maxActive(work.board))
		return
	}
	if r.activeForExecutor(work, plan.executor) >= maxActivePerUser(work.board) && sb == nil {
		log.FromContext(ctx).Info("fix deferred: executor at maxActivePerUser", "issue", plan.issue, "executor", plan.executor, "limit", maxActivePerUser(work.board))
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
	key := fmt.Sprintf("%s/review-pr-%d", plan.executor, plan.pr)

	sb := work.findPRSandbox(plan.pr)
	annotations := map[string]string{}
	if sb != nil && sb.GetAnnotations() != nil {
		annotations = sb.GetAnnotations()
	}
	// Abandoned and legacy draft-bearing sandboxes are terminal: relaunch
	// only on a fresh re-review marker (a new click stamps one).
	done := annotations[AnnotationAgentDraft] != "" || annotations[AnnotationReviewState] != "" ||
		annotations[AnnotationReviewAbandoned] != ""
	if done && !rerunRequested(sb, AnnotationRereviewRequested, AnnotationReviewedAt) {
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
		// Error, or a success with nothing recognizable in its output:
		// back off rather than hot-looping the agent.
		if time.Since(res.FinishedAt) < launchRetryBackoff {
			return
		}
	}

	if sb == nil && r.activeCount(work) >= maxActive(work.board) {
		logger.Info("review deferred: board at maxActive", "pr", plan.pr, "limit", maxActive(work.board))
		return
	}
	if r.activeForExecutor(work, plan.executor) >= maxActivePerUser(work.board) && sb == nil {
		logger.Info("review deferred: executor at maxActivePerUser", "pr", plan.pr, "executor", plan.executor, "limit", maxActivePerUser(work.board))
		return
	}
	if r.Factory.StartReview(key, factorycli.ReviewOptions{
		Namespace:         plan.executor,
		PRURL:             fmt.Sprintf("https://github.com/%s/%s/pull/%d", work.owner, work.repo, plan.pr),
		Image:             work.board.Spec.Sandbox.Image,
		WorkspaceDiskSize: work.board.Spec.Sandbox.DiskSize,
		GithubToken:       token,
		Publish:           "draft",
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
		rerun := rerunRequested(sb, AnnotationRereviewRequested, AnnotationReviewedAt)
		if !reviewish && !rerun {
			continue
		}
		done := annotations[AnnotationAgentDraft] != "" || annotations[AnnotationReviewState] != "" ||
			annotations[AnnotationReviewAbandoned] != ""
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
		executor := annotations[AnnotationExecutor]
		if executor == "" && sb.GetNamespace() != work.board.Namespace {
			executor = sb.GetNamespace()
		}
		if executor == "" && work.personal {
			executor = work.board.Namespace
		}
		r.ensureReview(ctx, work, reviewPlan{pr: pr, executor: executor})
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
		executor := annotations[AnnotationExecutor]
		if executor == "" && sb.GetNamespace() != work.board.Namespace {
			executor = sb.GetNamespace()
		}
		if executor == "" && work.personal {
			executor = work.board.Namespace
		}
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
			if work.findSandbox(work.board.Namespace, factorycli.TriageSandboxName(work.repo, n)) != nil {
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
