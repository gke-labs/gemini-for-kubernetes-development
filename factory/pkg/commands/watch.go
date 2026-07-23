package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/clients"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/config"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/envd"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/github"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/k8s"
	factorysandbox "github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/sandbox"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/usagereport"
	githubv39 "github.com/google/go-github/v39/github"
	"github.com/robfig/cron/v3"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/klog/v2"
)

type WatchFlags struct {
	Repo               string
	PollInterval       time.Duration
	Assignee           string
	Labels             []string
	DryRun             bool
	WatchTimeout       time.Duration
	MaxActions         int
	MaxPending         int
	Mode               string
	QueueDir           string
	Once               bool
	IssueMode          string
	PRMode             string
	ChoresMode         string
	ScanLimit          int
	TaskTimeout        time.Duration
	SandboxEvictionAge string
	SandboxIdleTimeout time.Duration
}

func NewWatchCommand(ctx context.Context) *cobra.Command {
	var flags WatchFlags

	cmd := &cobra.Command{
		Use:   "watch",
		Short: "Watch a GitHub repo for test failures and assigned issues to automatically fix and review",
		Example: `  # Watch for unassigned issues with specific labels
  factory watch --repo owner/repo --assignee "" --labels "bug,help wanted"

  # Watch for assigned issues with labels
  factory watch --repo owner/repo --assignee "factory-bot" --labels "p0,urgent"`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			_, err := ResolveRootFlags(cmd)
			if err != nil {
				return err
			}

			if flags.Repo == "" {
				return fmt.Errorf("--repo is required (e.g. owner/repo)")
			}
			parts := strings.Split(flags.Repo, "/")
			if len(parts) != 2 {
				return fmt.Errorf("invalid repo format, expected owner/repo, got %s", flags.Repo)
			}

			issueMode := os.Getenv("ISSUE_MODE")
			if flags.IssueMode != "" {
				issueMode = flags.IssueMode
			}
			if issueMode == "" {
				issueMode = "enabled"
			}

			prMode := os.Getenv("PR_MODE")
			if flags.PRMode != "" {
				prMode = flags.PRMode
			}
			if prMode == "" {
				prMode = "enabled"
			}

			choresMode := os.Getenv("CHORES_MODE")
			if flags.ChoresMode != "" {
				choresMode = flags.ChoresMode
			}
			cfg, _ := config.LoadConfig()
			if cfg != nil && cfg.Chores.Mode == "disabled" {
				choresMode = "disabled"
			}
			if choresMode == "" {
				choresMode = "enabled"
			}

			return runWatch(ctx, parts[0], parts[1], flags.PollInterval, flags.Assignee, cmd.Flags().Changed("assignee"), flags.Labels, flags.DryRun, flags.WatchTimeout, flags.MaxActions, flags.MaxPending, flags.Mode, flags.QueueDir, flags.Once, issueMode, prMode, choresMode, rootFlags.EphemeralStorage, rootFlags.ResolvedSecrets, flags.ScanLimit, flags.TaskTimeout, flags.SandboxEvictionAge, flags.SandboxIdleTimeout)
		},
	}

	cmd.Flags().StringVar(&flags.Repo, "repo", "", "GitHub repository (e.g. owner/repo)")
	cmd.Flags().DurationVar(&flags.PollInterval, "poll-interval", 2*time.Minute, "Polling interval")
	cmd.Flags().StringVar(&flags.Assignee, "assignee", "factory-bot", "GitHub username to watch for assigned issues (use empty string for unassigned issues)")
	cmd.Flags().StringSliceVar(&flags.Labels, "labels", nil, "Comma-separated list of labels to filter issues by")
	cmd.Flags().BoolVar(&flags.DryRun, "dryrun", false, "Print actions without creating sandboxes or executing tasks")
	cmd.Flags().DurationVar(&flags.WatchTimeout, "watch-timeout", 0, "Timeout for watching (default forever)")
	cmd.Flags().IntVar(&flags.MaxActions, "max-actions", 40, "Maximum number of actions to take in a single watch loop")
	cmd.Flags().IntVar(&flags.MaxPending, "max-pending", 40, "Maximum number of pending/running sandboxes allowed before skipping actions")
	cmd.Flags().StringVar(&flags.Mode, "mode", "all", "Watch mode: all (scan & run), scan (only scan & queue), run (only process queue)")
	cmd.Flags().StringVar(&flags.QueueDir, "queue-dir", "/workspaces/queues", "Directory path for the task queues")
	cmd.Flags().BoolVar(&flags.Once, "once", false, "Run watch once and exit (waits for active tasks to complete)")
	cmd.Flags().StringVar(&flags.IssueMode, "issue-mode", "", "Issue mode: enabled or disabled (defaults to ISSUE_MODE env or enabled)")
	cmd.Flags().StringVar(&flags.PRMode, "pr-mode", "", "PR mode: enabled or disabled (defaults to PR_MODE env or enabled)")
	cmd.Flags().StringVar(&flags.ChoresMode, "chores-mode", "", "Chores mode: enabled or disabled (defaults to CHORES_MODE env or enabled)")
	cmd.Flags().IntVar(&flags.ScanLimit, "scan-limit", 100, "Maximum number of issues/PRs to fetch from GitHub API in a scan cycle")
	cmd.Flags().DurationVar(&flags.TaskTimeout, "task-timeout", 3*time.Hour, "Timeout for each task execution (default 3h)")
	cmd.Flags().StringVar(&flags.SandboxEvictionAge, "sandbox-eviction-age", "7d", "Age threshold for idle sandbox eviction (e.g. '7d', '24h')")
	cmd.Flags().DurationVar(&flags.SandboxIdleTimeout, "sandbox-idle-timeout", getEnvDuration("SANDBOX_IDLE_TIMEOUT", 0), "Idle timeout after which a sandbox that has not run any task is suspended by setting replicas to 0 (e.g. '30m', '1h')")

	return cmd
}

func assignedBotUser(issue *githubv39.Issue, botUsers []string) string {
	for _, u := range issue.Assignees {
		for _, bot := range botUsers {
			if strings.EqualFold(u.GetLogin(), bot) {
				return u.GetLogin()
			}
		}
	}
	return ""
}

func resolveSandboxName(ctx context.Context, kubeClient *clients.KubernetesClient, ghClient *githubv39.Client, taskType string, num int, owner, repo string) string {
	if taskType == "issue-fix" || taskType == "agent-chore" {
		wfName := fmt.Sprintf("wf-issue-%d", num)
		if kubeClient != nil {
			if _, err := kubeClient.DynamicClient.Resource(k8s.SandboxGVR).Namespace(rootFlags.Namespace).Get(ctx, wfName, metav1.GetOptions{}); err == nil {
				return wfName
			}
		}
		return fmt.Sprintf("fix-%s-%d", repo, num)
	}

	// For PR tasks, check if there's an existing sandbox with the PR label
	if kubeClient != nil {
		listOpts := metav1.ListOptions{
			LabelSelector: fmt.Sprintf("factory.gemini.google.com/pr=%d", num),
		}
		sbs, err := kubeClient.DynamicClient.Resource(k8s.SandboxGVR).Namespace(rootFlags.Namespace).List(ctx, listOpts)
		if err == nil && len(sbs.Items) > 0 {
			return sbs.Items[0].GetName()
		}
	}

	// If no sandbox is labeled with this PR, try to find a matching issue sandbox by checking referenced issues
	if kubeClient != nil && ghClient != nil && owner != "" {
		pr, _, err := ghClient.PullRequests.Get(ctx, owner, repo, num)
		if err == nil {
			// Find referenced issue numbers
			referencedIssues := getReferencedIssues(pr)
			for issueNum := range referencedIssues {
				// Check if there is an active/existing sandbox for this issue
				issueSandboxName := fmt.Sprintf("fix-%s-%d", repo, issueNum)
				if _, err := kubeClient.DynamicClient.Resource(k8s.SandboxGVR).Namespace(rootFlags.Namespace).Get(ctx, issueSandboxName, metav1.GetOptions{}); err == nil {
					// We found a matching issue sandbox! Alias it to the PR now for future lookups.
					klog.Infof("Self-healing: Found matching issue sandbox '%s' for PR #%d. Aliasing sandbox to PR...", issueSandboxName, num)
					if aliasErr := factorysandbox.AliasSandboxToPR(ctx, kubeClient, rootFlags.Namespace, issueSandboxName, num, pr.GetHTMLURL()); aliasErr != nil {
						klog.Warningf("Failed to dynamically alias sandbox '%s' to PR #%d: %v", issueSandboxName, num, aliasErr)
					}
					return issueSandboxName
				}
			}
		}
	}

	return fmt.Sprintf("factory-pr-%d", num)
}

func isSandboxTaskRunning(ctx context.Context, kubeClient *clients.KubernetesClient, namespace, name string) (bool, error) {
	unstructObj, err := kubeClient.DynamicClient.Resource(k8s.SandboxGVR).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			return false, nil
		}
		return false, err
	}

	annotations := unstructObj.GetAnnotations()
	if annotations == nil {
		return true, nil
	}

	state := annotations["sandbox.gemini.google.com/last-task-state"]
	if state == "" || strings.EqualFold(state, "Running") {
		// First check if the sandbox pod has failed or been evicted before calling envd.Connect
		podList, err := kubeClient.Clientset.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{LabelSelector: fmt.Sprintf("sandbox=%s", name)})
		if err == nil && len(podList.Items) > 0 {
			hasLiveOrPending := false
			var lastFailedPod *corev1.Pod
			for i := range podList.Items {
				pod := &podList.Items[i]
				if pod.DeletionTimestamp == nil {
					if pod.Status.Phase == corev1.PodRunning || pod.Status.Phase == corev1.PodPending {
						hasLiveOrPending = true
					} else if pod.Status.Phase == corev1.PodFailed || pod.Status.Phase == corev1.PodSucceeded || strings.EqualFold(pod.Status.Reason, "Evicted") {
						lastFailedPod = pod
					}
				}
			}
			if !hasLiveOrPending && lastFailedPod != nil {
				reason := lastFailedPod.Status.Reason
				if reason == "" {
					reason = string(lastFailedPod.Status.Phase)
				}
				taskState := "Failed"
				if lastFailedPod.Status.Phase == corev1.PodSucceeded {
					taskState = "Completed"
				}
				taskType := annotations["sandbox.gemini.google.com/last-task-type"]
				if taskType == "" {
					taskType = "task"
				}
				klog.Warningf("Sandbox %s pod is in %s state (reason: %s). Updating sandbox annotation from Running to %s.", name, lastFailedPod.Status.Phase, reason, taskState)
				_ = factorysandbox.UpdateSandboxTaskAnnotation(ctx, kubeClient, namespace, name, taskType, taskState)
				if strings.EqualFold(reason, "Evicted") || (lastFailedPod.Status.Phase == corev1.PodFailed && strings.EqualFold(lastFailedPod.Status.Reason, "Evicted")) {
					klog.Infof("Deleting evicted pod %s so controller can recreate it.", lastFailedPod.Name)
					_ = factorysandbox.IncrementSandboxEvictionCount(ctx, kubeClient, namespace, name)
					_ = kubeClient.Clientset.CoreV1().Pods(namespace).Delete(ctx, lastFailedPod.Name, metav1.DeleteOptions{})
				}
				return false, nil
			}
		}

		// Verify if the task has actually finished by connecting to the sandbox via envd
		client, err := envd.Connect(ctx, namespace, name)
		if err == nil {
			defer client.Close()
			var buf bytes.Buffer
			// Check exit_code of the latest task, and fallback to checking process viability via PID
			checkCmd := `task_dir=$(ls -td /workspaces/tasks/* 2>/dev/null | head -1)
			if [ -z "$task_dir" ]; then
				echo "NOTASKS"
			elif [ -f "$task_dir/exit_code" ]; then
				cat "$task_dir/exit_code"
			else
				pid=$(cat "$task_dir/pid" 2>/dev/null)
				if [ -n "$pid" ] && ! kill -0 "$pid" 2>/dev/null; then
					echo "137" # Report SIGKILL/Crashed fallback exit code
				fi
			fi`
			if err := client.Exec(ctx, checkCmd, "/workspaces", nil, nil, &buf, nil); err == nil {
				exitStr := strings.TrimSpace(buf.String())
				if exitStr == "NOTASKS" {
					return false, nil
				} else if exitStr != "" {
					// Task has finished!
					taskState := "Completed"
					if exitStr != "0" {
						taskState = "Failed"
					}
					taskType := annotations["sandbox.gemini.google.com/last-task-type"]
					if taskType == "" {
						taskType = "task"
					}
					klog.Infof("Detected completed task %s inside sandbox %s with exit code %s. Updating sandbox annotation to %s.", taskType, name, exitStr, taskState)
					_ = factorysandbox.UpdateSandboxTaskAnnotation(ctx, kubeClient, namespace, name, taskType, taskState)
					return false, nil
				}
			}
		} else if strings.Contains(err.Error(), "cannot connect to terminated pod") || strings.Contains(err.Error(), "is in Failed state") {
			taskType := annotations["sandbox.gemini.google.com/last-task-type"]
			if taskType == "" {
				taskType = "task"
			}
			klog.Warningf("Sandbox %s pod cannot be connected (%v). Updating sandbox annotation to Failed.", name, err)
			_ = factorysandbox.UpdateSandboxTaskAnnotation(ctx, kubeClient, namespace, name, taskType, "Failed")
			return false, nil
		}
		return true, nil
	}

	return false, nil
}

