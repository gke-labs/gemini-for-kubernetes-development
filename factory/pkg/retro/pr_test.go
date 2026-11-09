package retro

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

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/tokenusage"
)

func TestNoteText(t *testing.T) {
	for body, want := range map[string]string{
		"/retro the agent skipped the skill":   "the agent skipped the skill",
		"  /Retro\n\nthe review missed it\n":   "the review missed it",
		"/retro":                               "",
		"/retro\tthe fixture was hand-written": "the fixture was hand-written",
	} {
		got, ok := NoteText(body)
		if !ok || got != want {
			t.Errorf("NoteText(%q) = %q, %t; want %q, true", body, got, ok, want)
		}
	}
	for _, body := range []string{"/retrospective", "see /retro", "", "retro: no", "/overseer-ignore /retro"} {
		if IsNote(body) {
			t.Errorf("IsNote(%q) = true", body)
		}
	}
}

// A PR with an outcome record: its record, tasks and the collector's
// feedback - notes left out - are staged, with the PR and its notes; the
// feedback read from GitHub is not.
func TestStagePRWithRecord(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/outcomes", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("repo") != "o/r" || r.URL.Query().Get("pr") != "7" {
			http.Error(w, "bad query", http.StatusBadRequest)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"outcomes": []tokenusage.OutcomeRecord{{Repo: "o/r", PR: 7, State: "closed", CarePasses: 2}}})
	})
	mux.HandleFunc("GET /v1/prs/o/r/7/feedback", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"author":"alice","body":"use the helper"}` + "\n" + `{"author":"bob","body":"/retro look at the helper"}` + "\n"))
	})
	mux.HandleFunc("GET /v1/tasks", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"tasks": []tokenusage.TaskRecord{{Sandbox: "fix-r-9", Task: "recipe-fix-1", Files: []tokenusage.TaskFile{{Name: "session/stream.ndjson", Bytes: 10}}}}})
	})
	mux.HandleFunc("GET /v1/tasks/fix-r-9/recipe-fix-1/files/session/stream.ndjson", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"type":"prompt"}` + "\n"))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	dir := t.TempDir()
	st, err := StagePR(context.Background(), PRConfig{
		Collector: srv.URL, Owner: "o", Repo: "r", Dir: dir,
		PR:       PRInfo{Number: 7, URL: "https://github.com/o/r/pull/7", State: "closed"},
		Notes:    []Note{{Author: "bob", Body: "look at the helper", At: time.Now()}},
		Feedback: []tokenusage.FeedbackItem{{Author: "carol", Body: "from GitHub"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !st.Outcome || len(st.Tasks) != 1 || st.Feedback != 1 || st.FeedbackFrom != "collector" || st.Notes != 1 || st.Empty() {
		t.Fatalf("staged %+v", st)
	}
	for _, f := range []string{StagedFile, "pr.json", "outcome.json", "notes.jsonl", "tasks/fix-r-9/recipe-fix-1/manifest.json", "tasks/fix-r-9/recipe-fix-1/session/stream.ndjson"} {
		if _, err := os.Stat(filepath.Join(dir, f)); err != nil {
			t.Errorf("%s: %v", f, err)
		}
	}
	fb, _ := os.ReadFile(filepath.Join(dir, "feedback.jsonl"))
	if !strings.Contains(string(fb), "use the helper") || strings.Contains(string(fb), "/retro") || strings.Contains(string(fb), "from GitHub") {
		t.Errorf("feedback.jsonl = %s", fb)
	}
	had, missing := st.Had()
	if len(had) != 4 || len(missing) != 0 {
		t.Errorf("had %q, missing %q", had, missing)
	}
}

// Without a record (or a collector), the feedback read from GitHub is
// staged; with nothing at all, it is empty.
func TestStagePRWithoutRecord(t *testing.T) {
	dir := t.TempDir()
	st, err := StagePR(context.Background(), PRConfig{Owner: "o", Repo: "r", Dir: dir, PR: PRInfo{Number: 7},
		Feedback: []tokenusage.FeedbackItem{{Author: "carol", Body: "from GitHub"}}})
	if err != nil {
		t.Fatal(err)
	}
	if st.Outcome || st.Feedback != 1 || st.FeedbackFrom != "pr" || st.Empty() {
		t.Fatalf("staged %+v", st)
	}
	st, err = StagePR(context.Background(), PRConfig{Owner: "o", Repo: "r", Dir: t.TempDir(), PR: PRInfo{Number: 7}})
	if err != nil {
		t.Fatal(err)
	}
	if !st.Empty() || st.FeedbackFrom != "" {
		t.Fatalf("staged %+v; want empty", st)
	}
}

// A collector that does not answer fails the staging: the watch waits on
// it rather than run without the traces.
func TestPROutcomeCollectorDown(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "down", http.StatusServiceUnavailable)
	}))
	defer srv.Close()
	if _, err := PROutcome(context.Background(), srv.URL, nil, "o", "r", 7); err == nil {
		t.Fatal("no error")
	}
}

func TestStagePRTasksError(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/outcomes", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"outcomes": []tokenusage.OutcomeRecord{{Repo: "o/r", PR: 7, State: "closed"}}})
	})
	mux.HandleFunc("GET /v1/prs/o/r/7/feedback", func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	})
	mux.HandleFunc("GET /v1/tasks", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	_, err := StagePR(context.Background(), PRConfig{
		Collector: srv.URL, Owner: "o", Repo: "r", Dir: t.TempDir(),
		PR: PRInfo{Number: 7, URL: "https://github.com/o/r/pull/7", State: "closed"},
	})
	if err == nil || !strings.Contains(err.Error(), "listing tasks") {
		t.Fatalf("StagePR err = %v; want listing tasks error", err)
	}
}
