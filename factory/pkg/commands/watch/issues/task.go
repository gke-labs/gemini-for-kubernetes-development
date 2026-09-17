package issues

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	githubv39 "github.com/google/go-github/v39/github"
	"gopkg.in/yaml.v3"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/commands/watch/api"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/commands/watch/conventions"
)

// taskOptions specifies the parts of an issue task that vary between a standard
// fix and a workflow run.
type taskOptions struct {
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

// newTask constructs the queue task for an issue with consistent defaults.
func (s *Scanner) newTask(opts taskOptions) *api.QueueTask {
	num := opts.Issue.GetNumber()
	return &api.QueueTask{
		Type:             opts.Type,
		URL:              fmt.Sprintf("https://github.com/%s/%s/issues/%d", s.gh.Owner(), s.gh.Repo(), num),
		Number:           num,
		Priority:         conventions.Priority(opts.Issue.Labels),
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

// triggerInfo determines what made an issue eligible and when, which is
// recorded on the task so that pickup latency can be measured against the event
// a person actually performed rather than against the scan that noticed it.
func triggerInfo(issue *githubv39.Issue, timeline []*githubv39.Timeline, triggerLabel string, wasAutoLabeled bool) (time.Time, api.TriggerReason, string) {
	createTime := issue.GetCreatedAt()
	num := issue.GetNumber()

	if wasAutoLabeled {
		user := ""
		if issue.GetUser() != nil {
			user = fmt.Sprintf(" by %s", issue.GetUser().GetLogin())
		}
		notes := fmt.Sprintf("Issue #%d created%s at %s; trigger label '%s' auto-applied by watcher", num, user, createTime.Format(time.RFC3339), triggerLabel)
		return createTime, api.TriggerReasonIssueCreated, notes
	}

	var latestLabelTime time.Time
	var labelActor string
	for _, event := range timeline {
		if event.GetEvent() == "labeled" && event.GetLabel() != nil {
			if strings.EqualFold(event.GetLabel().GetName(), triggerLabel) {
				t := event.GetCreatedAt()
				if !t.IsZero() && t.After(latestLabelTime) {
					latestLabelTime = t
					if event.GetActor() != nil {
						labelActor = event.GetActor().GetLogin()
					}
				}
			}
		}
	}

	if !latestLabelTime.IsZero() && latestLabelTime.After(createTime) {
		actorStr := ""
		if labelActor != "" {
			actorStr = fmt.Sprintf(" by %s", labelActor)
		}
		notes := fmt.Sprintf("Issue #%d created at %s; trigger label '%s' added%s at %s", num, createTime.Format(time.RFC3339), triggerLabel, actorStr, latestLabelTime.Format(time.RFC3339))
		return latestLabelTime, api.TriggerReasonIssueLabeled, notes
	}

	notes := fmt.Sprintf("Issue #%d created at %s with trigger label '%s'", num, createTime.Format(time.RFC3339), triggerLabel)
	return createTime, api.TriggerReasonIssueCreated, notes
}

// loadProcessedIssues recovers when each issue was last worked on from the
// completed task files on disk, so that a restart does not re-queue everything
// it had already finished.
//
// A task that failed is deliberately not recorded: the issue still needs work,
// and treating the failure as a completion would park it until someone touched
// the issue again.
func loadProcessedIssues(processedDir string) map[int]time.Time {
	processed := make(map[int]time.Time)
	files, err := os.ReadDir(processedDir)
	if err != nil {
		return processed
	}
	for _, f := range files {
		if f.IsDir() || !strings.HasSuffix(f.Name(), ".yaml") || !strings.HasPrefix(f.Name(), "task-issue-") {
			continue
		}
		trimmed := strings.TrimSuffix(strings.TrimPrefix(f.Name(), "task-issue-"), ".yaml")
		num, err := strconv.Atoi(trimmed)
		if err != nil {
			continue
		}

		var t api.QueueTask
		hasTask := false
		if data, err := os.ReadFile(filepath.Join(processedDir, f.Name())); err == nil {
			if err := yaml.Unmarshal(data, &t); err == nil {
				hasTask = true
			}
		}
		if hasTask && strings.EqualFold(string(t.Status), string(api.StatusFailed)) {
			continue
		}

		info, err := f.Info()
		if err != nil {
			continue
		}
		// The file's timestamp is only a proxy for when the task finished; the
		// recorded completion time is authoritative when the file holds one.
		tTime := info.ModTime()
		if hasTask && !t.CompletedAt.IsZero() {
			tTime = t.CompletedAt
		}
		processed[num] = tTime
	}
	return processed
}
