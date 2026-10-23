package issues

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	githubv39 "github.com/google/go-github/v39/github"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/commands/common"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/commands/watch/api"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/commands/watch/conventions"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/github"
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
	return newIssueTask(s.gh, opts)
}

// newIssueTask constructs the queue task for an issue of the repository gh is
// bound to, with consistent defaults.
func newIssueTask(gh *github.Client, opts taskOptions) *api.QueueTask {
	num := opts.Issue.GetNumber()
	return &api.QueueTask{
		Type:             opts.Type,
		URL:              fmt.Sprintf("https://github.com/%s/%s/issues/%d", gh.Owner(), gh.Repo(), num),
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

// issueWorkflow is the workflow an issue asks for in its description.
type issueWorkflow struct {
	// Path is the definition's path or URL as the issue names it. It is set
	// whenever the issue names a workflow, including one that is missing.
	Path string
	// Name is the workflow's name, which the task file is named after. An
	// empty Name means the issue gets no workflow run. Once checked, it is
	// set only for a workflow that was found.
	Name string
	// Status is the outcome of checking the definition. It is meaningful only
	// once checked.
	Status common.WorkflowStatus
	// Err explains why the definition could not be loaded, for a workflow
	// that is missing, unusable or unresolved.
	Err error
	// checked reports whether Status has been settled. A workflow named by
	// convention is identified from its path alone, so that its task name is
	// known - and the gates keyed on it can run - before its definition is
	// fetched.
	checked bool
}

// identifyWorkflow works out which workflow, if any, an issue asks for in its
// description, without fetching a definition that is a workflow by naming
// convention; call check to settle whether that one can be loaded.
//
// A path that is not a workflow by convention has to be fetched here, since
// only its content says whether it is one. A path naming an ordinary skill or
// agent prompt rather than a workflow definition counts as none: the issue
// gets the standard fix.
func identifyWorkflow(ctx context.Context, gh *github.Client, issue *githubv39.Issue) issueWorkflow {
	workflowPath := common.FindWorkflowPath(issue.GetBody())
	if workflowPath == "" {
		return issueWorkflow{Status: common.NotAWorkflow, checked: true}
	}
	if common.IsWorkflowPathByConvention(workflowPath) {
		return issueWorkflow{Path: workflowPath, Name: workflowNameFromPath(workflowPath)}
	}
	// A path outside the convention can only come back found or not a
	// workflow: one that cannot be read is not taken to be a workflow.
	if status, _ := common.CheckWorkflowDefinition(ctx, gh, workflowPath); status == common.WorkflowFound {
		return issueWorkflow{Path: workflowPath, Name: workflowNameFromPath(workflowPath), Status: status, checked: true}
	}
	return issueWorkflow{Status: common.NotAWorkflow, checked: true}
}

// check settles whether an identified workflow's definition can be loaded, so
// that the caller can tell the issue's author about a missing one rather than
// queue a run that can only fail. A workflow already checked is returned as is.
func (wf issueWorkflow) check(ctx context.Context, gh *github.Client) issueWorkflow {
	if wf.checked {
		return wf
	}
	status, err := common.CheckWorkflowDefinition(ctx, gh, wf.Path)
	wf.Status, wf.checked = status, true
	if status != common.WorkflowFound {
		wf.Name, wf.Err = "", err
	}
	return wf
}

// workflowNameFromPath names a workflow after its definition's file.
func workflowNameFromPath(path string) string {
	filenameOnly := filepath.Base(path)
	return strings.TrimSuffix(filenameOnly, filepath.Ext(filenameOnly))
}

// issueTaskFilename names the task file for an issue's standard fix or, given
// a workflow name, for its workflow run. The name is what the queue dedupes
// on, so everything that queues work for an issue must agree on it.
func issueTaskFilename(num int, workflowName string) string {
	if workflowName != "" {
		return fmt.Sprintf("task-workflow-%s-issue-%d.yaml", common.Slugify(workflowName), num)
	}
	return api.RuleTaskFile(false, num, "fix")
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
