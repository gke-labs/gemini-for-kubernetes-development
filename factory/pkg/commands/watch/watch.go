package watch

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/clients"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/commands/common"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/config"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/constants"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/github"
	factorysandbox "github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/sandbox"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/klog/v2"
)

func (w *Watcher) Run(ctx context.Context) error {
	if err := w.init(ctx); err != nil {
		return err
	}

	w.checkRepo(ctx)

	if w.Once {
		fmt.Println("Running in once mode. Waiting for active tasks to complete...")
		w.wg.Wait()
		fmt.Println("All tasks completed. Exiting.")
		return nil
	}

	for {
		fmt.Printf("Sleeping for 10s...\n")
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-w.timeoutChan:
			fmt.Printf("\nWatch timeout of %s expired. Shutting down gracefully...\n", w.WatchTimeout)
			w.state.mu.Lock()
			w.state.shuttingDown = true
			w.state.mu.Unlock()

			fmt.Println("Waiting for active tasks to complete...")
			doneChan := make(chan struct{})
			go func() {
				w.wg.Wait()
				close(doneChan)
			}()
			select {
			case <-doneChan:
				fmt.Println("All tasks completed. Exiting.")
			case <-time.After(5 * time.Minute):
				fmt.Println("Timeout waiting for active tasks to complete. Exiting.")
			}
			return nil
		case <-time.After(10 * time.Second):
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
		go startQueueHTTPServer(ctx, w.QueueDir, ":13338")
	}

	fmt.Printf("Starting watch for repository %s/%s (mode: %s, queueDir: %s, poll interval: %s, assignee: '%s', labels: %v, dryRun: %v, watchTimeout: %s)...\n", w.Repo.Owner, w.Repo.Repo, w.Mode, w.QueueDir, w.PollInterval, w.targetAssignee, w.Labels, w.DryRun, w.WatchTimeout)

	if w.WatchTimeout > 0 {
		w.timeoutChan = time.After(w.WatchTimeout)
	}

	w.processedIssues, w.processedPRs = loadProcessedTasks(w.processedDir)

	// Recovery: Handle any leftover tasks in processingDir on startup
	w.recoverStuckTasks(ctx)

	w.state = &watchState{
		referencedIssues: make(map[int]bool),
	}

	return nil
}

