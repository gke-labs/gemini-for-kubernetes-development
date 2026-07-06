package watch

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/clients"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/config"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/github"
	githubv39 "github.com/google/go-github/v39/github"
	"github.com/robfig/cron/v3"
	"gopkg.in/yaml.v3"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/klog/v2"
)

const KeyGithubLogin = "GITHUB_LOGIN"

var cronParser = cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow | cron.Descriptor)

var newGitHubClient = func(ctx context.Context) (*githubv39.Client, error) {
	return github.NewClient(ctx)
}

var newKubernetesClient = func() (*clients.KubernetesClient, error) {
	return clients.NewKubernetesClient()
}

func RunWatch(ctx context.Context, opts WatchOptions) error {
	cfg, err := config.LoadConfig()
	if err != nil {
		klog.Warningf("Failed to load factory config: %v", err)
	}
	triggerLabel := "factory"
	if cfg != nil && cfg.TriggerLabel != "" {
		triggerLabel = cfg.TriggerLabel
	}

	ghClient, err := newGitHubClient(ctx)
	if err != nil {
		return fmt.Errorf("creating github client: %w", err)
	}

	kubeClient, err := newKubernetesClient()
	if err != nil {
		return fmt.Errorf("creating k8s client: %w", err)
	}

	secret, err := kubeClient.Clientset.CoreV1().Secrets(opts.Namespace).Get(ctx, opts.SecretName, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("fetching %s secret in namespace %s: %w (make sure to run 'factory user onboard' first)", opts.SecretName, opts.Namespace, err)
	}
	githubLogin := string(secret.Data[KeyGithubLogin])

	targetAssignee := opts.Assignee
	if !opts.AssigneeChanged {
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

	incomingDir := filepath.Join(opts.QueueDir, "incoming")
	processingDir := filepath.Join(opts.QueueDir, "processing")
	processedDir := filepath.Join(opts.QueueDir, "processed")

	logDir := os.Getenv("FACTORY_LOGS")
	if logDir == "" {
		logDir = filepath.Join(opts.QueueDir, "logs")
	}
	processingLogDir := filepath.Join(logDir, "processing")
	processedLogDir := filepath.Join(logDir, "processed")

	if !opts.DryRun {
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

	fmt.Printf("Starting watch for repository %s/%s (mode: %s, queueDir: %s, poll interval: %s, assignee: '%s', labels: %v, dryRun: %v, watchTimeout: %s)...\n", opts.Owner, opts.Repo, opts.Mode, opts.QueueDir, opts.Interval, targetAssignee, opts.Labels, opts.DryRun, opts.WatchTimeout)

	var timeoutChan <-chan time.Time
	if opts.WatchTimeout > 0 {
		timeoutChan = time.After(opts.WatchTimeout)
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

	state := &watchState{
		referencedIssues: make(map[int]bool),
	}

	w := &watchContext{
		ctx:             ctx,
		opts:            opts,
		ghClient:        ghClient,
		kubeClient:      kubeClient,
		cfg:             cfg,
		githubLogin:     githubLogin,
		targetAssignee:  targetAssignee,
		allBotUsers:     allBotUsers,
		incomingDir:     incomingDir,
		processingDir:   processingDir,
		processedDir:    processedDir,
		queueDir:        opts.QueueDir,
		processedIssues: processedIssues,
		processedPRs:    processedPRs,
		state:           state,
		triggerLabel:    triggerLabel,
	}

	var wg sync.WaitGroup

	checkRepo := func() {
		w.state.mu.Lock()
		if w.state.shuttingDown {
			w.state.mu.Unlock()
			return
		}
		w.state.mu.Unlock()

		if w.isDoNotProcess() {
			runningCount, err := countRunningSandboxTasks(ctx, w.kubeClient, w.opts.Namespace)
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

		w.scan(ctx)

		// Determine if runner should run in this cycle
		now := time.Now()
		runRunner := false
		if w.opts.Mode == "all" || w.opts.Mode == "run" {
			if w.state.lastRunnerRun.IsZero() || now.Sub(w.state.lastRunnerRun) >= 30*time.Second {
				runRunner = true
			}
		}

		w.run(ctx, &wg, runRunner)
	}
	var tickChan <-chan time.Time
	if !opts.Once {
		ticker := time.NewTicker(10 * time.Second)
		defer ticker.Stop()
		tickChan = ticker.C
	}

	return w.runLoop(ctx, &wg, checkRepo, tickChan, timeoutChan)
}

func (w *watchContext) runLoop(ctx context.Context, wg *sync.WaitGroup, checkRepo func(), tickChan <-chan time.Time, timeoutChan <-chan time.Time) error {
	checkRepo()

	if w.opts.Once {
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
			fmt.Printf("\nWatch timeout of %s expired. Shutting down gracefully...\n", w.opts.WatchTimeout)
			w.state.mu.Lock()
			w.state.shuttingDown = true
			w.state.mu.Unlock()

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
		case <-tickChan:
			checkRepo()
		}
	}
}

func (w *watchContext) isDoNotProcess() bool {
	if os.Getenv("DO_NOT_PROCESS") == "true" || os.Getenv("FACTORY_DO_NOT_PROCESS") == "true" || os.Getenv("DRAIN") == "true" || os.Getenv("FACTORY_DRAIN") == "true" {
		return true
	}
	checkPaths := []string{
		filepath.Join(w.queueDir, ".do_not_process"),
		filepath.Join(w.queueDir, "do_not_process"),
		filepath.Join(w.queueDir, ".drain"),
		filepath.Join(w.queueDir, "drain"),
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