func isSandboxTaskCompleted(ctx context.Context, kubeClient *clients.KubernetesClient, namespace, name, taskType string) (bool, error) {
	unstructObj, err := kubeClient.DynamicClient.Resource(k8s.SandboxGVR).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			return false, nil
		}
		return false, err
	}

	annotations := unstructObj.GetAnnotations()
	if annotations == nil {
		return false, nil
	}

	state := annotations["sandbox.gemini.google.com/last-task-state"]
	tType := annotations["sandbox.gemini.google.com/last-task-type"]

	sbTaskType := taskType
	if taskType == "issue-fix" {
		sbTaskType = "fix-issue"
	} else if taskType == "agent-chore" {
		sbTaskType = "agent"
	}

	if strings.EqualFold(state, "Completed") && strings.EqualFold(tType, sbTaskType) {
		return true, nil
	}
	return false, nil
}

func countRunningSandboxTasks(ctx context.Context, kubeClient *clients.KubernetesClient, namespace string) (int, error) {
	list, err := kubeClient.DynamicClient.Resource(k8s.SandboxGVR).Namespace(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return 0, err
	}

	count := 0
	for _, item := range list.Items {
		if factorysandbox.IsCurrentSandbox(ctx, kubeClient, &item, namespace) {
			continue
		}

		annotations := item.GetAnnotations()
		if annotations == nil {
			count++
			continue
		}

		state := annotations["sandbox.gemini.google.com/last-task-state"]
		if state == "" || strings.EqualFold(state, "Running") {
			count++
		}
	}
	return count, nil
}

func reconcileRunningSandboxes(ctx context.Context, kubeClient *clients.KubernetesClient, namespace string) {
	if kubeClient == nil {
		return
	}
	list, err := kubeClient.DynamicClient.Resource(k8s.SandboxGVR).Namespace(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		klog.Warningf("Failed to list sandboxes during reconcileRunningSandboxes: %v", err)
		return
	}
	for _, item := range list.Items {
		if factorysandbox.IsCurrentSandbox(ctx, kubeClient, &item, namespace) {
			continue
		}
		annotations := item.GetAnnotations()
		state := ""
		if annotations != nil {
			state = annotations["sandbox.gemini.google.com/last-task-state"]
		}
		if state == "" || strings.EqualFold(state, "Running") {
			sbName := item.GetName()
			running, err := isSandboxTaskRunning(ctx, kubeClient, namespace, sbName)
			if err != nil {
				klog.Warningf("Failed to check status of running sandbox %s during reconcile: %v", sbName, err)
			} else if !running {
				klog.Infof("Sandbox '%s' task completed or stopped running; annotation updated.", sbName)
			}
		}
	}
}

type QueueTask struct {
	Type         string    `yaml:"type"` // "issue-fix", "pr-investigate", "pr-comments", "pr-iterate", "pr-review", "agent-chore"
	URL          string    `yaml:"url"`
	Number       int       `yaml:"number"`
	Priority     string    `yaml:"priority"` // "critical", "urgent", "important", "high", "medium", "low"
	Phase        int       `yaml:"phase"`    // 1: Rebase/iterate, 2: Comments, 3: Investigate/Fix, 4: Chores
	CreatedAt    time.Time `yaml:"createdAt"`
	Assignee     string    `yaml:"assignee,omitempty"`
	Status       string    `yaml:"status"` // "Pending", "Running", "Completed", "Failed"
	Error        string    `yaml:"error,omitempty"`
	AgentFile    string    `yaml:"agentFile,omitempty"` // For chore tasks
	SessionID    string    `yaml:"sessionId,omitempty"` // For workflow sessions
	CommitSHA    string    `yaml:"commitSHA,omitempty"`
	Instructions []string  `yaml:"instructions,omitempty"`
	Recovered    bool      `yaml:"recovered,omitempty"`
	CompletedAt  time.Time `yaml:"completedAt,omitempty"`
}

type ChoreRunState struct {
	LastRun time.Time `json:"lastRun"`
}

var cronParser = cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow | cron.Descriptor)

func shouldRunChore(schedule string, lastRun time.Time) bool {
	return shouldRunChoreAt(schedule, lastRun, time.Now())
}

func shouldRunChoreAt(schedule string, lastRun time.Time, now time.Time) bool {
	schedule = strings.TrimSpace(schedule)
	if strings.ToLower(schedule) == "never" || strings.ToLower(schedule) == "paused" {
		return false
	}

	if lastRun.IsZero() {
		return true
	}

	sched, err := cronParser.Parse(schedule)
	if err != nil {
		klog.Warningf("Failed to parse cron expression %q: %v, falling back to 24h", schedule, err)
		return now.Sub(lastRun) >= 24*time.Hour
	}

	nextRun := sched.Next(lastRun)
	return !nextRun.After(now)
}

func writeTaskAtomically(dir string, filename string, task *QueueTask) error {
	data, err := yaml.Marshal(task)
	if err != nil {
		return fmt.Errorf("marshaling task to YAML: %w", err)
	}

	tempFile := filepath.Join(dir, fmt.Sprintf(".temp-%s", filename))
	if err := os.WriteFile(tempFile, data, 0644); err != nil {
		return fmt.Errorf("writing temp task file: %w", err)
	}

	targetFile := filepath.Join(dir, filename)
	if err := os.Rename(tempFile, targetFile); err != nil {
		os.Remove(tempFile)
		return fmt.Errorf("renaming temp file to target %s: %w", targetFile, err)
	}

	return nil
}

func taskExists(incomingDir, processingDir, filename string) bool {
	if _, err := os.Stat(filepath.Join(incomingDir, filename)); err == nil {
		return true
	}
	if _, err := os.Stat(filepath.Join(processingDir, filename)); err == nil {
		return true
	}
	return false
}

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

func getIssuePriority(issue *githubv39.Issue) string {
	for _, l := range issue.Labels {
		name := l.GetName()
		if strings.HasPrefix(name, "priority/") {
			return strings.TrimPrefix(name, "priority/")
		}
	}
	return "medium"
}

func getPRPriority(prIssue *githubv39.Issue) string {
	return getIssuePriority(prIssue)
}

var workflowURLRegex = regexp.MustCompile(`(?:\s|^)(https?://[^\s\)]+(?:\.(?:md|txt|yaml)|/(?:workflows|agents)/)[^\s\)]*)`)

var workflowFileRegex = regexp.MustCompile(`(?:\s|^)(\.?\.?/?(?:\.?agents?|\.gemini)/[a-zA-Z0-9_\-\./]+)\b`)

func findWorkflowPath(body string) string {
	urlMatch := workflowURLRegex.FindStringSubmatch(body)
	if len(urlMatch) > 1 {
		return strings.TrimSpace(urlMatch[1])
	}

	matches := workflowFileRegex.FindStringSubmatch(body)
	if len(matches) > 1 {
		return matches[1]
	}
	return ""
}

func isWorkflowDefinition(ctx context.Context, ghClient *githubv39.Client, owner, repo, path string) bool {
	if strings.HasPrefix(path, "http://") || strings.HasPrefix(path, "https://") {
		// 1. Path/URL convention check
		if strings.Contains(path, "/workflows/") || strings.Contains(path, "/agents/") {
			return true
		}

		// 2. Download and verify headers
		content, err := fetchWorkflowContent(ctx, ghClient, path)
		if err != nil {
			klog.V(4).Infof("Failed to fetch content from workflow URL %s: %v", path, err)
			return false
		}

		limit := 2000
		if len(content) < limit {
			limit = len(content)
		}
		header := string(content[:limit])
		if strings.Contains(header, "mode: workflow") || strings.Contains(header, "mode: \"workflow\"") || strings.Contains(header, "AGENT_MODE=workflow") {
			return true
		}
		return false
	}

	// 1. Directory convention: any path containing "/workflows/" is treated as a workflow
	if strings.Contains(path, "/workflows/") {
		return true
	}

	// Clean up leading dot slashes from path to match GitHub API format
	cleanPath := strings.TrimPrefix(path, "./")
	cleanPath = strings.TrimPrefix(cleanPath, "/")

	// 2. Fetch remote content from GitHub and search for keywords/metadata
	fileContent, _, _, err := ghClient.Repositories.GetContents(ctx, owner, repo, cleanPath, &githubv39.RepositoryContentGetOptions{})
	if err != nil {
		klog.V(4).Infof("Failed to get content for %s: %v", cleanPath, err)
		return false
	}
	if fileContent == nil {
		klog.V(4).Infof("Content is nil for %s (possibly a directory or submodule)", cleanPath)
		return false
	}
	content, err := fileContent.GetContent()
	if err != nil {
		return false
	}

	limit := 2000
	if len(content) < limit {
		limit = len(content)
	}
	header := content[:limit]

	// Look for mode: workflow metadata in header or front-matter
	if strings.Contains(header, "mode: workflow") || strings.Contains(header, "mode: \"workflow\"") || strings.Contains(header, "AGENT_MODE=workflow") {
		return true
	}

	return false
}

func getWorkflowCooldown(ctx context.Context, ghClient *githubv39.Client, owner, repo, path string) time.Duration {
	defaultCooldown := 10 * time.Minute
	if path == "" {
		return defaultCooldown
	}

	var content []byte
	var err error
	if strings.HasPrefix(path, "http://") || strings.HasPrefix(path, "https://") {
		content, err = fetchWorkflowContent(ctx, ghClient, path)
	} else {
		cleanPath := strings.TrimPrefix(path, "./")
		cleanPath = strings.TrimPrefix(cleanPath, "/")
		var fileContent *githubv39.RepositoryContent
		fileContent, _, _, err = ghClient.Repositories.GetContents(ctx, owner, repo, cleanPath, &githubv39.RepositoryContentGetOptions{})
		if err == nil {
			var contentStr string
			contentStr, err = fileContent.GetContent()
			content = []byte(contentStr)
		}
	}
	if err != nil {
		return defaultCooldown
	}

	agentDef, err := ParseAgent(content)
	if err != nil || agentDef.Cooldown == "" {
		return defaultCooldown
	}

	d, err := time.ParseDuration(agentDef.Cooldown)
	if err != nil {
		klog.Warningf("Failed to parse workflow cooldown duration %q: %v", agentDef.Cooldown, err)
		return defaultCooldown
	}
	return d
}

