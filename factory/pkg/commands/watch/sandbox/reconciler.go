package sandbox

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/commands/common"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/commands/watch/concurrency"
	factorysandbox "github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/sandbox"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/usagereport"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/klog/v2"
)

const (
	// DefaultInterval is how often sandbox state is reconciled against the cluster.
	DefaultInterval = 30 * time.Second
	// DefaultGCInterval is how often sandboxes are garbage collected. Collection
	// consults GitHub for entities missing from the cache, so it runs on a slower
	// cadence than plain state reconciliation.
	DefaultGCInterval = 5 * time.Minute
	// DefaultEvictionAge is how long an idle sandbox is kept before it is evicted.
	DefaultEvictionAge = 7 * 24 * time.Hour

	// gcLeaseHolder is the lease owner the reconciler records while it is
	// collecting a sandbox, in the same namespace of names the dispatcher uses
	// for task files. It cannot collide with one: task leases are file names.
	gcLeaseHolder = "sandbox-gc"
)

// EntityState answers, without any network call, whether a GitHub entity is
// still open. The reconciler uses it as a fast path: sandboxes belonging to
// entities that are known to be open are skipped outright, and only the
// remainder are confirmed against GitHub before being deleted.
//
// Pull request and issue state are published by different scans, so each has
// its own "is this populated yet" question and the reconciler gates the two
// collection passes independently.
type EntityState interface {
	// HasOpenPRs reports whether a pull request scan has populated the cache.
	HasOpenPRs() bool
	// HasOpenIssues reports whether an issue scan has populated the cache.
	HasOpenIssues() bool
	// IsOpenPR reports whether the given pull request is currently open.
	IsOpenPR(num int) bool
	// IsOpenIssue reports whether the given issue is currently open.
	IsOpenIssue(num int) bool
}

// Config holds the tuning knobs of a Reconciler.
type Config struct {
	// Interval is the delay between sandbox state reconcile cycles.
	Interval time.Duration
	// GCInterval is the delay between garbage collection sweeps.
	GCInterval time.Duration
	// EvictionAge is how long a sandbox may live without running a task before
	// it is deleted, expressed as a Go duration or a number of days ("7d").
	// An empty value means DefaultEvictionAge.
	EvictionAge string
	// IdleTimeout is how long a sandbox may sit idle before it is suspended
	// (scaled to zero replicas). Zero disables suspension.
	IdleTimeout time.Duration
	// DryRun reports what would be cleaned up without mutating the cluster.
	DryRun bool
}

// Deps holds the collaborators of a Reconciler.
type Deps struct {
	// Sandboxes provides the cluster and GitHub access the reconciler needs.
	Sandboxes *Service
	// Locks records which sandboxes are leased by an in-flight task.
	Locks *concurrency.SandboxLockRegistry
	// Entities is the shared cache of open issues and pull requests. It may be
	// nil, in which case every candidate is confirmed against GitHub.
	Entities EntityState
	// Paused reports whether reclaiming sandboxes must be held off, which is how
	// the watcher propagates drain mode. It is deliberately a plain read-only
	// signal rather than a queue handle: the reconciler must never be able to
	// mutate queue state. A nil Paused never pauses.
	//
	// Shutdown does not arrive through here. Cancelling the context passed to
	// Run is what stops the reconciler, and unlike this signal it also aborts a
	// sweep that has already started.
	Paused func() bool
}

// Reconciler keeps cluster sandbox state in sync with reality and reclaims
// sandboxes that are no longer needed.
//
// It runs as an autonomous goroutine so that its cluster and GitHub calls,
// which are the slowest in the daemon, never delay issue scanning or task
// dispatching. It only ever observes queue state through the sandbox lease
// registry, and it takes the lease of any sandbox it is about to reclaim, so a
// sandbox with an in-flight task is never deleted, suspended or evicted.
type Reconciler struct {
	cfg       Config
	sandboxes *Service
	locks     *concurrency.SandboxLockRegistry
	entities  EntityState
	paused    func() bool
}

