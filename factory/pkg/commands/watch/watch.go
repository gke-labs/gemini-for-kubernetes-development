package watch

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/clients"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/commands/watch/dispatcher"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/config"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/constants"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/github"
	githubv39 "github.com/google/go-github/v39/github"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/klog/v2"
)

// checkRepoInterval is how often the watch loop polls the repo for new work.
const checkRepoInterval = 1 * time.Minute

func (w *Watcher) Run(ctx context.Context) error {
	if err := w.init(ctx); err != nil {
		return err
	}

	if w.Once {
		w.reconciler.ReconcileOnce(ctx)
		w.checkRepo(ctx)
		w.reconciler.CollectGarbage(ctx)
		if w.Mode == "all" || w.Mode == "run" {
			w.dispatcher.DispatchOnce(ctx)
		}
		fmt.Println("Running in once mode. Waiting for active tasks to complete...")
		w.Wait()
		fmt.Println("All tasks completed. Exiting.")
		return nil
	}

	daemonCtx, daemonCancel := context.WithCancel(ctx)
	defer daemonCancel()

	// Each subcontroller runs in its own goroutine and drains its own workers
	// before returning, so closing doneChan means the daemon has fully quiesced.
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_ = w.reconciler.Run(daemonCtx)
	}()
	if w.Mode == "all" || w.Mode == "run" {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = w.dispatcher.Run(daemonCtx)
		}()
	}

	doneChan := make(chan struct{})
	go func() {
		defer close(doneChan)
		wg.Wait()
	}()

	w.checkRepo(ctx)

	for {
		fmt.Printf("Sleeping for %s...\n", checkRepoInterval)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-w.timeoutChan:
			fmt.Printf("\nWatch timeout of %s expired. Shutting down gracefully...\n", w.WatchTimeout)
			w.state.mu.Lock()
			w.state.shuttingDown = true
			w.state.mu.Unlock()

			// Stops new tasks being claimed. Tasks already running keep their
			// supervisor: the dispatcher drains them within its own grace period,
			// so this wait has to outlast that.
			daemonCancel()

			fmt.Println("Waiting for active tasks to complete...")
			select {
			case <-doneChan:
				fmt.Println("Active tasks settled. Exiting.")
			case <-time.After(dispatcher.DefaultShutdownGracePeriod + time.Minute):
				fmt.Println("Timed out waiting for active tasks to settle. Exiting; they are recovered on the next run.")
			}
			return nil
		case <-time.After(checkRepoInterval):
			w.checkRepo(ctx)
		}
	}
}

