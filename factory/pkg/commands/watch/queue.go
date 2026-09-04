package watch

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	githubv39 "github.com/google/go-github/v39/github"
	"gopkg.in/yaml.v3"
	"k8s.io/klog/v2"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/commands/watch/api"
)

func isDoNotProcess(queueDir string) bool {
	if os.Getenv("DO_NOT_PROCESS") == "true" || os.Getenv("FACTORY_DO_NOT_PROCESS") == "true" || os.Getenv("DRAIN") == "true" || os.Getenv("FACTORY_DRAIN") == "true" {
		return true
	}
	checkPaths := []string{
		filepath.Join(queueDir, ".do_not_process"),
		filepath.Join(queueDir, "do_not_process"),
		filepath.Join(queueDir, ".drain"),
		filepath.Join(queueDir, "drain"),
		"/workspaces/.do_not_process",
		"/workspaces/do_not_process",
		"/workspaces/.drain",
		"/workspaces/drain",
	}
	for _, p := range checkPaths {
		if _, err := os.Stat(p); err == nil {
			return true
		}
	}
	return false
}

func getIssuePriority(issue *githubv39.Issue) api.TaskPriority {
	for _, l := range issue.Labels {
		name := l.GetName()
		if strings.HasPrefix(name, "priority/") {
			return api.TaskPriority(strings.TrimPrefix(name, "priority/"))
		}
	}
	return api.PriorityMedium
}

func getPRPriority(prIssue *githubv39.Issue) api.TaskPriority {
	return getIssuePriority(prIssue)
}

func parseProcessedPRTask(filePath string, name string, fInfo os.FileInfo, state prWatchState) prWatchState {
	isComments := strings.HasSuffix(name, "-comments")
	isInvestigate := strings.HasSuffix(name, "-investigate")
	isReview := strings.HasSuffix(name, "-review")
	isIterate := strings.HasSuffix(name, "-iterate")

	var t api.QueueTask
	hasTask := false
	if data, err := os.ReadFile(filePath); err == nil {
		if err := yaml.Unmarshal(data, &t); err == nil {
			hasTask = true
			if strings.EqualFold(string(t.Status), string(api.StatusFailed)) {
				return state
			}
		}
	}

	if fInfo != nil {
		tTime := fInfo.ModTime()
		if hasTask && !t.CompletedAt.IsZero() {
			tTime = t.CompletedAt
		}
		if isComments {
			if tTime.After(state.lastCommentAddressedTime) {
				state.lastCommentAddressedTime = tTime
			}
			if hasTask && t.CommitSHA != "" {
				state.lastCommentAddressedSHA = t.CommitSHA
			}
		} else if isInvestigate {
			if tTime.After(state.lastInvestigatedTime) {
				state.lastInvestigatedTime = tTime
			}
			if hasTask && t.CommitSHA != "" {
				state.lastInvestigatedSHA = t.CommitSHA
			}
		} else if isReview {
			if hasTask && t.CommitSHA != "" {
				state.lastReviewedSHA = t.CommitSHA
			}
		} else if isIterate {
			if tTime.After(state.lastIteratedTime) {
				state.lastIteratedTime = tTime
			}
			if hasTask && t.CommitSHA != "" {
				state.lastIteratedSHA = t.CommitSHA
			}
		}
	}
	return state
}