// New constructs a Reconciler from its configuration and dependencies.
func New(cfg Config, deps Deps) *Reconciler {
	if cfg.Interval <= 0 {
		cfg.Interval = DefaultInterval
	}
	if cfg.GCInterval <= 0 {
		cfg.GCInterval = DefaultGCInterval
	}
	return &Reconciler{
		cfg:       cfg,
		sandboxes: deps.Sandboxes,
		locks:     deps.Locks,
		entities:  deps.Entities,
		paused:    deps.Paused,
	}
}

// Run reconciles sandbox state until ctx is cancelled, returning nil once it
// has stopped.
//
// Both cycles run once at startup so that a restart reclaims what it can
// without waiting out a full interval, and then on their own tickers.
func (r *Reconciler) Run(ctx context.Context) error {
	ticker := time.NewTicker(r.cfg.Interval)
	defer ticker.Stop()

	gcTicker := time.NewTicker(r.cfg.GCInterval)
	defer gcTicker.Stop()

	r.ReconcileOnce(ctx)
	r.CollectGarbage(ctx)

	for {
		select {
		case <-ctx.Done():
			// Cancellation is how this subcontroller is asked to stop, so it is
			// not an error worth propagating to the caller.
			return nil
		case <-ticker.C:
			r.ReconcileOnce(ctx)
		case <-gcTicker.C:
			r.CollectGarbage(ctx)
		}
	}
}

// ReconcileOnce executes a single state reconciliation cycle: it clears out
// evicted pods and refreshes the recorded task state of sandboxes that still
// claim to be running.
//
// It keeps running while the watcher is draining or shutting down. Nothing it
// does is destructive, and the drain report counts running tasks from exactly
// the annotations this cycle corrects.
func (r *Reconciler) ReconcileOnce(ctx context.Context) {
	if r.sandboxes == nil || r.sandboxes.kube == nil {
		return
	}

	r.deleteEvictedPods(ctx)
	r.reconcileRunningSandboxes(ctx)
}

// CollectGarbage executes a single garbage collection sweep, reclaiming
// sandboxes whose issue or pull request is closed, evicting sandboxes that
// outlived the configured eviction age, and suspending idle ones.
func (r *Reconciler) CollectGarbage(ctx context.Context) {
	if r.sandboxes == nil || r.sandboxes.kube == nil {
		return
	}
	if r.paused != nil && r.paused() {
		klog.V(2).Infof("Skipping sandbox garbage collection because the watcher is draining.")
		return
	}

	// One listing serves the whole sweep. The passes below only ever delete or
	// suspend, so a sandbox created after this point is simply handled next time.
	items, err := r.sandboxes.List(ctx)
	if err != nil {
		klog.Errorf("Failed to list sandboxes for garbage collection: %v", err)
		return
	}

	// collected records what this sweep already deleted, so a later pass does
	// not act on the same sandbox again from the stale listing.
	collected := make(map[string]bool)

	// Collecting a closed entity's sandbox means confirming every sandbox the
	// cache cannot vouch for against GitHub, one request at a time. Wait for the
	// scan that populates each cache rather than issuing that storm on a cold
	// start. The two caches are filled by different scans, so they gate
	// separately: an issue scan must not be blocked behind a PR scan.
	if r.entities == nil || r.entities.HasOpenPRs() {
		r.cleanupClosedPRSandboxes(ctx, items, collected)
	} else {
		klog.V(2).Infof("Skipping closed PR sandbox collection until the open pull request cache is populated.")
	}
	if r.entities == nil || r.entities.HasOpenIssues() {
		r.cleanupClosedIssueSandboxes(ctx, items, collected)
	} else {
		klog.V(2).Infof("Skipping closed issue sandbox collection until the open issue cache is populated.")
	}

	// Eviction and suspension decide purely on cluster state, so they need no
	// cache and stay useful even in modes where no scanner ever runs.
	if err := r.cleanupStaleIdleSandboxes(ctx, items, collected); err != nil {
		klog.Errorf("Failed to clean up stale idle sandboxes: %v", err)
	}
	r.suspendIdleSandboxes(ctx, items, collected)
}

