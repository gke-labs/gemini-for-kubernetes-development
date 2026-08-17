package watch

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/clients"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/commands/common"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/config"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/constants"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/github"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/k8s"
	factorysandbox "github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/sandbox"
	"gopkg.in/yaml.v3"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/klog/v2"
)

func (w *Watcher) Run(ctx context.Context) error {
	cfg, err := config.LoadConfig()
	if err != nil {
		klog.Warningf("Failed to load factory config: %v", err)
	}
	triggerLabel := "factory"
	if cfg != nil && cfg.TriggerLabel != "" {
		triggerLabel = cfg.TriggerLabel
	}

	ghClient, err := github.NewClient(ctx)
	if err != nil {
		return fmt.Errorf("creating github client: %w", err)
	}

	kubeClient, err := clients.NewKubernetesClient()
	if err != nil {
		return fmt.Errorf("creating k8s client: %w", err)
	}

	secret, err := kubeClient.Clientset.CoreV1().Secrets(w.Namespace).Get(ctx, w.SecretName, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("fetching %s secret in namespace %s: %w (make sure to run 'factory user onboard' first)", w.SecretName, w.Namespace, err)
	}
	githubLogin := string(secret.Data[constants.KeyGithubLogin])

	targetAssignee := w.Assignee
	if !w.AssigneeChanged {
		targetAssignee = githubLogin
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
	if targetAssignee != "" {
		found := false
		for _, u := range allBotUsers {
			if strings.EqualFold(u, targetAssignee) {
				found = true
				break
			}
		}
		if !found {
			allBotUsers = append(allBotUsers, targetAssignee)
		}
	}

	incomingDir := filepath.Join(w.QueueDir, "incoming")
	processingDir := filepath.Join(w.QueueDir, "processing")
	processedDir := filepath.Join(w.QueueDir, "processed")

	logDir := os.Getenv("FACTORY_LOGS")
	if logDir == "" {
		logDir = filepath.Join(w.QueueDir, "logs")
	}
	processingLogDir := filepath.Join(logDir, "processing")
	processedLogDir := filepath.Join(logDir, "processed")

	if !w.DryRun {
		if err := os.MkdirAll(incomingDir, 0755); err != nil {
			return fmt.Errorf("failed to create incoming queue dir: %w", err)
		}
		if err := os.MkdirAll(processingDir, 0755); err != nil {
			return fmt.Errorf("failed to create processing queue dir: %w", err)
		}
		if err := os.MkdirAll(processedDir, 0755); err != nil {
			return fmt.Errorf("failed to create processed queue dir: %w", err)
		}
		if err := os.MkdirAll(processingLogDir, 0755); err != nil {
			return fmt.Errorf("failed to create processing log dir: %w", err)
		}
		if err := os.MkdirAll(processedLogDir, 0755); err != nil {
			return fmt.Errorf("failed to create processed log dir: %w", err)
		}
		go startQueueHTTPServer(ctx, w.QueueDir, ":13338")
	}

	fmt.Printf("Starting watch for repository %s/%s (mode: %s, queueDir: %s, poll interval: %s, assignee: '%s', labels: %v, dryRun: %v, watchTimeout: %s)...\n", w.Repo.Owner, w.Repo.Repo, w.Mode, w.QueueDir, w.PollInterval, targetAssignee, w.Labels, w.DryRun, w.WatchTimeout)

	var timeoutChan <-chan time.Time
	if w.WatchTimeout > 0 {
		timeoutChan = time.After(w.WatchTimeout)
	}

	processedIssues, processedPRs := loadProcessedTasks(processedDir)

	// Recovery: Handle any leftover tasks in processingDir on startup
	w.recoverStuckTasks(ctx, kubeClient, ghClient, incomingDir, processingDir, processedDir)

	state := &watchState{
		referencedIssues: make(map[int]bool),
	}

	var wg sync.WaitGroup

	checkRepo := func() {
		state.mu.Lock()
		if state.shuttingDown {
			state.mu.Unlock()
			return
		}
		state.mu.Unlock()

		// Proactively delete any evicted sandbox pods in the namespace so the sandbox controller can recreate them or free resources.
		func() {
			podList, err := kubeClient.Clientset.CoreV1().Pods(w.Namespace).List(ctx, metav1.ListOptions{LabelSelector: "sandbox"})
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
						_ = factorysandbox.IncrementSandboxEvictionCount(ctx, kubeClient, w.Namespace, sbName)
					}
					_ = kubeClient.Clientset.CoreV1().Pods(w.Namespace).Delete(ctx, pod.Name, metav1.DeleteOptions{})
				}
			}
		}()

		reconcileRunningSandboxes(ctx, kubeClient, w.Namespace)

		if isDoNotProcess(w.QueueDir) {
			runningCount, err := countRunningSandboxTasks(ctx, kubeClient, w.Namespace)
			if err != nil {
				klog.Errorf("Failed to count running sandbox tasks during drain: %v", err)
			}
			processingFiles, _ := os.ReadDir(processingDir)
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
		actionsTaken := 0

		// Determine what to run
		runIssueScan := false
		if w.Mode == "all" || w.Mode == "scan" || w.Mode == "scan-issue" {
			if state.lastIssueScan.IsZero() || now.Sub(state.lastIssueScan) >= 30*time.Second {
				runIssueScan = true
			}
		}

		runPRScan := false
		if w.Mode == "all" || w.Mode == "scan" || w.Mode == "scan-pr" {
			if state.lastPRScan.IsZero() || now.Sub(state.lastPRScan) >= 5*time.Minute {
				runPRScan = true
			}
		}

		runRunner := false
		if w.Mode == "all" || w.Mode == "run" {
			if state.lastRunnerRun.IsZero() || now.Sub(state.lastRunnerRun) >= 30*time.Second {
				runRunner = true
			}
		}

		state.mu.Lock()
		refIssues := make(map[int]bool)
		for k, v := range state.referencedIssues {
			refIssues[k] = v
		}
		hasPRs := len(state.openPRs) > 0 || !state.lastPRScan.IsZero()
		state.mu.Unlock()

		// Populate PR cache once on startup if needed by issue scan
		if !hasPRs && runIssueScan {
			klog.Infof("Populating open PRs cache for referenced issues...")
			prs, err := listAllOpenPRs(ctx, ghClient, w.Repo.Owner, w.Repo.Repo)
			if err == nil {
				state.mu.Lock()
				state.openPRs = prs
				state.referencedIssues = make(map[int]bool)
				for _, pr := range prs {
					for num := range common.GetReferencedIssues(pr) {
						state.referencedIssues[num] = true
						refIssues[num] = true
					}
				}
				state.mu.Unlock()
			} else {
				klog.Errorf("Failed to populate open PRs cache: %v", err)
			}
		}

		// 1. Slow PR Scan Cycle
		if runPRScan {
			klog.Infof("Running slow PR scan cycle...")
			prs, err := listAllOpenPRs(ctx, ghClient, w.Repo.Owner, w.Repo.Repo)
			if err == nil {
				state.mu.Lock()
				state.openPRs = prs
				state.referencedIssues = make(map[int]bool)
				for _, pr := range prs {
					for num := range common.GetReferencedIssues(pr) {
						state.referencedIssues[num] = true
						refIssues[num] = true
					}
				}
				state.lastPRScan = now
				state.mu.Unlock()
			} else {
				klog.Errorf("Failed to list open PRs: %v", err)
			}

			// Scan issues labeled with triggerLabel (handling pagination)
			slowIssues, err := w.scanSlowIssues(ctx, ghClient, triggerLabel)
			if err != nil {
				klog.Errorf("Failed to list issues for label %s: %v", triggerLabel, err)
			}

			// Process slow issues
			if w.IssueMode != "disabled" {
				w.queueIssueTasks(ctx, ghClient, kubeClient, cfg, slowIssues, processedIssues, refIssues, targetAssignee, allBotUsers, incomingDir, processingDir, processedDir, triggerLabel)
			}

			// Process Pull Requests (Scanner)
			prIssues, err := w.scanPRIssues(ctx, ghClient, allBotUsers, triggerLabel)
			if err != nil {
				klog.Errorf("Failed to scan PR issues: %v", err)
			}

			w.processPRs(ctx, ghClient, kubeClient, cfg, prIssues, processedPRs, allBotUsers, githubLogin, incomingDir, processingDir, processedDir, triggerLabel)

			// Scan chores
			if (w.Mode == "all" || w.Mode == "scan" || w.Mode == "scan-pr") && w.ChoresMode != "disabled" {
				scanChores(ctx, ghClient, w.Repo.Owner, w.Repo.Repo, incomingDir, processingDir, w.QueueDir, w.DryRun)
			}

			openPRMap := make(map[int]bool)
			for _, pr := range prIssues {
				openPRMap[pr.GetNumber()] = true
			}

			openIssueMap := make(map[int]bool)
			for _, iss := range slowIssues {
				openIssueMap[iss.GetNumber()] = true
			}
			for issNum := range processedIssues {
				openIssueMap[issNum] = true
			}

			// Clean up sandboxes of merged or closed PRs
			if err := cleanupClosedPRSandboxes(ctx, ghClient, kubeClient, w.Repo.Owner, w.Repo.Repo, w.Namespace, openPRMap, w.DryRun); err != nil {
				klog.Errorf("Failed to clean up closed PR sandboxes: %v", err)
			}

			// Clean up sandboxes of closed issues
			if err := cleanupClosedIssueSandboxes(ctx, ghClient, kubeClient, w.Repo.Owner, w.Repo.Repo, w.Namespace, openIssueMap, w.DryRun); err != nil {
				klog.Errorf("Failed to clean up closed issue sandboxes: %v", err)
			}

			// Clean up stale idle sandboxes older than eviction age (defaults to 1 week)
			if err := cleanupStaleIdleSandboxes(ctx, kubeClient, w.Repo.Owner+"/"+w.Repo.Repo, w.Namespace, w.SandboxEvictionAge, w.DryRun); err != nil {
				klog.Errorf("Failed to clean up stale idle sandboxes: %v", err)
			}

			if w.SandboxIdleTimeout > 0 {
				if _, err := factorysandbox.SuspendIdleSandboxes(ctx, kubeClient, w.Namespace, w.SandboxIdleTimeout, w.DryRun); err != nil {
					klog.Errorf("Failed to suspend idle sandboxes: %v", err)
				}
			}
		}

		// 2. Fast Issue Scan Cycle
		if runIssueScan {
			klog.Infof("Running fast issue scan cycle...")
			issues, fastPRIssues, err := w.scanFastIssues(ctx, ghClient, allBotUsers, githubLogin, triggerLabel, targetAssignee)
			if err != nil {
				klog.Errorf("Failed to scan fast issues: %v", err)
			}

			if w.IssueMode != "disabled" {
				w.queueIssueTasks(ctx, ghClient, kubeClient, cfg, issues, processedIssues, refIssues, targetAssignee, allBotUsers, incomingDir, processingDir, processedDir, triggerLabel)
			}

			// Process PRs assigned to the bot in the fast cycle
			if len(fastPRIssues) > 0 {
				klog.Infof("Processing %d assigned PRs in fast cycle...", len(fastPRIssues))
				w.processPRs(ctx, ghClient, kubeClient, cfg, fastPRIssues, processedPRs, allBotUsers, githubLogin, incomingDir, processingDir, processedDir, triggerLabel)
			}

			state.mu.Lock()
			state.lastIssueScan = now
			state.mu.Unlock()
		}

		// 3. Runner Mode execution
		if runRunner {
			incomingFiles, err := os.ReadDir(incomingDir)
			if err != nil {
				if !os.IsNotExist(err) {
					klog.Errorf("Failed to read incoming queue directory: %v", err)
				}
				return
			}

			var taskItems []taskItem

			for _, f := range incomingFiles {
				if f.IsDir() || !strings.HasPrefix(f.Name(), "task-") || !strings.HasSuffix(f.Name(), ".yaml") {
					continue
				}

				filename := f.Name()
				filePath := filepath.Join(incomingDir, filename)
				data, err := os.ReadFile(filePath)
				if err != nil {
					klog.Errorf("Failed to read task file %s: %v", filename, err)
					continue
				}

				var t QueueTask
				if err := yaml.Unmarshal(data, &t); err != nil {
					klog.Errorf("Failed to unmarshal task file %s: %v", filename, err)
					continue
				}

				if t.EnqueuedAt.IsZero() {
					info, err := f.Info()
					var modTime time.Time
					if err == nil {
						modTime = info.ModTime()
					}
					t.EnqueuedAt = getEnqueueTime(&t, modTime)
				}

				taskItems = append(taskItems, taskItem{
					filename: filename,
					task:     &t,
				})
			}

			tasksToRun := sortTasksFairly(taskItems)

			processingFiles, _ := os.ReadDir(processingDir)
			filesInProcessing := 0
			for _, f := range processingFiles {
				if !f.IsDir() && strings.HasPrefix(f.Name(), "task-") && strings.HasSuffix(f.Name(), ".yaml") {
					filesInProcessing++
				}
			}

			activeSandboxesInCycle := make(map[string]bool)

			for _, item := range tasksToRun {
				if isDoNotProcess(w.QueueDir) {
					klog.Infof("[DO NOT PROCESS] Drain mode detected during cycle execution. Stopping scheduling of remaining queued tasks.")
					break
				}
				if actionsTaken >= w.MaxActions {
					fmt.Printf("Reached maximum actions limit (%d) for this cycle. Stopping execution.\n", w.MaxActions)
					break
				}

				runningCount, err := countRunningSandboxTasks(ctx, kubeClient, w.Namespace)
				if err != nil {
					klog.Errorf("Failed to count running sandbox tasks: %v", err)
				}
				activeCount := max(runningCount, filesInProcessing)

				if activeCount >= w.MaxPending {
					fmt.Printf("Reached maximum pending sandboxes limit (%d). Skipping remaining queue items.\n", w.MaxPending)
					break
				}

				filename := item.filename
				task := item.task

				sandboxName := w.resolveSandboxName(ctx, kubeClient, ghClient, task.Type, task.Number)
				if activeSandboxesInCycle[sandboxName] {
					klog.Infof("Skipping task %s because sandbox %s is already scheduled to run a task in this cycle.", filename, sandboxName)
					continue
				}

				running, err := isSandboxTaskRunning(ctx, kubeClient, w.Namespace, sandboxName)
				if err != nil {
					klog.Errorf("Failed to check if sandbox %s is running: %v", sandboxName, err)
					continue
				}
				if running {
					klog.Infof("Skipping task %s because sandbox %s is currently busy running another task.", filename, sandboxName)
					continue
				}

				if task.Number > 0 && !w.DryRun {
					if issueOrPR, _, err := ghClient.Issues.Get(ctx, w.Repo.Owner, w.Repo.Repo, task.Number); err == nil && issueOrPR != nil {
						if hasStopLabel(issueOrPR.Labels, triggerLabel) {
							klog.Infof("Skipping task %s and removing from incoming because target #%d has the stop label ('overseer/stop' or '%s/stop')", filename, task.Number, triggerLabel)
							_ = os.Remove(filepath.Join(incomingDir, filename))
							continue
						}
						if issueOrPR.GetState() == "closed" {
							klog.Infof("Skipping task %s and removing from incoming because target #%d is closed", filename, task.Number)
							_ = os.Remove(filepath.Join(incomingDir, filename))
							continue
						}
					}
				}

				if task.Type != "agent-chore" && task.Recovered {
					completed, err := isSandboxTaskCompleted(ctx, kubeClient, w.Namespace, sandboxName, task.Type)
					if err != nil {
						klog.Errorf("Failed to check if sandbox %s completed task: %v", sandboxName, err)
						continue
					}
					if completed {
						klog.Infof("Recovered task %s is already completed in sandbox %s. Marking as completed.", filename, sandboxName)
						if w.DryRun {
							continue
						}
						incomingPath := filepath.Join(incomingDir, filename)
						processedPath := filepath.Join(processedDir, filename)
						task.Status = "Completed"
						task.CompletedAt = time.Now()
						_ = writeTaskAtomically(incomingDir, filename, task)
						writeTaskJournalEvent(w.QueueDir, filename, task, "Completed", 0)
						if err := os.Rename(incomingPath, processedPath); err != nil {
							klog.Errorf("Failed to move completed task %s to processed: %v", filename, err)
						}
						continue
					}
				}

				incomingPath := filepath.Join(incomingDir, filename)
				processingPath := filepath.Join(processingDir, filename)

				if w.DryRun {
					fmt.Printf("[DRYRUN] Would process task %s (Type: %s, URL: %s)\n", filename, task.Type, task.URL)
					activeSandboxesInCycle[sandboxName] = true
					actionsTaken++
					filesInProcessing++
					continue
				}

				if err := os.Rename(incomingPath, processingPath); err != nil {
					klog.Warningf("Failed to move task %s to processing (might be processed by another run): %v", filename, err)
					continue
				}

				activeSandboxesInCycle[sandboxName] = true
				task.Status = "Running"
				_ = writeTaskAtomically(processingDir, filename, task)
				writeTaskJournalEvent(w.QueueDir, filename, task, "Started", 0)

				actionsTaken++
				filesInProcessing++

				wg.Add(1)
				go func(taskFilename string, t *QueueTask) {
					defer wg.Done()
					fmt.Printf("Starting task %s (Type: %s, URL: %s)...\n", taskFilename, t.Type, t.URL)
					startTime := time.Now()

					taskCtx, taskCancel := context.WithTimeout(ctx, w.TaskTimeout)
					defer taskCancel()

					if t.Number > 0 {
						if (t.Type == "issue-fix" || t.Type == "agent-chore") && t.Assignee != "" {
							klog.Infof("Assigning issue #%d to %s as claimed", t.Number, t.Assignee)
							if _, _, err := ghClient.Issues.AddAssignees(ctx, w.Repo.Owner, w.Repo.Repo, t.Number, []string{t.Assignee}); err != nil {
								klog.Errorf("Failed to assign issue #%d to %s: %v", t.Number, t.Assignee, err)
							}
							if t.Assignee != targetAssignee {
								if _, _, err := ghClient.Issues.RemoveAssignees(ctx, w.Repo.Owner, w.Repo.Repo, t.Number, []string{targetAssignee}); err != nil {
									klog.Errorf("Failed to remove watcher bot %s from issue #%d: %v", targetAssignee, t.Number, err)
								}
							}
						}

						if t.Type != "agent-chore" {
							var commentBody string
							switch t.Type {
							case "issue-fix":
								commentBody = "🤖 AI Factory started fixing this issue in a sandbox."
							case "pr-investigate":
								commentBody = "🤖 AI Factory started investigating CI check failures for this pull request."
							case "pr-comments":
								commentBody = "🤖 AI Factory started addressing review feedback for this pull request."
							case "pr-iterate":
								commentBody = "🤖 AI Factory started resolving merge conflicts / rebasing this pull request in a sandbox."
							case "pr-review":
								commentBody = "🤖 AI Factory started reviewing this pull request in a sandbox."
							}
							if commentBody != "" {
								addGitHubComment(ctx, ghClient, w.Repo.Owner, w.Repo.Repo, t.Number, commentBody)
							}
						}
					}

					selectedUser := t.Assignee
					var sUserErr error
					if selectedUser == "" || (isPRTask(t.Type) && strings.EqualFold(selectedUser, targetAssignee)) {
						selectedUser, sUserErr = w.selectUserForTask(ctx, ghClient, kubeClient, cfg, t.Type, t.Number)
					}
					if sUserErr != nil {
						klog.Errorf("Failed to select user for task %s: %v", taskFilename, sUserErr)
						t.Status = "Failed"
						t.Error = sUserErr.Error()
						_ = writeTaskAtomically(processingDir, taskFilename, t)
						writeTaskJournalEvent(w.QueueDir, taskFilename, t, "Failed", 0)
						processedPath := filepath.Join(processedDir, taskFilename)
						_ = os.Rename(processingPath, processedPath)
						return
					}

					executable, err := os.Executable()
					if err != nil {
						klog.Errorf("Failed to get executable path: %v", err)
						return
					}

					var args []string
					switch t.Type {
					case "issue-fix":
						args = []string{"fix", "--url", t.URL, "--instruction", "Fix this issue"}
					case "pr-investigate":
						args = []string{"pr", "investigate", "--pr-url", t.URL}
					case "pr-comments":
						args = []string{"pr", "address-comments", "--pr-url", t.URL}
					case "pr-iterate":
						args = []string{"pr", "iterate", "--pr-url", t.URL, "--prompt", "Please resolve merge conflicts in this PR by rebasing onto the latest master/main branch and resolving any conflicts that arise."}
					case "pr-review":
						args = []string{"pr", "review", "--pr-url", t.URL, "--publish", "yes"}
						for _, inst := range t.Instructions {
							args = append(args, "--instruction", inst)
						}
					case "agent-chore":
						args = []string{"agent", "create", "--url", t.URL, "--agent", t.AgentFile}
						if t.SessionID != "" {
							args = append(args, "--session-id", t.SessionID)
						}
					default:
						klog.Errorf("Unknown task type: %s", t.Type)
						return
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

					cmd := exec.CommandContext(taskCtx, executable, args...)

					logFilename := strings.TrimSuffix(taskFilename, ".yaml") + ".log"
					processingLogPath := filepath.Join(processingLogDir, logFilename)
					processedLogPath := filepath.Join(processedLogDir, logFilename)

					logFile, err := os.OpenFile(processingLogPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
					if err != nil {
						klog.Errorf("Failed to create log file: %v", err)
					} else {
						cmd.Stdout = logFile
						cmd.Stderr = logFile
						defer logFile.Close()
					}

					taskErr := cmd.Run()

					processingPathLocal := filepath.Join(processingDir, taskFilename)
					processedPathLocal := filepath.Join(processedDir, taskFilename)
					duration := time.Since(startTime)

					if taskErr != nil {
						klog.Errorf("Task %s failed: %v", taskFilename, taskErr)
						t.Status = "Failed"
						t.Error = taskErr.Error()
						t.CompletedAt = time.Now()
						writeTaskJournalEvent(w.QueueDir, taskFilename, t, "Failed", duration)
						if t.Type == "pr-comments" {
							resolvePRCommentReactions(ctx, ghClient, w.Repo.Owner, w.Repo.Repo, t.Number, "confused", cfg.AllowlistedBots, githubLogin)
						}

						// Force clean up sandbox if the task timed out
						if taskCtx.Err() == context.DeadlineExceeded {
							var sandboxName string
							switch t.Type {
							case "issue-fix":
								if t.SessionID != "" {
									sandboxName = fmt.Sprintf("wf-issue-%d", t.Number)
								} else {
									sandboxName = fmt.Sprintf("fix-%s-%d", w.Repo.Repo, t.Number)
								}
							case "agent-chore":
								if t.SessionID != "" {
									sandboxName = fmt.Sprintf("wf-issue-%d", t.Number)
								} else {
									sandboxName = fmt.Sprintf("agent-%s-%d", w.Repo.Repo, t.Number)
								}
							case "pr-investigate", "pr-comments", "pr-iterate", "pr-review":
								sandboxName = w.resolveSandboxName(ctx, kubeClient, ghClient, t.Type, t.Number)
							}

							if sandboxName != "" {
								klog.Warningf("Task %s timed out after %s! Force cleaning up sandbox '%s'...", taskFilename, w.TaskTimeout, sandboxName)
								manager := k8s.NewManager(kubeClient)
								if err := manager.DeleteSandbox(ctx, w.Namespace, sandboxName); err != nil {
									klog.Errorf("Failed to delete sandbox '%s' on timeout: %v", sandboxName, err)
								}
							}
						}
					} else {
						fmt.Printf("Task %s completed successfully.\n", taskFilename)
						t.Status = "Completed"
						t.CompletedAt = time.Now()
						writeTaskJournalEvent(w.QueueDir, taskFilename, t, "Completed", duration)
						if t.Type == "pr-comments" {
							resolvePRCommentReactions(ctx, ghClient, w.Repo.Owner, w.Repo.Repo, t.Number, "+1", cfg.AllowlistedBots, githubLogin)
						}
					}

					_ = writeTaskAtomically(processingDir, taskFilename, t)
					if err := os.Rename(processingPathLocal, processedPathLocal); err != nil {
						klog.Errorf("Failed to move task %s to processed directory: %v", taskFilename, err)
					}
					if _, err := os.Stat(processingLogPath); err == nil {
						if err := os.Rename(processingLogPath, processedLogPath); err != nil {
							klog.Errorf("Failed to move log file to processed directory: %v", err)
						}
					}
				}(filename, task)
			}
			state.mu.Lock()
			state.lastRunnerRun = now
			state.mu.Unlock()
		}
	}

	checkRepo()

	if w.Once {
		fmt.Println("Running in once mode. Waiting for active tasks to complete...")
		wg.Wait()
		fmt.Println("All tasks completed. Exiting.")
		return nil
	}

	for {
		fmt.Printf("Sleeping for 10s...\n")
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timeoutChan:
			fmt.Printf("\nWatch timeout of %s expired. Shutting down gracefully...\n", w.WatchTimeout)
			state.mu.Lock()
			state.shuttingDown = true
			state.mu.Unlock()

			fmt.Println("Waiting for active tasks to complete...")
			doneChan := make(chan struct{})
			go func() {
				wg.Wait()
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
			checkRepo()
		}
	}
}
