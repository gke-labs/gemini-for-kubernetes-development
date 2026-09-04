package watch

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/commands/watch/api"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/k8s"
	"k8s.io/klog/v2"
)

func (w *Watcher) buildTaskCommandArgs(t *api.QueueTask, selectedUser string) []string {
	var args []string
	switch t.Type {
	case api.TypeIssueFix:
		args = []string{"fix", "--url", t.URL, "--instruction", "Fix this issue"}
	case api.TypePRInvestigate:
		args = []string{"pr", "investigate", "--pr-url", t.URL}
	case api.TypePRComments:
		args = []string{"pr", "address-comments", "--pr-url", t.URL}
	case api.TypePRIterate:
		args = []string{"pr", "iterate", "--pr-url", t.URL, "--prompt", "Please resolve merge conflicts in this PR by rebasing onto the latest master/main branch and resolving any conflicts that arise."}
	case api.TypePRReview:
		args = []string{"pr", "review", "--pr-url", t.URL, "--publish", "yes"}
		for _, inst := range t.Instructions {
			args = append(args, "--instruction", inst)
		}
	case api.TypeAgentChore:
		args = []string{"agent", "create", "--url", t.URL, "--agent", t.AgentFile}
		if t.SessionID != "" {
			args = append(args, "--session-id", t.SessionID)
		}
	default:
		return nil
	}

	if w.Namespace != "" {
		args = append(args, "--namespace", w.Namespace)
	}
	if selectedUser != "" {
		args = append(args, "--user", selectedUser)
	}
	if w.Image != "" {
		args = append(args, "--image", w.Image)
	}
	if w.DiskSize != "" {
		args = append(args, "--workspace-disk-size", w.DiskSize)
	}
	if w.EphemeralStorage != "" {
		args = append(args, "--ephemeral-storage", w.EphemeralStorage)
	}
	if w.CPURequest != "" {
		args = append(args, "--cpu-request", w.CPURequest)
	}
	if w.CPULimit != "" {
		args = append(args, "--cpu-limit", w.CPULimit)
	}
	if w.MemoryRequest != "" {
		args = append(args, "--memory-request", w.MemoryRequest)
	}
	if w.MemoryLimit != "" {
		args = append(args, "--memory-limit", w.MemoryLimit)
	}
	if w.TaskTimeout > 0 {
		args = append(args, "--timeout", w.TaskTimeout.String())
	}
	args = append(args, "--abort-on-cancel=false")

	return args
}

