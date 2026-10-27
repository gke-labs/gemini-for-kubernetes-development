/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package tokenusage

import (
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Server exposes the Store over HTTP.
type Server struct {
	store *Store
	mux   *http.ServeMux
}

func NewServer(storageRoot string) (*Server, error) {
	store, err := NewStore(storageRoot)
	if err != nil {
		return nil, err
	}
	return NewServerForStore(store), nil
}

// NewServerForStore serves an open store.
func NewServerForStore(store *Store) *Server {
	s := &Server{store: store, mux: http.NewServeMux()}
	s.mux.HandleFunc("POST /v1/usage", s.handleIngest)
	s.mux.HandleFunc("POST /v1/subjects", s.handleSubjectIngest)
	s.mux.HandleFunc("GET /v1/usage/records", s.handleRecords)
	s.mux.HandleFunc("GET /v1/usage/rollups/issues", s.handleRollupIssues)
	s.mux.HandleFunc("GET /v1/usage/rollups/prs", s.handleRollupPRs)
	s.mux.HandleFunc("GET /v1/usage/rollups/daily", s.handleRollupDaily)
	s.mux.HandleFunc("GET /v1/usage/rollups/workflows", s.handleRollupWorkflows)
	s.mux.HandleFunc("GET /v1/usage/rollups/workflows/{session}", s.handleWorkflowDetail)
	s.mux.HandleFunc("POST /v1/workflows/{session}/mark-summarized", s.handleMarkSummarized)
	s.mux.HandleFunc("GET /v1/usage/tools", s.handleUsageTools)
	s.mux.HandleFunc("PUT /v1/tasks/{sandbox}/{task}", s.handleTaskPut)
	s.mux.HandleFunc("GET /v1/tasks/{sandbox}/{task}/files/{name...}", s.handleTaskFile)
	s.mux.HandleFunc("GET /v1/tasks", s.handleTasks)
	s.mux.HandleFunc("PUT /v1/prs/{owner}/{repo}/{n}/feedback", s.handleFeedbackPut)
	s.mux.HandleFunc("PUT /v1/outcomes/{owner}/{repo}/{pr}", s.handleOutcomePut)
	s.mux.HandleFunc("GET /v1/outcomes/rollup", s.handleOutcomesRollup)
	s.mux.HandleFunc("GET /v1/outcomes", s.handleOutcomes)
	s.mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	return s
}

// handleTaskPut takes a task's files as a tar stream in the body, with
// repo, issue, pr and taskType in the query, and spools them for the
// archive.
func (s *Server) handleTaskPut(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	issue, err1 := queryInt(q.Get("issue"))
	pr, err2 := queryInt(q.Get("pr"))
	if err1 != nil || err2 != nil {
		http.Error(w, "issue and pr must be numbers", http.StatusBadRequest)
		return
	}
	rec := TaskRecord{
		Repo: q.Get("repo"), Sandbox: r.PathValue("sandbox"), Task: r.PathValue("task"),
		TaskType: q.Get("taskType"), Issue: issue, PR: pr,
	}
	err := s.store.PutTask(rec, r.Body)
	switch {
	case err == nil:
		w.WriteHeader(http.StatusOK)
	case errors.Is(err, ErrNoArchive):
		http.Error(w, err.Error(), http.StatusNotImplemented)
	case errors.Is(err, ErrSpoolFull):
		http.Error(w, err.Error(), http.StatusInsufficientStorage)
	case errors.Is(err, ErrTaskTooLarge):
		http.Error(w, err.Error(), http.StatusRequestEntityTooLarge)
	case strings.HasPrefix(err.Error(), "invalid "):
		http.Error(w, err.Error(), http.StatusBadRequest)
	default:
		log.Printf("task ingest failed for %s/%s: %v", rec.Sandbox, rec.Task, err)
		http.Error(w, "storage error", http.StatusInternalServerError)
	}
}

// handleTasks lists the archived tasks' index records, never their files.
func (s *Server) handleTasks(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	issue, err1 := queryInt(q.Get("issue"))
	pr, err2 := queryInt(q.Get("pr"))
	if err1 != nil || err2 != nil {
		http.Error(w, "issue and pr must be numbers", http.StatusBadRequest)
		return
	}
	writeJSON(w, map[string]any{"tasks": s.store.ListTasks(TaskFilter{
		Repo: q.Get("repo"), Issue: issue, PR: pr, Sandbox: q.Get("sandbox"),
	})})
}

// handleTaskFile streams a single file from an archived task, unzipping if gzipped in the archive.
func (s *Server) handleTaskFile(w http.ResponseWriter, r *http.Request) {
	sandbox := r.PathValue("sandbox")
	task := r.PathValue("task")
	name := r.PathValue("name")

	if !pathPart.MatchString(sandbox) || !pathPart.MatchString(task) || !validTaskFilePath(name) {
		http.Error(w, "invalid path", http.StatusBadRequest)
		return
	}

	rc, err := s.store.ReadTaskFile(r.Context(), sandbox, task, name)
	if err != nil {
		switch {
		case errors.Is(err, ErrNoArchive):
			http.Error(w, err.Error(), http.StatusNotImplemented)
		case errors.Is(err, ErrNotFound):
			http.Error(w, "not found", http.StatusNotFound)
		default:
			log.Printf("reading task file %s/%s/%s: %v", sandbox, task, name, err)
			http.Error(w, "storage error", http.StatusInternalServerError)
		}
		return
	}
	defer rc.Close()

	w.Header().Set("Content-Type", detectContentType(name))
	w.WriteHeader(http.StatusOK)
	if _, err := io.Copy(w, rc); err != nil {
		log.Printf("streaming task file %s/%s/%s: %v", sandbox, task, name, err)
	}
}

func detectContentType(name string) string {
	switch {
	case strings.HasSuffix(name, ".json"):
		return "application/json"
	case strings.HasSuffix(name, ".ndjson"):
		return "application/x-ndjson"
	case strings.HasSuffix(name, ".yaml"), strings.HasSuffix(name, ".yml"):
		return "application/yaml"
	case strings.HasSuffix(name, ".log"), strings.HasSuffix(name, ".txt"):
		return "text/plain; charset=utf-8"
	default:
		return "application/octet-stream"
	}
}

// handleFeedbackPut takes a PR's feedback JSONL in the body and spools it
// for upload to the archive under prs/<owner>/<repo>/<n>/feedback.jsonl.gz.
func (s *Server) handleFeedbackPut(w http.ResponseWriter, r *http.Request) {
	owner := r.PathValue("owner")
	repo := r.PathValue("repo")
	nStr := r.PathValue("n")
	n, err := strconv.Atoi(nStr)
	if err != nil || n <= 0 {
		http.Error(w, "invalid pr number", http.StatusBadRequest)
		return
	}
	err = s.store.PutFeedback(owner, repo, n, r.Body)
	switch {
	case err == nil:
		w.WriteHeader(http.StatusOK)
	case errors.Is(err, ErrNoArchive):
		http.Error(w, err.Error(), http.StatusNotImplemented)
	case errors.Is(err, ErrSpoolFull):
		http.Error(w, err.Error(), http.StatusInsufficientStorage)
	case errors.Is(err, ErrTaskTooLarge):
		http.Error(w, err.Error(), http.StatusRequestEntityTooLarge)
	case strings.HasPrefix(err.Error(), "invalid "):
		http.Error(w, err.Error(), http.StatusBadRequest)
	default:
		log.Printf("feedback ingest failed for %s/%s#%d: %v", owner, repo, n, err)
		http.Error(w, "storage error", http.StatusInternalServerError)
	}
}

func (s *Server) handleUsageTools(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, s.store.SlowestShellCommands(r.URL.Query().Get("repo")))
}