func loadProcessedTasks(processedDir string) (map[int]time.Time, map[int]prWatchState) {
	processedIssues := make(map[int]time.Time)
	processedPRs := make(map[int]prWatchState)
	files, err := os.ReadDir(processedDir)
	if err != nil {
		return processedIssues, processedPRs
	}
	for _, f := range files {
		if f.IsDir() || !strings.HasSuffix(f.Name(), ".yaml") {
			continue
		}
		filePath := filepath.Join(processedDir, f.Name())
		if strings.HasPrefix(f.Name(), "task-issue-") {
			trimmed := strings.TrimPrefix(f.Name(), "task-issue-")
			trimmed = strings.TrimSuffix(trimmed, ".yaml")
			if num, err := strconv.Atoi(trimmed); err == nil {
				var t api.QueueTask
				hasTask := false
				if data, err := os.ReadFile(filePath); err == nil {
					if err := yaml.Unmarshal(data, &t); err == nil {
						hasTask = true
					}
				}
				if hasTask && strings.EqualFold(string(t.Status), string(api.StatusFailed)) {
					continue
				}
				if info, err := f.Info(); err == nil {
					tTime := info.ModTime()
					if hasTask && !t.CompletedAt.IsZero() {
						tTime = t.CompletedAt
					}
					processedIssues[num] = tTime
				}
			}
		} else if strings.HasPrefix(f.Name(), "task-pr-") {
			name := strings.TrimPrefix(f.Name(), "task-pr-")
			name = strings.TrimSuffix(name, ".yaml")

			isComments := strings.HasSuffix(name, "-comments")
			isInvestigate := strings.HasSuffix(name, "-investigate")
			isReview := strings.HasSuffix(name, "-review")
			isIterate := strings.HasSuffix(name, "-iterate")

			var numStr string
			if isComments {
				numStr = strings.TrimSuffix(name, "-comments")
			} else if isInvestigate {
				numStr = strings.TrimSuffix(name, "-investigate")
			} else if isReview {
				numStr = strings.TrimSuffix(name, "-review")
			} else if isIterate {
				numStr = strings.TrimSuffix(name, "-iterate")
			}

			if numStr != "" {
				if num, err := strconv.Atoi(numStr); err == nil {
					state := processedPRs[num]
					info, _ := f.Info()
					processedPRs[num] = parseProcessedPRTask(filePath, name, info, state)
				}
			}
		}
	}
	return processedIssues, processedPRs
}

func (w *Watcher) recoverStuckTasks(ctx context.Context) {
	files, err := os.ReadDir(w.processingDir)
	if err != nil {
		return
	}
	for _, f := range files {
		if !f.IsDir() && strings.HasPrefix(f.Name(), "task-") && strings.HasSuffix(f.Name(), ".yaml") {
			processingPath := filepath.Join(w.processingDir, f.Name())

			// Read the task
			if data, err := os.ReadFile(processingPath); err == nil {
				var t api.QueueTask
				if err := yaml.Unmarshal(data, &t); err == nil {
					sandboxName := w.resolveSandboxName(ctx, t.Type, t.Number)
					if w.kubeClient != nil && sandboxName != "" {
						running, err := isSandboxTaskRunning(ctx, w.kubeClient, w.Namespace, sandboxName)
						if err == nil && running {
							klog.Infof("Task %s is still actively running in sandbox %s. Leaving in processing.", f.Name(), sandboxName)
							continue
						}
						completed, err := isSandboxTaskCompleted(ctx, w.kubeClient, w.Namespace, sandboxName, t.Type)
						if err == nil && completed {
							klog.Infof("Task %s already completed in sandbox %s. Moving from processing to processed.", f.Name(), sandboxName)
							if err := w.queueMgr.CompleteTask(f.Name(), &t); err == nil {
								continue
							}
						}
					}

					t.Status = api.StatusPending
					t.Recovered = true
					if err := w.queueMgr.Enqueue(f.Name(), &t); err == nil {
						klog.Infof("Recovered stuck task %s from processing to incoming", f.Name())
						continue
					}
				}
			}

			// Fallback to simple rename if parsing fails
			incomingPath := filepath.Join(w.incomingDir, f.Name())
			if err := os.Rename(processingPath, incomingPath); err == nil {
				klog.Infof("Recovered stuck task %s (fallback rename) to incoming", f.Name())
			} else {
				klog.Errorf("Failed to recover stuck task %s: %v", f.Name(), err)
			}
		}
	}
}

