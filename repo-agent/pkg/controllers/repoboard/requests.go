// Copyright 2026 The Kubernetes Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package repoboard

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	boardv1alpha1 "github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/api/repoboard/v1alpha1"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/factorycli"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/research"
)

// How long a Request lives after it stops being interesting.
//
// A Request is not history. It is the receipt for a click, and the only
// reader that matters is the member who made it, within a session or
// two. Everything durable — the plan draft, the review state, the run's
// directory on the branch — is somewhere else by then.
//
// So the two terminal phases keep different clocks. A success is read
// once, if at all, and then the sandbox is the answer; an hour is long
// enough for the UI to stop showing a row as pending and short enough
// that a busy board does not accumulate. A failure is the opposite: it
// is the only place the reason is written down, and the member may not
// look until Monday.
const (
	succeededTTL = time.Hour
	failedTTL    = 7 * 24 * time.Hour

	// pendingTTL bounds a click that never launched. The annotation
	// mailbox it replaces had no such bound: an entry nothing could
	// serve sat there forever, and the only way to find out was to read
	// the board's YAML.
	//
	// A day, not an hour, because waiting is legitimate — a fix queued
	// behind maxActive sandboxes can honestly take most of an afternoon.
	// A day later it is not waiting, it is stuck, and launching it then
	// would be a surprise rather than a service.
	pendingTTL = 24 * time.Hour

	// maxHistory caps the terminal Requests kept per (verb, subject).
	// Clicking Deploy six times leaves six receipts and you only ever
	// read the last one; the cap keeps a board's request list bounded by
	// the work on it rather than by how long it has existed.
	maxHistory = 5
)

// loadRequests reads every Request filed against this board.
//
// Requests live in the board's namespace and carry its name as a label,
// which makes this a single indexed list with no cross-namespace read —
// the same blast radius the annotation had, now as objects.
func (r *Reconciler) loadRequests(ctx context.Context, work *workState) error {
	list := &boardv1alpha1.RequestList{}
	if err := r.List(ctx, list,
		client.InNamespace(work.board.Namespace),
		client.MatchingLabels{boardv1alpha1.LabelBoard: work.board.Name},
	); err != nil {
		return fmt.Errorf("listing requests: %w", err)
	}
	// Oldest first: a member who clicked twice gets the click they made
	// first, and the dedup below keeps it.
	sort.Slice(list.Items, func(i, j int) bool {
		return list.Items[i].CreationTimestamp.Before(&list.Items[j].CreationTimestamp)
	})
	work.requests = make([]*boardv1alpha1.Request, 0, len(list.Items))
	for i := range list.Items {
		work.requests = append(work.requests, &list.Items[i])
	}
	return nil
}

// activeRequests are the ones still owed something, one per (verb,
// subject).
//
// Deduping here rather than trusting the API to file only one: the API
// checks before it creates, but two browser tabs racing that check both
// pass it, and the cost of the duplicate landing is a second launch. The
// map write the mailbox used to do gave this for free; an object per
// click has to say it.
func (w *workState) activeRequests() []*boardv1alpha1.Request {
	seen := map[string]bool{}
	var out []*boardv1alpha1.Request
	for _, req := range w.requests {
		if !req.Active() {
			continue
		}
		key := req.Spec.Key()
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, req)
	}
	return out
}

// requestMailbox turns the standing Requests into the same plan slices
// the annotation mailbox produced, so that every ensure pass downstream
// is unchanged: it still receives a fixPlan, and does not care that the
// click now has an object behind it.
func (r *Reconciler) requestMailbox(work *workState) mailbox {
	var box mailbox
	for _, req := range work.activeRequests() {
		spec := req.Spec
		switch spec.Verb {
		case boardv1alpha1.VerbFix:
			box.fixes = append(box.fixes, fixPlan{issue: spec.Number, executor: spec.Member})
		case boardv1alpha1.VerbReview:
			box.reviews = append(box.reviews, reviewPlan{pr: spec.Number, executor: spec.Member})
		case boardv1alpha1.VerbTriage:
			box.triages = append(box.triages, spec.Number)
		case boardv1alpha1.VerbPlan:
			box.plans = append(box.plans, planRequest{issue: spec.Number, member: spec.Member})
		case boardv1alpha1.VerbIterate, boardv1alpha1.VerbAddress, boardv1alpha1.VerbInvestigate:
			box.prTasks = append(box.prTasks, prTaskClaim{
				pr:          spec.Number,
				member:      spec.Member,
				kind:        spec.Verb,
				instruction: spec.Instruction,
			})
		case boardv1alpha1.VerbRun:
			if claim, ok := runClaimFrom(req); ok {
				box.runbooks = append(box.runbooks, claim)
			}
		case boardv1alpha1.VerbResearch:
			if claim, ok := researchClaimFrom(req); ok {
				box.research = append(box.research, claim)
			}
		}
	}
	return box
}

