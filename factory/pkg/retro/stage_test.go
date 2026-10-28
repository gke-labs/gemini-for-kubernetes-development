package retro

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/tokenusage"
)

// A fake collector: three outcomes, of which two are picked; one with
// feedback and an archived task, one with neither.
func fakeCollector(t *testing.T) *httptest.Server {
	t.Helper()
	outcomes := []tokenusage.OutcomeRecord{
		{Repo: "o/r", PR: 1, State: "merged"},
		{Repo: "o/r", PR: 2, State: "closed", Feedback: &tokenusage.FeedbackSummary{Items: 2}},
		{Repo: "o/r", PR: 3, State: "merged", CarePasses: 4},
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/outcomes", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("repo") != "o/r" || r.URL.Query().Get("since") == "" || r.URL.Query().Get("until") == "" {
			http.Error(w, "bad query "+r.URL.RawQuery, http.StatusBadRequest)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"outcomes": outcomes})
	})
	mux.HandleFunc("GET /v1/outcomes/rollup", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"rollups": []tokenusage.OutcomeRollup{{Name: r.URL.Query().Get("by") + "-x", TotalPRs: 3}}})
	})
	mux.HandleFunc("GET /v1/prs/o/r/2/feedback", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"author":"alice","body":"use the helper"}` + "\n"))
	})
	mux.HandleFunc("GET /v1/tasks", func(w http.ResponseWriter, r *http.Request) {
		var tasks []tokenusage.TaskRecord
		if r.URL.Query().Get("pr") == "2" {
			tasks = []tokenusage.TaskRecord{{Sandbox: "fix-r-9", Task: "recipe-fix-1", Files: []tokenusage.TaskFile{
				{Name: "session/stream.ndjson", Bytes: 10},
				{Name: "huge.log", Bytes: 1 << 40},
				{Name: "../escape", Bytes: 1},
			}}}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"tasks": tasks})
	})
	mux.HandleFunc("GET /v1/tasks/fix-r-9/recipe-fix-1/files/session/stream.ndjson", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"type":"prompt"}` + "\n"))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// Stage writes the layout: the window's records and rollups, and for each
// picked PR its feedback and archived task files, skipping a file too big
// or with an unsafe name.
func TestStage(t *testing.T) {
	srv := fakeCollector(t)
	dir := t.TempDir()
	st, err := Stage(context.Background(), Config{Collector: srv.URL, Owner: "o", Repo: "r", Since: time.Now().Add(-7 * 24 * time.Hour), Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if st.Outcomes != 3 || len(st.PRs) != 2 || st.PRs[0].PR != 2 || st.PRs[1].PR != 3 {
		t.Fatalf("staged %+v", st)
	}
	two, three := st.PRs[0], st.PRs[1]
	if !two.Feedback || !slices.Contains(two.Reasons, "closed unmerged") || !slices.Equal(two.Tasks, []string{"fix-r-9/recipe-fix-1"}) || len(two.Skipped) != 2 {
		t.Errorf("PR 2: %+v", two)
	}
	if three.Feedback || !slices.Contains(three.Reasons, "4 care passes") || len(three.Skipped) != 0 {
		t.Errorf("PR 3: %+v", three)
	}
	for _, f := range []string{"staged.json", "outcomes.jsonl", "rollups/skill.json", "rollups/workflow.json",
		"prs/2/feedback.jsonl", "prs/2/tasks/fix-r-9/recipe-fix-1/manifest.json", "prs/2/tasks/fix-r-9/recipe-fix-1/session/stream.ndjson"} {
		if _, err := os.Stat(filepath.Join(dir, f)); err != nil {
			t.Errorf("%s: %v", f, err)
		}
	}
	for _, f := range []string{"prs/1", "prs/3/feedback.jsonl", "prs/2/tasks/fix-r-9/recipe-fix-1/huge.log", "prs/2/tasks/fix-r-9/escape"} {
		if _, err := os.Stat(filepath.Join(dir, f)); err == nil {
			t.Errorf("%s staged", f)
		}
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "outcomes.jsonl")); strings.Count(string(b), "\n") != 3 {
		t.Errorf("outcomes.jsonl:\n%s", b)
	}
}

// What makes a PR worth a look.
func TestReasons(t *testing.T) {
	for name, tc := range map[string]struct {
		o    tokenusage.OutcomeRecord
		want int
	}{
		"merged, nothing else":  {tokenusage.OutcomeRecord{State: "merged"}, 0},
		"closed":                {tokenusage.OutcomeRecord{State: "closed"}, 1},
		"two care passes":       {tokenusage.OutcomeRecord{State: "merged", CarePasses: 2}, 0},
		"small human edit":      {tokenusage.OutcomeRecord{State: "merged", HumanEdit: &tokenusage.HumanEdit{Files: 1, Added: 3}}, 0},
		"large human edit":      {tokenusage.OutcomeRecord{State: "merged", HumanEdit: &tokenusage.HumanEdit{Files: 2, Added: 15, Deleted: 10}}, 1},
		"closed with feedback":  {tokenusage.OutcomeRecord{State: "closed", Feedback: &tokenusage.FeedbackSummary{Items: 1}}, 2},
		"feedback summary of 0": {tokenusage.OutcomeRecord{State: "merged", Feedback: &tokenusage.FeedbackSummary{}}, 0},
	} {
		if got := Reasons(tc.o); len(got) != tc.want {
			t.Errorf("%s: %v", name, got)
		}
	}
}

// A collector that refuses the window fails the stage.
func TestStageNeedsTheWindow(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "no archive", http.StatusNotImplemented)
	}))
	defer srv.Close()
	if _, err := Stage(context.Background(), Config{Collector: srv.URL, Owner: "o", Repo: "r", Dir: t.TempDir()}); err == nil || !strings.Contains(err.Error(), "outcome records") {
		t.Errorf("err = %v", err)
	}
}
