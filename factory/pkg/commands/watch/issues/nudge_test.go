package issues

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	githubv39 "github.com/google/go-github/v39/github"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/commands/watch/api"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/github"
)

const greenfieldWorkflow = "https://raw.githubusercontent.com/test-owner/test-repo/refs/heads/master/.agents/workflows/kcc-greenfield.txt"

// fakeWorkflowSandboxes reports the workflow sandboxes that exist.
type fakeWorkflowSandboxes map[string]bool

func (s fakeWorkflowSandboxes) Exists(_ context.Context, name string) (bool, error) {
	return s[name], nil
}

// newLinkedIssueGitHub serves the shape of the case the nudge exists for. A
// merged PR #70 closed step issue #65; the step's parent workflow issue #54
// tracks it in a progress comment, which GitHub records as a cross-reference on
// #65's timeline and #65 itself never mentions.
//
// Around it are the near misses, each with a workflow sandbox so that only the
// one property under test keeps it from being nudged: #70, the PR, which also
// cross-references #65; #33, mentioned in #65's body but closed; #44, open
// but asking for no workflow; and #22, an open workflow issue with no sandbox.
func newLinkedIssueGitHub(t *testing.T) *github.Client {
	t.Helper()
	issues := map[int]map[string]interface{}{
		54: {"number": 54, "state": "open", "body": "Workflow: " + greenfieldWorkflow},
		70: {"number": 70, "state": "closed", "pull_request": map[string]interface{}{"url": "x"}},
		33: {"number": 33, "state": "closed", "body": "Workflow: " + greenfieldWorkflow},
		44: {"number": 44, "state": "open", "body": "just an issue"},
		22: {"number": 22, "state": "open", "body": "Workflow: " + greenfieldWorkflow},
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/repos/test-owner/test-repo/issues/65/timeline" {
			_ = json.NewEncoder(w).Encode([]map[string]interface{}{
				{"event": "cross-referenced", "source": map[string]interface{}{"issue": map[string]interface{}{"number": 54}}},
				{"event": "cross-referenced", "source": map[string]interface{}{"issue": map[string]interface{}{"number": 70, "pull_request": map[string]interface{}{"url": "x"}}}},
				{"event": "labeled"},
			})
			return
		}
		var num int
		if _, err := fmt.Sscanf(r.URL.Path, "/repos/test-owner/test-repo/issues/%d", &num); err == nil {
			if issue, ok := issues[num]; ok {
				_ = json.NewEncoder(w).Encode(issue)
				return
			}
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(server.Close)

	client := githubv39.NewClient(nil)
	client.BaseURL, _ = url.Parse(server.URL + "/")
	return github.ForRepo(client, "test-owner", "test-repo")
}

// closedStepIssue is #65 as the reconciler hands it over: confirmed closed, and
// mentioning the issues around it in its body, but not its parent.
func closedStepIssue(closedAt time.Time) *githubv39.Issue {
	return &githubv39.Issue{
		Number:   githubv39.Int(65),
		State:    githubv39.String("closed"),
		Body:     githubv39.String("Step of a greenfield. See #33, #44 and #22."),
		ClosedAt: &closedAt,
	}
}

func newTestNudger(t *testing.T, cfg Config, queue Queue) *Nudger {
	t.Helper()
	if cfg.TriggerLabel == "" {
		cfg.TriggerLabel = "factory"
	}
	return NewNudger(cfg, NudgerDeps{
		GitHub: newLinkedIssueGitHub(t),
		Queue:  queue,
		Sandboxes: fakeWorkflowSandboxes{
			"wf-issue-54": true,
			"wf-issue-70": true,
			"wf-issue-33": true,
			"wf-issue-44": true,
		},
		Users: fakeUsers{user: "bot1"},
	})
}

func TestNudgeLinkedWorkflowsQueuesTheWaitingWorkflow(t *testing.T) {
	queue := newFakeQueue()
	closedAt := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)

	newTestNudger(t, Config{}, queue).NudgeLinkedWorkflows(context.Background(), closedStepIssue(closedAt))

	// The scanner's own name for the task, so the queue dedupes the two.
	filename := issueTaskFilename(54, "kcc-greenfield")
	if len(queue.enqueued) != 1 || queue.enqueued[filename] == nil {
		t.Fatalf("queued %v, want only %s", keys(queue.enqueued), filename)
	}
	task := queue.enqueued[filename]
	if task.Type != api.TypeAgentChore || task.Phase != api.PhaseChores {
		t.Errorf("queued a %s task in phase %v, want an agent chore in the chores phase", task.Type, task.Phase)
	}
	if task.AgentFile != greenfieldWorkflow || task.SessionID != "issue-54" || task.Number != 54 {
		t.Errorf("queued agent %q session %q for #%d, want the workflow of #54 in session issue-54", task.AgentFile, task.SessionID, task.Number)
	}
	if task.URL != "https://github.com/test-owner/test-repo/issues/54" {
		t.Errorf("queued URL %s, want issue #54's", task.URL)
	}
	if task.TriggerReason != api.TriggerReasonLinkedIssueClosed || !task.TriggerEventTime.Equal(closedAt) {
		t.Errorf("trigger %s at %s, want %s at the closure %s", task.TriggerReason, task.TriggerEventTime, api.TriggerReasonLinkedIssueClosed, closedAt)
	}
	if task.Assignee != "bot1" {
		t.Errorf("assigned %q, want the selected bot", task.Assignee)
	}
}

// A workflow run already queued or in progress is not queued twice.
func TestNudgeLinkedWorkflowsLeavesAQueuedRunAlone(t *testing.T) {
	queue := newFakeQueue()
	queue.existing[issueTaskFilename(54, "kcc-greenfield")] = true

	newTestNudger(t, Config{}, queue).NudgeLinkedWorkflows(context.Background(), closedStepIssue(time.Now()))

	if len(queue.enqueued) != 0 {
		t.Errorf("queued %v, want nothing while the workflow's run is already queued", keys(queue.enqueued))
	}
}

// The cooldown that holds back the scanner's re-runs is exactly what the nudge
// is there to cut short.
func TestNudgeLinkedWorkflowsSkipsTheCooldown(t *testing.T) {
	queue := newFakeQueue()
	queue.finish(issueTaskFilename(54, "kcc-greenfield"), &api.QueueTask{CompletedAt: time.Now().Add(-time.Minute)})

	newTestNudger(t, Config{}, queue).NudgeLinkedWorkflows(context.Background(), closedStepIssue(time.Now()))

	if queue.enqueued[issueTaskFilename(54, "kcc-greenfield")] == nil {
		t.Errorf("queued %v, want the workflow of #54 despite its run a minute ago", keys(queue.enqueued))
	}
}

func TestNudgeLinkedWorkflowsDryRunQueuesNothing(t *testing.T) {
	queue := newFakeQueue()

	newTestNudger(t, Config{DryRun: true}, queue).NudgeLinkedWorkflows(context.Background(), closedStepIssue(time.Now()))

	if len(queue.enqueued) != 0 {
		t.Errorf("queued %v in a dry run", keys(queue.enqueued))
	}
}

func TestNudgeLinkedWorkflowsHonoursMinNumber(t *testing.T) {
	queue := newFakeQueue()

	newTestNudger(t, Config{MinNumber: 60}, queue).NudgeLinkedWorkflows(context.Background(), closedStepIssue(time.Now()))

	if len(queue.enqueued) != 0 {
		t.Errorf("queued %v for issues below the minimum number", keys(queue.enqueued))
	}
}

func keys(m map[string]*api.QueueTask) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