// runClaimFrom reads a run Request as the claim the launch pass takes.
//
// The claim keeps the request on it. Run is the one verb whose "already
// done this" receipt lives in the controller's memory rather than on an
// object, so it is the one verb that has to write down that it is about
// to spend money — see markLaunching.
//
// The runbook is shape-checked for the reason the research session id
// is: it becomes an argument to the factory CLI, and a Request is
// writable by anything with access to the namespace.
func runClaimFrom(req *boardv1alpha1.Request) (runbookClaim, bool) {
	run := req.Spec.Run
	if run == nil || run.Name == "" {
		return runbookClaim{}, false
	}
	if run.Runbook != "" && !runbookNameRE.MatchString(run.Runbook) {
		return runbookClaim{}, false
	}
	if run.Target < 0 {
		return runbookClaim{}, false
	}
	// scenario is the shape, instance is the run. They were one string
	// in the annotation, which is why runIntent still has to ask whether
	// they differ.
	scenario := run.Scenario
	if scenario == "" {
		scenario = run.Name
	}
	return runbookClaim{
		mode:      run.Mode,
		scenario:  scenario,
		instance:  run.Name,
		member:    req.Spec.Member,
		claimedAt: req.CreationTimestamp.Time,
		intent:    run.Intent,
		runbook:   run.Runbook,
		target:    run.Target,
		request:   req,
	}, true
}

