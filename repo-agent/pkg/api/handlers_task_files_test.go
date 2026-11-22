package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// fakeCollector is the token-usage collector's task archive: its index
// (GET /v1/tasks) and files (GET /v1/tasks/{sandbox}/{task}/files/{name...}).
func fakeCollector(t *testing.T, index string, files map[string]string) *[]string {
	t.Helper()
	calls := &[]string{}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/tasks", func(w http.ResponseWriter, r *http.Request) {
		*calls = append(*calls, "GET /v1/tasks?"+r.URL.RawQuery)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(index))
	})
	mux.HandleFunc("GET /v1/tasks/{sandbox}/{task}/files/{name...}", func(w http.ResponseWriter, r *http.Request) {
		key := r.PathValue("sandbox") + "/" + r.PathValue("task") + "/" + r.PathValue("name")
		*calls = append(*calls, "GET "+key)
		data, ok := files[key]
		if !ok {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte(data))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	t.Setenv("COLLECTOR_URL", srv.URL)
	return calls
}

const archiveIndex = `{"tasks": [
  {"repo": "test/repo", "sandbox": "fix-repo-99", "task": "recipe-validate-kind-1",
   "files": [{"name": "steps/01-1-setup-git.log", "size": 10}, {"name": "receipts.md", "size": 20}, {"name": "runs/run1__REPORT.md", "size": 30}]},
  {"repo": "test/elsewhere", "sandbox": "fix-repo-98", "task": "recipe-validate-kind-2",
   "files": [{"name": "receipts.md", "size": 5}]}
]}`

// While its sandbox runs, a task's files are listed and read live from its
// task server; the archive is not asked.
func TestTaskFilesLive(t *testing.T) {
	daemon := &fakeSessions{files: map[string]string{
		"recipe-validate-kind-1/receipts.md":          "# Validation receipts\n",
		"recipe-validate-kind-1/steps/01-1-setup.log": "ok\n",
		"recipe-validate-kind-1/change.yaml":          "change: {}\n",
		"recipe-validate-kind-1/runs/run1__page.html": "<script>alert(1)</script>",
		"recipe-other-2/receipts.md":                  "not this task\n",
	}}
	r := overseerSessionServer(t, daemon)
	calls := fakeCollector(t, archiveIndex, nil)

	w := doAs(t, r, "root", http.MethodGet, "/api/overseers/kcc/task-files/fix-repo-42/recipe-validate-kind-1", "")
	if w.Code != http.StatusOK {
		t.Fatalf("list: %d %s", w.Code, w.Body.String())
	}
	var got TaskFiles
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, f := range got.Files {
		names = append(names, f.Name)
	}
	want := "change.yaml,receipts.md,runs/run1__page.html,steps/01-1-setup.log"
	if got.Source != "live" || strings.Join(names, ",") != want {
		t.Errorf("list = %s %v, want live %s", got.Source, names, want)
	}

	w = doAs(t, r, "root", http.MethodGet, "/api/overseers/kcc/task-files/fix-repo-42/recipe-validate-kind-1/file/receipts.md", "")
	if w.Code != http.StatusOK || w.Body.String() != "# Validation receipts\n" ||
		!strings.HasPrefix(w.Header().Get("Content-Type"), "text/markdown") || w.Header().Get("X-Task-File-Source") != "live" {
		t.Errorf("file: %d %q %s %s", w.Code, w.Body.String(), w.Header().Get("Content-Type"), w.Header().Get("X-Task-File-Source"))
	}
	// One directory down, and never as HTML.
	w = doAs(t, r, "root", http.MethodGet, "/api/overseers/kcc/task-files/fix-repo-42/recipe-validate-kind-1/file/runs/run1__page.html", "")
	if w.Code != http.StatusOK || !strings.HasPrefix(w.Header().Get("Content-Type"), "text/plain") || w.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Errorf("html file served as %d %s %s", w.Code, w.Header().Get("Content-Type"), w.Header().Get("X-Content-Type-Options"))
	}
	if len(*calls) != 0 {
		t.Errorf("the archive was asked for a live task: %v", *calls)
	}
}