func (s *Server) Handler() http.Handler { return s.mux }

func (s *Server) handleIngest(w http.ResponseWriter, r *http.Request) {
	var rec UsageRecord
	if err := json.NewDecoder(r.Body).Decode(&rec); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	if err := s.store.Put(rec); err != nil {
		if strings.Contains(err.Error(), "key is required") {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		log.Printf("ingest failed for key %q: %v", rec.Key, err)
		http.Error(w, "storage error", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func (s *Server) handleSubjectIngest(w http.ResponseWriter, r *http.Request) {
	var sub Subject
	if err := json.NewDecoder(r.Body).Decode(&sub); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	if err := s.store.PutSubject(sub); err != nil {
		if strings.Contains(err.Error(), "key is required") {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		log.Printf("subject ingest failed for key %q: %v", sub.Key, err)
		http.Error(w, "storage error", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func (s *Server) handleRollupDaily(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]any{"rollups": s.store.RollupByDay(r.URL.Query().Get("repo"))})
}

func (s *Server) handleRecords(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := ListFilter{
		Repo:     q.Get("repo"),
		Issue:    atoiOrZero(q.Get("issue")),
		PR:       atoiOrZero(q.Get("pr")),
		Workflow: q.Get("workflow"),
		Limit:    atoiOrZero(q.Get("limit")),
	}
	writeJSON(w, map[string]any{"records": s.store.List(f)})
}

func (s *Server) handleRollupIssues(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]any{"rollups": s.store.RollupByIssue(r.URL.Query().Get("repo"))})
}

func (s *Server) handleRollupPRs(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]any{"rollups": s.store.RollupByPR(r.URL.Query().Get("repo"))})
}

func (s *Server) handleRollupWorkflows(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]any{"rollups": s.store.RollupByWorkflow(r.URL.Query().Get("repo"))})
}