func (w *Watcher) init(ctx context.Context) error {
	cfg, err := config.LoadConfig()
	if err != nil {
		klog.Warningf("Failed to load factory config: %v", err)
	}
	w.cfg = cfg
	w.triggerLabel = "factory"
	if cfg != nil && cfg.TriggerLabel != "" {
		w.triggerLabel = cfg.TriggerLabel
	}

	ghClient, err := github.NewClient(ctx)
	if err != nil {
		return fmt.Errorf("creating github client: %w", err)
	}
	w.ghClient = ghClient

	kubeClient, err := clients.NewKubernetesClient()
	if err != nil {
		return fmt.Errorf("creating k8s client: %w", err)
	}
	w.kubeClient = kubeClient

	secret, err := kubeClient.Clientset.CoreV1().Secrets(w.Namespace).Get(ctx, w.SecretName, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("fetching %s secret in namespace %s: %w (make sure to run 'factory user onboard' first)", w.SecretName, w.Namespace, err)
	}
	w.githubLogin = string(secret.Data[constants.KeyGithubLogin])

	w.targetAssignee = w.Assignee
	if !w.AssigneeChanged {
		w.targetAssignee = w.githubLogin
	}

	var allBotUsers []string
	if cfg != nil {
		for _, rCfg := range cfg.Roles {
			for _, u := range rCfg.Users {
				if u != "" {
					allBotUsers = append(allBotUsers, u)
				}
			}
		}
	}
	if w.targetAssignee != "" {
		found := false
		for _, u := range allBotUsers {
			if strings.EqualFold(u, w.targetAssignee) {
				found = true
				break
			}
		}
		if !found {
			allBotUsers = append(allBotUsers, w.targetAssignee)
		}
	}
	w.allBotUsers = allBotUsers

	w.incomingDir = filepath.Join(w.QueueDir, "incoming")
	w.processingDir = filepath.Join(w.QueueDir, "processing")
	w.processedDir = filepath.Join(w.QueueDir, "processed")

	logDir := os.Getenv("FACTORY_LOGS")
	if logDir == "" {
		logDir = filepath.Join(w.QueueDir, "logs")
	}
	w.processingLogDir = filepath.Join(logDir, "processing")
	w.processedLogDir = filepath.Join(logDir, "processed")

	w.initComponents()

	if !w.DryRun {
		if err := os.MkdirAll(w.incomingDir, 0755); err != nil {
			return fmt.Errorf("failed to create incoming queue dir: %w", err)
		}
		if err := os.MkdirAll(w.processingDir, 0755); err != nil {
			return fmt.Errorf("failed to create processing queue dir: %w", err)
		}
		if err := os.MkdirAll(w.processedDir, 0755); err != nil {
			return fmt.Errorf("failed to create processed queue dir: %w", err)
		}
		if err := os.MkdirAll(w.processingLogDir, 0755); err != nil {
			return fmt.Errorf("failed to create processing log dir: %w", err)
		}
		if err := os.MkdirAll(w.processedLogDir, 0755); err != nil {
			return fmt.Errorf("failed to create processed log dir: %w", err)
		}
		go startQueueHTTPServer(ctx, w.queueMgr, ":13338")
	}

	fmt.Printf("Starting watch for repository %s/%s (mode: %s, queueDir: %s, poll interval: %s, assignee: '%s', labels: %v, dryRun: %v, watchTimeout: %s)...\n", w.Repo.Owner, w.Repo.Repo, w.Mode, w.QueueDir, w.PollInterval, w.targetAssignee, w.Labels, w.DryRun, w.WatchTimeout)

	if w.WatchTimeout > 0 {
		w.timeoutChan = time.After(w.WatchTimeout)
	}

	if err := w.queueMgr.LoadFromDisk(); err != nil {
		klog.Warningf("Failed to load queue tasks from disk: %v", err)
	}

	w.processedIssues, w.processedPRs = loadProcessedTasks(w.processedDir)

	// Recovery: Reconcile any leftover tasks in processingDir on startup, adopting
	// tasks that are still running inside their sandbox.
	w.dispatcher.Recover(ctx)

	return nil
}

// canQueueIssueTasks reports whether the watcher has enough state to safely
// decide which issues still need work.
//
// The open PR cache is the primary duplicate-suppression signal for issue
// scans. An empty cache is indistinguishable from "no open PR references this
// issue", so scanning with an unpopulated cache makes the watcher re-trigger
// fixes for issues that already have an open PR. This happens in practice when
// the process restarts into a GitHub rate limit window and every attempt to
// list open PRs fails. Fail closed and wait for a successful PR scan instead.
func (w *Watcher) canQueueIssueTasks(prCachePopulated bool) bool {
	if w.IssueMode == "disabled" {
		return false
	}
	if !prCachePopulated {
		klog.Warningf("Skipping issue task queueing: the open PR cache is not populated, so issues with an open fix PR cannot be identified. Waiting for a successful PR scan.")
		return false
	}
	return true
}

