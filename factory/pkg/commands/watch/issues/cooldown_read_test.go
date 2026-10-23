package issues

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	githubv39 "github.com/google/go-github/v39/github"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/commands/watch/api"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/watch/rules"
)

// A workflow's step after a success waits out the cooldown its definition
// declares; when the definition cannot be read, the issue is held back
// this scan (gated), not stepped on a 10-minute default.
func TestEngineHoldsOnAnUnreadCooldown(t *testing.T) {
	up := true
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !up {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		_, _ = w.Write([]byte("---\nname: wf\nmode: workflow\ncooldown: 2h\n---\nprompt\n"))
	}))
	t.Cleanup(srv.Close)
	path := srv.URL + "/wf.md"
	record := filepath.Join(t.TempDir(), "d.jsonl")
	s, queue, _ := newScanner(t, Config{}, Deps{Recorder: rules.NewRecorder(record, 0, nil)})
	filename := issueTaskFilename(7, "wf")
	ended := time.Now().Add(-54 * time.Minute)
	queue.processed[filename] = &api.QueueTask{Status: api.StatusCompleted, StartedAt: ended.Add(-2 * time.Minute), CompletedAt: ended}
	updated := time.Now().Add(-time.Hour)
	issue := &githubv39.Issue{Number: githubv39.Int(7), UpdatedAt: &updated, Labels: []*githubv39.Label{{Name: githubv39.String("factory")}}}

	for _, tc := range []struct {
		up    bool
		gated string
	}{{true, ""}, {false, "reading its workflow's cooldown failed"}} {
		up = tc.up
		s.rec = s.startRecording(issue)
		s.rec.Fill(func(it *rules.Item) { it.Workflow = "wf" })
		s.recordRuns("workflow", filename)
		held := s.engineHolds(context.Background(), issue, filename, path, "wf", "workflow")
		s.rec.Finish()
		s.rec = nil
		data, err := os.ReadFile(record)
		if err != nil {
			t.Fatal(err)
		}
		lines := strings.Split(strings.TrimSpace(string(data)), "\n")
		var last rules.Record
		if err := json.Unmarshal([]byte(lines[len(lines)-1]), &last); err != nil {
			t.Fatal(err)
		}
		gated, cooldown := last.Gate, last.Item.WorkflowCooldown
		if !held {
			t.Errorf("up %v: not held, 54m after a success", tc.up)
		}
		if gated != tc.gated || (tc.up && cooldown != 2*time.Hour) {
			t.Errorf("up %v: gated %q, cooldown %v; want gated %q, cooldown 2h when read", tc.up, gated, cooldown, tc.gated)
		}
	}
}
