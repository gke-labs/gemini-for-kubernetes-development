package watch

import (
	"context"
	"fmt"
	"strings"

	"k8s.io/klog/v2"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/commands/watch/api"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/commands/watch/dispatcher"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/k8s"
)

// newDispatcher constructs the task dispatcher used by the watcher, wiring it to
// the watcher's queue manager, sandbox leases, cluster clients and GitHub client.
// The runner is passed in so tests can substitute the child process execution.
func (w *Watcher) newDispatcher(runner dispatcher.TaskRunner) *dispatcher.Dispatcher {
	return dispatcher.New(dispatcher.Config{
		Interval:    dispatcher.DefaultInterval,
		MaxActions:  w.MaxActions,
		MaxPending:  w.MaxPending,
		TaskTimeout: w.TaskTimeout,
		DryRun:      w.DryRun,
	}, dispatcher.Deps{
		Queue:        w.queueMgr,
		SandboxLocks: w.sandboxLocks,
		Sandboxes:    &watcherSandboxService{w: w},
		Coordinator:  &watcherTaskCoordinator{w: w},
		Runner:       runner,
	})
}

// newCLIRunner builds the runner that executes tasks as child factory CLI processes.
func (w *Watcher) newCLIRunner() *dispatcher.CLIRunner {
	return dispatcher.NewCLIRunner(dispatcher.CLIRunnerConfig{
		Namespace:        w.Namespace,
		Image:            w.Image,
		DiskSize:         w.DiskSize,
		EphemeralStorage: w.EphemeralStorage,
		CPURequest:       w.CPURequest,
		CPULimit:         w.CPULimit,
		MemoryRequest:    w.MemoryRequest,
		MemoryLimit:      w.MemoryLimit,
		TaskTimeout:      w.TaskTimeout,
		ProcessingLogDir: w.processingLogDir,
	})
}

// watcherSandboxService adapts the watcher's Kubernetes helpers to the dispatcher.SandboxService interface.
type watcherSandboxService struct {
	w *Watcher
}

var _ dispatcher.SandboxService = (*watcherSandboxService)(nil)

func (s *watcherSandboxService) ResolveSandboxName(ctx context.Context, taskType api.TaskType, number int) string {
	return s.w.resolveSandboxName(ctx, taskType, number)
}

func (s *watcherSandboxService) IsTaskRunning(ctx context.Context, sandboxName string) (bool, error) {
	return isSandboxTaskRunning(ctx, s.w.kubeClient, s.w.Namespace, sandboxName)
}

func (s *watcherSandboxService) IsTaskCompleted(ctx context.Context, sandboxName string, taskType api.TaskType) (bool, error) {
	return isSandboxTaskCompleted(ctx, s.w.kubeClient, s.w.Namespace, sandboxName, taskType)
}

func (s *watcherSandboxService) CountRunningTasks(ctx context.Context) (int, error) {
	return countRunningSandboxTasks(ctx, s.w.kubeClient, s.w.Namespace)
}

func (s *watcherSandboxService) DeleteSandbox(ctx context.Context, sandboxName string) error {
	if s.w.kubeClient == nil {
		return nil
	}
	return k8s.NewManager(s.w.kubeClient).DeleteSandbox(ctx, s.w.Namespace, sandboxName)
}

// watcherTaskCoordinator adapts the watcher's GitHub interactions to the TaskCoordinator interface.
type watcherTaskCoordinator struct {
	w *Watcher
}

var _ dispatcher.TaskCoordinator = (*watcherTaskCoordinator)(nil)