// checkRepo runs one scan cycle over the repository, queueing work for issues,
// pull requests and chores. Sandbox reconciliation and garbage collection are
// owned by the sandbox reconciler goroutine and deliberately absent here.
func (w *Watcher) checkRepo(ctx context.Context) {
	w.state.mu.Lock()
	if w.state.shuttingDown {
		w.state.mu.Unlock()
		return
	}
	w.state.mu.Unlock()

	if w.queueMgr.IsDrainMode() {
		runningCount, err := w.sandboxes.CountRunningTasks(ctx)
		if err != nil {
			klog.Errorf("Failed to count running sandbox tasks during drain: %v", err)
		}
		processingFiles, _ := os.ReadDir(w.processingDir)
		filesInProcessing := 0
		for _, f := range processingFiles {
			if !f.IsDir() && strings.HasPrefix(f.Name(), "task-") && strings.HasSuffix(f.Name(), ".yaml") {
				filesInProcessing++
			}
		}
		klog.Infof("[DO NOT PROCESS] Drain mode active. Active child sandboxes: %d, Tasks in processing: %d. Pausing new scanning and task execution.", runningCount, filesInProcessing)
		return
	}

	now := time.Now()

	// Determine what to run
	runIssueScan := false
	if w.Mode == "all" || w.Mode == "scan" || w.Mode == "scan-issue" {
		if w.state.lastIssueScan.IsZero() || now.Sub(w.state.lastIssueScan) >= 30*time.Second {
			runIssueScan = true
		}
	}

	runPRScan := false
	if w.Mode == "all" || w.Mode == "scan" || w.Mode == "scan-pr" {
		if w.state.lastPRScan.IsZero() || now.Sub(w.state.lastPRScan) >= 5*time.Minute {
			runPRScan = true
		}
	}

	refIssues := w.entityCache.GetReferencedIssuesMap()
	hasPRs := w.entityCache.HasOpenPRs()

	// Populate PR cache once on startup if needed by issue scan
	if !hasPRs && runIssueScan {
		klog.Infof("Populating open PRs cache for referenced issues...")
		prs, err := listAllOpenPRs(ctx, w.ghClient, w.Repo.Owner, w.Repo.Repo)
		if err == nil {
			w.entityCache.UpdateOpenPRs(prs)
			refIssues = w.entityCache.GetReferencedIssuesMap()
			hasPRs = true
		} else {
			klog.Errorf("Failed to populate open PRs cache: %v", err)
		}
	}

	// 1. Slow PR Scan Cycle
	if runPRScan {
		klog.Infof("Running slow PR scan cycle...")
		prs, err := listAllOpenPRs(ctx, w.ghClient, w.Repo.Owner, w.Repo.Repo)
		if err == nil {
			w.entityCache.UpdateOpenPRs(prs)
			refIssues = w.entityCache.GetReferencedIssuesMap()
			w.state.mu.Lock()
			w.state.lastPRScan = now
			w.state.mu.Unlock()
			hasPRs = true
		} else {
			klog.Errorf("Failed to list open PRs: %v", err)
		}

		// Scan issues labeled with triggerLabel (handling pagination)
		slowIssues, slowIssuesErr := w.scanSlowIssues(ctx)
		if slowIssuesErr != nil {
			klog.Errorf("Failed to list issues for label %s: %v", w.triggerLabel, slowIssuesErr)
		}

		// Process slow issues
		if w.canQueueIssueTasks(hasPRs) {
			w.queueIssueTasks(ctx, slowIssues, refIssues)
		}

		// Process Pull Requests (Scanner)
		prIssues, err := w.scanPRIssues(ctx)
		if err != nil {
			klog.Errorf("Failed to scan PR issues: %v", err)
		}

		w.processPRs(ctx, prIssues)

		// Scan chores
		if (w.Mode == "all" || w.Mode == "scan" || w.Mode == "scan-pr") && w.ChoresMode != "disabled" {
			w.scanChores(ctx)
		}

		// Publish the entities observed by this cycle so the sandbox reconciler
		// can garbage collect closed ones without re-querying GitHub. A failed
		// scan returns whatever it managed to page in, which would publish a
		// truncated set as though it were the whole picture.
		if slowIssuesErr == nil {
			w.publishOpenEntities(slowIssues)
		}
	}

	// 2. Fast Issue Scan Cycle
	if runIssueScan {
		klog.Infof("Running fast issue scan cycle...")
		issues, fastPRIssues, err := w.scanFastIssues(ctx)
		if err != nil {
			klog.Errorf("Failed to scan fast issues: %v", err)
		}

		if w.canQueueIssueTasks(hasPRs) {
			w.queueIssueTasks(ctx, issues, refIssues)
		}

		// Process PRs assigned to the bot in the fast cycle
		if len(fastPRIssues) > 0 {
			klog.Infof("Processing %d assigned PRs in fast cycle...", len(fastPRIssues))
			w.processPRs(ctx, fastPRIssues)
		}

		w.state.mu.Lock()
		w.state.lastIssueScan = now
		w.state.mu.Unlock()
	}
}

// publishOpenEntities records which issues are known to be open in the shared
// entity cache. The sandbox reconciler uses it to skip sandboxes belonging to
// live work instead of confirming every one of them against GitHub.
//
// Issues that have already been processed are treated as open: their sandbox
// may still hold a workspace whose result has not been pushed yet, and the
// reconciler confirms the state with GitHub before deleting anything anyway.
func (w *Watcher) publishOpenEntities(openIssues []*githubv39.Issue) {
	nums := make([]int, 0, len(openIssues)+len(w.processedIssues))
	for _, iss := range openIssues {
		if num := iss.GetNumber(); num > 0 {
			nums = append(nums, num)
		}
	}
	for num := range w.processedIssues {
		nums = append(nums, num)
	}
	w.entityCache.SetOpenIssueNumbers(nums)
}