func (s *Server) handleWorkflowDetail(w http.ResponseWriter, r *http.Request) {
	session := r.PathValue("session")
	rollup := s.store.WorkflowRollup(r.URL.Query().Get("repo"), session, true)
	if rollup == nil {
		http.Error(w, "no usage recorded for workflow "+session, http.StatusNotFound)
		return
	}
	writeJSON(w, rollup)
}

func (s *Server) handleMarkSummarized(w http.ResponseWriter, r *http.Request) {
	session := r.PathValue("session")
	alreadyPosted, err := s.store.MarkSummarized(session)
	if err != nil {
		log.Printf("mark-summarized failed for %q: %v", session, err)
		http.Error(w, "storage error", http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]bool{"alreadyPosted": alreadyPosted})
}

func (s *Server) handleOutcomePut(w http.ResponseWriter, r *http.Request) {
	owner := r.PathValue("owner")
	repo := r.PathValue("repo")
	prStr := r.PathValue("pr")
	pr, err := strconv.Atoi(prStr)
	if err != nil || pr <= 0 {
		http.Error(w, "invalid pr number", http.StatusBadRequest)
		return
	}
	var rec OutcomeRecord
	if err := json.NewDecoder(r.Body).Decode(&rec); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	rec.Repo = owner + "/" + repo
	rec.PR = pr
	rec.Key = OutcomeKey(rec.Repo, rec.PR)

	if err := s.store.PutOutcome(rec); err != nil {
		log.Printf("outcome ingest failed for %s#%d: %v", rec.Repo, rec.PR, err)
		http.Error(w, "storage error", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func (s *Server) handleOutcomes(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := OutcomeFilter{
		Repo: q.Get("repo"),
		PR:   atoiOrZero(q.Get("pr")),
	}
	if sinceStr := q.Get("since"); sinceStr != "" {
		if t, err := parseTime(sinceStr); err == nil {
			f.Since = t
		} else {
			http.Error(w, "invalid since parameter: "+err.Error(), http.StatusBadRequest)
			return
		}
	}
	if untilStr := q.Get("until"); untilStr != "" {
		if t, err := parseTime(untilStr); err == nil {
			f.Until = t
		} else {
			http.Error(w, "invalid until parameter: "+err.Error(), http.StatusBadRequest)
			return
		}
	}
	writeJSON(w, map[string]any{"outcomes": s.store.ListOutcomes(f)})
}

func (s *Server) handleOutcomesRollup(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	repo := q.Get("repo")
	by := q.Get("by")
	if by != "skill" && by != "workflow" {
		http.Error(w, "by parameter must be 'skill' or 'workflow'", http.StatusBadRequest)
		return
	}
	var since time.Time
	if sinceStr := q.Get("since"); sinceStr != "" {
		var err error
		since, err = parseTime(sinceStr)
		if err != nil {
			http.Error(w, "invalid since parameter: "+err.Error(), http.StatusBadRequest)
			return
		}
	}
	rollups, err := s.store.RollupOutcomes(repo, since, by)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, map[string]any{"rollups": rollups})
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("encoding response: %v", err)
	}
}

func atoiOrZero(s string) int {
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0
	}
	return n
}