// ShouldCancelTask reports whether the target issue or pull request carries a
// stop label or has been closed since the task was queued.
func (c *watcherTaskCoordinator) ShouldCancelTask(ctx context.Context, task *api.QueueTask) (bool, string) {
	w := c.w
	if task.Number <= 0 || w.DryRun || w.ghClient == nil {
		return false, ""
	}

	issueOrPR, _, err := w.ghClient.Issues.Get(ctx, w.Repo.Owner, w.Repo.Repo, task.Number)
	if err != nil || issueOrPR == nil {
		return false, ""
	}
	if hasStopLabel(issueOrPR.Labels, w.triggerLabel) {
		return true, fmt.Sprintf("target #%d has the stop label ('overseer/stop' or '%s/stop')", task.Number, w.triggerLabel)
	}
	if issueOrPR.GetState() == "closed" {
		return true, fmt.Sprintf("target #%d is closed", task.Number)
	}
	return false, ""
}

// SelectUser returns the bot account the task should run as, preferring the
// assignee recorded on the task and otherwise falling back to role selection.
func (c *watcherTaskCoordinator) SelectUser(ctx context.Context, task *api.QueueTask) (string, error) {
	w := c.w
	selectedUser := task.Assignee
	if selectedUser == "" || (api.IsPRTask(task.Type) && strings.EqualFold(selectedUser, w.targetAssignee)) {
		return w.selectUserForTask(ctx, task.Type, task.Number)
	}
	return selectedUser, nil
}

// NotifyTaskStarted claims the issue for the assignee and posts a start comment on GitHub.
func (c *watcherTaskCoordinator) NotifyTaskStarted(ctx context.Context, task *api.QueueTask) {
	w := c.w
	if task.Number <= 0 || w.ghClient == nil {
		return
	}

	if (task.Type == api.TypeIssueFix || task.Type == api.TypeAgentChore) && task.Assignee != "" {
		klog.Infof("Assigning issue #%d to %s as claimed", task.Number, task.Assignee)
		if _, _, err := w.ghClient.Issues.AddAssignees(ctx, w.Repo.Owner, w.Repo.Repo, task.Number, []string{task.Assignee}); err != nil {
			klog.Errorf("Failed to assign issue #%d to %s: %v", task.Number, task.Assignee, err)
		}
		if task.Assignee != w.targetAssignee {
			if _, _, err := w.ghClient.Issues.RemoveAssignees(ctx, w.Repo.Owner, w.Repo.Repo, task.Number, []string{w.targetAssignee}); err != nil {
				klog.Errorf("Failed to remove watcher bot %s from issue #%d: %v", w.targetAssignee, task.Number, err)
			}
		}
	}

	if commentBody := taskStartedComment(task.Type); commentBody != "" {
		addGitHubComment(ctx, w.ghClient, w.Repo.Owner, w.Repo.Repo, task.Number, commentBody)
	}
}

// NotifyTaskFinished resolves the acknowledgement reactions on PR review comments.
func (c *watcherTaskCoordinator) NotifyTaskFinished(ctx context.Context, task *api.QueueTask, taskErr error) {
	w := c.w
	if task.Type != api.TypePRComments || w.cfg == nil {
		return
	}
	resolution := "+1"
	if taskErr != nil {
		resolution = "confused"
	}
	resolvePRCommentReactions(ctx, w.ghClient, w.Repo.Owner, w.Repo.Repo, task.Number, resolution, w.cfg.AllowlistedBots, w.githubLogin)
}

// taskStartedComment returns the GitHub comment announcing that a task has started,
// or an empty string if the task type does not warrant a comment.
func taskStartedComment(taskType api.TaskType) string {
	switch taskType {
	case api.TypeIssueFix:
		return "🤖 AI Factory started fixing this issue in a sandbox."
	case api.TypePRInvestigate:
		return "🤖 AI Factory started investigating CI check failures for this pull request."
	case api.TypePRComments:
		return "🤖 AI Factory started addressing review feedback for this pull request."
	case api.TypePRIterate:
		return "🤖 AI Factory started resolving merge conflicts / rebasing this pull request in a sandbox."
	case api.TypePRReview:
		return "🤖 AI Factory started reviewing this pull request in a sandbox."
	default:
		return ""
	}
}