func (w *Watcher) checkRepo(ctx context.Context) {
	w.state.mu.Lock()
	if w.state.shuttingDown {
		w.state.mu.Unlock()
		return
	}
	w.state.mu.Unlock()

	// Proactively delete any evicted sandbox pods in the namespace so the sandbox controller can recreate them or free resources.
	func() {
		podList, err := w.kubeClient.Clientset.CoreV1().Pods(w.Namespace).List(ctx, metav1.ListOptions{LabelSelector: "sandbox"})
		if err != nil {
			return
		}
		for i := range podList.Items {
			pod := &podList.Items[i]
			if pod.DeletionTimestamp == nil && pod.Status.Phase == corev1.PodFailed && strings.EqualFold(pod.Status.Reason, "Evicted") {
				klog.Infof("Found evicted sandbox pod %s in namespace %s. Deleting pod so controller can recreate or clean up.", pod.Name, w.Namespace)
				sbName := pod.Labels["sandbox"]
				if sbName == "" {
					sbName = pod.Labels["agents.x-k8s.io/sandbox"]
				}
				if sbName != "" {
					_ = factorysandbox.IncrementSandboxEvictionCount(ctx, w.kubeClient, w.Namespace, sbName)
				}
				_ = w.kubeClient.Clientset.CoreV1().Pods(w.Namespace).Delete(ctx, pod.Name, metav1.DeleteOptions{})
			}
		}
	}()

	w.reconcileRunningSandboxes(ctx)

	if isDoNotProcess(w.QueueDir) {
		runningCount, err := countRunningSandboxTasks(ctx, w.kubeClient, w.Namespace)
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

	runRunner := false
	if w.Mode == "all" || w.Mode == "run" {
		if w.state.lastRunnerRun.IsZero() || now.Sub(w.state.lastRunnerRun) >= 30*time.Second {
			runRunner = true
		}
	}

	w.state.mu.Lock()
	refIssues := make(map[int]bool)
	for k, v := range w.state.referencedIssues {
		refIssues[k] = v
	}
	hasPRs := len(w.state.openPRs) > 0 || !w.state.lastPRScan.IsZero()
	w.state.mu.Unlock()

	// Populate PR cache once on startup if needed by issue scan
	if !hasPRs && runIssueScan {
		klog.Infof("Populating open PRs cache for referenced issues...")
		prs, err := listAllOpenPRs(ctx, w.ghClient, w.Repo.Owner, w.Repo.Repo)
		if err == nil {
			w.state.mu.Lock()
			w.state.openPRs = prs
			w.state.referencedIssues = make(map[int]bool)
			for _, pr := range prs {
				for num := range common.GetReferencedIssues(pr) {
					w.state.referencedIssues[num] = true
					refIssues[num] = true
				}
			}
			w.state.mu.Unlock()
		} else {
			klog.Errorf("Failed to populate open PRs cache: %v", err)
		}
	}

	// 1. Slow PR Scan Cycle
	if runPRScan {
		klog.Infof("Running slow PR scan cycle...")
		prs, err := listAllOpenPRs(ctx, w.ghClient, w.Repo.Owner, w.Repo.Repo)
		if err == nil {
			w.state.mu.Lock()
			w.state.openPRs = prs
			w.state.referencedIssues = make(map[int]bool)
			for _, pr := range prs {
				for num := range common.GetReferencedIssues(pr) {
					w.state.referencedIssues[num] = true
					refIssues[num] = true
				}
			}
			w.state.lastPRScan = now
			w.state.mu.Unlock()
		} else {
			klog.Errorf("Failed to list open PRs: %v", err)
		}

		// Scan issues labeled with triggerLabel (handling pagination)
		slowIssues, err := w.scanSlowIssues(ctx)
		if err != nil {
			klog.Errorf("Failed to list issues for label %s: %v", w.triggerLabel, err)
		}

		// Process slow issues
		if w.IssueMode != "disabled" {
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

		openPRMap := make(map[int]bool)
		for _, pr := range prIssues {
			openPRMap[pr.GetNumber()] = true
		}

		openIssueMap := make(map[int]bool)
		for _, iss := range slowIssues {
			openIssueMap[iss.GetNumber()] = true
		}
		for issNum := range w.processedIssues {
			openIssueMap[issNum] = true
		}

		// Clean up sandboxes of merged or closed PRs
		if err := w.cleanupClosedPRSandboxes(ctx, openPRMap); err != nil {
			klog.Errorf("Failed to clean up closed PR sandboxes: %v", err)
		}

		// Clean up sandboxes of closed issues
		if err := w.cleanupClosedIssueSandboxes(ctx, openIssueMap); err != nil {
			klog.Errorf("Failed to clean up closed issue sandboxes: %v", err)
		}

		// Clean up stale idle sandboxes older than eviction age (defaults to 1 week)
		if err := w.cleanupStaleIdleSandboxes(ctx); err != nil {
			klog.Errorf("Failed to clean up stale idle sandboxes: %v", err)
		}

		if w.SandboxIdleTimeout > 0 {
			if _, err := factorysandbox.SuspendIdleSandboxes(ctx, w.kubeClient, w.Namespace, w.SandboxIdleTimeout, w.DryRun); err != nil {
				klog.Errorf("Failed to suspend idle sandboxes: %v", err)
			}
		}
	}

	// 2. Fast Issue Scan Cycle
	if runIssueScan {
		klog.Infof("Running fast issue scan cycle...")
		issues, fastPRIssues, err := w.scanFastIssues(ctx)
		if err != nil {
			klog.Errorf("Failed to scan fast issues: %v", err)
		}

		if w.IssueMode != "disabled" {
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

	// 3. Runner Mode execution
	if runRunner {
		w.runTasks(ctx)
		w.state.mu.Lock()
		w.state.lastRunnerRun = now
		w.state.mu.Unlock()
	}
}