// evictedPodLabelKeys are the label keys that can carry the sandbox name on a
// sandbox pod. A label selector cannot express "has either key", so each is
// listed in turn and the results are merged.
var evictedPodLabelKeys = []string{"sandbox", "agents.x-k8s.io/sandbox"}

// deleteEvictedPods proactively removes evicted sandbox pods so the sandbox
// controller can recreate them or release their resources.
func (r *Reconciler) deleteEvictedPods(ctx context.Context) {
	kube := r.sandboxes.kube
	if kube.Clientset == nil {
		return
	}
	namespace := r.sandboxes.namespace

	seen := make(map[string]bool)
	for _, labelKey := range evictedPodLabelKeys {
		podList, err := kube.Clientset.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{LabelSelector: labelKey})
		if err != nil {
			klog.Warningf("Failed to list sandbox pods by label %q for eviction cleanup: %v", labelKey, err)
			continue
		}
		for i := range podList.Items {
			pod := &podList.Items[i]
			if seen[pod.Name] {
				continue
			}
			seen[pod.Name] = true
			r.deleteEvictedPod(ctx, pod, namespace)
		}
	}
}

// deleteEvictedPod removes one evicted pod and records the eviction against its
// sandbox.
//
// The delete is what arbitrates the eviction count. Service.IsTaskRunning
// cleans up evicted pods too, and another watcher may be sweeping the same
// namespace, so the counter is incremented only by whoever wins the delete.
// Everyone else sees NotFound and leaves the count alone.
func (r *Reconciler) deleteEvictedPod(ctx context.Context, pod *corev1.Pod, namespace string) {
	if pod.DeletionTimestamp != nil || pod.Status.Phase != corev1.PodFailed || !strings.EqualFold(pod.Status.Reason, "Evicted") {
		return
	}

	klog.Infof("Found evicted sandbox pod %s in namespace %s. Deleting pod so controller can recreate or clean up.", pod.Name, namespace)
	kube := r.sandboxes.kube
	if err := kube.Clientset.CoreV1().Pods(namespace).Delete(ctx, pod.Name, metav1.DeleteOptions{}); err != nil {
		if !apierrors.IsNotFound(err) {
			klog.Warningf("Failed to delete evicted sandbox pod %s: %v", pod.Name, err)
		}
		return
	}

	sbName := pod.Labels["sandbox"]
	if sbName == "" {
		sbName = pod.Labels["agents.x-k8s.io/sandbox"]
	}
	if sbName != "" {
		_ = factorysandbox.IncrementSandboxEvictionCount(ctx, kube, namespace, sbName)
	}
}

// reconcileRunningSandboxes re-probes every sandbox that still claims to be
// running a task, which corrects the annotation of sandboxes whose task
// finished without the watcher observing it.
func (r *Reconciler) reconcileRunningSandboxes(ctx context.Context) {
	items, err := r.sandboxes.List(ctx)
	if err != nil {
		klog.Warningf("Failed to list sandboxes during reconcile: %v", err)
		return
	}

	for i := range items {
		// Each probe is a network round trip, so give up as soon as the cycle
		// budget is spent instead of running the rest against a dead context.
		if ctx.Err() != nil {
			return
		}

		item := &items[i]
		if factorysandbox.IsCurrentSandbox(ctx, r.sandboxes.kube, item, r.sandboxes.namespace) {
			continue
		}
		state := ""
		if annotations := item.GetAnnotations(); annotations != nil {
			state = annotations[annotationLastTaskState]
		}
		if state != "" && !strings.EqualFold(state, taskStateRunning) {
			continue
		}

		name := item.GetName()
		running, err := r.sandboxes.RefreshTaskState(ctx, name)
		if err != nil {
			klog.Warningf("Failed to check status of running sandbox %s during reconcile: %v", name, err)
		} else if !running {
			klog.Infof("Sandbox '%s' task completed or stopped running; annotation updated.", name)
		}
	}
}