// Once the sandbox is gone (or paused), the files come from the archive -
// the overseer's repository's tasks only.
func TestTaskFilesArchive(t *testing.T) {
	r := overseerSessionServer(t, &fakeSessions{})
	calls := fakeCollector(t, archiveIndex, map[string]string{
		"fix-repo-99/recipe-validate-kind-1/runs/run1__REPORT.md": "**Verdict: PASS** (3 passed, 0 failed)\n",
		"fix-repo-98/recipe-validate-kind-2/receipts.md":          "another repository's\n",
	})

	w := doAs(t, r, "root", http.MethodGet, "/api/overseers/kcc/task-files/fix-repo-99/recipe-validate-kind-1", "")
	if w.Code != http.StatusOK {
		t.Fatalf("list: %d %s", w.Code, w.Body.String())
	}
	var got TaskFiles
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Source != "archive" || len(got.Files) != 3 || got.Files[0].Name != "receipts.md" || got.Files[0].Size != 20 {
		t.Errorf("list = %+v", got)
	}
	if len(*calls) == 0 || !strings.Contains((*calls)[0], "repo=test%2Frepo") || !strings.Contains((*calls)[0], "sandbox=fix-repo-99") {
		t.Errorf("index asked as %v, want the overseer's repository and the sandbox", *calls)
	}

	w = doAs(t, r, "root", http.MethodGet, "/api/overseers/kcc/task-files/fix-repo-99/recipe-validate-kind-1/file/runs/run1__REPORT.md", "")
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "Verdict: PASS") || w.Header().Get("X-Task-File-Source") != "archive" {
		t.Errorf("archived file: %d %q", w.Code, w.Body.String())
	}

	// Another repository's archived task is not this overseer's to read.
	for _, path := range []string{
		"/api/overseers/kcc/task-files/fix-repo-98/recipe-validate-kind-2",
		"/api/overseers/kcc/task-files/fix-repo-98/recipe-validate-kind-2/file/receipts.md",
	} {
		if w := doAs(t, r, "root", http.MethodGet, path, ""); w.Code != http.StatusNotFound {
			t.Errorf("GET %s: %d, want 404", path, w.Code)
		}
	}

	// A paused sandbox reads from the archive too (its pod is down).
	w = doAs(t, r, "root", http.MethodGet, "/api/overseers/kcc/task-files/fix-repo-43/recipe-validate-kind-1", "")
	if w.Code != http.StatusNotFound {
		t.Errorf("paused sandbox with nothing archived: %d, want 404", w.Code)
	}
}

// Paths that are not a task file's are refused before anything is read.
func TestTaskFilesInvalidNames(t *testing.T) {
	r := overseerSessionServer(t, &fakeSessions{})
	calls := fakeCollector(t, archiveIndex, nil)
	for _, path := range []string{
		"/api/overseers/kcc/task-files/fix-repo-99/recipe-validate-kind-1/file/a/b/c",
		"/api/overseers/kcc/task-files/fix-repo-99/recipe-validate-kind-1/file/..%2Fsecret",
		"/api/overseers/kcc/task-files/fix-repo-99/recipe-validate-kind-1/file/.env",
		"/api/overseers/kcc/task-files/Bad_Sandbox/recipe-validate-kind-1",
	} {
		if w := doAs(t, r, "root", http.MethodGet, path, ""); w.Code != http.StatusBadRequest {
			t.Errorf("GET %s: %d, want 400", path, w.Code)
		}
	}
	if len(*calls) != 0 {
		t.Errorf("the archive was asked for an invalid path: %v", *calls)
	}
}

func TestValidTaskFileName(t *testing.T) {
	for name, want := range map[string]bool{
		"receipts.md": true, "steps/01-1-setup-git.log": true, "runs/run1__cloud__create.json": true,
		"a/b/c": false, "../x": false, ".hidden": false, "": false, "a//b": false,
	} {
		if got := validTaskFileName(name); got != want {
			t.Errorf("validTaskFileName(%q) = %v, want %v", name, got, want)
		}
	}
}
