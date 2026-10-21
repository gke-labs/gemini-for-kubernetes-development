package issues

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	githubv39 "github.com/google/go-github/v39/github"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/github"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/watch/rules"
)

// Facts reads one issue's facts without a scan, writing nothing, and they
// are the facts the scan records for it; a pull request's number is
// refused.
func TestFactsAreTheScansFacts(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/repos/test-owner/test-repo/issues/5":
			_ = json.NewEncoder(w).Encode(fanoutIssue(5, "please fix", "factory"))
		case r.URL.Path == "/repos/test-owner/test-repo/issues/6":
			_ = json.NewEncoder(w).Encode(&githubv39.Issue{Number: githubv39.Int(6), PullRequestLinks: &githubv39.PullRequestLinks{}})
		case strings.HasSuffix(r.URL.Path, "/timeline"):
			_ = json.NewEncoder(w).Encode([]*githubv39.Timeline{})
		case r.URL.Path == "/search/issues":
			_ = json.NewEncoder(w).Encode(githubv39.IssuesSearchResult{Total: githubv39.Int(0)})
		default:
			_, _ = w.Write([]byte("{}"))
		}
	}))
	defer server.Close()
	gh := githubv39.NewClient(nil)
	gh.BaseURL, _ = url.Parse(server.URL + "/")
	var rec rules.Record
	s, queue, _ := newScanner(t, Config{TriggerLabel: "factory", DryRun: true}, Deps{GitHub: github.ForRepo(gh, "test-owner", "test-repo"),
		Recorder: rules.NewSinkRecorder(func(r rules.Record) { rec = r })})

	it, _, err := s.Facts(context.Background(), 5)
	if err != nil {
		t.Fatal(err)
	}
	if it.Kind != rules.OnIssue || it.Number != 5 || it.LinkedPR != "" || it.Workflow != "" || len(it.Labels) != 1 {
		t.Errorf("item = %+v", it)
	}
	if len(queue.enqueued) != 0 {
		t.Errorf("Facts queued %v", queue.enqueued)
	}
	s.queueTasks(context.Background(), []*githubv39.Issue{fanoutIssue(5, "please fix", "factory")}, map[int]bool{})
	want, _ := json.Marshal(rec.Item)
	got, _ := json.Marshal(it)
	if string(want) != string(got) {
		t.Errorf("facts differ from the scan's:\nscan  %s\nfacts %s", want, got)
	}
	if _, _, err := s.Facts(context.Background(), 6); err == nil || !strings.Contains(err.Error(), "is a pull request") {
		t.Errorf("a pull request: %v", err)
	}
}

// An issue the engine does not decide fix for (a fix rule that never
// holds): nothing is queued, and nothing is recorded as decided.
func TestQueueTask_NothingWhenTheEngineDoesNotDecide(t *testing.T) {
	base, err := rules.Builtin()
	if err != nil {
		t.Fatal(err)
	}
	var fix rules.Rule
	for _, r := range base {
		if r.Name == "fix" {
			fix = *r
		}
	}
	fix.When.Labels = &rules.Labels{All: []string{"never"}}
	set, err := rules.Overlay(base, []*rules.Rule{&fix})
	if err != nil {
		t.Fatal(err)
	}
	control, err := rules.NewControl(nil, set)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/timeline"):
			_ = json.NewEncoder(w).Encode([]*githubv39.Timeline{})
		case r.URL.Path == "/search/issues":
			_ = json.NewEncoder(w).Encode(githubv39.IssuesSearchResult{Total: githubv39.Int(0)})
		default:
			_, _ = w.Write([]byte("{}"))
		}
	}))
	defer server.Close()
	gh := githubv39.NewClient(nil)
	gh.BaseURL, _ = url.Parse(server.URL + "/")
	var rec rules.Record
	s, queue, _ := newScanner(t, Config{}, Deps{GitHub: github.ForRepo(gh, "test-owner", "test-repo"), Rules: control,
		Recorder: rules.NewSinkRecorder(func(r rules.Record) { rec = r })})

	s.queueTasks(context.Background(), []*githubv39.Issue{fanoutIssue(61, "please fix", "factory")}, map[int]bool{})

	if len(queue.enqueued) != 0 {
		t.Errorf("queued %v under the engine's veto", keys(queue.enqueued))
	}
	if len(rec.Decided) != 0 {
		t.Errorf("record decided %v, want nothing", rec.Decided)
	}
}
