// Copyright 2026 The Kubernetes Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

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
	cases := []struct {
		name string
		rec  tokenusage.OutcomeRecord
		want bool
	}{
		{
			name: "merged clean",
			rec:  tokenusage.OutcomeRecord{State: "merged"},
			want: false,
		},
		{
			name: "closed unmerged",
			rec:  tokenusage.OutcomeRecord{State: "closed"},
			want: true,
		},
		{
			name: "3 care passes",
			rec:  tokenusage.OutcomeRecord{State: "merged", CarePasses: 3},
			want: true,
		},
		{
			name: "human feedback",
			rec:  tokenusage.OutcomeRecord{State: "merged", Feedback: &tokenusage.FeedbackSummary{Items: 1}},
			want: true,
		},
		{
			name: "human edits",
			rec:  tokenusage.OutcomeRecord{State: "merged", HumanEdit: &tokenusage.HumanEdit{Files: 1}},
			want: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsCandidatePR(tc.rec); got != tc.want {
				t.Errorf("IsCandidatePR() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestStageWeeklyData(t *testing.T) {
	mux := http.NewServeMux()

	// Rollup endpoint
	mux.HandleFunc("/v1/outcomes/rollup", func(w http.ResponseWriter, r *http.Request) {
		by := r.URL.Query().Get("by")
		rollups := []tokenusage.OutcomeRollup{
			{Name: by + "-test", TotalPRs: 2, MergedPRs: 1},
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"rollups": rollups})
	})

	// Outcomes endpoint
	mux.HandleFunc("/v1/outcomes", func(w http.ResponseWriter, r *http.Request) {
		outcomes := []tokenusage.OutcomeRecord{
			{
				Repo:       "test-owner/test-repo",
				PR:         100,
				State:      "merged",
				CarePasses: 0,
			},
			{
				Repo:       "test-owner/test-repo",
				PR:         101,
				State:      "closed",
				CarePasses: 1,
				Tasks: []tokenusage.TaskOutcomeRef{
					{Sandbox: "sb-1", Task: "task-1"},
				},
			},
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"outcomes": outcomes})
	})

	// Files endpoint
	mux.HandleFunc("/v1/tasks/sb-1/task-1/files/session/stream.ndjson", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-ndjson")
		_, _ = w.Write([]byte(`{"type":"message","text":"hello"}` + "\n"))
	})
	mux.HandleFunc("/v1/tasks/sb-1/task-1/files/execution.log", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("run finished with exit code 0\n"))
	})
	mux.HandleFunc("/v1/tasks/sb-1/task-1/files/feedback.jsonl", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"author":"reviewer","body":"fix this"}` + "\n"))
	})

	srv := httptest.NewServer(mux)
	defer srv.Close()

	targetDir := t.TempDir()
	opts := StagingOptions{
		CollectorURL: srv.URL,
		Repo:         "test-owner/test-repo",
		Since:        time.Now().Add(-7 * 24 * time.Hour),
		TargetDir:    targetDir,
		HTTPClient:   srv.Client(),
	}

	result, err := StageWeeklyData(context.Background(), opts)
	if err != nil {
		t.Fatalf("StageWeeklyData failed: %v", err)
	}

	if len(result.Outcomes) != 2 {
		t.Errorf("len(Outcomes) = %d, want 2", len(result.Outcomes))
	}
	if len(result.Candidates) != 1 || result.Candidates[0].PR != 101 {
		t.Errorf("candidates = %+v, want PR 101", result.Candidates)
	}

	// Verify files written
	for _, expectedFile := range []string{
		"rollup-skill.json",
		"rollup-workflow.json",
		"outcomes.json",
		"candidates.json",
		filepath.Join("candidates", "pr-101", "outcome.json"),
		filepath.Join("candidates", "pr-101", "feedback.jsonl"),
		filepath.Join("candidates", "pr-101", "sb-1-task-1", "session", "stream.ndjson"),
		filepath.Join("candidates", "pr-101", "sb-1-task-1", "execution.log"),
		filepath.Join("candidates", "pr-101", "sb-1-task-1", "feedback.jsonl"),
	} {
		p := filepath.Join(targetDir, expectedFile)
		if _, err := os.Stat(p); err != nil {
			t.Errorf("expected staged file %s missing: %v", expectedFile, err)
		}
	}
}