func queueIssueTasks(ctx context.Context, ghClient *githubv39.Client, kubeClient *clients.KubernetesClient, cfg *config.FactoryConfig, owner, repo string, issues []*githubv39.Issue, processedIssues map[int]time.Time, refIssues map[int]bool, targetAssignee string, allBotUsers []string, incomingDir, processingDir, processedDir, queueDir string, dryRun bool, triggerLabel string) {
	klog.Infof("queueIssueTasks called with %d issues", len(issues))
	for _, issue := range issues {
		num := issue.GetNumber()
		if cfg != nil && cfg.MinNumber > 0 && num < cfg.MinNumber {
			continue
		}
		if hasStopLabel(issue.Labels, triggerLabel) {
			klog.Infof("Skipping issue #%d because it has the stop label ('overseer/stop' or '%s/stop')", num, triggerLabel)
			removePendingTasksForNumber(incomingDir, num)
			continue
		}
		if refIssues[num] {
			klog.Infof("Skipping issue #%d because there is already a PR referencing it.", num)
			continue
		}

		// Check if the issue specifies a workflow path in its description
		workflowPath := findWorkflowPath(issue.GetBody())
		workflowName := ""
		if workflowPath != "" {
			if isWorkflowDefinition(ctx, ghClient, owner, repo, workflowPath) {
				filenameOnly := filepath.Base(workflowPath)
				ext := filepath.Ext(filenameOnly)
				workflowName = strings.TrimSuffix(filenameOnly, ext)
			} else {
				// It was just a standard skill/agent prompt mentioned, not a workflow.
				// Fallback to standard issue-fix
				workflowPath = ""
			}
		}

		filename := fmt.Sprintf("task-issue-%d.yaml", num)
		if workflowName != "" {
			filename = fmt.Sprintf("task-workflow-%s-issue-%d.yaml", Slugify(workflowName), num)
		}

		if taskExists(incomingDir, processingDir, filename) {
			continue
		}

		// Check if the workflow session already completed recently
		processedPath := filepath.Join(processedDir, filename)
		if info, err := os.Stat(processedPath); err == nil {
			lastRunTime := info.ModTime()
			if data, err := os.ReadFile(processedPath); err == nil {
				var t QueueTask
				if err := yaml.Unmarshal(data, &t); err == nil && !t.CompletedAt.IsZero() {
					lastRunTime = t.CompletedAt
				}
			}
			cooldown := getWorkflowCooldown(ctx, ghClient, owner, repo, workflowPath)
			if time.Since(lastRunTime) < cooldown {
				continue
			}
		}

		lastProcessed, ok := processedIssues[num]
		if !ok || issue.GetUpdatedAt().After(lastProcessed) || workflowName != "" {
			// Skip KRM check for workflow triggers since they don't necessarily have linked code PRs
			if workflowName == "" {
				linked, err := hasLinkedPR(ctx, ghClient, owner, repo, num)
				if err != nil {
					klog.Errorf("Failed to check linked PR for issue #%d: %v", num, err)
					continue
				} else if linked {
					klog.Infof("Skipping issue #%d because it has a linked PR according to the Timeline API.", num)
					continue
				}
			}

			sandboxName := fmt.Sprintf("fix-%s-%d", repo, num)
			if workflowName != "" {
				sandboxName = fmt.Sprintf("wf-issue-%d", num)
			}

			running, err := isSandboxTaskRunning(ctx, kubeClient, rootFlags.Namespace, sandboxName)
			if err != nil {
				klog.Errorf("Failed to check if sandbox %s is running: %v", sandboxName, err)
				continue
			} else if running {
				klog.Infof("Skipping issue #%d because there is an in-flight sandbox %s.", num, sandboxName)
				continue
			}

			hasTriggerLabel := false
			for _, label := range issue.Labels {
				if strings.EqualFold(label.GetName(), triggerLabel) {
					hasTriggerLabel = true
					break
				}
			}
			if !hasTriggerLabel {
				if dryRun {
					fmt.Printf("[DRYRUN] Would add label '%s' to issue #%d\n", triggerLabel, num)
				} else {
					klog.Infof("Adding '%s' label to issue #%d", triggerLabel, num)
					if _, _, err := ghClient.Issues.AddLabelsToIssue(ctx, owner, repo, num, []string{triggerLabel}); err != nil {
						klog.Errorf("Failed to add label '%s' to issue #%d: %v", triggerLabel, num, err)
					}
				}
			}

			taskType := "issue-fix"
			if workflowName != "" {
				taskType = "agent-chore"
			}

			taskAssignee, err := selectUserForTask(ctx, ghClient, kubeClient, cfg, taskType, num, owner, repo)
			if err != nil {
				klog.Errorf("Failed to select user for issue #%d: %v", num, err)
				taskAssignee = targetAssignee
			}
			if taskAssignee == "" {
				taskAssignee = targetAssignee
			}

			issueURL := fmt.Sprintf("https://github.com/%s/%s/issues/%d", owner, repo, num)
			var task *QueueTask
			if workflowName != "" {
				task = &QueueTask{
					Type:      "agent-chore",
					URL:       issueURL,
					Number:    num,
					Priority:  getIssuePriority(issue),
					Phase:     4,
					CreatedAt: issue.GetCreatedAt(),
					Assignee:  taskAssignee,
					Status:    "Pending",
					AgentFile: workflowPath,
					SessionID: fmt.Sprintf("issue-%d", num),
				}
			} else {
				task = &QueueTask{
					Type:      "issue-fix",
					URL:       issueURL,
					Number:    num,
					Priority:  getIssuePriority(issue),
					Phase:     3,
					CreatedAt: issue.GetCreatedAt(),
					Assignee:  taskAssignee,
					Status:    "Pending",
				}
			}

			if dryRun {
				if workflowName != "" {
					fmt.Printf("[DRYRUN] Would queue workflow task %s for issue #%d: %s\n", workflowName, num, issueURL)
				} else {
					fmt.Printf("[DRYRUN] Would queue fix task for issue #%d: %s\n", num, issueURL)
				}
			} else {
				if workflowName != "" {
					fmt.Printf("Queueing workflow task %s for issue #%d...\n", workflowName, num)
				} else {
					fmt.Printf("Queueing fix task for issue #%d...\n", num)
				}
				processedIssues[num] = time.Now()
				if err := writeTaskAtomically(incomingDir, filename, task); err != nil {
					klog.Errorf("Failed to queue task for issue #%d: %v", num, err)
				} else {
					writeTaskJournalEvent(queueDir, filename, task, "Created", 0)
				}
			}
		}
	}
}

func scanChores(ctx context.Context, ghClient *githubv39.Client, owner, repo, incomingDir, processingDir, queueDir string, dryRun bool) {
	_, directoryContent, _, err := ghClient.Repositories.GetContents(ctx, owner, repo, ".agents", &githubv39.RepositoryContentGetOptions{})
	if err != nil {
		if !strings.Contains(err.Error(), "404") {
			klog.Errorf("Failed to list .agents directory: %v", err)
		}
		return
	}

	choresStatePath := filepath.Join(queueDir, "chores_state.json")
	choresState := make(map[string]ChoreRunState)
	if data, err := os.ReadFile(choresStatePath); err == nil {
		_ = json.Unmarshal(data, &choresState)
	}

	stateChanged := false

	for _, file := range directoryContent {
		if file.GetType() == "file" && (strings.HasSuffix(file.GetName(), ".yaml") || strings.HasSuffix(file.GetName(), ".md")) {
			fileContent, _, _, err := ghClient.Repositories.GetContents(ctx, owner, repo, ".agents/"+file.GetName(), &githubv39.RepositoryContentGetOptions{})
			if err != nil {
				klog.Errorf("Failed to fetch chore file %s: %v", file.GetName(), err)
				continue
			}
			contentStr, err := fileContent.GetContent()
			if err != nil {
				klog.Errorf("Failed to decode chore file %s: %v", file.GetName(), err)
				continue
			}

			agentDef, err := ParseAgent([]byte(contentStr))
			if err != nil {
				klog.Errorf("Failed to parse chore agent %s: %v", file.GetName(), err)
				continue
			}

			if agentDef.Schedule == "" {
				continue
			}

			filename := fmt.Sprintf("task-chore-%s.yaml", Slugify(agentDef.Name))
			if taskExists(incomingDir, processingDir, filename) {
				continue
			}

			lastRun := choresState[agentDef.Name].LastRun
			if shouldRunChore(agentDef.Schedule, lastRun) {
				task := &QueueTask{
					Type:      "agent-chore",
					URL:       fmt.Sprintf("https://github.com/%s/%s", owner, repo),
					Priority:  "medium",
					Phase:     4,
					CreatedAt: time.Now(),
					Status:    "Pending",
					AgentFile: ".agents/" + file.GetName(),
				}

				if dryRun {
					fmt.Printf("[DRYRUN] Would queue chore agent task %s (schedule: %s)\n", agentDef.Name, agentDef.Schedule)
				} else {
					fmt.Printf("Queueing chore agent task %s...\n", agentDef.Name)
					if err := writeTaskAtomically(incomingDir, filename, task); err != nil {
						klog.Errorf("Failed to queue chore task %s: %v", agentDef.Name, err)
					} else {
						choresState[agentDef.Name] = ChoreRunState{LastRun: time.Now()}
						stateChanged = true
						writeTaskJournalEvent(queueDir, filename, task, "Created", 0)
					}
				}
			}
		}
	}

	if stateChanged && !dryRun {
		if data, err := json.MarshalIndent(choresState, "", "  "); err == nil {
			_ = os.WriteFile(choresStatePath, data, 0644)
		}
	}
}

func addGitHubComment(ctx context.Context, client *githubv39.Client, owner, repo string, number int, body string) {
	comment := &githubv39.IssueComment{
		Body: githubv39.String(body),
	}
	_, _, err := client.Issues.CreateComment(ctx, owner, repo, number, comment)
	if err != nil {
		klog.Errorf("Failed to create GitHub comment on #%d: %v", number, err)
	}
}

type JournalEvent struct {
	Timestamp      time.Time `json:"timestamp"`
	TaskID         string    `json:"taskId"`
	Event          string    `json:"event"`
	Type           string    `json:"type"`
	URL            string    `json:"url"`
	Priority       string    `json:"priority"`
	Error          string    `json:"error,omitempty"`
	DurationSecond float64   `json:"durationSeconds,omitempty"`
}

func writeTaskJournalEvent(queueDir string, taskFilename string, task *QueueTask, event string, duration time.Duration) {
	journalPath := filepath.Join(queueDir, "journal.jsonl")
	f, err := os.OpenFile(journalPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		klog.Errorf("Failed to open journal file: %v", err)
		return
	}
	defer f.Close()

	je := JournalEvent{
		Timestamp: time.Now(),
		TaskID:    strings.TrimSuffix(taskFilename, ".yaml"),
		Event:     event,
		Type:      task.Type,
		URL:       task.URL,
		Priority:  task.Priority,
		Error:     task.Error,
	}
	if duration > 0 {
		je.DurationSecond = duration.Seconds()
	}

	data, err := json.Marshal(je)
	if err != nil {
		klog.Errorf("Failed to marshal journal event: %v", err)
		return
	}

	if _, err := f.Write(append(data, '\n')); err != nil {
		klog.Errorf("Failed to write journal event: %v", err)
	}
}

