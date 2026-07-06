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

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/k8s"
	"gopkg.in/yaml.v3"
	"k8s.io/klog/v2"
)

func (w *watchContext) run(ctx context.Context, wg *sync.WaitGroup, runRunner bool) {
	if !runRunner {
		return
	}
	now := time.Now()

	incomingFiles, err := os.ReadDir(w.incomingDir)
	if err != nil {
		if !os.IsNotExist(err) {
			klog.Errorf("Failed to read incoming queue directory: %v", err)
		}
		return
	}

	var tasksToRun []struct {
		filename string
		task     *QueueTask
	}

	for _, f := range incomingFiles {
		if f.IsDir() || !strings.HasPrefix(f.Name(), "task-") || !strings.HasSuffix(f.Name(), ".yaml") {
			continue
		}

		filename := f.Name()
		filePath := filepath.Join(w.incomingDir, filename)
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

		tasksToRun = append(tasksToRun, struct {
			filename string
			task     *QueueTask
		}{filename, &t})
	}

	priorityRank := map[string]int{
		"critical":  1,
		"urgent":    2,
		"important": 3,
		"high":      4,
		"medium":    5,
		"low":       6,
	}
	getRank := func(p string) int {
		if r, ok := priorityRank[strings.ToLower(p)]; ok {
			return r
		}
		return 5
	}

	// Sort tasks by priority level (critical first), phase rank (rebase > comments > investigate), and createdAt (newest first)
	for i := 0; i < len(tasksToRun); i++ {
		for j := i + 1; j < len(tasksToRun); j++ {
			tI := tasksToRun[i].task
			tJ := tasksToRun[j].task
			rankI := getRank(tI.Priority)
			rankJ := getRank(tJ.Priority)

			swap := false
			if rankI > rankJ {
				swap = true
			} else if rankI == rankJ {
				if tI.Phase > tJ.Phase {
					swap = true
				} else if tI.Phase == tJ.Phase {
					if tI.CreatedAt.Before(tJ.CreatedAt) {
						swap = true
					}
				}
			}

			if swap {
				tasksToRun[i], tasksToRun[j] = tasksToRun[j], tasksToRun[i]
			}
		}
	}

	processingFiles, _ := os.ReadDir(w.processingDir)
	filesInProcessing := 0
	for _, f := range processingFiles {
		if !f.IsDir() && strings.HasPrefix(f.Name(), "task-") && strings.HasSuffix(f.Name(), ".yaml") {
			filesInProcessing++
		}
	}

	activeSandboxesInCycle := make(map[string]bool)
	actionsTaken := 0

	// Set up paths for logs
	logDir := os.Getenv("FACTORY_LOGS")
	if logDir == "" {
		logDir = filepath.Join(w.opts.QueueDir, "logs")
	}
	processingLogDir := filepath.Join(logDir, "processing")
	processedLogDir := filepath.Join(logDir, "processed")

	for _, item := range tasksToRun {
		if actionsTaken >= w.opts.MaxActions {
			fmt.Printf("Reached maximum actions limit (%d) for this cycle. Stopping execution.\n", w.opts.MaxActions)
			break
		}

		runningCount, err := countRunningSandboxTasks(ctx, w.kubeClient, w.opts.Namespace)
		if err != nil {
			klog.Errorf("Failed to count running sandbox tasks: %v", err)
		}
		activeCount := runningCount + filesInProcessing

		if activeCount >= w.opts.MaxPending {
			fmt.Printf("Reached maximum pending sandboxes limit (%d). Skipping remaining queue items.\n", w.opts.MaxPending)
			break
		}

		filename := item.filename
		task := item.task

		sandboxName := resolveSandboxName(ctx, w.kubeClient, w.ghClient, task.Type, task.Number, w.opts.Owner, w.opts.Repo, w.opts.Namespace)
		if activeSandboxesInCycle[sandboxName] {
			klog.Infof("Skipping task %s because sandbox %s is already scheduled to run a task in this cycle.", filename, sandboxName)
			continue
		}

		running, err := isSandboxTaskRunning(ctx, w.kubeClient, w.opts.Namespace, sandboxName)
		if err != nil {
			klog.Errorf("Failed to check if sandbox %s is running: %v", sandboxName, err)
			continue
		}
		if running {
			klog.Infof("Skipping task %s because sandbox %s is currently busy running another task.", filename, sandboxName)
			continue
		}

		if task.Type != "agent-chore" && task.Recovered {
			completed, err := isSandboxTaskCompleted(ctx, w.kubeClient, w.opts.Namespace, sandboxName, task.Type)
			if err != nil {
				klog.Errorf("Failed to check if sandbox %s completed task: %v", sandboxName, err)
				continue
			}
			if completed {
				klog.Infof("Recovered task %s is already completed in sandbox %s. Marking as completed.", filename, sandboxName)
				if w.opts.DryRun {
					continue
				}
				incomingPath := filepath.Join(w.incomingDir, filename)
				processedPath := filepath.Join(w.processedDir, filename)
				task.Status = "Completed"
				task.CompletedAt = time.Now()
				_ = writeTaskAtomically(w.incomingDir, filename, task)
				writeTaskJournalEvent(w.opts.QueueDir, filename, task, "Completed", 0)
				if err := os.Rename(incomingPath, processedPath); err != nil {
					klog.Errorf("Failed to move completed task %s to processed: %v", filename, err)
				}
				continue
			}
		}

		incomingPath := filepath.Join(w.incomingDir, filename)
		processingPath := filepath.Join(w.processingDir, filename)

		if w.opts.DryRun {
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
		_ = writeTaskAtomically(w.processingDir, filename, task)
		writeTaskJournalEvent(w.opts.QueueDir, filename, task, "Started", 0)

		actionsTaken++
		filesInProcessing++

		wg.Add(1)
		go func(taskFilename string, t *QueueTask) {
			defer wg.Done()
			fmt.Printf("Starting task %s (Type: %s, URL: %s)...\n", taskFilename, t.Type, t.URL)
			startTime := time.Now()

			taskCtx, taskCancel := context.WithTimeout(ctx, w.opts.TaskTimeout)
			defer taskCancel()

			if t.Number > 0 {
				if (t.Type == "issue-fix" || t.Type == "agent-chore") && t.Assignee != "" {
					klog.Infof("Assigning issue #%d to %s as claimed", t.Number, t.Assignee)
					if _, _, err := w.ghClient.Issues.AddAssignees(ctx, w.opts.Owner, w.opts.Repo, t.Number, []string{t.Assignee}); err != nil {
						klog.Errorf("Failed to assign issue #%d to %s: %v", t.Number, t.Assignee, err)
					}
					if t.Assignee != w.targetAssignee {
						if _, _, err := w.ghClient.Issues.RemoveAssignees(ctx, w.opts.Owner, w.opts.Repo, t.Number, []string{w.targetAssignee}); err != nil {
							klog.Errorf("Failed to remove watcher bot %s from issue #%d: %v", w.targetAssignee, t.Number, err)
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
						_ = addGitHubComment(ctx, w.ghClient, w.opts.Owner, w.opts.Repo, t.Number, commentBody)
					}
				}
			}

			selectedUser := t.Assignee
			var sUserErr error
			if selectedUser == "" || (isPRTask(t.Type) && strings.EqualFold(selectedUser, w.targetAssignee)) {
				selectedUser, sUserErr = selectUserForTask(ctx, w.ghClient, w.kubeClient, w.cfg, t.Type, t.Number, w.opts.Owner, w.opts.Repo, w.opts.Namespace)
			}
			if sUserErr != nil {
				klog.Errorf("Failed to select user for task %s: %v", taskFilename, sUserErr)
				t.Status = "Failed"
				t.Error = sUserErr.Error()
				_ = writeTaskAtomically(w.processingDir, taskFilename, t)
				writeTaskJournalEvent(w.opts.QueueDir, taskFilename, t, "Failed", 0)
				processedPath := filepath.Join(w.processedDir, taskFilename)
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
			case "agent-chore":
				args = []string{"agent", "create", "--url", t.URL, "--agent", t.AgentFile}
				if t.SessionID != "" {
					args = append(args, "--session-id", t.SessionID)
				}
			default:
				klog.Errorf("Unknown task type: %s", t.Type)
				return
			}

			if w.opts.Namespace != "" {
				args = append(args, "--namespace", w.opts.Namespace)
			}
			if selectedUser != "" {
				args = append(args, "--user", selectedUser)
			}
			if w.opts.Image != "" {
				args = append(args, "--image", w.opts.Image)
			}
			if w.opts.DiskSize != "" {
				args = append(args, "--workspace-disk-size", w.opts.DiskSize)
			}
			if w.opts.EphemeralStorage != "" {
				args = append(args, "--ephemeral-storage", w.opts.EphemeralStorage)
			}
			if w.opts.TaskTimeout > 0 {
				args = append(args, "--timeout", w.opts.TaskTimeout.String())
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

			processingPathLocal := filepath.Join(w.processingDir, taskFilename)
			processedPathLocal := filepath.Join(w.processedDir, taskFilename)
			duration := time.Since(startTime)

			if taskErr != nil {
				klog.Errorf("Task %s failed: %v", taskFilename, taskErr)
				t.Status = "Failed"
				t.Error = taskErr.Error()
				t.CompletedAt = time.Now()
				writeTaskJournalEvent(w.opts.QueueDir, taskFilename, t, "Failed", duration)

				// Force clean up sandbox if the task timed out
				if taskCtx.Err() == context.DeadlineExceeded {
					var sandboxName string
					switch t.Type {
					case "issue-fix":
						if t.SessionID != "" {
							sandboxName = fmt.Sprintf("wf-issue-%d", t.Number)
						} else {
							sandboxName = fmt.Sprintf("fix-%s-%d", w.opts.Repo, t.Number)
						}
					case "agent-chore":
						if t.SessionID != "" {
							sandboxName = fmt.Sprintf("wf-issue-%d", t.Number)
						} else {
							sandboxName = fmt.Sprintf("agent-%s-%d", w.opts.Repo, t.Number)
						}
					case "pr-investigate", "pr-comments", "pr-iterate", "pr-review":
						sandboxName = resolveSandboxName(ctx, w.kubeClient, w.ghClient, t.Type, t.Number, w.opts.Owner, w.opts.Repo, w.opts.Namespace)
					}

					if sandboxName != "" {
						klog.Warningf("Task %s timed out after %s! Force cleaning up sandbox '%s'...", taskFilename, w.opts.TaskTimeout, sandboxName)
						manager := k8s.NewManager(w.kubeClient)
						if err := manager.DeleteSandbox(ctx, w.opts.Namespace, sandboxName); err != nil {
							klog.Errorf("Failed to delete sandbox '%s' on timeout: %v", sandboxName, err)
						}
					}
				}
			} else {
				fmt.Printf("Task %s completed successfully.\n", taskFilename)
				t.Status = "Completed"
				t.CompletedAt = time.Now()
				writeTaskJournalEvent(w.opts.QueueDir, taskFilename, t, "Completed", duration)
			}

			_ = writeTaskAtomically(w.processingDir, taskFilename, t)
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

	w.state.mu.Lock()
	w.state.lastRunnerRun = now
	w.state.mu.Unlock()
}