// PRTaskOptions specifies parameters for constructing a pull request api.QueueTask.
type PRTaskOptions struct {
	Type             api.TaskType
	PR               *githubv39.PullRequest
	PRIssue          *githubv39.Issue
	Phase            api.TaskPhase
	Assignee         string
	CommitSHA        string
	TriggerEventTime time.Time
	TriggerReason    api.TriggerReason
	TriggerNotes     string
	Instructions     []string
}

// newPRQueueTask constructs an api.QueueTask for pull request tasks with consistent defaults.
func (w *Watcher) newPRQueueTask(opts PRTaskOptions) *api.QueueTask {
	num := opts.PR.GetNumber()
	eventTime := opts.TriggerEventTime
	if eventTime.IsZero() {
		eventTime = opts.PR.GetUpdatedAt()
	}
	if eventTime.IsZero() {
		eventTime = opts.PR.GetCreatedAt()
	}
	return &api.QueueTask{
		Type:             opts.Type,
		URL:              fmt.Sprintf("https://github.com/%s/%s/pull/%d", w.Repo.Owner, w.Repo.Repo, num),
		Number:           num,
		Priority:         getPRPriority(opts.PRIssue),
		Phase:            opts.Phase,
		CreatedAt:        opts.PR.GetCreatedAt(),
		EnqueuedAt:       time.Now(),
		TriggerEventTime: eventTime,
		TriggerReason:    opts.TriggerReason,
		TriggerNotes:     opts.TriggerNotes,
		Assignee:         opts.Assignee,
		Status:           api.StatusPending,
		CommitSHA:        opts.CommitSHA,
		Instructions:     opts.Instructions,
	}
}

// IssueTaskOptions specifies parameters for constructing an issue api.QueueTask.
type IssueTaskOptions struct {
	Type             api.TaskType
	Issue            *githubv39.Issue
	Phase            api.TaskPhase
	Assignee         string
	TriggerEventTime time.Time
	TriggerReason    api.TriggerReason
	TriggerNotes     string
	AgentFile        string
	SessionID        string
}

// newIssueQueueTask constructs an api.QueueTask for issue tasks with consistent defaults.
func (w *Watcher) newIssueQueueTask(opts IssueTaskOptions) *api.QueueTask {
	num := opts.Issue.GetNumber()
	return &api.QueueTask{
		Type:             opts.Type,
		URL:              fmt.Sprintf("https://github.com/%s/%s/issues/%d", w.Repo.Owner, w.Repo.Repo, num),
		Number:           num,
		Priority:         getIssuePriority(opts.Issue),
		Phase:            opts.Phase,
		CreatedAt:        opts.Issue.GetCreatedAt(),
		EnqueuedAt:       time.Now(),
		TriggerEventTime: opts.TriggerEventTime,
		TriggerReason:    opts.TriggerReason,
		TriggerNotes:     opts.TriggerNotes,
		Assignee:         opts.Assignee,
		Status:           api.StatusPending,
		AgentFile:        opts.AgentFile,
		SessionID:        opts.SessionID,
	}
}

// ChoreTaskOptions specifies parameters for constructing a scheduled chore api.QueueTask.
type ChoreTaskOptions struct {
	AgentFile        string
	TriggerEventTime time.Time
	TriggerReason    api.TriggerReason
	TriggerNotes     string
}

// newChoreQueueTask constructs an api.QueueTask for scheduled chore tasks with consistent defaults.
func (w *Watcher) newChoreQueueTask(opts ChoreTaskOptions) *api.QueueTask {
	return &api.QueueTask{
		Type:             api.TypeAgentChore,
		URL:              fmt.Sprintf("https://github.com/%s/%s", w.Repo.Owner, w.Repo.Repo),
		Priority:         api.PriorityMedium,
		Phase:            api.PhaseChores,
		CreatedAt:        time.Now(),
		EnqueuedAt:       time.Now(),
		TriggerEventTime: opts.TriggerEventTime,
		TriggerReason:    opts.TriggerReason,
		TriggerNotes:     opts.TriggerNotes,
		Status:           api.StatusPending,
		AgentFile:        opts.AgentFile,
	}
}