func (w *Watcher) runSingleTask(ctx context.Context, taskFilename string, t *api.QueueTask) {
	fmt.Printf("Starting task %s (Type: %s, URL: %s)...\n", taskFilename, t.Type, t.URL)
	if t.StartedAt.IsZero() {
		t.StartedAt = time.Now()
	}

	taskCtx, taskCancel := context.WithTimeout(ctx, w.TaskTimeout)
	defer taskCancel()

	if t.Number > 0 && w.ghClient != nil {
		if (t.Type == api.TypeIssueFix || t.Type == api.TypeAgentChore) && t.Assignee != "" {
			klog.Infof("Assigning issue #%d to %s as claimed", t.Number, t.Assignee)
			if _, _, err := w.ghClient.Issues.AddAssignees(ctx, w.Repo.Owner, w.Repo.Repo, t.Number, []string{t.Assignee}); err != nil {
				klog.Errorf("Failed to assign issue #%d to %s: %v", t.Number, t.Assignee, err)
			}
			if t.Assignee != w.targetAssignee {
				if _, _, err := w.ghClient.Issues.RemoveAssignees(ctx, w.Repo.Owner, w.Repo.Repo, t.Number, []string{w.targetAssignee}); err != nil {
					klog.Errorf("Failed to remove watcher bot %s from issue #%d: %v", w.targetAssignee, t.Number, err)
				}
			}
		}

		if t.Type != api.TypeAgentChore {
			var commentBody string
			switch t.Type {
			case api.TypeIssueFix:
				commentBody = "🤖 AI Factory started fixing this issue in a sandbox."
			case api.TypePRInvestigate:
				commentBody = "🤖 AI Factory started investigating CI check failures for this pull request."
			case api.TypePRComments:
				commentBody = "🤖 AI Factory started addressing review feedback for this pull request."
			case api.TypePRIterate:
				commentBody = "🤖 AI Factory started resolving merge conflicts / rebasing this pull request in a sandbox."
			case api.TypePRReview:
				commentBody = "🤖 AI Factory started reviewing this pull request in a sandbox."
			}
			if commentBody != "" {
				addGitHubComment(ctx, w.ghClient, w.Repo.Owner, w.Repo.Repo, t.Number, commentBody)
			}
		}
	}

	selectedUser := t.Assignee
	var sUserErr error
	if selectedUser == "" || (api.IsPRTask(t.Type) && strings.EqualFold(selectedUser, w.targetAssignee)) {
		selectedUser, sUserErr = w.selectUserForTask(ctx, t.Type, t.Number)
	}
	if sUserErr != nil {
		klog.Errorf("Failed to select user for task %s: %v", taskFilename, sUserErr)
		_ = w.queueMgr.FailTask(taskFilename, t, sUserErr.Error())
		return
	}

	executable, err := os.Executable()
	if err != nil {
		klog.Errorf("Failed to get executable path: %v", err)
		return
	}

	args := w.buildTaskCommandArgs(t, selectedUser)
	if args == nil {
		klog.Errorf("Unknown task type: %s", t.Type)
		return
	}

	cmd := exec.CommandContext(taskCtx, executable, args...)

	logFilename := strings.TrimSuffix(taskFilename, ".yaml") + ".log"
	processingLogPath := filepath.Join(w.processingLogDir, logFilename)

	logFile, err := os.OpenFile(processingLogPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
	if err != nil {
		klog.Errorf("Failed to create log file: %v", err)
	} else {
		cmd.Stdout = logFile
		cmd.Stderr = logFile
		defer logFile.Close()
	}

	taskErr := cmd.Run()

	if taskErr != nil {
		klog.Errorf("Task %s failed: %v", taskFilename, taskErr)
		_ = w.queueMgr.FailTask(taskFilename, t, taskErr.Error())
		if t.Type == api.TypePRComments && w.cfg != nil {
			resolvePRCommentReactions(ctx, w.ghClient, w.Repo.Owner, w.Repo.Repo, t.Number, "confused", w.cfg.AllowlistedBots, w.githubLogin)
		}

		// Force clean up sandbox if the task timed out
		if taskCtx.Err() == context.DeadlineExceeded {
			var sandboxName string
			switch t.Type {
			case api.TypeIssueFix:
				if t.SessionID != "" {
					sandboxName = fmt.Sprintf("wf-issue-%d", t.Number)
				} else {
					sandboxName = fmt.Sprintf("fix-%s-%d", w.Repo.Repo, t.Number)
				}
			case api.TypeAgentChore:
				if t.SessionID != "" {
					sandboxName = fmt.Sprintf("wf-issue-%d", t.Number)
				} else {
					sandboxName = fmt.Sprintf("agent-%s-%d", w.Repo.Repo, t.Number)
				}
			case api.TypePRInvestigate, api.TypePRComments, api.TypePRIterate, api.TypePRReview:
				sandboxName = w.resolveSandboxName(ctx, t.Type, t.Number)
			}

			if sandboxName != "" && w.kubeClient != nil {
				klog.Warningf("Task %s timed out after %s! Force cleaning up sandbox '%s'...", taskFilename, w.TaskTimeout, sandboxName)
				manager := k8s.NewManager(w.kubeClient)
				if err := manager.DeleteSandbox(ctx, w.Namespace, sandboxName); err != nil {
					klog.Errorf("Failed to delete sandbox '%s' on timeout: %v", sandboxName, err)
				}
			}
		}
	} else {
		fmt.Printf("Task %s completed successfully.\n", taskFilename)
		_ = w.queueMgr.CompleteTask(taskFilename, t)
		if t.Type == api.TypePRComments && w.cfg != nil {
			resolvePRCommentReactions(ctx, w.ghClient, w.Repo.Owner, w.Repo.Repo, t.Number, "+1", w.cfg.AllowlistedBots, w.githubLogin)
		}
	}
}

func (w *Watcher) runTasks(ctx context.Context) {
	// sync from disk in case task files were changed out of band (e.g. admin editing files on disk)
	_ = w.queueMgr.SyncIncomingFromDisk()

	var releasedTasks []string
	defer func() {
		for _, fn := range releasedTasks {
			_ = w.queueMgr.ReleaseTask(fn)
		}
	}()

	actionsTaken := 0
	activeSandboxesInCycle := make(map[string]bool)

	for {
		if isDoNotProcess(w.QueueDir) {
			klog.Infof("[DO NOT PROCESS] Drain mode detected during cycle execution. Stopping scheduling of remaining queued tasks.")
			break
		}
		if actionsTaken >= w.MaxActions {
			fmt.Printf("Reached maximum actions limit (%d) for this cycle. Stopping execution.\n", w.MaxActions)
			break
		}

		runningCount, err := countRunningSandboxTasks(ctx, w.kubeClient, w.Namespace)
		if err != nil {
			klog.Errorf("Failed to count running sandbox tasks: %v", err)
		}
		_, filesInProcessing, _ := w.queueMgr.GetCounts()
		activeCount := max(runningCount, filesInProcessing)

		if activeCount >= w.MaxPending {
			fmt.Printf("Reached maximum pending sandboxes limit (%d). Skipping remaining queue items.\n", w.MaxPending)
			break
		}

		// Pop next fair-share candidate
		filename, task, err := w.queueMgr.ClaimNextCandidate()
		if err != nil {
			klog.Errorf("Failed to claim next candidate task: %v", err)
			break
		}
		if task == nil {
			// No more eligible tasks ready in incoming queue
			break
		}

		sandboxName := w.resolveSandboxName(ctx, task.Type, task.Number)
		if activeSandboxesInCycle[sandboxName] {
			klog.Infof("Skipping task %s because sandbox %s is already scheduled to run a task in this cycle.", filename, sandboxName)
			releasedTasks = append(releasedTasks, filename)
			continue
		}

		// Check if target sandbox pod is currently running in Kubernetes
		running, err := isSandboxTaskRunning(ctx, w.kubeClient, w.Namespace, sandboxName)
		if err != nil {
			klog.Errorf("Failed to check if sandbox %s is running: %v", sandboxName, err)
			releasedTasks = append(releasedTasks, filename)
			continue
		}
		if running {
			klog.Infof("Skipping task %s because sandbox %s is currently busy running another task.", filename, sandboxName)
			releasedTasks = append(releasedTasks, filename)
			continue
		}

		// Validate GitHub status (stop label or closed)
		if task.Number > 0 && !w.DryRun && w.ghClient != nil {
			if issueOrPR, _, err := w.ghClient.Issues.Get(ctx, w.Repo.Owner, w.Repo.Repo, task.Number); err == nil && issueOrPR != nil {
				if hasStopLabel(issueOrPR.Labels, w.triggerLabel) {
					klog.Infof("Skipping task %s and removing from incoming because target #%d has the stop label ('overseer/stop' or '%s/stop')", filename, task.Number, w.triggerLabel)
					_ = w.queueMgr.RemoveTask(filename)
					continue
				}
				if issueOrPR.GetState() == "closed" {
					klog.Infof("Skipping task %s and removing from incoming because target #%d is closed", filename, task.Number)
					_ = w.queueMgr.RemoveTask(filename)
					continue
				}
			}
		}

		// Check if recovered task is already completed in sandbox
		if task.Type != api.TypeAgentChore && task.Recovered {
			completed, err := isSandboxTaskCompleted(ctx, w.kubeClient, w.Namespace, sandboxName, task.Type)
			if err != nil {
				klog.Errorf("Failed to check if sandbox %s completed task: %v", sandboxName, err)
				releasedTasks = append(releasedTasks, filename)
				continue
			}
			if completed {
				klog.Infof("Recovered task %s is already completed in sandbox %s. Marking as completed.", filename, sandboxName)
				if !w.DryRun {
					_ = w.queueMgr.CompleteTask(filename, task)
				} else {
					releasedTasks = append(releasedTasks, filename)
				}
				continue
			}
		}

		if w.DryRun {
			fmt.Printf("[DRYRUN] Would process task %s (Type: %s, URL: %s)\n", filename, task.Type, task.URL)
			activeSandboxesInCycle[sandboxName] = true
			actionsTaken++
			releasedTasks = append(releasedTasks, filename)
			continue
		}

		activeSandboxesInCycle[sandboxName] = true
		actionsTaken++

		if err := w.queueMgr.StartTask(filename, task); err != nil {
			klog.Errorf("Failed to start task %s: %v", filename, err)
			releasedTasks = append(releasedTasks, filename)
			continue
		}

		w.wg.Add(1)
		go func(taskFilename string, t *api.QueueTask) {
			defer w.wg.Done()
			w.runSingleTask(ctx, taskFilename, t)
		}(filename, task)
	}
}
