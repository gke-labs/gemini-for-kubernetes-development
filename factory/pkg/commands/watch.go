package commands

import (
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
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/github"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/k8s"
	factorysandbox "github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/sandbox"
	githubv39 "github.com/google/go-github/v39/github"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/klog/v2"
)

type WatchFlags struct {
	Repo         string
	PollInterval time.Duration
	Assignee     string
	Labels       []string
	DryRun       bool
	WatchTimeout time.Duration
	MaxActions   int
	MaxPending   int
	Mode         string
	QueueDir     string
	Once         bool
	IssueMode    string
	PRMode       string
	ChoresMode   string
	ScanLimit    int
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

			return runWatch(ctx, parts[0], parts[1], flags.PollInterval, flags.Assignee, cmd.Flags().Changed("assignee"), flags.Labels, flags.DryRun, flags.WatchTimeout, flags.MaxActions, flags.MaxPending, flags.Mode, flags.QueueDir, flags.Once, issueMode, prMode, choresMode, rootFlags.EphemeralStorage, rootFlags.ResolvedSecrets, flags.ScanLimit)
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

	return cmd
}

func isAssigned(issue *githubv39.Issue, assignee string) bool {
	if assignee == "" {
		return false
	}
	for _, u := range issue.Assignees {
		if strings.EqualFold(u.GetLogin(), assignee) {
			return true
		}
	}
	return false
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

type QueueTask struct {
	Type      string    `yaml:"type"` // "issue-fix", "pr-investigate", "pr-comments", "pr-iterate", "pr-review", "agent-chore"
	URL       string    `yaml:"url"`
	Number    int       `yaml:"number"`
	Priority  string    `yaml:"priority"` // "critical", "urgent", "important", "high", "medium", "low"
	Phase     int       `yaml:"phase"`    // 1: Rebase/iterate, 2: Comments, 3: Investigate/Fix, 4: Chores
	CreatedAt time.Time `yaml:"createdAt"`
	Assignee  string    `yaml:"assignee,omitempty"`
	Status    string    `yaml:"status"` // "Pending", "Running", "Completed", "Failed"
	Error     string    `yaml:"error,omitempty"`
	AgentFile string    `yaml:"agentFile,omitempty"` // For chore tasks
}

type ChoreRunState struct {
	LastRun time.Time `json:"lastRun"`
}

func shouldRunChore(schedule string, lastRun time.Time) bool {
	if lastRun.IsZero() {
		return true
	}
	now := time.Now()
	switch strings.ToLower(strings.TrimSpace(schedule)) {
	case "never", "paused":
		return false
	case "@hourly":
		return now.Sub(lastRun) >= 1*time.Hour
	case "@daily":
		return now.Sub(lastRun) >= 24*time.Hour
	case "@weekly":
		return now.Sub(lastRun) >= 7*24*time.Hour
	default:
		return now.Sub(lastRun) >= 24*time.Hour
	}
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

func runWatch(ctx context.Context, owner, repo string, interval time.Duration, assignee string, assigneeChanged bool, labels []string, dryRun bool, watchTimeout time.Duration, maxActions int, maxPending int, mode string, queueDir string, once bool, issueMode string, prMode string, choresMode string, ephemeralStorage string, secrets []factorysandbox.SecretMount, scanLimit int) error {
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
	}

	processedIssues := make(map[int]time.Time)
	processedPRs := make(map[int]prWatchState)

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

		now := time.Now()
		actionsTaken := 0
		unassignedPRs := make(map[int]bool)

		processPRsFunc := func(prIssues []*githubv39.Issue) {
			if prMode == "disabled" {
				return
			}
			for _, prIssue := range prIssues {
				num := prIssue.GetNumber()
				pr, _, err := ghClient.PullRequests.Get(ctx, owner, repo, num)
				if err != nil {
					klog.Errorf("Failed to fetch full PR #%d: %v", num, err)
					continue
				}

				// Verify PR Author: Only process PRs created by the bot
				author := pr.GetUser().GetLogin()
				if !strings.EqualFold(author, githubLogin) {
					klog.Infof("Skipping PR #%d because it was created by %s (not %s). We do not have permission to push to external forks.", num, author, githubLogin)
					continue
				}

				headSHA := pr.GetHead().GetSHA()

				// Fetch PR commits to find the last commit timestamp
				prCommits, _, err := ghClient.PullRequests.ListCommits(ctx, owner, repo, num, nil)
				var lastCommitTime time.Time
				if err == nil {
					for _, c := range prCommits {
						if c.GetCommit().GetAuthor().GetDate().After(lastCommitTime) {
							lastCommitTime = c.GetCommit().GetAuthor().GetDate()
						}
					}
				}

				// Fetch PR comments
				comments, _, listCommentsErr := ghClient.Issues.ListComments(ctx, owner, repo, num, nil)

				// Check Phase 1: Rebase/Conflicts
				isConflicting := pr.Mergeable != nil && !*pr.Mergeable

				if isConflicting {
					filename := fmt.Sprintf("task-pr-%d-iterate.yaml", num)
					if !taskExists(incomingDir, processingDir, filename) {
						sandboxName := fmt.Sprintf("factory-pr-%d", num)
						running, err := isSandboxTaskRunning(ctx, kubeClient, rootFlags.Namespace, sandboxName)
						if err != nil {
							klog.Errorf("Failed to check if sandbox %s is running: %v", sandboxName, err)
							continue
						} else if running {
							klog.Infof("Skipping PR #%d rebase because there is an in-flight sandbox %s.", num, sandboxName)
						} else {
							if isAssigned(prIssue, targetAssignee) && !unassignedPRs[num] {
								if dryRun {
									fmt.Printf("[DRYRUN] Would unassign %s from PR #%d\n", targetAssignee, num)
								} else {
									fmt.Printf("Unassigning %s from PR #%d...\n", targetAssignee, num)
									if _, _, err := ghClient.Issues.RemoveAssignees(ctx, owner, repo, num, []string{targetAssignee}); err != nil {
										klog.Errorf("Failed to unassign %s from PR #%d: %v", targetAssignee, num, err)
									}
									unassignedPRs[num] = true
								}
							}

							prURL := fmt.Sprintf("https://github.com/%s/%s/pull/%d", owner, repo, num)
							task := &QueueTask{
								Type:      "pr-iterate",
								URL:       prURL,
								Number:    num,
								Priority:  getPRPriority(prIssue),
								Phase:     1,
								CreatedAt: pr.GetCreatedAt(),
								Assignee:  targetAssignee,
								Status:    "Pending",
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
						if run.GetConclusion() == "failure" {
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

				if hasFailure {
					// Count investigations since last commit
					investigationCount := 0
					if listCommentsErr == nil {
						for _, c := range comments {
							if strings.EqualFold(c.GetUser().GetLogin(), githubLogin) &&
								strings.Contains(c.GetBody(), "started investigating CI check failures") &&
								c.GetCreatedAt().After(lastCommitTime) {
								investigationCount++
							}
						}
					}

					if investigationCount >= 3 {
						// Post giving up comment if we haven't already posted it since the last commit
						hasPostedGivingUp := false
						if listCommentsErr == nil {
							for _, c := range comments {
								if strings.EqualFold(c.GetUser().GetLogin(), githubLogin) &&
									strings.Contains(c.GetBody(), "giving up. Human assistance is required") &&
									c.GetCreatedAt().After(lastCommitTime) {
									hasPostedGivingUp = true
									break
								}
							}
						}
						if !hasPostedGivingUp && !dryRun {
							addGitHubComment(ctx, ghClient, owner, repo, num, "🤖 AI Factory has attempted to fix CI failures for this PR 3 times since the last commit and is giving up. Human assistance is required.")
						}
						klog.Infof("Skipping PR #%d investigate because it has reached the maximum retry limit (3).", num)
					} else if state.lastSHA != headSHA || time.Since(state.lastInvestigatedTime) > 6*time.Hour {
						filename := fmt.Sprintf("task-pr-%d-investigate.yaml", num)
						if !taskExists(incomingDir, processingDir, filename) {
							sandboxName := fmt.Sprintf("factory-pr-%d", num)
							running, err := isSandboxTaskRunning(ctx, kubeClient, rootFlags.Namespace, sandboxName)
							if err != nil {
								klog.Errorf("Failed to check if sandbox %s is running: %v", sandboxName, err)
								continue
							} else if running {
								klog.Infof("Skipping PR #%d investigate because there is an in-flight sandbox %s.", num, sandboxName)
							} else {
								if isAssigned(prIssue, targetAssignee) && !unassignedPRs[num] {
									if dryRun {
										fmt.Printf("[DRYRUN] Would unassign %s from PR #%d\n", targetAssignee, num)
									} else {
										fmt.Printf("Unassigning %s from PR #%d...\n", targetAssignee, num)
										if _, _, err := ghClient.Issues.RemoveAssignees(ctx, owner, repo, num, []string{targetAssignee}); err != nil {
											klog.Errorf("Failed to unassign %s from PR #%d: %v", targetAssignee, num, err)
										}
										unassignedPRs[num] = true
									}
								}

								prURL := fmt.Sprintf("https://github.com/%s/%s/pull/%d", owner, repo, num)
								task := &QueueTask{
									Type:      "pr-investigate",
									URL:       prURL,
									Number:    num,
									Priority:  getPRPriority(prIssue),
									Phase:     3,
									CreatedAt: pr.GetCreatedAt(),
									Assignee:  targetAssignee,
									Status:    "Pending",
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

				// Check review comments
				if listCommentsErr == nil {
					hasNewComments := false
					for _, c := range comments {
						if strings.Contains(strings.ToLower(c.GetUser().GetLogin()), "bot") {
							continue
						}
						if c.GetCreatedAt().After(lastCommitTime) && c.GetCreatedAt().After(state.lastCommentAddressedTime) {
							hasNewComments = true
							break
						}
					}

					if hasNewComments {
						if os.Getenv("DRY_RUN") == "true" {
							continue
						}
						filename := fmt.Sprintf("task-pr-%d-comments.yaml", num)
						if !taskExists(incomingDir, processingDir, filename) {
							sandboxName := fmt.Sprintf("factory-pr-%d", num)
							running, err := isSandboxTaskRunning(ctx, kubeClient, rootFlags.Namespace, sandboxName)
							if err != nil {
								klog.Errorf("Failed to check if sandbox %s is running: %v", sandboxName, err)
								continue
							} else if running {
								klog.Infof("Skipping PR #%d address-comments because there is an in-flight sandbox %s.", num, sandboxName)
							} else {
								if isAssigned(prIssue, targetAssignee) && !unassignedPRs[num] {
									if dryRun {
										fmt.Printf("[DRYRUN] Would unassign %s from PR #%d\n", targetAssignee, num)
									} else {
										fmt.Printf("Unassigning %s from PR #%d...\n", targetAssignee, num)
										if _, _, err := ghClient.Issues.RemoveAssignees(ctx, owner, repo, num, []string{targetAssignee}); err != nil {
											klog.Errorf("Failed to unassign %s from PR #%d: %v", targetAssignee, num, err)
										}
										unassignedPRs[num] = true
									}
								}

								prURL := fmt.Sprintf("https://github.com/%s/%s/pull/%d", owner, repo, num)
								task := &QueueTask{
									Type:      "pr-comments",
									URL:       prURL,
									Number:    num,
									Priority:  getPRPriority(prIssue),
									Phase:     2,
									CreatedAt: pr.GetCreatedAt(),
									Assignee:  targetAssignee,
									Status:    "Pending",
								}

								if dryRun {
									fmt.Printf("[DRYRUN] Would queue address-comments task for PR #%d: %s\n", num, prURL)
								} else {
									fmt.Printf("Queueing address-comments task for PR #%d...\n", num)
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

			// Scan issues labeled "overseer"
			var slowIssues []*githubv39.Issue
			opts2 := &githubv39.IssueListByRepoOptions{
				Labels:      []string{"overseer"},
				State:       "open",
				ListOptions: githubv39.ListOptions{PerPage: 100},
			}
			issues2, _, err := ghClient.Issues.ListByRepo(ctx, owner, repo, opts2)
			if err != nil {
				klog.Errorf("Failed to list issues for label overseer: %v", err)
			} else {
				for _, item := range issues2 {
					if item.PullRequestLinks == nil {
						slowIssues = append(slowIssues, item)
					}
				}
			}

			// Process slow issues
			if issueMode != "disabled" {
				for _, issue := range slowIssues {
					num := issue.GetNumber()
					if refIssues[num] {
						klog.Infof("Skipping issue #%d because there is already a PR referencing it.", num)
						continue
					}
					if lastProcessed, ok := processedIssues[num]; !ok || time.Since(lastProcessed) > 24*time.Hour {
						linked, err := hasLinkedPR(ctx, ghClient, owner, repo, num)
						if err != nil {
							klog.Errorf("Failed to check linked PR for issue #%d: %v", num, err)
							continue
						} else if linked {
							klog.Infof("Skipping issue #%d because it has a linked PR according to the Timeline API.", num)
							continue
						}

						filename := fmt.Sprintf("task-issue-%d.yaml", num)
						if taskExists(incomingDir, processingDir, filename) {
							continue
						}

						sandboxName := fmt.Sprintf("fix-%s-%d", repo, num)
						running, err := isSandboxTaskRunning(ctx, kubeClient, rootFlags.Namespace, sandboxName)
						if err != nil {
							klog.Errorf("Failed to check if sandbox %s is running: %v", sandboxName, err)
							continue
						} else if running {
							klog.Infof("Skipping issue #%d because there is an in-flight sandbox %s.", num, sandboxName)
							continue
						}

						if isAssigned(issue, targetAssignee) {
							if dryRun {
								fmt.Printf("[DRYRUN] Would unassign %s from issue #%d\n", targetAssignee, num)
							} else {
								fmt.Printf("Unassigning %s from issue #%d...\n", targetAssignee, num)
								if _, _, err := ghClient.Issues.RemoveAssignees(ctx, owner, repo, num, []string{targetAssignee}); err != nil {
									klog.Errorf("Failed to unassign %s from issue #%d: %v", targetAssignee, num, err)
								}
							}
						}

						issueURL := fmt.Sprintf("https://github.com/%s/%s/issues/%d", owner, repo, num)
						task := &QueueTask{
							Type:      "issue-fix",
							URL:       issueURL,
							Number:    num,
							Priority:  getIssuePriority(issue),
							Phase:     3,
							CreatedAt: issue.GetCreatedAt(),
							Assignee:  targetAssignee,
							Status:    "Pending",
						}

						if dryRun {
							fmt.Printf("[DRYRUN] Would queue fix task for issue #%d: %s\n", num, issueURL)
						} else {
							fmt.Printf("Queueing fix task for issue #%d...\n", num)
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

			// Process Pull Requests (Scanner)
			var prIssues []*githubv39.Issue
			var allPRIssues []*githubv39.Issue
			if targetAssignee != "" {
				opts1 := &githubv39.IssueListByRepoOptions{
					Assignee:    targetAssignee,
					State:       "open",
					ListOptions: githubv39.ListOptions{PerPage: 100},
				}
				iss1, _, err := ghClient.Issues.ListByRepo(ctx, owner, repo, opts1)
				if err == nil {
					for _, item := range iss1 {
						if item.PullRequestLinks != nil {
							allPRIssues = append(allPRIssues, item)
						}
					}
				}
			}
			opts2PR := &githubv39.IssueListByRepoOptions{
				Labels:      []string{"overseer"},
				State:       "open",
				ListOptions: githubv39.ListOptions{PerPage: 100},
			}
			iss2, _, err := ghClient.Issues.ListByRepo(ctx, owner, repo, opts2PR)
			if err == nil {
				for _, item := range iss2 {
					if item.PullRequestLinks != nil {
						allPRIssues = append(allPRIssues, item)
					}
				}
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
		}

		// 2. Fast Issue Scan Cycle
		if runIssueScan {
			klog.Infof("Running fast issue scan cycle...")
			var allItems []*githubv39.Issue

			limit := scanLimit
			if limit <= 0 {
				limit = 30
			}

			if targetAssignee != "" {
				opts1 := &githubv39.IssueListByRepoOptions{
					Assignee:    targetAssignee,
					State:       "open",
					Sort:        "updated",
					Direction:   "desc",
					ListOptions: githubv39.ListOptions{PerPage: limit},
				}
				issues1, _, err := ghClient.Issues.ListByRepo(ctx, owner, repo, opts1)
				if err != nil {
					klog.Errorf("Failed to list issues for assignee %s: %v", targetAssignee, err)
				} else {
					allItems = append(allItems, issues1...)
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
				for _, issue := range issues {
					num := issue.GetNumber()
					if refIssues[num] {
						klog.Infof("Skipping issue #%d because there is already a PR referencing it.", num)
						continue
					}
					if lastProcessed, ok := processedIssues[num]; !ok || time.Since(lastProcessed) > 24*time.Hour {
						linked, err := hasLinkedPR(ctx, ghClient, owner, repo, num)
						if err != nil {
							klog.Errorf("Failed to check linked PR for issue #%d: %v", num, err)
							continue
						} else if linked {
							klog.Infof("Skipping issue #%d because it has a linked PR according to the Timeline API.", num)
							continue
						}

						filename := fmt.Sprintf("task-issue-%d.yaml", num)
						if taskExists(incomingDir, processingDir, filename) {
							continue
						}

						sandboxName := fmt.Sprintf("fix-%s-%d", repo, num)
						running, err := isSandboxTaskRunning(ctx, kubeClient, rootFlags.Namespace, sandboxName)
						if err != nil {
							klog.Errorf("Failed to check if sandbox %s is running: %v", sandboxName, err)
							continue
						} else if running {
							klog.Infof("Skipping issue #%d because there is an in-flight sandbox %s.", num, sandboxName)
							continue
						}

						if isAssigned(issue, targetAssignee) {
							if dryRun {
								fmt.Printf("[DRYRUN] Would unassign %s from issue #%d\n", targetAssignee, num)
							} else {
								fmt.Printf("Unassigning %s from issue #%d...\n", targetAssignee, num)
								if _, _, err := ghClient.Issues.RemoveAssignees(ctx, owner, repo, num, []string{targetAssignee}); err != nil {
									klog.Errorf("Failed to unassign %s from issue #%d: %v", targetAssignee, num, err)
								}
							}
						}

						issueURL := fmt.Sprintf("https://github.com/%s/%s/issues/%d", owner, repo, num)
						task := &QueueTask{
							Type:      "issue-fix",
							URL:       issueURL,
							Number:    num,
							Priority:  getIssuePriority(issue),
							Phase:     3,
							CreatedAt: issue.GetCreatedAt(),
							Assignee:  targetAssignee,
							Status:    "Pending",
						}

						if dryRun {
							fmt.Printf("[DRYRUN] Would queue fix task for issue #%d: %s\n", num, issueURL)
						} else {
							fmt.Printf("Queueing fix task for issue #%d...\n", num)
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

			for _, item := range tasksToRun {
				if actionsTaken >= maxActions {
					fmt.Printf("Reached maximum actions limit (%d) for this cycle. Stopping execution.\n", maxActions)
					break
				}

				runningCount, err := countRunningSandboxTasks(ctx, kubeClient, rootFlags.Namespace)
				if err != nil {
					klog.Errorf("Failed to count running sandbox tasks: %v", err)
				}
				activeCount := runningCount + filesInProcessing

				if activeCount >= maxPending {
					fmt.Printf("Reached maximum pending sandboxes limit (%d). Skipping remaining queue items.\n", maxPending)
					break
				}

				filename := item.filename
				task := item.task

				incomingPath := filepath.Join(incomingDir, filename)
				processingPath := filepath.Join(processingDir, filename)

				if dryRun {
					fmt.Printf("[DRYRUN] Would process task %s (Type: %s, URL: %s)\n", filename, task.Type, task.URL)
					actionsTaken++
					filesInProcessing++
					continue
				}

				if err := os.Rename(incomingPath, processingPath); err != nil {
					klog.Warningf("Failed to move task %s to processing (might be processed by another run): %v", filename, err)
					continue
				}

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

					if t.Type != "agent-chore" && t.Number > 0 {
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
					default:
						klog.Errorf("Unknown task type: %s", t.Type)
						return
					}

					if rootFlags.Namespace != "" {
						args = append(args, "--namespace", rootFlags.Namespace)
					}
					if rootFlags.SecretName != "" {
						args = append(args, "--secret-name", rootFlags.SecretName)
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

					cmd := exec.Command(executable, args...)

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
						writeTaskJournalEvent(queueDir, taskFilename, t, "Failed", duration)
					} else {
						fmt.Printf("Task %s completed successfully.\n", taskFilename)
						t.Status = "Completed"
						writeTaskJournalEvent(queueDir, taskFilename, t, "Completed", duration)
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
			wg.Wait()
			fmt.Println("All tasks completed. Exiting.")
			return nil
		case <-time.After(10 * time.Second):
			checkRepo()
		}
	}
}

func getReferencedIssues(pr *githubv39.PullRequest) map[int]bool {
	referenced := make(map[int]bool)

	// Check branch name
	if pr.GetHead().GetRef() != "" {
		re := regexp.MustCompile(`\b\d+\b`)
		for _, match := range re.FindAllString(pr.GetHead().GetRef(), -1) {
			if num, err := strconv.Atoi(match); err == nil {
				referenced[num] = true
			}
		}
	}

	// Check title and body
	re := regexp.MustCompile(`#(\d+)\b`)
	for _, text := range []string{pr.GetTitle(), pr.GetBody()} {
		for _, match := range re.FindAllStringSubmatch(text, -1) {
			if len(match) > 1 {
				if num, err := strconv.Atoi(match[1]); err == nil {
					referenced[num] = true
				}
			}
		}
	}

	return referenced
}

func hasLinkedPR(ctx context.Context, client *githubv39.Client, owner, repo string, issueNum int) (bool, error) {
	timeline, _, err := client.Issues.ListIssueTimeline(ctx, owner, repo, issueNum, nil)
	if err != nil {
		return false, err
	}
	for _, event := range timeline {
		if event.GetEvent() == "cross-referenced" && event.Source != nil {
			if event.Source.Issue != nil && event.Source.Issue.PullRequestLinks != nil {
				return true, nil
			}
		}
	}
	return false, nil
}