// collect runs fn while holding the garbage collection lease on sandboxName,
// reporting whether the sandbox was collected.
//
// Taking the lease rather than merely testing it is what makes the "never touch
// a sandbox with an in-flight task" rule hold. Confirming an entity against
// GitHub or probing a sandbox takes long enough that the dispatcher could
// otherwise lease the sandbox and start a task in the gap between the test and
// the delete, and the delete destroys the workspace that task is using. While
// the reconciler holds the lease the dispatcher's TryAcquire fails and it
// simply retries the task on a later cycle.
func (r *Reconciler) collect(sandboxName, operation string, fn func() bool) bool {
	if !r.locks.TryAcquire(sandboxName, gcLeaseHolder) {
		klog.Infof("Skipping %s of sandbox '%s' because it is leased by an in-flight task.", operation, sandboxName)
		return false
	}
	defer r.locks.Release(sandboxName, gcLeaseHolder)

	return fn()
}

// cleanupClosedPRSandboxes deletes sandboxes belonging to pull requests that
// have been merged or closed.
func (r *Reconciler) cleanupClosedPRSandboxes(ctx context.Context, items []unstructured.Unstructured, collected map[string]bool) {
	for i := range items {
		if ctx.Err() != nil {
			return
		}

		item := &items[i]
		name := item.GetName()
		if !strings.HasPrefix(name, "factory-pr-") {
			continue
		}
		num, err := strconv.Atoi(strings.TrimPrefix(name, "factory-pr-"))
		if err != nil {
			continue
		}

		// Fast-path: skip PRs that the latest scan reported as open.
		if r.entities != nil && r.entities.IsOpenPR(num) {
			continue
		}

		operation := fmt.Sprintf("collection of closed PR #%d", num)
		if r.collect(name, operation, func() bool { return r.deleteClosedPRSandbox(ctx, item, name, num) }) {
			collected[name] = true
		}
	}
}

// deleteClosedPRSandbox confirms against GitHub that a pull request really is
// closed and, if so, harvests its usage data and deletes the sandbox. It
// reports whether the sandbox was deleted, and must be called while holding the
// sandbox's collection lease.
func (r *Reconciler) deleteClosedPRSandbox(ctx context.Context, item *unstructured.Unstructured, name string, num int) bool {
	gh := r.sandboxes.gh
	if gh == nil {
		return false
	}
	pr, _, err := gh.PullRequests.Get(ctx, r.sandboxes.owner, r.sandboxes.repo, num)
	if err != nil {
		klog.Warningf("Failed to fetch PR #%d for sandbox cleanup check: %v", num, err)
		return false
	}
	if pr.GetState() != "closed" {
		return false
	}

	klog.Infof("Pull Request #%d is closed/merged. Deleting corresponding sandbox '%s'...", num, name)
	if r.cfg.DryRun {
		fmt.Printf("[DRYRUN] Would delete sandbox '%s' for closed PR #%d\n", name, num)
		return false
	}

	meta := common.SandboxUsageMeta(item, r.repoSlug())
	meta.PR = num
	meta.Issues = common.ReferencedIssueList(pr)
	usagereport.HarvestSandbox(ctx, r.sandboxes.namespace, name, meta)
	usagereport.ReportPRSubject(ctx, r.repoSlug(), pr)
	if err := r.sandboxes.Delete(ctx, name); err != nil {
		klog.Errorf("Failed to delete sandbox '%s' for closed PR #%d: %v", name, num, err)
		return false
	}
	return true
}

