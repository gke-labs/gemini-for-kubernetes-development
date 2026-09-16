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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/klog/v2"
)

func (w *Watcher) Run(ctx context.Context) error {
	if err := w.init(ctx); err != nil {
		return err
	}

	if w.Once {
		w.reconciler.ReconcileOnce(ctx)
		if w.issuesEnabled() {
			w.issueScanner.ScanOnce(ctx)
		}
		if w.prsEnabled() {
			w.prScanner.ScanOnce(ctx)
		}
		if w.choresEnabled() {
			w.chores.ScheduleOnce(ctx)
		}
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
	if w.issuesEnabled() {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = w.issueScanner.Run(daemonCtx)
		}()
	}
	if w.prsEnabled() {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = w.prScanner.Run(daemonCtx)
		}()
	}
	if w.choresEnabled() {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = w.chores.Run(daemonCtx)
		}()
	}
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

	// Every cycle now belongs to a subcontroller, so this goroutine only
	// supervises them: it waits for a reason to stop, and then stops them.
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-w.timeoutChan:
		fmt.Printf("\nWatch timeout of %s expired. Shutting down gracefully...\n", w.WatchTimeout)

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

	// Recovery: Reconcile any leftover tasks in processingDir on startup, adopting
	// tasks that are still running inside their sandbox.
	w.dispatcher.Recover(ctx)

	return nil
}

// issuesEnabled reports whether the issue scanner runs in this watcher's mode.
//
// It decides whether the goroutine starts at all, which is where mode gating
// belongs now that scanning is not a branch of a shared cycle: a mode that does
// not scan issues should not pay for a scanner that wakes up every interval to
// discover it has nothing to do.
func (w *Watcher) issuesEnabled() bool {
	if w.IssueMode == "disabled" {
		return false
	}
	return w.Mode == "all" || w.Mode == "scan" || w.Mode == "scan-issue"
}

// prsEnabled reports whether the pull request scanner runs in this watcher's mode.
//
// This is the same gate the old scan cycle applied to its PR branch, moved to
// where the goroutine is started: a mode that does not scan pull requests
// should not pay for the scanner at all.
func (w *Watcher) prsEnabled() bool {
	if w.PRMode == "disabled" {
		return false
	}
	return w.Mode == "all" || w.Mode == "scan" || w.Mode == "scan-pr"
}

// choresEnabled reports whether scheduled chores run in this watcher's mode.
//
// The set of modes is inherited from when chore scanning lived inside the slow
// pull request cycle, "scan-pr" included: chores have nothing to do with pull
// requests, but a deployment running that mode is one that has been scheduling
// them all along, and moving the scheduler out of that cycle is not the change
// that should turn them off.
func (w *Watcher) choresEnabled() bool {
	if w.ChoresMode == "disabled" {
		return false
	}
	return w.Mode == "all" || w.Mode == "scan" || w.Mode == "scan-pr"
}
