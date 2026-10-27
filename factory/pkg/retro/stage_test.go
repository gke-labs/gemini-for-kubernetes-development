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

package retro

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/tokenusage"
)

func TestIsCandidatePR(t *testing.T) {
	tests := []struct {
		name string
		rec  *tokenusage.OutcomeRecord
		want bool
	}{
		{
			name: "merged without special conditions",
			rec:  &tokenusage.OutcomeRecord{State: "merged", CarePasses: 1},
			want: false,
		},
		{
			name: "unmerged (closed)",
			rec:  &tokenusage.OutcomeRecord{State: "closed"},
			want: true,
		},
		{
			name: "3 care passes",
			rec:  &tokenusage.OutcomeRecord{State: "merged", CarePasses: 3},
			want: true,
		},
		{
			name: "4 care passes",
			rec:  &tokenusage.OutcomeRecord{State: "merged", CarePasses: 4},
			want: true,
		},
		{
			name: "human feedback present",
			rec: &tokenusage.OutcomeRecord{
				State:    "merged",
				Feedback: &tokenusage.FeedbackSummary{Humans: 1, Items: 2},
			},
			want: true,
		},
		{
			name: "significant human edits",
			rec: &tokenusage.OutcomeRecord{
				State:     "merged",
				HumanEdit: &tokenusage.HumanEdit{Files: 2, Added: 10, Deleted: 5},
			},
			want: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := IsCandidatePR(tc.rec)
			if got != tc.want {
				t.Errorf("IsCandidatePR() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestStage(t *testing.T) {
	mux := http.NewServeMux()

	// Rollups endpoints
	mux.HandleFunc("GET /v1/outcomes/rollup", func(w http.ResponseWriter, r *http.Request) {
		by := r.URL.Query().Get("by")
		switch by {
		case "skill":
			_ = json.NewEncoder(w).Encode([]map[string]any{
				{"name": "fix-lint", "totalPRs": 5, "mergedPRs": 4},
			})
		case "workflow":
			_ = json.NewEncoder(w).Encode([]map[string]any{
				{"name": "standard", "totalPRs": 10, "mergedPRs": 8},
			})
		default:
			http.Error(w, "invalid by", http.StatusBadRequest)
		}
	})

	// Outcomes endpoint
	mux.HandleFunc("GET /v1/outcomes", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"outcomes": []tokenusage.OutcomeRecord{
				{
					Repo:       "test-owner/test-repo",
					PR:         101,
					State:      "closed", // unmerged candidate
					CarePasses: 1,
					Tasks: []tokenusage.TaskOutcomeRef{
						{Sandbox: "sb-1", Task: "task-1"},
					},
				},
				{
					Repo:       "test-owner/test-repo",
					PR:         102,
					State:      "merged",
					CarePasses: 1, // not a candidate
				},
				{
					Repo:       "test-owner/test-repo",
					PR:         103,
					State:      "merged",
					CarePasses: 4, // candidate: >= 3 passes
					Feedback:   &tokenusage.FeedbackSummary{Humans: 1, Items: 1},
					Tasks: []tokenusage.TaskOutcomeRef{
						{Sandbox: "sb-3", Task: "task-3"},
					},
				},
			},
		})
	})

	// Feedback endpoint
	mux.HandleFunc("GET /v1/prs/{owner}/{repo}/{n}/feedback", func(w http.ResponseWriter, r *http.Request) {
		n := r.PathValue("n")
		if n == "103" {
			w.Header().Set("Content-Type", "application/x-ndjson")
			_, _ = w.Write([]byte(`{"author":"alice","kind":"review","body":"fix this"}` + "\n"))
			return
		}
		http.Error(w, "not found", http.StatusNotFound)
	})

	// Task files endpoint
	mux.HandleFunc("GET /v1/tasks/{sandbox}/{task}/files/{name...}", func(w http.ResponseWriter, r *http.Request) {
		task := r.PathValue("task")
		name := r.PathValue("name")
		if task == "task-1" && name == "session/stream.ndjson" {
			_, _ = w.Write([]byte(`{"turn":1,"content":"hello"}` + "\n"))
			return
		}
		if task == "task-1" && name == "execution.log" {
			_, _ = w.Write([]byte("log output\n"))
			return
		}
		if task == "task-3" && name == "task.json" {
			_, _ = w.Write([]byte(`{"id":"task-3"}`))
			return
		}
		http.Error(w, "not found", http.StatusNotFound)
	})

	srv := httptest.NewServer(mux)
	defer srv.Close()

	targetDir := t.TempDir()
	cfg := StageConfig{
		CollectorURL: srv.URL,
		Repo:         "test-owner/test-repo",
		Since:        time.Now().Add(-7 * 24 * time.Hour),
		TargetDir:    targetDir,
	}

	res, err := Stage(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Stage failed: %v", err)
	}

	if len(res.Candidates) != 2 {
		t.Fatalf("expected 2 candidates, got %d", len(res.Candidates))
	}
	if res.Candidates[0].PR != 101 || res.Candidates[1].PR != 103 {
		t.Errorf("candidates: got %+v, want PR 101 and 103", res.Candidates)
	}

	// Verify rollups were staged
	if _, err := os.Stat(filepath.Join(targetDir, "rollups", "skill.json")); err != nil {
		t.Errorf("skill.json missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(targetDir, "rollups", "workflow.json")); err != nil {
		t.Errorf("workflow.json missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(targetDir, "rollups", "outcomes.json")); err != nil {
		t.Errorf("outcomes.json missing: %v", err)
	}

	// Verify PR 101 staged files
	if _, err := os.Stat(filepath.Join(targetDir, "prs", "101", "outcome.json")); err != nil {
		t.Errorf("PR 101 outcome.json missing: %v", err)
	}
	streamContent, err := os.ReadFile(filepath.Join(targetDir, "prs", "101", "tasks", "task-1", "session", "stream.ndjson"))
	if err != nil {
		t.Errorf("PR 101 stream.ndjson missing: %v", err)
	} else if string(streamContent) != `{"turn":1,"content":"hello"}`+"\n" {
		t.Errorf("PR 101 stream.ndjson content mismatch: %q", string(streamContent))
	}

	// Verify PR 103 staged files (feedback)
	fbContent, err := os.ReadFile(filepath.Join(targetDir, "prs", "103", "feedback.jsonl"))
	if err != nil {
		t.Errorf("PR 103 feedback.jsonl missing: %v", err)
	} else if string(fbContent) != `{"author":"alice","kind":"review","body":"fix this"}`+"\n" {
		t.Errorf("PR 103 feedback content mismatch: %q", string(fbContent))
	}
}