// runbookNameRE is the shape factory slugs a runbook name to, and the
// Request CRD's pattern for it.
var runbookNameRE = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?$`)

// researchClaimFrom reads a research Request as the claim the launch
// pass takes. The session id is still shape-checked: it becomes an
// argument to the factory CLI, and a Request is writable by anything
// with access to the namespace.
func researchClaimFrom(req *boardv1alpha1.Request) (researchClaim, bool) {
	spec := req.Spec.Research
	if spec == nil || !researchSessionIDRE.MatchString(spec.SessionID) {
		return researchClaim{}, false
	}
	return researchClaim{
		sessionID: spec.SessionID,
		member:    req.Spec.Member,
		claimedAt: req.CreationTimestamp.Time,
		kickoff: research.Kickoff{
			Kind:  spec.Kind,
			Topic: spec.Topic,
			Since: spec.Since,
			Title: spec.Title,
		},
	}, true
}

// markLaunching records, before the launch rather than after, that this
// Request is about to spend something.
//
// Only the run verb calls it, and the asymmetry is the point. Every
// other verb's receipt is durable already: a fix, a review, a triage, a
// plan and a conversation are all "served" when their named sandbox
// exists, which survives a restart because the sandbox does. A run is
// served when the runner reports a result, and the runner's memory dies
// with the process — so a controller killed between StartRun and the
// result has no way to know it already deployed. It knows now.
//
// The flag that survives is launchedAt, not the phase. The phase moves
// on to Running within the same reconcile, which would leave the whole
// length of the run — an hour, and the part a restart is actually likely
// to land in — looking exactly like a click nobody had acted on yet.
func (r *Reconciler) markLaunching(ctx context.Context, req *boardv1alpha1.Request, sandbox string) {
	if req == nil || req.Status.LaunchedAt != nil {
		return
	}
	now := metav1.Now()
	req.Status.Phase = boardv1alpha1.RequestLaunching
	req.Status.Sandbox = sandbox
	req.Status.LaunchedAt = &now
	if err := r.Status().Update(ctx, req); err != nil {
		log.FromContext(ctx).Error(err, "unable to mark request launching", "request", req.Name)
	}
}

// unmarkLaunching takes the stamp back when the launch it announced did
// not happen.
//
// StartRun returns false without spending anything — the sandbox is busy
// with a task this process did not start, or the single-flight slot is
// taken. Leaving launchedAt behind in that case would strand a click
// that never launched, and tell the member their controller restarted
// mid-launch when it did no such thing. The window where the stamp is
// wrong is the width of one refused launch, and it errs toward stranding
// rather than spending, which is the side to be wrong on.
func (r *Reconciler) unmarkLaunching(ctx context.Context, req *boardv1alpha1.Request) {
	if req == nil || req.Status.LaunchedAt == nil {
		return
	}
	req.Status.Phase = boardv1alpha1.RequestPending
	req.Status.LaunchedAt = nil
	if err := r.Status().Update(ctx, req); err != nil {
		log.FromContext(ctx).Error(err, "unable to clear a refused launch", "request", req.Name)
	}
}

// requestOutcome is what the reap pass decides about one standing
// Request: the phase it should be in, and why.
type requestOutcome struct {
	phase   string
	reason  string
	message string
	sandbox string
}

// stillPending is the zero outcome: nothing has happened yet, and the
// Request stays as it is.
var stillPending = requestOutcome{}

// reapRequests is the whole lifecycle in one pass: it settles every
// standing Request against what the launch left behind, then deletes the
// ones that have been settled long enough.
//
// It is the trim pass the annotation mailbox had, and it runs in the
// same place for the same reason — after every ensure pass, so that a
// sandbox created this very reconcile counts as served. The difference
// is that a served claim used to be deleted on the spot, taking with it
// the only record that the click happened. Now it is written down, and
// deleted later.
func (r *Reconciler) reapRequests(ctx context.Context, work *workState) error {
	logger := log.FromContext(ctx)
	now := time.Now()

	for _, req := range work.activeRequests() {
		out := r.settle(ctx, work, req, now)
		if out.phase == "" || out.phase == req.Status.Phase {
			continue
		}
		req.Status.Phase = out.phase
		req.Status.Reason = out.reason
		req.Status.Message = out.message
		if out.sandbox != "" {
			req.Status.Sandbox = out.sandbox
		}
		if out.phase == boardv1alpha1.RequestSucceeded || out.phase == boardv1alpha1.RequestFailed {
			stamp := metav1.NewTime(now)
			req.Status.CompletedAt = &stamp
		}
		if err := r.Status().Update(ctx, req); err != nil {
			logger.Error(err, "unable to settle request", "request", req.Name, "phase", out.phase)
		}
	}

	r.collectRequests(ctx, work, now)
	return nil
}

// settle reads one standing Request's world and says what phase it is
// in. It writes nothing except the executor stamp a review consent owes
// its sandbox, which has to happen before the Request stops being the
// record of who consented.
func (r *Reconciler) settle(ctx context.Context, work *workState, req *boardv1alpha1.Request, now time.Time) requestOutcome {
	spec := req.Spec
	switch spec.Verb {
	case boardv1alpha1.VerbFix:
		if sb := work.findSandbox(spec.Member, work.fixSandboxName(spec.Number)); sb != nil {
			return served(sb.GetName())
		}

	case boardv1alpha1.VerbReview:
		if sb := work.findPRSandbox(spec.Number); sb != nil {
			// Persist the consenting executor on the sandbox before the
			// Request stops being read: it is the only durable record
			// that this review was a member's click, and draft-publish
			// depends on it.
			if sb.GetAnnotations()[AnnotationExecutor] != spec.Member {
				annotations := sb.GetAnnotations()
				if annotations == nil {
					annotations = map[string]string{}
				}
				annotations[AnnotationExecutor] = spec.Member
				sb.SetAnnotations(annotations)
				if err := r.Update(ctx, sb); err != nil {
					// Not settled: try again next reconcile rather than
					// drop the consent on the floor.
					log.FromContext(ctx).Error(err, "stamping executor", "sandbox", sb.GetName())
					return stillPending
				}
			}
			return served(sb.GetName())
		}

	case boardv1alpha1.VerbTriage:
		// The click stands until a draft is stored: the sandbox may be a
		// rejected leftover whose tombstone the click overrides.
		name := factorycli.TriageSandboxName(work.repo, spec.Number)
		if sb := work.findSandbox(work.board.Namespace, name); sb != nil && sb.GetAnnotations()[AnnotationTriagedAt] != "" {
			return served(sb.GetName())
		}

	case boardv1alpha1.VerbPlan:
		// Likewise: the fix sandbox may predate the click, so its
		// existence proves nothing. A stored draft does.
		if sb := work.findSandbox(spec.Member, work.fixSandboxName(spec.Number)); sb != nil && sb.GetAnnotations()[AnnotationPlannedAt] != "" {
			return served(sb.GetName())
		}

	case boardv1alpha1.VerbIterate, boardv1alpha1.VerbAddress, boardv1alpha1.VerbInvestigate:
		// Served once the claim has converted to the sandbox annotation
		// the click pass drives from. From there the sandbox is the
		// durable consent and this Request is only the receipt.
		reqKey := map[string]string{
			boardv1alpha1.VerbIterate:     AnnotationIterateRequested,
			boardv1alpha1.VerbAddress:     AnnotationAddressRequested,
			boardv1alpha1.VerbInvestigate: AnnotationInvestigateRequested,
		}[spec.Verb]
		if sb := work.findPRSandbox(spec.Number); sb != nil && sb.GetAnnotations()[reqKey] != "" {
			return served(sb.GetName())
		}

	case boardv1alpha1.VerbRun:
		return r.settleRun(work, req, now)

	case boardv1alpha1.VerbResearch:
		claim, ok := researchClaimFrom(req)
		if !ok {
			return requestOutcome{
				phase:  boardv1alpha1.RequestFailed,
				reason: "Malformed",
				// Nothing will ever serve it, and a Pending row for a
				// session that cannot be created is worse than an error.
				message: "the session id is not a name a sandbox can carry",
			}
		}
		// Not "the sandbox exists" — "the sandbox finished being built".
		// factory creates the object first and works on it for minutes,
		// so settling on existence retires the click while the session is
		// still a pod with an empty disk, and an interrupted launch then
		// has nothing standing to make anyone go back and finish it.
		if sb := researchSandboxDone(work, claim.member, claim.sessionID); sb != nil {
			return served(sb.GetName())
		}
		if researchClaimExpired(claim, now) {
			return requestOutcome{
				phase:   boardv1alpha1.RequestFailed,
				reason:  "Expired",
				message: "no sandbox came up for this conversation within the hour; start another",
			}
		}

	default:
		return requestOutcome{
			phase:   boardv1alpha1.RequestFailed,
			reason:  "UnknownVerb",
			message: fmt.Sprintf("nothing in this controller serves %q", spec.Verb),
		}
	}

	// Nothing served it. Report what the launcher is doing, so that a
	// click sitting there for minutes says which kind of waiting it is.
	return pendingOutcome(req, now)
}

// served is the ordinary happy ending: the thing the click asked for
// exists, and from here the sandbox is the answer.
func served(sandbox string) requestOutcome {
	return requestOutcome{
		phase:   boardv1alpha1.RequestSucceeded,
		reason:  "Launched",
		sandbox: sandbox,
	}
}

// pendingOutcome distinguishes the kinds of not-yet.
func pendingOutcome(req *boardv1alpha1.Request, now time.Time) requestOutcome {
	if now.Sub(req.CreationTimestamp.Time) > pendingTTL {
		return requestOutcome{
			phase:   boardv1alpha1.RequestFailed,
			reason:  "Expired",
			message: "never launched within a day of the click; click again if it is still wanted",
		}
	}
	return stillPending
}

// settleRun is the one verb whose receipt is not a sandbox.
//
// A run's sandbox is shared by every mode of that run and outlives all
// of them, so its existence says nothing about whether this click's
// deploy happened. The runner's result does — and a click buys exactly
// one attempt, whatever the outcome, because a deploy has cloud side
// effects and a watcher timeout must not duplicate work the pod is still
// doing.
func (r *Reconciler) settleRun(work *workState, req *boardv1alpha1.Request, now time.Time) requestOutcome {
	claim, ok := runClaimFrom(req)
	if !ok {
		return requestOutcome{
			phase:   boardv1alpha1.RequestFailed,
			reason:  "Malformed",
			message: "a run request needs a mode and a name",
		}
	}
	key := runbookKey(claim.member, work.repo, claim)
	sandbox := factorycli.RunbookSandboxName(work.repo, factorycli.RunbookInstance(claim.scenario, claim.instance))

	if res, found := r.Factory.LastResult(key); found && res.FinishedAt.After(claim.claimedAt) {
		if res.Err != nil {
			return requestOutcome{
				phase:   boardv1alpha1.RequestFailed,
				reason:  "LaunchFailed",
				message: clipMessage(strings.TrimSpace(res.Err.Error()), 400),
				sandbox: sandbox,
			}
		}
		return requestOutcome{phase: boardv1alpha1.RequestSucceeded, reason: "Completed", sandbox: sandbox}
	}
	if r.Factory.IsRunning(key) {
		return requestOutcome{phase: boardv1alpha1.RequestRunning, sandbox: sandbox}
	}

	// Not running, no result, and we launched it once: the process that
	// did is gone. The task itself may well be running still — the pod
	// outlives the controller — so ask the sandbox, which is the only
	// thing that watched.
	if req.Status.LaunchedAt != nil {
		return interruptedRun(work.findSandbox(claim.member, sandbox), req.Status.LaunchedAt.Time, sandbox, now)
	}
	return pendingOutcome(req, now)
}

// runInFlightGrace bounds how long a sandbox saying "Running" is taken
// at its word after the controller that launched the task died.
//
// The annotation is stamped at dispatch and only corrected at the end,
// so a task whose watcher was killed leaves it saying Running forever.
// The CLI's own ceiling is an hour; twice that is long enough that no
// honest run is cut off and short enough that a stuck one is eventually
// reported rather than shown as working.
const runInFlightGrace = 2 * time.Hour

// interruptedRun reads a restarted controller's unfinished business off
// the sandbox the run happened in.
//
// It deliberately never relaunches. Stranding a deploy costs a second
// click; repeating one costs a second cloud footprint, and the member
// finds out from the bill.
func interruptedRun(sb *unstructured.Unstructured, launchedAt time.Time, sandbox string, now time.Time) requestOutcome {
	if sb != nil {
		annotations := sb.GetAnnotations()
		// last-task-time is stamped when a task ENDS, so a sandbox still
		// working carries the previous task's stamp and nothing else.
		// Reading that as "the launch left no trace" is how a healthy
		// forty-minute deploy gets declared dead at minute five.
		if annotations[factorycli.AnnotationTaskState] == factorycli.TaskStateRunning &&
			now.Sub(launchedAt) < runInFlightGrace {
			return requestOutcome{phase: boardv1alpha1.RequestRunning, sandbox: sandbox}
		}
		at, err := time.Parse(time.RFC3339, annotations["sandbox.gemini.google.com/last-task-time"])
		if err == nil && at.After(launchedAt) {
			switch annotations[factorycli.AnnotationTaskState] {
			case factorycli.TaskStateFailed:
				return requestOutcome{
					phase:   boardv1alpha1.RequestFailed,
					reason:  "TaskFailed",
					message: "the task ran and failed; its output is in the sandbox",
					sandbox: sandbox,
				}
			case factorycli.TaskStateCompleted:
				return requestOutcome{phase: boardv1alpha1.RequestSucceeded, reason: "Completed", sandbox: sandbox}
			}
		}
	}
	return requestOutcome{
		phase:   boardv1alpha1.RequestFailed,
		reason:  "LaunchInterrupted",
		message: "the controller restarted mid-launch; check the run before clicking again",
		sandbox: sandbox,
	}
}

// collectRequests deletes the settled ones: past their phase's TTL, or
// past the history cap for their subject.
//
// Both bounds are needed. The TTL alone lets a board that is clicked at
// hourly-TTL speed hold an unbounded number of successes at once; the
// cap alone lets one long-abandoned failure outlive the repository. The
// cap is per (verb, subject) rather than per board so that a busy issue
// cannot evict a quiet one's only failure.
func (r *Reconciler) collectRequests(ctx context.Context, work *workState, now time.Time) {
	logger := log.FromContext(ctx)

	// Newest first within a subject, so the survivors are the ones
	// someone might still read.
	bySubject := map[string][]*boardv1alpha1.Request{}
	for _, req := range work.requests {
		if req.Active() {
			continue
		}
		key := req.Spec.Key()
		bySubject[key] = append(bySubject[key], req)
	}
	for _, reqs := range bySubject {
		sort.Slice(reqs, func(i, j int) bool {
			return reqs[j].CreationTimestamp.Before(&reqs[i].CreationTimestamp)
		})
		for i, req := range reqs {
			why := ""
			switch {
			case i >= maxHistory:
				why = "history cap"
			case expired(req, now):
				why = "ttl"
			default:
				continue
			}
			if err := r.Delete(ctx, req); err != nil && !errors.IsNotFound(err) {
				logger.Error(err, "unable to delete settled request", "request", req.Name)
				continue
			}
			logger.V(1).Info("collected request", "request", req.Name, "phase", req.Status.Phase, "why", why)
		}
	}
}

// expired reports whether a settled Request is past its phase's TTL. A
// terminal Request with no completedAt is one this controller settled
// before it stamped them; its creation time is close enough.
func expired(req *boardv1alpha1.Request, now time.Time) bool {
	ttl := succeededTTL
	if req.Status.Phase == boardv1alpha1.RequestFailed {
		ttl = failedTTL
	}
	since := req.CreationTimestamp.Time
	if req.Status.CompletedAt != nil {
		since = req.Status.CompletedAt.Time
	}
	return now.Sub(since) > ttl
}
