package watch

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/commands/common"
	githubv39 "github.com/google/go-github/v39/github"
	"github.com/robfig/cron/v3"
	"k8s.io/klog/v2"
)

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

			agentDef, err := common.ParseAgent([]byte(contentStr))
			if err != nil {
				klog.Errorf("Failed to parse chore agent %s: %v", file.GetName(), err)
				continue
			}

			if agentDef.Schedule == "" {
				continue
			}

			filename := fmt.Sprintf("task-chore-%s.yaml", common.Slugify(agentDef.Name))
			if taskExists(incomingDir, processingDir, filename) {
				continue
			}

			lastRun := choresState[agentDef.Name].LastRun
			if shouldRunChore(agentDef.Schedule, lastRun) {
				task := &QueueTask{
					Type:       "agent-chore",
					URL:        fmt.Sprintf("https://github.com/%s/%s", owner, repo),
					Priority:   "medium",
					Phase:      4,
					CreatedAt:  time.Now(),
					EnqueuedAt: time.Now(),
					Status:     "Pending",
					AgentFile:  ".agents/" + file.GetName(),
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