func runWatch(ctx context.Context, owner, repo string, interval time.Duration, assignee string, assigneeChanged bool, labels []string, dryRun bool, watchTimeout time.Duration, maxActions int, maxPending int, mode string, queueDir string, once bool, issueMode string, prMode string, choresMode string, ephemeralStorage string, secrets []factorysandbox.SecretMount, scanLimit int, taskTimeout time.Duration, sandboxEvictionAge string, sandboxIdleTimeout time.Duration) error {
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

	secret, err := kubeClient.Clientset.CoreV1().Secrets(rootFlags.Namespace).Get(ctx, rootFlags.SecretName, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("fetching %s secret in namespace %s: %w (make sure to run 'factory user onboard' first)", rootFlags.SecretName, rootFlags.Namespace, err)
	}
	githubLogin := string(secret.Data[KeyGithubLogin])

	targetAssignee := assignee
	if !assigneeChanged {
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

	incomingDir := filepath.Join(queueDir, "incoming")
	processingDir := filepath.Join(queueDir, "processing")
	processedDir := filepath.Join(queueDir, "processed")

	logDir := os.Getenv("FACTORY_LOGS")
	if logDir == "" {
		logDir = filepath.Join(queueDir, "logs")
	}
	processingLogDir := filepath.Join(logDir, "processing")
	processedLogDir := filepath.Join(logDir, "processed")

	if !dryRun {
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
	}

	fmt.Printf("Starting watch for repository %s/%s (mode: %s, queueDir: %s, poll interval: %s, assignee: '%s', labels: %v, dryRun: %v, watchTimeout: %s)...\n", owner, repo, mode, queueDir, interval, targetAssignee, labels, dryRun, watchTimeout)

	var timeoutChan <-chan time.Time
	if watchTimeout > 0 {
		timeoutChan = time.After(watchTimeout)
	}

	type prWatchState struct {
		lastSHA                  string
		lastInvestigatedTime     time.Time
		lastCommentAddressedTime time.Time
		lastReviewedSHA          string
		unassignedSHA            string
	}

	processedIssues := make(map[int]time.Time)
	processedPRs := make(map[int]prWatchState)
	if files, err := os.ReadDir(processedDir); err == nil {
		for _, f := range files {
			if f.IsDir() || !strings.HasSuffix(f.Name(), ".yaml") {
				continue
			}
			filePath := filepath.Join(processedDir, f.Name())
			if strings.HasPrefix(f.Name(), "task-issue-") {
				trimmed := strings.TrimPrefix(f.Name(), "task-issue-")
				trimmed = strings.TrimSuffix(trimmed, ".yaml")
				if num, err := strconv.Atoi(trimmed); err == nil {
					var t QueueTask
					hasTask := false
					if data, err := os.ReadFile(filePath); err == nil {
						if err := yaml.Unmarshal(data, &t); err == nil {
							hasTask = true
						}
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
				// Format could be task-pr-%d-comments.yaml or task-pr-%d-investigate.yaml
				name := strings.TrimPrefix(f.Name(), "task-pr-")
				name = strings.TrimSuffix(name, ".yaml")

				isComments := strings.HasSuffix(name, "-comments")
				isInvestigate := strings.HasSuffix(name, "-investigate")

				var numStr string
				if isComments {
					numStr = strings.TrimSuffix(name, "-comments")
				} else if isInvestigate {
					numStr = strings.TrimSuffix(name, "-investigate")
				}

				if numStr != "" {
					if num, err := strconv.Atoi(numStr); err == nil {
						state := processedPRs[num]
						var t QueueTask
						hasTask := false
						if data, err := os.ReadFile(filePath); err == nil {
							if err := yaml.Unmarshal(data, &t); err == nil {
								hasTask = true
								if t.CommitSHA != "" {
									state.lastSHA = t.CommitSHA
								}
							}
						}

						if info, err := f.Info(); err == nil {
							tTime := info.ModTime()
							if hasTask && !t.CompletedAt.IsZero() {
								tTime = t.CompletedAt
							}
							if isComments {
								state.lastCommentAddressedTime = tTime
							} else if isInvestigate {
								state.lastInvestigatedTime = tTime
							}
						}

						processedPRs[num] = state
					}
				}
			}
		}
	}

	// Recovery: Move any stuck tasks from processingDir back to incomingDir on startup
	if files, err := os.ReadDir(processingDir); err == nil {
		for _, f := range files {
			if !f.IsDir() && strings.HasPrefix(f.Name(), "task-") && strings.HasSuffix(f.Name(), ".yaml") {
				processingPath := filepath.Join(processingDir, f.Name())

				// Read the task to reset its status to Pending
				if data, err := os.ReadFile(processingPath); err == nil {
					var t QueueTask
					if err := yaml.Unmarshal(data, &t); err == nil {
						t.Status = "Pending"
						t.Recovered = true
						if err := writeTaskAtomically(incomingDir, f.Name(), &t); err == nil {
							_ = os.Remove(processingPath)
							klog.Infof("Recovered stuck task %s from processing to incoming", f.Name())
							continue
						}
					}
				}

				// Fallback to simple rename if parsing fails
				incomingPath := filepath.Join(incomingDir, f.Name())
				if err := os.Rename(processingPath, incomingPath); err == nil {
					klog.Infof("Recovered stuck task %s (fallback rename) to incoming", f.Name())
				} else {
					klog.Errorf("Failed to recover stuck task %s: %v", f.Name(), err)
				}
			}
		}
	}

	type watchState struct {
		mu               sync.Mutex
		openPRs          []*githubv39.PullRequest
		referencedIssues map[int]bool
		lastPRScan       time.Time
		lastIssueScan    time.Time
		lastRunnerRun    time.Time
		shuttingDown     bool
	}

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
			podList, err := kubeClient.Clientset.CoreV1().Pods(rootFlags.Namespace).List(ctx, metav1.ListOptions{LabelSelector: "sandbox"})
			if err != nil {
				return
			}
			for i := range podList.Items {
				pod := &podList.Items[i]
				if pod.DeletionTimestamp == nil && pod.Status.Phase == corev1.PodFailed && strings.EqualFold(pod.Status.Reason, "Evicted") {
					klog.Infof("Found evicted sandbox pod %s in namespace %s. Deleting pod so controller can recreate or clean up.", pod.Name, rootFlags.Namespace)
					sbName := pod.Labels["sandbox"]
					if sbName == "" {
						sbName = pod.Labels["agents.x-k8s.io/sandbox"]
					}
					if sbName != "" {
						_ = factorysandbox.IncrementSandboxEvictionCount(ctx, kubeClient, rootFlags.Namespace, sbName)
					}
					_ = kubeClient.Clientset.CoreV1().Pods(rootFlags.Namespace).Delete(ctx, pod.Name, metav1.DeleteOptions{})
				}
			}
		}()

		reconcileRunningSandboxes(ctx, kubeClient, rootFlags.Namespace)

		if isDoNotProcess(queueDir) {
			runningCount, err := countRunningSandboxTasks(ctx, kubeClient, rootFlags.Namespace)
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

		processPRsFunc := func(prIssues []*githubv39.Issue) {
			if prMode == "disabled" {
				return
			}
			for _, prIssue := range prIssues {
				num := prIssue.GetNumber()
				if cfg != nil && cfg.MinNumber > 0 && num < cfg.MinNumber {
					continue
				}
				if hasStopLabel(prIssue.Labels, triggerLabel) {
					klog.Infof("Skipping PR #%d because it has the stop label ('overseer/stop' or '%s/stop')", num, triggerLabel)
					removePendingTasksForNumber(incomingDir, num)
					continue
				}
				pr, _, err := ghClient.PullRequests.Get(ctx, owner, repo, num)
				if err != nil {
					klog.Errorf("Failed to fetch full PR #%d: %v", num, err)
					continue
				}

				// Verify PR Author: Only process PRs created by any bot in the pool
				author := pr.GetUser().GetLogin()
				isBotPR := false
				for _, bot := range allBotUsers {
					if strings.EqualFold(author, bot) {
						isBotPR = true
						break
					}
				}
				if !isBotPR {
					klog.Infof("Skipping PR #%d because it was created by %s (not in our bot pool). We do not have permission to push to external forks.", num, author)
					continue
				}

				// Sync labels from referenced parent issues to the PR
				syncReferencedIssueLabels(ctx, ghClient, owner, repo, pr, prIssue)
				if hasStopLabel(prIssue.Labels, triggerLabel) {
					klog.Infof("Skipping PR #%d after label sync because it has the stop label ('overseer/stop' or '%s/stop')", num, triggerLabel)
					removePendingTasksForNumber(incomingDir, num)
					continue
				}

				headSHA := pr.GetHead().GetSHA()

				// Fetch PR commits to find the last commit timestamp
				prCommits, err := github.ListAllCommits(ctx, ghClient, owner, repo, num)
				var lastCommitTime time.Time
				if err == nil {
					for _, c := range prCommits {
						if c.GetCommit().GetCommitter().GetDate().After(lastCommitTime) {
							lastCommitTime = c.GetCommit().GetCommitter().GetDate()
						}
					}
				}

				// Fetch all PR comments (handling pagination)
				comments, listCommentsErr := github.ListAllIssueComments(ctx, ghClient, owner, repo, num)

				// Check Phase 1: Rebase/Conflicts
				isConflicting := pr.Mergeable != nil && !*pr.Mergeable

				if isConflicting {
					filename := fmt.Sprintf("task-pr-%d-iterate.yaml", num)
					if !taskExists(incomingDir, processingDir, filename) {
						sandboxName := resolveSandboxName(ctx, kubeClient, ghClient, "pr-iterate", num, owner, repo)
						running, err := isSandboxTaskRunning(ctx, kubeClient, rootFlags.Namespace, sandboxName)
						if err != nil {
							klog.Errorf("Failed to check if sandbox %s is running: %v", sandboxName, err)
							continue
						} else if running {
							klog.Infof("Skipping PR #%d rebase because there is an in-flight sandbox %s.", num, sandboxName)
						} else {
							assignedBot := assignedBotUser(prIssue, allBotUsers)

							taskAssignee := assignedBot
							if taskAssignee == "" {
								taskAssignee = author
							}

							prURL := fmt.Sprintf("https://github.com/%s/%s/pull/%d", owner, repo, num)
							task := &QueueTask{
								Type:      "pr-iterate",
								URL:       prURL,
								Number:    num,
								Priority:  getPRPriority(prIssue),
								Phase:     1,
								CreatedAt: pr.GetCreatedAt(),
								Assignee:  taskAssignee,
								Status:    "Pending",
								CommitSHA: headSHA,
							}

							if dryRun {
								fmt.Printf("[DRYRUN] Would queue rebase task for PR #%d: %s\n", num, prURL)
							} else {
								fmt.Printf("Queueing rebase task for PR #%d...\n", num)
								if err := writeTaskAtomically(incomingDir, filename, task); err != nil {
									klog.Errorf("Failed to queue rebase task for PR #%d: %v", num, err)
								} else {
									writeTaskJournalEvent(queueDir, filename, task, "Created", 0)
								}
							}
						}
					}
					// If conflicting, we prioritize rebase and skip other PR checks for this PR in this loop
					continue
				}

				// Check CI Check Failures
				hasFailure := false
				checkRuns, err := listAllCheckRuns(ctx, ghClient, owner, repo, headSHA)
				if err == nil {
					for _, run := range checkRuns {
						c := run.GetConclusion()
						if c == "failure" || c == "timed_out" || c == "cancelled" {
							hasFailure = true
							break
						}
					}
				}

				statuses, _, err := ghClient.Repositories.ListStatuses(ctx, owner, repo, headSHA, nil)
				if err == nil {
					for _, status := range statuses {
						if status.GetState() == "failure" || status.GetState() == "error" {
							hasFailure = true
							break
						}
					}
				}

				state := processedPRs[num]

				assignedBot := assignedBotUser(prIssue, allBotUsers)
				isExplicitlyAssigned := assignedBot != "" && state.unassignedSHA != headSHA

				if state.lastSHA != "" && state.lastSHA != headSHA {
					if shouldUnassignStaleBot(state.lastSHA, state.unassignedSHA, headSHA, assignedBot) {
						if dryRun {
							fmt.Printf("[DRYRUN] Would unassign stale bot %s from PR #%d due to new commit %s\n", assignedBot, num, headSHA)
						} else {
							fmt.Printf("Unassigning stale bot %s from PR #%d due to new commit %s...\n", assignedBot, num, headSHA)
							if _, _, err := ghClient.Issues.RemoveAssignees(ctx, owner, repo, num, []string{assignedBot}); err != nil {
								klog.Errorf("Failed to unassign stale bot %s from PR #%d: %v", assignedBot, num, err)
							}
							state.unassignedSHA = headSHA
							processedPRs[num] = state
							isExplicitlyAssigned = false
							assignedBot = ""
						}
					}
					// Remove the giving up label if present
					hasGivingUpLabel := false
					for _, l := range prIssue.Labels {
						if l.GetName() == "overseer/giving-up" {
							hasGivingUpLabel = true
							break
						}
					}
					if hasGivingUpLabel {
						if dryRun {
							fmt.Printf("[DRYRUN] Would remove giving up label from PR #%d due to new commit %s\n", num, headSHA)
						} else {
							fmt.Printf("Removing giving up label from PR #%d due to new commit %s...\n", num, headSHA)
							if _, err := ghClient.Issues.RemoveLabelForIssue(ctx, owner, repo, num, "overseer/giving-up"); err != nil {
								klog.Errorf("Failed to remove giving up label from PR #%d: %v", num, err)
							}
						}
					}
				}

				if hasFailure {
					filename := fmt.Sprintf("task-pr-%d-investigate.yaml", num)
					if !taskExists(incomingDir, processingDir, filename) {
						investigationCount := 0
						if listCommentsErr == nil {
							var bots []string
							if cfg != nil {
								bots = cfg.AllowlistedBots
							}
							investigationCount = getInvestigationCount(comments, lastCommitTime, allBotUsers, githubLogin, bots)
						}

						if investigationCount >= 3 {
							stopLabel := getStopLabel(triggerLabel)
							if !dryRun {
								addGitHubComment(ctx, ghClient, owner, repo, num, fmt.Sprintf("🤖 AI Factory has attempted to investigate/fix CI check failures for this pull request 3 times since the last commit or update without success. To prevent infinite loops, I am pausing automated investigation and attaching the `%s` label.\n\nTo request another attempt or resume automated processing, please remove the `%s` label from this pull request (and/or push a new commit or leave a comment).", stopLabel, stopLabel))
								if _, _, err := ghClient.Issues.AddLabelsToIssue(ctx, owner, repo, num, []string{stopLabel}); err != nil {
									klog.Errorf("Failed to add stop label '%s' to PR #%d: %v", stopLabel, num, err)
								}
							}
							klog.Infof("Skipping PR #%d investigate because it has reached the maximum retry limit (3 attempts since last update) and applying stop label '%s'.", num, stopLabel)
						} else {
							prevFailed := false
							processedPath := filepath.Join(processedDir, filename)
							if data, err := os.ReadFile(processedPath); err == nil {
								var t QueueTask
								if err := yaml.Unmarshal(data, &t); err == nil {
									if t.Status == "Failed" {
										prevFailed = true
									}
								}
							}

							if state.lastSHA != headSHA || prevFailed || isExplicitlyAssigned || time.Since(state.lastInvestigatedTime) > 2*time.Hour {
								sandboxName := resolveSandboxName(ctx, kubeClient, ghClient, "pr-investigate", num, owner, repo)
								running, err := isSandboxTaskRunning(ctx, kubeClient, rootFlags.Namespace, sandboxName)
								if err != nil {
									klog.Errorf("Failed to check if sandbox %s is running: %v", sandboxName, err)
								} else if running {
									klog.Infof("Skipping PR #%d investigate because there is an in-flight sandbox %s.", num, sandboxName)
								} else {
									assignedBot := assignedBotUser(prIssue, allBotUsers)

									taskAssignee := assignedBot
									if taskAssignee == "" {
										taskAssignee = author
									}

									prURL := fmt.Sprintf("https://github.com/%s/%s/pull/%d", owner, repo, num)
									task := &QueueTask{
										Type:      "pr-investigate",
										URL:       prURL,
										Number:    num,
										Priority:  getPRPriority(prIssue),
										Phase:     3,
										CreatedAt: pr.GetCreatedAt(),
										Assignee:  taskAssignee,
										Status:    "Pending",
										CommitSHA: headSHA,
									}

									if dryRun {
										fmt.Printf("[DRYRUN] Would queue investigate task for PR #%d: %s\n", num, prURL)
									} else {
										fmt.Printf("Queueing investigate task for PR #%d...\n", num)
										state.lastSHA = headSHA
										state.lastInvestigatedTime = time.Now()
										processedPRs[num] = state
										if err := writeTaskAtomically(incomingDir, filename, task); err != nil {
											klog.Errorf("Failed to queue investigate task for PR #%d: %v", num, err)
										} else {
											writeTaskJournalEvent(queueDir, filename, task, "Created", 0)
										}
									}
								}
							}
						}
					}
				} else if state.lastSHA != headSHA {
					state.lastSHA = headSHA
					processedPRs[num] = state
				}

				// Check review comments and approvals
				var reviews []*githubv39.PullRequestReview
				if listReviews, err := github.ListAllReviews(ctx, ghClient, owner, repo, num); err == nil {
					reviews = listReviews
				}

				isApproved := isPRApprovedOrLGTM(pr, prIssue, reviews)

				if listCommentsErr == nil {
					hasNewComments := false

					var bots []string
					if cfg != nil {
						bots = cfg.AllowlistedBots
					}

					// Find the latest timestamp of any reply made by an allowlisted bot user
					var latestBotReplyTime time.Time
					for _, c := range comments {
						if isBotReply(c.GetUser(), githubLogin, bots) && c.GetCreatedAt().After(latestBotReplyTime) {
							latestBotReplyTime = c.GetCreatedAt()
						}
					}
					for _, r := range reviews {
						if isBotReply(r.GetUser(), githubLogin, bots) && r.GetSubmittedAt().After(latestBotReplyTime) {
							latestBotReplyTime = r.GetSubmittedAt()
						}
					}

					var unackCommentIDs []int64
					for _, c := range comments {
						if shouldIgnoreUser(c.GetUser(), githubLogin, bots) {
							continue
						}
						if strings.EqualFold(c.GetUser().GetLogin(), pr.GetUser().GetLogin()) {
							continue
						}
						if c.GetCreatedAt().After(lastCommitTime) && c.GetCreatedAt().After(state.lastCommentAddressedTime) && c.GetCreatedAt().After(latestBotReplyTime) {
							if hasIssueCommentReaction(ctx, ghClient, owner, repo, c.GetID(), "+1", true, bots, githubLogin) {
								continue
							}
							humanRocket := hasIssueCommentReaction(ctx, ghClient, owner, repo, c.GetID(), "rocket", false, bots, githubLogin)
							if !humanRocket && hasIssueCommentReaction(ctx, ghClient, owner, repo, c.GetID(), "eyes", true, bots, githubLogin) {
								continue
							}
							if !humanRocket && hasIssueCommentReaction(ctx, ghClient, owner, repo, c.GetID(), "confused", true, bots, githubLogin) {
								continue
							}
							hasNewComments = true
							unackCommentIDs = append(unackCommentIDs, c.GetID())
						}
					}

					// Also check inline PR review comments directly
					if !hasNewComments {
						for _, r := range reviews {
							if shouldIgnoreUser(r.GetUser(), githubLogin, bots) {
								if r.GetSubmittedAt().After(latestBotReplyTime) {
									latestBotReplyTime = r.GetSubmittedAt()
								}
								continue
							}
							if strings.EqualFold(r.GetUser().GetLogin(), pr.GetUser().GetLogin()) {
								continue
							}
							if r.GetSubmittedAt().After(lastCommitTime) && r.GetSubmittedAt().After(state.lastCommentAddressedTime) && r.GetSubmittedAt().After(latestBotReplyTime) {
								hasNewComments = true
								break
							}

							revComments, err := github.ListAllReviewComments(ctx, ghClient, owner, repo, num, r.GetID())
							if err == nil {
								for _, rc := range revComments {
									if shouldIgnoreUser(rc.GetUser(), githubLogin, bots) {
										if rc.GetCreatedAt().After(latestBotReplyTime) {
											latestBotReplyTime = rc.GetCreatedAt()
										}
										continue
									}
									if strings.EqualFold(rc.GetUser().GetLogin(), pr.GetUser().GetLogin()) {
										continue
									}
									if rc.GetCreatedAt().After(lastCommitTime) && rc.GetCreatedAt().After(state.lastCommentAddressedTime) && rc.GetCreatedAt().After(latestBotReplyTime) {
										hasNewComments = true
										break
									}
								}
							}
							if hasNewComments {
								break
							}
						}
					}

					if isApproved {
						klog.V(2).Infof("PR #%d is approved / LGTM'd", num)
					}

					if hasNewComments {
						if os.Getenv("DRY_RUN") == "true" {
							continue
						}
						filename := fmt.Sprintf("task-pr-%d-comments.yaml", num)
						if !taskExists(incomingDir, processingDir, filename) {
							sandboxName := resolveSandboxName(ctx, kubeClient, ghClient, "pr-comments", num, owner, repo)
							running, err := isSandboxTaskRunning(ctx, kubeClient, rootFlags.Namespace, sandboxName)
							if err != nil {
								klog.Errorf("Failed to check if sandbox %s is running: %v", sandboxName, err)
								continue
							} else if running {
								klog.Infof("Skipping PR #%d address-comments because there is an in-flight sandbox %s.", num, sandboxName)
							} else {
								taskAssignee := assignedBot
								if taskAssignee == "" {
									taskAssignee = author
								}

								prURL := fmt.Sprintf("https://github.com/%s/%s/pull/%d", owner, repo, num)
								task := &QueueTask{
									Type:      "pr-comments",
									URL:       prURL,
									Number:    num,
									Priority:  getPRPriority(prIssue),
									Phase:     2,
									CreatedAt: pr.GetCreatedAt(),
									Assignee:  taskAssignee,
									Status:    "Pending",
									CommitSHA: headSHA,
								}

								if dryRun {
									fmt.Printf("[DRYRUN] Would queue address-comments task for PR #%d: %s\n", num, prURL)
								} else {
									fmt.Printf("Queueing address-comments task for PR #%d...\n", num)
									for _, cid := range unackCommentIDs {
										addIssueCommentReaction(ctx, ghClient, owner, repo, cid, "eyes")
									}
									state.lastCommentAddressedTime = time.Now()
									processedPRs[num] = state
									if err := writeTaskAtomically(incomingDir, filename, task); err != nil {
										klog.Errorf("Failed to queue address-comments task for PR #%d: %v", num, err)
									} else {
										writeTaskJournalEvent(queueDir, filename, task, "Created", 0)
									}
								}
							}
						}
					} else if !hasFailure && !isApproved && state.lastReviewedSHA != headSHA && shouldAutoReviewPR(ctx, ghClient, owner, repo, pr, prIssue, triggerLabel) {
						hasBotReviewAfterLastCommit := false
						for _, r := range reviews {
							if isBotReply(r.GetUser(), githubLogin, bots) && (r.GetSubmittedAt().After(lastCommitTime) || r.GetCommitID() == headSHA) {
								hasBotReviewAfterLastCommit = true
								break
							}
						}

						if !hasBotReviewAfterLastCommit {
							if os.Getenv("DRY_RUN") == "true" {
								continue
							}
							filename := fmt.Sprintf("task-pr-%d-review.yaml", num)
							if !taskExists(incomingDir, processingDir, filename) {
								sandboxName := resolveSandboxName(ctx, kubeClient, ghClient, "pr-review", num, owner, repo)
								running, err := isSandboxTaskRunning(ctx, kubeClient, rootFlags.Namespace, sandboxName)
								if err != nil {
									klog.Errorf("Failed to check if sandbox %s is running: %v", sandboxName, err)
									continue
								} else if running {
									klog.Infof("Skipping PR #%d review because there is an in-flight sandbox %s.", num, sandboxName)
								} else {
									var bodies []string
									if pr.GetBody() != "" {
										bodies = append(bodies, pr.GetBody())
									}
									for refIssueNum := range getReferencedIssues(pr) {
										refIssue, _, err := ghClient.Issues.Get(ctx, owner, repo, refIssueNum)
										if err == nil && refIssue.GetBody() != "" {
											bodies = append(bodies, refIssue.GetBody())
										}
									}
									instructions := ExtractReviewInstructions(bodies...)

									prURL := fmt.Sprintf("https://github.com/%s/%s/pull/%d", owner, repo, num)
									task := &QueueTask{
										Type:         "pr-review",
										URL:          prURL,
										Number:       num,
										Priority:     getPRPriority(prIssue),
										Phase:        2,
										CreatedAt:    pr.GetCreatedAt(),
										Status:       "Pending",
										CommitSHA:    headSHA,
										Instructions: instructions,
									}

									if dryRun {
										fmt.Printf("[DRYRUN] Would queue review task for PR #%d: %s\n", num, prURL)
									} else {
										fmt.Printf("Queueing review task for PR #%d (Instructions: %d)...\n", num, len(instructions))
										state.lastReviewedSHA = headSHA
										processedPRs[num] = state
										if err := writeTaskAtomically(incomingDir, filename, task); err != nil {
											klog.Errorf("Failed to queue review task for PR #%d: %v", num, err)
										} else {
											writeTaskJournalEvent(queueDir, filename, task, "Created", 0)
										}
									}
								}
							}
						}
					}
				}
			}
		}

		// Determine what to run
		runIssueScan := false
		if mode == "all" || mode == "scan" || mode == "scan-issue" {
			if state.lastIssueScan.IsZero() || now.Sub(state.lastIssueScan) >= 30*time.Second {
				runIssueScan = true
			}
		}

		runPRScan := false
		if mode == "all" || mode == "scan" || mode == "scan-pr" {
			if state.lastPRScan.IsZero() || now.Sub(state.lastPRScan) >= 5*time.Minute {
				runPRScan = true
			}
		}

		runRunner := false
		if mode == "all" || mode == "run" {
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
			prOpts := &githubv39.PullRequestListOptions{
				State:       "open",
				ListOptions: githubv39.ListOptions{PerPage: 100},
			}
			prs, _, err := ghClient.PullRequests.List(ctx, owner, repo, prOpts)
			if err == nil {
				state.mu.Lock()
				state.openPRs = prs
				state.referencedIssues = make(map[int]bool)
				for _, pr := range prs {
					for num := range getReferencedIssues(pr) {
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
			prOpts := &githubv39.PullRequestListOptions{
				State:       "open",
				ListOptions: githubv39.ListOptions{PerPage: 100},
			}
			prs, _, err := ghClient.PullRequests.List(ctx, owner, repo, prOpts)
			if err == nil {
				state.mu.Lock()
				state.openPRs = prs
				state.referencedIssues = make(map[int]bool)
				for _, pr := range prs {
					for num := range getReferencedIssues(pr) {
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
			var slowIssues []*githubv39.Issue
			opts2 := &githubv39.IssueListByRepoOptions{
				Labels:      []string{triggerLabel},
				State:       "open",
				ListOptions: githubv39.ListOptions{PerPage: 100},
			}
			for {
				pageIssues, resp, err := ghClient.Issues.ListByRepo(ctx, owner, repo, opts2)
				if err != nil {
					klog.Errorf("Failed to list issues for label %s: %v", triggerLabel, err)
					break
				}
				for _, item := range pageIssues {
					if item.PullRequestLinks == nil {
						slowIssues = append(slowIssues, item)
					}
				}
				if resp.NextPage == 0 {
					break
				}
				opts2.Page = resp.NextPage
			}

			// Process slow issues
			if issueMode != "disabled" {
				queueIssueTasks(ctx, ghClient, kubeClient, cfg, owner, repo, slowIssues, processedIssues, refIssues, targetAssignee, allBotUsers, incomingDir, processingDir, processedDir, queueDir, dryRun, triggerLabel)
			}

			// Process Pull Requests (Scanner)
			var prIssues []*githubv39.Issue
			var allPRIssues []*githubv39.Issue
			for _, botUser := range allBotUsers {
				opts1 := &githubv39.IssueListByRepoOptions{
					Assignee:    botUser,
					State:       "open",
					ListOptions: githubv39.ListOptions{PerPage: 100},
				}
				for {
					iss1, resp, err := ghClient.Issues.ListByRepo(ctx, owner, repo, opts1)
					if err != nil {
						klog.Errorf("Failed to list PR issues for assignee %s: %v", botUser, err)
						break
					}
					for _, item := range iss1 {
						if item.PullRequestLinks != nil {
							allPRIssues = append(allPRIssues, item)
						}
					}
					if resp.NextPage == 0 {
						break
					}
					opts1.Page = resp.NextPage
				}
			}
			opts2PR := &githubv39.IssueListByRepoOptions{
				Labels:      []string{triggerLabel},
				State:       "open",
				ListOptions: githubv39.ListOptions{PerPage: 100},
			}
			for {
				iss2, resp, err := ghClient.Issues.ListByRepo(ctx, owner, repo, opts2PR)
				if err != nil {
					klog.Errorf("Failed to list PR issues for label %s: %v", triggerLabel, err)
					break
				}
				for _, item := range iss2 {
					if item.PullRequestLinks != nil {
						allPRIssues = append(allPRIssues, item)
					}
				}
				if resp.NextPage == 0 {
					break
				}
				opts2PR.Page = resp.NextPage
			}

			// Deduplicate allPRIssues
			uniquePRIssues := make(map[int]*githubv39.Issue)
			for _, item := range allPRIssues {
				uniquePRIssues[item.GetNumber()] = item
			}
			for _, item := range uniquePRIssues {
				prIssues = append(prIssues, item)
			}

			processPRsFunc(prIssues)

			// Scan chores
			if (mode == "all" || mode == "scan" || mode == "scan-pr") && choresMode != "disabled" {
				scanChores(ctx, ghClient, owner, repo, incomingDir, processingDir, queueDir, dryRun)
			}

			// Clean up sandboxes of merged or closed PRs
			if err := cleanupClosedPRSandboxes(ctx, ghClient, kubeClient, owner, repo, rootFlags.Namespace, dryRun); err != nil {
				klog.Errorf("Failed to clean up closed PR sandboxes: %v", err)
			}

			// Clean up sandboxes of closed issues
			if err := cleanupClosedIssueSandboxes(ctx, ghClient, kubeClient, owner, repo, rootFlags.Namespace, dryRun); err != nil {
				klog.Errorf("Failed to clean up closed issue sandboxes: %v", err)
			}

			// Clean up stale idle sandboxes older than eviction age (defaults to 1 week)
			if err := cleanupStaleIdleSandboxes(ctx, kubeClient, owner+"/"+repo, rootFlags.Namespace, sandboxEvictionAge, dryRun); err != nil {
				klog.Errorf("Failed to clean up stale idle sandboxes: %v", err)
			}

			if sandboxIdleTimeout > 0 {
				if _, err := factorysandbox.SuspendIdleSandboxes(ctx, kubeClient, rootFlags.Namespace, sandboxIdleTimeout, dryRun); err != nil {
					klog.Errorf("Failed to suspend idle sandboxes: %v", err)
				}
			}
		}

		// 2. Fast Issue Scan Cycle
		if runIssueScan {
			klog.Infof("Running fast issue scan cycle...")
			var allItems []*githubv39.Issue

			limit := scanLimit
			if limit <= 0 {
				limit = 30
			}

			for _, botUser := range allBotUsers {
				opts1 := &githubv39.IssueListByRepoOptions{
					Assignee:    botUser,
					State:       "open",
					Sort:        "updated",
					Direction:   "desc",
					ListOptions: githubv39.ListOptions{PerPage: limit},
				}
				issues1, _, err := ghClient.Issues.ListByRepo(ctx, owner, repo, opts1)
				if err != nil {
					klog.Errorf("Failed to list issues for assignee %s: %v", botUser, err)
				} else {
					klog.Infof("Fetched %d issues assigned to %s from GitHub API", len(issues1), botUser)
					allItems = append(allItems, issues1...)
				}
			}

			if githubLogin != "" {
				optsCreator := &githubv39.IssueListByRepoOptions{
					Creator:     githubLogin,
					State:       "open",
					Sort:        "updated",
					Direction:   "desc",
					ListOptions: githubv39.ListOptions{PerPage: limit},
				}
				issuesCreator, _, err := ghClient.Issues.ListByRepo(ctx, owner, repo, optsCreator)
				if err != nil {
					klog.Errorf("Failed to list issues created by %s: %v", githubLogin, err)
				} else {
					klog.Infof("Fetched %d issues created by %s from GitHub API", len(issuesCreator), githubLogin)
					for _, issue := range issuesCreator {
						if issue.PullRequestLinks != nil {
							continue
						}
						if hasStopLabel(issue.Labels, triggerLabel) {
							klog.Infof("Skipping auto labeling/assigning issue #%d because it has the stop label ('overseer/stop' or '%s/stop')", issue.GetNumber(), triggerLabel)
							continue
						}

						hasTriggerLabel := false
						for _, l := range issue.Labels {
							if strings.EqualFold(l.GetName(), triggerLabel) {
								hasTriggerLabel = true
								break
							}
						}

						hasAssignee := false
						for _, u := range issue.Assignees {
							for _, bot := range allBotUsers {
								if strings.EqualFold(u.GetLogin(), bot) {
									hasAssignee = true
									break
								}
							}
							if hasAssignee {
								break
							}
						}

						if !hasTriggerLabel || !hasAssignee {
							if dryRun {
								fmt.Printf("[DRYRUN] Would label issue #%d created by %s with '%s' and assign to %s\n", issue.GetNumber(), githubLogin, triggerLabel, targetAssignee)
							} else {
								fmt.Printf("Labelling issue #%d created by %s with '%s' and assigning to %s...\n", issue.GetNumber(), githubLogin, triggerLabel, targetAssignee)
								if !hasTriggerLabel {
									if _, _, err := ghClient.Issues.AddLabelsToIssue(ctx, owner, repo, issue.GetNumber(), []string{triggerLabel}); err != nil {
										klog.Errorf("Failed to add label '%s' to issue #%d: %v", triggerLabel, issue.GetNumber(), err)
									} else {
										issue.Labels = append(issue.Labels, &githubv39.Label{Name: githubv39.String(triggerLabel)})
									}
								}
								if !hasAssignee && targetAssignee != "" {
									if _, _, err := ghClient.Issues.AddAssignees(ctx, owner, repo, issue.GetNumber(), []string{targetAssignee}); err != nil {
										klog.Errorf("Failed to assign %s to issue #%d: %v", targetAssignee, issue.GetNumber(), err)
									} else {
										issue.Assignees = append(issue.Assignees, &githubv39.User{Login: githubv39.String(targetAssignee)})
									}
								}
							}
						}
						allItems = append(allItems, issue)
					}
				}
			}

			uniqueIssues := make(map[int]*githubv39.Issue)
			for _, item := range allItems {
				uniqueIssues[item.GetNumber()] = item
			}

			var issues []*githubv39.Issue
			var fastPRIssues []*githubv39.Issue
			for _, item := range uniqueIssues {
				if item.PullRequestLinks == nil {
					issues = append(issues, item)
				} else {
					fastPRIssues = append(fastPRIssues, item)
				}
			}

			if issueMode != "disabled" {
				queueIssueTasks(ctx, ghClient, kubeClient, cfg, owner, repo, issues, processedIssues, refIssues, targetAssignee, allBotUsers, incomingDir, processingDir, processedDir, queueDir, dryRun, triggerLabel)
			}

			// Process PRs assigned to the bot in the fast cycle
			if len(fastPRIssues) > 0 {
				klog.Infof("Processing %d assigned PRs in fast cycle...", len(fastPRIssues))
				processPRsFunc(fastPRIssues)
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

			var tasksToRun []struct {
				filename string
				task     *QueueTask
			}

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

			processingFiles, _ := os.ReadDir(processingDir)
			filesInProcessing := 0
			for _, f := range processingFiles {
				if !f.IsDir() && strings.HasPrefix(f.Name(), "task-") && strings.HasSuffix(f.Name(), ".yaml") {
					filesInProcessing++
				}
			}

			activeSandboxesInCycle := make(map[string]bool)

			for _, item := range tasksToRun {
				if isDoNotProcess(queueDir) {
					klog.Infof("[DO NOT PROCESS] Drain mode detected during cycle execution. Stopping scheduling of remaining queued tasks.")
					break
				}
				if actionsTaken >= maxActions {
					fmt.Printf("Reached maximum actions limit (%d) for this cycle. Stopping execution.\n", maxActions)
					break
				}

				runningCount, err := countRunningSandboxTasks(ctx, kubeClient, rootFlags.Namespace)
				if err != nil {
					klog.Errorf("Failed to count running sandbox tasks: %v", err)
				}
				activeCount := max(runningCount, filesInProcessing)

				if activeCount >= maxPending {
					fmt.Printf("Reached maximum pending sandboxes limit (%d). Skipping remaining queue items.\n", maxPending)
					break
				}

				filename := item.filename
				task := item.task

				sandboxName := resolveSandboxName(ctx, kubeClient, ghClient, task.Type, task.Number, owner, repo)
				if activeSandboxesInCycle[sandboxName] {
					klog.Infof("Skipping task %s because sandbox %s is already scheduled to run a task in this cycle.", filename, sandboxName)
					continue
				}

				running, err := isSandboxTaskRunning(ctx, kubeClient, rootFlags.Namespace, sandboxName)
				if err != nil {
					klog.Errorf("Failed to check if sandbox %s is running: %v", sandboxName, err)
					continue
				}
				if running {
					klog.Infof("Skipping task %s because sandbox %s is currently busy running another task.", filename, sandboxName)
					continue
				}

				if task.Number > 0 && !dryRun {
					if issueOrPR, _, err := ghClient.Issues.Get(ctx, owner, repo, task.Number); err == nil && issueOrPR != nil {
						if hasStopLabel(issueOrPR.Labels, triggerLabel) {
							klog.Infof("Skipping task %s and removing from incoming because target #%d has the stop label ('overseer/stop' or '%s/stop')", filename, task.Number, triggerLabel)
							_ = os.Remove(filepath.Join(incomingDir, filename))
							continue
						}
					}
				}

				if task.Type != "agent-chore" && task.Recovered {
					completed, err := isSandboxTaskCompleted(ctx, kubeClient, rootFlags.Namespace, sandboxName, task.Type)
					if err != nil {
						klog.Errorf("Failed to check if sandbox %s completed task: %v", sandboxName, err)
						continue
					}
					if completed {
						klog.Infof("Recovered task %s is already completed in sandbox %s. Marking as completed.", filename, sandboxName)
						if dryRun {
							continue
						}
						incomingPath := filepath.Join(incomingDir, filename)
						processedPath := filepath.Join(processedDir, filename)
						task.Status = "Completed"
						task.CompletedAt = time.Now()
						_ = writeTaskAtomically(incomingDir, filename, task)
						writeTaskJournalEvent(queueDir, filename, task, "Completed", 0)
						if err := os.Rename(incomingPath, processedPath); err != nil {
							klog.Errorf("Failed to move completed task %s to processed: %v", filename, err)
						}
						continue
					}
				}

				incomingPath := filepath.Join(incomingDir, filename)
				processingPath := filepath.Join(processingDir, filename)

				if dryRun {
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
				writeTaskJournalEvent(queueDir, filename, task, "Started", 0)

				actionsTaken++
				filesInProcessing++

				wg.Add(1)
				go func(taskFilename string, t *QueueTask) {
					defer wg.Done()
					fmt.Printf("Starting task %s (Type: %s, URL: %s)...\n", taskFilename, t.Type, t.URL)
					startTime := time.Now()

					taskCtx, taskCancel := context.WithTimeout(ctx, taskTimeout)
					defer taskCancel()

					if t.Number > 0 {
						if (t.Type == "issue-fix" || t.Type == "agent-chore") && t.Assignee != "" {
							klog.Infof("Assigning issue #%d to %s as claimed", t.Number, t.Assignee)
							if _, _, err := ghClient.Issues.AddAssignees(ctx, owner, repo, t.Number, []string{t.Assignee}); err != nil {
								klog.Errorf("Failed to assign issue #%d to %s: %v", t.Number, t.Assignee, err)
							}
							if t.Assignee != targetAssignee {
								if _, _, err := ghClient.Issues.RemoveAssignees(ctx, owner, repo, t.Number, []string{targetAssignee}); err != nil {
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
								addGitHubComment(ctx, ghClient, owner, repo, t.Number, commentBody)
							}
						}
					}

					selectedUser := t.Assignee
					var sUserErr error
					if selectedUser == "" || (isPRTask(t.Type) && strings.EqualFold(selectedUser, targetAssignee)) {
						selectedUser, sUserErr = selectUserForTask(ctx, ghClient, kubeClient, cfg, t.Type, t.Number, owner, repo)
					}
					if sUserErr != nil {
						klog.Errorf("Failed to select user for task %s: %v", taskFilename, sUserErr)
						t.Status = "Failed"
						t.Error = sUserErr.Error()
						_ = writeTaskAtomically(processingDir, taskFilename, t)
						writeTaskJournalEvent(queueDir, taskFilename, t, "Failed", 0)
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

					if rootFlags.Namespace != "" {
						args = append(args, "--namespace", rootFlags.Namespace)
					}
					if selectedUser != "" {
						args = append(args, "--user", selectedUser)
					}
					if rootFlags.Image != "" {
						args = append(args, "--image", rootFlags.Image)
					}
					if rootFlags.DiskSize != "" {
						args = append(args, "--workspace-disk-size", rootFlags.DiskSize)
					}
					if rootFlags.EphemeralStorage != "" {
						args = append(args, "--ephemeral-storage", rootFlags.EphemeralStorage)
					}
					if rootFlags.CPURequest != "" {
						args = append(args, "--cpu-request", rootFlags.CPURequest)
					}
					if rootFlags.CPULimit != "" {
						args = append(args, "--cpu-limit", rootFlags.CPULimit)
					}
					if rootFlags.MemoryRequest != "" {
						args = append(args, "--memory-request", rootFlags.MemoryRequest)
					}
					if rootFlags.MemoryLimit != "" {
						args = append(args, "--memory-limit", rootFlags.MemoryLimit)
					}
					if taskTimeout > 0 {
						args = append(args, "--timeout", taskTimeout.String())
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
						writeTaskJournalEvent(queueDir, taskFilename, t, "Failed", duration)
						if t.Type == "pr-comments" {
							resolvePRCommentReactions(ctx, ghClient, owner, repo, t.Number, "confused", cfg.AllowlistedBots, githubLogin)
						}

						// Force clean up sandbox if the task timed out
						if taskCtx.Err() == context.DeadlineExceeded {
							var sandboxName string
							switch t.Type {
							case "issue-fix":
								if t.SessionID != "" {
									sandboxName = fmt.Sprintf("wf-issue-%d", t.Number)
								} else {
									sandboxName = fmt.Sprintf("fix-%s-%d", repo, t.Number)
								}
							case "agent-chore":
								if t.SessionID != "" {
									sandboxName = fmt.Sprintf("wf-issue-%d", t.Number)
								} else {
									sandboxName = fmt.Sprintf("agent-%s-%d", repo, t.Number)
								}
							case "pr-investigate", "pr-comments", "pr-iterate", "pr-review":
								sandboxName = resolveSandboxName(ctx, kubeClient, ghClient, t.Type, t.Number, owner, repo)
							}

							if sandboxName != "" {
								klog.Warningf("Task %s timed out after %s! Force cleaning up sandbox '%s'...", taskFilename, taskTimeout, sandboxName)
								manager := k8s.NewManager(kubeClient)
								if err := manager.DeleteSandbox(ctx, rootFlags.Namespace, sandboxName); err != nil {
									klog.Errorf("Failed to delete sandbox '%s' on timeout: %v", sandboxName, err)
								}
							}
						}
					} else {
						fmt.Printf("Task %s completed successfully.\n", taskFilename)
						t.Status = "Completed"
						t.CompletedAt = time.Now()
						writeTaskJournalEvent(queueDir, taskFilename, t, "Completed", duration)
						if t.Type == "pr-comments" {
							resolvePRCommentReactions(ctx, ghClient, owner, repo, t.Number, "+1", cfg.AllowlistedBots, githubLogin)
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

	if once {
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
			fmt.Printf("\nWatch timeout of %s expired. Shutting down gracefully...\n", watchTimeout)
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

func getReferencedIssues(pr *githubv39.PullRequest) map[int]bool {
	referenced := make(map[int]bool)

	// Check branch name, ignoring epoch timestamps (num >= 10000000)
	if pr.GetHead().GetRef() != "" {
		re := regexp.MustCompile(`\d+`)
		for _, match := range re.FindAllString(pr.GetHead().GetRef(), -1) {
			if num, err := strconv.Atoi(match); err == nil && num < 10000000 {
				referenced[num] = true
			}
		}
	}

	// Check title and body for #1234 or "Fixes/Closes/Resolves/Issue 1234"
	re := regexp.MustCompile(`(?:#|(?i:\b(?:fixes|closes|resolves|issue)\s+))(\d+)\b`)
	for _, text := range []string{pr.GetTitle(), pr.GetBody()} {
		for _, match := range re.FindAllStringSubmatch(text, -1) {
			if len(match) > 1 {
				if num, err := strconv.Atoi(match[1]); err == nil && num < 10000000 {
					referenced[num] = true
				}
			}
		}
	}

	return referenced
}

func syncReferencedIssueLabels(ctx context.Context, ghClient *githubv39.Client, owner, repo string, pr *githubv39.PullRequest, prIssue *githubv39.Issue) {
	var refIssues []*githubv39.Issue
	for refIssueNum := range getReferencedIssues(pr) {
		refIssue, _, err := ghClient.Issues.Get(ctx, owner, repo, refIssueNum)
		if err != nil {
			klog.Warningf("Failed to fetch referenced parent issue #%d for PR #%d: %v", refIssueNum, pr.GetNumber(), err)
			continue
		}
		refIssues = append(refIssues, refIssue)
	}

	allMissingLabels := getMissingLabelsForPR(prIssue.Labels, refIssues)

	if len(allMissingLabels) > 0 {
		klog.Infof("Adding inherited labels %v to PR #%d", allMissingLabels, pr.GetNumber())
		if _, _, err := ghClient.Issues.AddLabelsToIssue(ctx, owner, repo, pr.GetNumber(), allMissingLabels); err != nil {
			klog.Errorf("Failed to add labels %v to PR #%d: %v", allMissingLabels, pr.GetNumber(), err)
		}
	}
}

func getMissingLabelsForPR(prLabels []*githubv39.Label, refIssues []*githubv39.Issue) []string {
	prLabelsSet := make(map[string]bool)
	for _, label := range prLabels {
		if label.GetName() != "" {
			prLabelsSet[label.GetName()] = true
		}
	}

	var allMissingLabels []string
	missingLabelsSet := make(map[string]bool)

	for _, refIssue := range refIssues {
		if refIssue == nil {
			continue
		}

		for _, label := range refIssue.Labels {
			labelName := label.GetName()
			if labelName != "" && !prLabelsSet[labelName] && !missingLabelsSet[labelName] {
				missingLabelsSet[labelName] = true
				allMissingLabels = append(allMissingLabels, labelName)
			}
		}
	}

	return allMissingLabels
}

func hasLinkedPR(ctx context.Context, client *githubv39.Client, owner, repo string, issueNum int) (bool, error) {
	// 1. Try timeline check (quick and standard)
	timeline, _, err := client.Issues.ListIssueTimeline(ctx, owner, repo, issueNum, nil)
	if err == nil {
		for _, event := range timeline {
			if event.GetEvent() == "cross-referenced" && event.Source != nil {
				if event.Source.Issue != nil && event.Source.Issue.PullRequestLinks != nil {
					if event.Source.Issue.GetState() == "open" {
						return true, nil
					}
				}
			}
		}
	} else {
		klog.Warningf("Failed to list issue timeline for #%d: %v. Falling back to search API.", issueNum, err)
	}

	// 2. Fallback to Search API: search for open PRs referencing the issue number
	query := fmt.Sprintf("repo:%s/%s type:pr state:open \"%d\"", owner, repo, issueNum)
	opts := &githubv39.SearchOptions{
		ListOptions: githubv39.ListOptions{PerPage: 10},
	}
	result, _, err := client.Search.Issues(ctx, query, opts)
	if err != nil {
		return false, fmt.Errorf("failed to search PRs for issue #%d: %w", issueNum, err)
	}

	if result.GetTotal() > 0 {
		return true, nil
	}

	return false, nil
}

func isPRApprovedOrLGTM(pr *githubv39.PullRequest, prIssue *githubv39.Issue, reviews []*githubv39.PullRequestReview) bool {
	// 1. Check labels
	for _, label := range prIssue.Labels {
		if strings.EqualFold(label.GetName(), "lgtm") || strings.EqualFold(label.GetName(), "approved") {
			return true
		}
	}

	// 2. Check reviews
	hasApproved := false
	hasChangesRequested := false
	latestReviews := make(map[string]string)
	for _, r := range reviews {
		if r.GetUser() != nil && r.GetState() != "" {
			latestReviews[r.GetUser().GetLogin()] = r.GetState()
		}
	}
	for _, state := range latestReviews {
		if state == "APPROVED" {
			hasApproved = true
		} else if state == "CHANGES_REQUESTED" {
			hasChangesRequested = true
		}
	}

	return hasApproved && !hasChangesRequested
}

func isBotReply(user *githubv39.User, githubLogin string, allowlistedBots []string) bool {
	if user == nil {
		return false
	}
	login := user.GetLogin()
	if strings.EqualFold(login, githubLogin) {
		return true
	}
	for _, b := range allowlistedBots {
		if strings.EqualFold(login, b) {
			return true
		}
	}
	return shouldIgnoreUser(user, githubLogin, nil)
}

func shouldIgnoreUser(user *githubv39.User, githubLogin string, allowlistedBots []string) bool {
	if user == nil {
		return false
	}
	login := user.GetLogin()
	if strings.EqualFold(login, githubLogin) {
		return true // always ignore our own bot
	}

	loginLower := strings.ToLower(login)
	isBotUser := strings.EqualFold(user.GetType(), "Bot") ||
		strings.HasSuffix(loginLower, "[bot]") ||
		strings.HasSuffix(loginLower, "-bot") ||
		strings.HasSuffix(loginLower, "-robot") ||
		strings.Contains(loginLower, "prow")

	if isBotUser {
		// Check if it's in the allowlist
		for _, b := range allowlistedBots {
			if strings.EqualFold(login, b) {
				return false // DO NOT ignore (it is allowlisted)
			}
		}
		return true // ignore since it is not allowlisted
	}

	return false
}

func hasStopLabel(labels []*githubv39.Label, triggerLabel string) bool {
	stopLabels := []string{"overseer/stop"}
	if triggerLabel != "" && !strings.EqualFold(triggerLabel, "overseer") {
		stopLabels = append(stopLabels, triggerLabel+"/stop")
	}
	for _, label := range labels {
		for _, stop := range stopLabels {
			if strings.EqualFold(label.GetName(), stop) {
				return true
			}
		}
	}
	return false
}

func shouldUnassignStaleBot(lastSHA, unassignedSHA, headSHA, assignedBot string) bool {
	if lastSHA == "" || lastSHA == headSHA {
		return false
	}
	if assignedBot == "" {
		return false
	}
	if unassignedSHA == headSHA {
		return false
	}
	return true
}

func getInvestigationCount(comments []*githubv39.IssueComment, lastCommitTime time.Time, allBotUsers []string, githubLogin string, bots []string) int {
	lastResetTime := lastCommitTime
	for _, c := range comments {
		isPoolBot := false
		for _, bot := range allBotUsers {
			if strings.EqualFold(c.GetUser().GetLogin(), bot) {
				isPoolBot = true
				break
			}
		}
		isHuman := !isPoolBot && !shouldIgnoreUser(c.GetUser(), githubLogin, bots)
		if (isHuman || strings.Contains(c.GetBody(), "pausing automated investigation")) && c.GetCreatedAt().After(lastResetTime) {
			lastResetTime = c.GetCreatedAt()
		}
	}

	investigationCount := 0
	for _, c := range comments {
		isPoolBot := false
		for _, bot := range allBotUsers {
			if strings.EqualFold(c.GetUser().GetLogin(), bot) {
				isPoolBot = true
				break
			}
		}
		if isPoolBot &&
			strings.Contains(c.GetBody(), "started investigating CI check failures") &&
			c.GetCreatedAt().After(lastResetTime) {
			investigationCount++
		}
	}
	return investigationCount
}

func getStopLabel(triggerLabel string) string {
	if triggerLabel != "" && !strings.EqualFold(triggerLabel, "overseer") {
		return triggerLabel + "/stop"
	}
	return "overseer/stop"
}

func hasReviewLabel(labels []*githubv39.Label, triggerLabel string) bool {
	reviewLabels := []string{"overseer/review"}
	if triggerLabel != "" && !strings.EqualFold(triggerLabel, "overseer") {
		reviewLabels = append(reviewLabels, triggerLabel+"/review")
	}
	for _, label := range labels {
		for _, rev := range reviewLabels {
			if strings.EqualFold(label.GetName(), rev) {
				return true
			}
		}
	}
	return false
}

func shouldAutoReviewPR(ctx context.Context, ghClient *githubv39.Client, owner, repo string, pr *githubv39.PullRequest, prIssue *githubv39.Issue, triggerLabel string) bool {
	if hasReviewLabel(prIssue.Labels, triggerLabel) {
		return true
	}
	for refIssueNum := range getReferencedIssues(pr) {
		refIssue, _, err := ghClient.Issues.Get(ctx, owner, repo, refIssueNum)
		if err == nil && hasReviewLabel(refIssue.Labels, triggerLabel) {
			return true
		}
	}
	return false
}

func removePendingTasksForNumber(incomingDir string, number int) {
	files, err := os.ReadDir(incomingDir)
	if err != nil {
		return
	}
	pattern1 := fmt.Sprintf("-issue-%d.yaml", number)
	pattern2 := fmt.Sprintf("-pr-%d-", number)
	for _, f := range files {
		if !f.IsDir() && (strings.Contains(f.Name(), pattern1) || strings.Contains(f.Name(), pattern2)) {
			_ = os.Remove(filepath.Join(incomingDir, f.Name()))
		}
	}
}

func cleanupClosedPRSandboxes(ctx context.Context, ghClient *githubv39.Client, kubeClient *clients.KubernetesClient, owner, repo, namespace string, dryRun bool) error {
	list, err := kubeClient.DynamicClient.Resource(k8s.SandboxGVR).Namespace(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("listing sandboxes for cleanup: %w", err)
	}

	manager := k8s.NewManager(kubeClient)
	for _, item := range list.Items {
		name := item.GetName()
		if !strings.HasPrefix(name, "factory-pr-") {
			continue
		}
		numStr := strings.TrimPrefix(name, "factory-pr-")
		num, err := strconv.Atoi(numStr)
		if err != nil {
			continue
		}

		// Fetch the PR state from GitHub
		pr, _, err := ghClient.PullRequests.Get(ctx, owner, repo, num)
		if err != nil {
			klog.Warningf("Failed to fetch PR #%d for sandbox cleanup check: %v", num, err)
			continue
		}

		// Check if it is closed or merged
		if pr.GetState() == "closed" {
			klog.Infof("Pull Request #%d is closed/merged. Deleting corresponding sandbox '%s'...", num, name)
			if dryRun {
				fmt.Printf("[DRYRUN] Would delete sandbox '%s' for closed PR #%d\n", name, num)
				continue
			}
			meta := sandboxUsageMeta(&item, owner+"/"+repo)
			meta.PR = num
			meta.Issues = referencedIssueList(pr)
			usagereport.HarvestSandbox(ctx, namespace, name, meta)
			usagereport.ReportPRSubject(ctx, owner+"/"+repo, pr)
			if err := manager.DeleteSandbox(ctx, namespace, name); err != nil {
				klog.Errorf("Failed to delete sandbox '%s' for closed PR #%d: %v", name, num, err)
			}
		}
	}
	return nil
}

func cleanupClosedIssueSandboxes(ctx context.Context, ghClient *githubv39.Client, kubeClient *clients.KubernetesClient, owner, repo, namespace string, dryRun bool) error {
	list, err := kubeClient.DynamicClient.Resource(k8s.SandboxGVR).Namespace(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("listing sandboxes for issue cleanup: %w", err)
	}

	manager := k8s.NewManager(kubeClient)
	for _, item := range list.Items {
		name := item.GetName()
		var num int
		var isIssueSandbox bool

		if strings.HasPrefix(name, "wf-issue-") {
			numStr := strings.TrimPrefix(name, "wf-issue-")
			if n, err := strconv.Atoi(numStr); err == nil {
				num = n
				isIssueSandbox = true
			}
		} else if strings.HasPrefix(name, "fix-") {
			idx := strings.LastIndex(name, "-")
			if idx != -1 {
				numStr := name[idx+1:]
				if n, err := strconv.Atoi(numStr); err == nil {
					num = n
					isIssueSandbox = true
				}
			}
		}

		if !isIssueSandbox {
			continue
		}

		// Fetch the issue state from GitHub
		issue, _, err := ghClient.Issues.Get(ctx, owner, repo, num)
		if err != nil {
			klog.Warningf("Failed to fetch issue #%d for sandbox cleanup check: %v", num, err)
			continue
		}

		// Check if the issue is closed
		if issue.GetState() == "closed" {
			klog.Infof("Issue #%d is closed. Deleting corresponding sandbox '%s'...", num, name)
			if dryRun {
				fmt.Printf("[DRYRUN] Would delete sandbox '%s' for closed issue #%d\n", name, num)
				continue
			}
			meta := sandboxUsageMeta(&item, owner+"/"+repo)
			meta.Issue = num
			usagereport.HarvestSandbox(ctx, namespace, name, meta)
			usagereport.ReportIssueSubject(ctx, owner+"/"+repo, issue)
			if strings.HasPrefix(name, "wf-issue-") {
				usagereport.PostWorkflowSummaryIfNeeded(ctx, ghClient, owner, repo, num)
			}
			if err := manager.DeleteSandbox(ctx, namespace, name); err != nil {
				klog.Errorf("Failed to delete sandbox '%s' for closed issue #%d: %v", name, num, err)
			}
		}
	}
	return nil
}

func selectUserForTask(ctx context.Context, ghClient *githubv39.Client, kubeClient *clients.KubernetesClient, cfg *config.FactoryConfig, taskType string, prNum int, owner, repo string) (string, error) {
	if cfg == nil || len(cfg.Roles) == 0 {
		return "", nil // default fallback to factory-user
	}

	// 1. Determine role for task type
	role := ""
	for roleName, rCfg := range cfg.Roles {
		for _, t := range rCfg.Tasks {
			if strings.EqualFold(t, taskType) {
				role = roleName
				break
			}
		}
		if role != "" {
			break
		}
	}

	if role == "" {
		switch {
		case taskType == "agent-chore":
			if rCfg, ok := cfg.Roles["agent"]; ok && len(rCfg.Users) > 0 {
				role = "agent"
			} else {
				role = "coder"
			}
		case isPRTask(taskType):
			if prNum > 0 {
				pr, _, err := ghClient.PullRequests.Get(ctx, owner, repo, prNum)
				if err == nil {
					author := pr.GetUser().GetLogin()
					if author != "" {
						inAgentPool := false
						if agentCfg, ok := cfg.Roles["agent"]; ok {
							for _, u := range agentCfg.Users {
								if strings.EqualFold(u, author) {
									inAgentPool = true
									break
								}
							}
						}
						if inAgentPool {
							role = "agent"
						} else {
							role = "coder"
						}
					}
				}
			}
			if role == "" {
				role = "coder"
			}
		case taskType == "issue-fix":
			role = "coder"
		case taskType == "pr-review":
			role = "reviewer"
		default:
			return "", nil // default fallback
		}
	}

	roleCfg, exists := cfg.Roles[role]
	if !exists || len(roleCfg.Users) == 0 {
		if role == "agent" {
			role = "coder"
			roleCfg, exists = cfg.Roles[role]
		}
		if !exists || len(roleCfg.Users) == 0 {
			return "", nil // default fallback
		}
	}

	// 2. Select bot based on new vs existing PR/Issue
	isIssueTask := taskType == "issue-fix" || taskType == "agent-chore"
	if isIssueTask {
		if prNum > 0 {
			// A. First check if a Sandbox already exists for this task on the cluster
			// and has been pinned to a specific user.
			var sandboxName string
			if taskType == "issue-fix" {
				sandboxName = fmt.Sprintf("fix-%s-%d", repo, prNum)
			} else if taskType == "agent-chore" {
				sandboxName = fmt.Sprintf("wf-issue-%d", prNum)
			}

			if sandboxName != "" && kubeClient != nil {
				sb, err := kubeClient.DynamicClient.Resource(k8s.SandboxGVR).Namespace(rootFlags.Namespace).Get(ctx, sandboxName, metav1.GetOptions{})
				if err == nil {
					labels := sb.GetLabels()
					if user, ok := labels["factory.gemini.google.com/user"]; ok && user != "" {
						inPool := false
						for _, u := range roleCfg.Users {
							if strings.EqualFold(u, user) {
								inPool = true
								break
							}
						}
						if inPool {
							klog.Infof("Pinned user '%s' for issue #%d from existing sandbox '%s'", user, prNum, sandboxName)
							return user, nil
						}
					}
				}
			}

			// B. Fallback to GitHub issue assignee check
			issue, _, err := ghClient.Issues.Get(ctx, owner, repo, prNum)
			if err == nil {
				for _, a := range issue.Assignees {
					assignee := a.GetLogin()
					if assignee != "" {
						inPool := false
						for _, u := range roleCfg.Users {
							if strings.EqualFold(u, assignee) {
								inPool = true
								break
							}
						}
						if inPool {
							return assignee, nil
						}
					}
				}
			} else {
				klog.Warningf("Failed to fetch issue details for task %s #%d: %v", taskType, prNum, err)
			}
		}

		idx := time.Now().UnixNano() % int64(len(roleCfg.Users))
		return roleCfg.Users[idx], nil
	}

	if prNum > 0 {
		pr, _, err := ghClient.PullRequests.Get(ctx, owner, repo, prNum)
		if err != nil {
			return "", fmt.Errorf("fetching PR details: %w", err)
		}
		author := pr.GetUser().GetLogin()
		if author == "" {
			return "", fmt.Errorf("empty author login for PR %d", prNum)
		}

		if taskType == "pr-review" {
			reviewerRoleCfg, ok := cfg.Roles["reviewer"]
			if ok && len(reviewerRoleCfg.Users) > 0 {
				idx := time.Now().UnixNano() % int64(len(reviewerRoleCfg.Users))
				return reviewerRoleCfg.Users[idx], nil
			}
			idx := time.Now().UnixNano() % int64(len(roleCfg.Users))
			return roleCfg.Users[idx], nil
		}

		inPool := false
		for _, u := range roleCfg.Users {
			if strings.EqualFold(u, author) {
				inPool = true
				break
			}
		}
		if !inPool {
			return "", fmt.Errorf("PR author '%s' is not in the configured bot pool for role '%s'", author, role)
		}
		return author, nil
	}

	return "", nil
}

func hasIssueCommentReaction(ctx context.Context, ghClient *githubv39.Client, owner, repo string, commentID int64, content string, filterBot bool, bots []string, selfLogin string) bool {
	if ghClient == nil {
		return false
	}
	reactions, _, err := ghClient.Reactions.ListIssueCommentReactions(ctx, owner, repo, commentID, nil)
	if err != nil {
		return false
	}
	for _, r := range reactions {
		if r.GetContent() == content {
			isBot := shouldIgnoreUser(r.GetUser(), selfLogin, bots)
			if filterBot && isBot {
				return true
			} else if !filterBot && !isBot {
				return true
			}
		}
	}
	return false
}

func resolvePRCommentReactions(ctx context.Context, ghClient *githubv39.Client, owner, repo string, prNum int, resolutionContent string, bots []string, selfLogin string) {
	if ghClient == nil {
		return
	}
	comments, _, err := ghClient.Issues.ListComments(ctx, owner, repo, prNum, nil)
	if err != nil {
		return
	}
	for _, c := range comments {
		if shouldIgnoreUser(c.GetUser(), selfLogin, bots) {
			continue
		}
		if hasIssueCommentReaction(ctx, ghClient, owner, repo, c.GetID(), "eyes", true, bots, selfLogin) {
			addIssueCommentReaction(ctx, ghClient, owner, repo, c.GetID(), resolutionContent)
		}
	}
}

func addIssueCommentReaction(ctx context.Context, ghClient *githubv39.Client, owner, repo string, commentID int64, content string) {
	if ghClient == nil {
		return
	}
	_, _, err := ghClient.Reactions.CreateIssueCommentReaction(ctx, owner, repo, commentID, content)
	if err != nil {
		klog.Warningf("Failed to create reaction '%s' on comment %d: %v", content, commentID, err)
	}
}

func isPRTask(taskType string) bool {
	return taskType == "pr-investigate" || taskType == "pr-comments" || taskType == "pr-iterate"
}

func cleanupStaleIdleSandboxes(ctx context.Context, kubeClient *clients.KubernetesClient, repoFullName string, namespace string, ageStr string, dryRun bool) error {
	if kubeClient == nil {
		return nil
	}

	evictionAge, err := parseEvictionAge(ageStr)
	if err != nil {
		return fmt.Errorf("parsing sandbox eviction age: %w", err)
	}

	list, err := kubeClient.DynamicClient.Resource(k8s.SandboxGVR).Namespace(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("listing sandboxes for idle cleanup: %w", err)
	}

	manager := k8s.NewManager(kubeClient)
	now := time.Now()
	for _, item := range list.Items {
		name := item.GetName()
		if factorysandbox.IsCurrentSandbox(ctx, kubeClient, &item, namespace) {
			continue
		}
		creationTime := item.GetCreationTimestamp().Time
		if creationTime.IsZero() {
			continue
		}

		if now.Sub(creationTime) > evictionAge {
			running, err := isSandboxTaskRunning(ctx, kubeClient, namespace, name)
			if err != nil {
				klog.Errorf("Failed to check if sandbox %s is running: %v", name, err)
				continue
			}

			if !running {
				klog.Infof("Sandbox '%s' has been idle and is older than configured eviction age %s (created: %v). Evicting...", name, ageStr, creationTime)
				if dryRun {
					fmt.Printf("[DRYRUN] Would evict stale/idle sandbox '%s'\n", name)
					continue
				}

				usagereport.HarvestSandbox(ctx, namespace, name, sandboxUsageMeta(&item, repoFullName))
				if err := manager.DeleteSandbox(ctx, namespace, name); err != nil {
					klog.Errorf("Failed to delete stale/idle sandbox '%s': %v", name, err)
				}
			}
		}
	}

	return nil
}

func parseEvictionAge(ageStr string) (time.Duration, error) {
	ageStr = strings.TrimSpace(ageStr)
	if ageStr == "" {
		return 7 * 24 * time.Hour, nil
	}

	if strings.HasSuffix(ageStr, "d") {
		daysStr := strings.TrimSuffix(ageStr, "d")
		days, err := strconv.Atoi(daysStr)
		if err != nil {
			return 0, fmt.Errorf("invalid days format %q: %w", ageStr, err)
		}
		return time.Duration(days) * 24 * time.Hour, nil
	}

	return time.ParseDuration(ageStr)
}
