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
	"log"
	"net/http"
	"strconv"
	"strings"
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
	s.mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	return s, nil
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