// cleanupClosedIssueSandboxes deletes sandboxes belonging to closed issues,
// covering both workflow sandboxes ("wf-issue-N") and issue fix sandboxes ("fix-<repo>-N").
func (r *Reconciler) cleanupClosedIssueSandboxes(ctx context.Context, items []unstructured.Unstructured, collected map[string]bool) {
	for i := range items {
		if ctx.Err() != nil {
			return
		}

		item := &items[i]
		name := item.GetName()
		num, ok := issueNumberFromSandboxName(name)
		if !ok {
			continue
		}

		// Fast-path: skip issues that the latest scan reported as open.
		if r.entities != nil && r.entities.IsOpenIssue(num) {
			continue
		}

		operation := fmt.Sprintf("collection of closed issue #%d", num)
		if r.collect(name, operation, func() bool { return r.deleteClosedIssueSandbox(ctx, item, name, num) }) {
			collected[name] = true
		}
	}
}

// deleteClosedIssueSandbox confirms against GitHub that an issue really is
// closed and, if so, harvests its usage data and deletes the sandbox. It
// reports whether the sandbox was deleted, and must be called while holding the
// sandbox's collection lease.
func (r *Reconciler) deleteClosedIssueSandbox(ctx context.Context, item *unstructured.Unstructured, name string, num int) bool {
	gh := r.sandboxes.gh
	if gh == nil {
		return false
	}
	issue, _, err := gh.Issues.Get(ctx, r.sandboxes.owner, r.sandboxes.repo, num)
	if err != nil {
		klog.Warningf("Failed to fetch issue #%d for sandbox cleanup check: %v", num, err)
		return false
	}
	if issue.GetState() != "closed" {
		return false
	}

	klog.Infof("Issue #%d is closed. Deleting corresponding sandbox '%s'...", num, name)
	if r.cfg.DryRun {
		fmt.Printf("[DRYRUN] Would delete sandbox '%s' for closed issue #%d\n", name, num)
		return false
	}

	meta := common.SandboxUsageMeta(item, r.repoSlug())
	meta.Issue = num
	usagereport.HarvestSandbox(ctx, r.sandboxes.namespace, name, meta)
	usagereport.ReportIssueSubject(ctx, r.repoSlug(), issue)
	if strings.HasPrefix(name, "wf-issue-") {
		usagereport.PostWorkflowSummaryIfNeeded(ctx, gh, r.sandboxes.owner, r.sandboxes.repo, num)
	}
	if err := r.sandboxes.Delete(ctx, name); err != nil {
		klog.Errorf("Failed to delete sandbox '%s' for closed issue #%d: %v", name, num, err)
		return false
	}
	return true
}

// cleanupStaleIdleSandboxes evicts sandboxes that have outlived the configured
// eviction age without running a task.
func (r *Reconciler) cleanupStaleIdleSandboxes(ctx context.Context, items []unstructured.Unstructured, collected map[string]bool) error {
	evictionAge, err := ParseEvictionAge(r.cfg.EvictionAge)
	if err != nil {
		return fmt.Errorf("parsing sandbox eviction age: %w", err)
	}

	now := time.Now()
	for i := range items {
		if ctx.Err() != nil {
			return nil
		}

		item := &items[i]
		name := item.GetName()
		if collected[name] {
			continue
		}
		if factorysandbox.IsCurrentSandbox(ctx, r.sandboxes.kube, item, r.sandboxes.namespace) {
			continue
		}
		creationTime := item.GetCreationTimestamp().Time
		if creationTime.IsZero() || now.Sub(creationTime) <= evictionAge {
			continue
		}

		operation := "eviction"
		if r.collect(name, operation, func() bool { return r.evictStaleSandbox(ctx, item, name, evictionAge, creationTime) }) {
			collected[name] = true
		}
	}

	return nil
}

