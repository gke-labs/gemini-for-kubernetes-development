package issues

import (
	"context"
	"net/http"
	"testing"
	"time"

	githubv39 "github.com/google/go-github/v39/github"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/commands/watch/api"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/watch/rules"
)

// A workflow's step after a success waits out the cooldown its definition
// declares; when the definition cannot be read, the issue is held back
// this scan (gated), not stepped on a 10-minute default.
func TestQueueTask_HoldsOnAnUnreadCooldown(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		gated  string
	}{{"read", 0, ""}, {"unread", http.StatusBadGateway, "reading its workflow's cooldown failed"}} {
		t.Run(tc.name, func(t *testing.T) {
			fake := &workflowGitHub{contentsStatus: tc.status, contents: map[string]string{
				".agents/workflows/wf.md": "---\nname: wf\nmode: workflow\ncooldown: 2h\n---\nprompt\n",
			}}
			var rec rules.Record
			s, queue, _ := newScanner(t, Config{}, Deps{GitHub: fake.client(t),
				Recorder: rules.NewSinkRecorder(func(r rules.Record) { rec = r })})
			filename := issueTaskFilename(7, "wf")
			ended := time.Now().Add(-54 * time.Minute)
			queue.processed[filename] = &api.QueueTask{Status: api.StatusCompleted, StartedAt: ended.Add(-2 * time.Minute), CompletedAt: ended}
			s.queueTasks(context.Background(), []*githubv39.Issue{workflowIssue(7, "Run this: .agents/workflows/wf.md")}, map[int]bool{})
			if len(queue.enqueued) != 0 {
				t.Errorf("queued %v, 54m after a success", keys(queue.enqueued))
			}
			if rec.Gate != tc.gated {
				t.Errorf("gate %q, want %q", rec.Gate, tc.gated)
			}
			if tc.status == 0 && rec.Item.WorkflowCooldown != 2*time.Hour {
				t.Errorf("cooldown %v, want 2h read from the definition", rec.Item.WorkflowCooldown)
			}
		})
	}
}