// evictStaleSandbox probes a sandbox that is past its eviction age and deletes
// it if no task is running inside. It reports whether the sandbox was deleted,
// and must be called while holding the sandbox's collection lease.
func (r *Reconciler) evictStaleSandbox(ctx context.Context, item *unstructured.Unstructured, name string, evictionAge time.Duration, creationTime time.Time) bool {
	running, err := r.sandboxes.IsTaskRunning(ctx, name)
	if err != nil {
		klog.Errorf("Failed to check if sandbox %s is running: %v", name, err)
		return false
	}
	if running {
		return false
	}

	klog.Infof("Sandbox '%s' has been idle and is older than configured eviction age %s (created: %v). Evicting...", name, evictionAge, creationTime)
	if r.cfg.DryRun {
		fmt.Printf("[DRYRUN] Would evict stale/idle sandbox '%s'\n", name)
		return false
	}

	usagereport.HarvestSandbox(ctx, r.sandboxes.namespace, name, common.SandboxUsageMeta(item, r.repoSlug()))
	if err := r.sandboxes.Delete(ctx, name); err != nil {
		klog.Errorf("Failed to delete stale/idle sandbox '%s': %v", name, err)
		return false
	}
	return true
}

// suspendIdleSandboxes scales idle sandboxes down to zero replicas, skipping
// any sandbox leased by an in-flight task.
//
// Each sandbox is handled on its own so that it can be bracketed by the same
// lease the other passes take, which means a sandbox is only ever held for as
// long as its own suspension takes.
func (r *Reconciler) suspendIdleSandboxes(ctx context.Context, items []unstructured.Unstructured, collected map[string]bool) {
	if r.cfg.IdleTimeout <= 0 {
		return
	}

	for i := range items {
		if ctx.Err() != nil {
			return
		}

		item := &items[i]
		name := item.GetName()
		if collected[name] {
			continue
		}

		r.collect(name, "suspension", func() bool {
			suspended, err := factorysandbox.SuspendSandboxIfIdle(ctx, r.sandboxes.kube, r.sandboxes.namespace, item, r.cfg.IdleTimeout, r.cfg.DryRun)
			if err != nil {
				klog.Errorf("Failed to suspend idle sandbox '%s': %v", name, err)
			}
			return suspended
		})
	}
}

// repoSlug returns the "owner/repo" identifier of the watched repository.
func (r *Reconciler) repoSlug() string {
	return r.sandboxes.owner + "/" + r.sandboxes.repo
}

// issueNumberFromSandboxName extracts the issue number encoded in a workflow
// ("wf-issue-N") or issue fix ("fix-<repo>-N") sandbox name.
func issueNumberFromSandboxName(name string) (int, bool) {
	var numStr string
	switch {
	case strings.HasPrefix(name, "wf-issue-"):
		numStr = strings.TrimPrefix(name, "wf-issue-")
	case strings.HasPrefix(name, "fix-"):
		idx := strings.LastIndex(name, "-")
		if idx == -1 {
			return 0, false
		}
		numStr = name[idx+1:]
	default:
		return 0, false
	}

	num, err := strconv.Atoi(numStr)
	if err != nil || num <= 0 {
		return 0, false
	}
	return num, true
}

// ParseEvictionAge parses a sandbox eviction age, which is either a Go duration
// ("12h") or a number of days ("7d"). An empty value yields DefaultEvictionAge.
func ParseEvictionAge(ageStr string) (time.Duration, error) {
	ageStr = strings.TrimSpace(ageStr)
	if ageStr == "" {
		return DefaultEvictionAge, nil
	}

	if strings.HasSuffix(ageStr, "d") {
		days, err := strconv.Atoi(strings.TrimSuffix(ageStr, "d"))
		if err != nil {
			return 0, fmt.Errorf("invalid days format %q: %w", ageStr, err)
		}
		return time.Duration(days) * 24 * time.Hour, nil
	}

	return time.ParseDuration(ageStr)
}
