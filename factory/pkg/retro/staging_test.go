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
	"fmt"
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
		rec  *tokenusage.OutcomeRecord
		want bool
	}{
		{
			name: "nil record",
			rec:  nil,
			want: false,
		},
		{
			name: "clean merged PR",
			rec: &tokenusage.OutcomeRecord{
				State:      "merged",
				CarePasses: 1,
			},
			want: false,
		},
		{
			name: "closed unmerged PR",
			rec: &tokenusage.OutcomeRecord{
				State: "closed",
			},
			want: true,
		},
		{
			name: "high care passes",
			rec: &tokenusage.OutcomeRecord{
				State:      "merged",
				CarePasses: 3,
			},
			want: true,
		},
		{
			name: "human feedback present",
			rec: &tokenusage.OutcomeRecord{
				State: "merged",
				Feedback: &tokenusage.FeedbackSummary{
					Items: 2,
				},
			},
			want: true,
		},
		{
			name: "human edits present",
			rec: &tokenusage.OutcomeRecord{
				State: "merged",
				HumanEdit: &tokenusage.HumanEdit{
					Files: 1,
					Added: 5,
				},
			},
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

func TestStageInputs(t *testing.T) {
	mux := http.NewServeMux()

	// Mock rollups
	mux.HandleFunc("/v1/outcomes/rollup", func(w http.ResponseWriter, r *http.Request) {
		by := r.URL.Query().Get("by")
		var rollups []tokenusage.OutcomeRollup
		switch by {
		case "skill":
			rollups = []tokenusage.OutcomeRollup{
				{Name: "fix-skill", TotalPRs: 5, MergedPRs: 4, AcceptanceRate: 0.8},
			}
		case "workflow":
			rollups = []tokenusage.OutcomeRollup{
				{Name: "greenfield", TotalPRs: 3, MergedPRs: 3, AcceptanceRate: 1.0},
			}
		default:
			http.Error(w, "bad by", http.StatusBadRequest)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"rollups": rollups})
	})

	// Mock outcomes
	mux.HandleFunc("/v1/outcomes", func(w http.ResponseWriter, r *http.Request) {
		outcomes := []tokenusage.OutcomeRecord{
			{
				Repo:       "GoogleCloudPlatform/k8s-config-connector",
				PR:         100,
				State:      "merged",
				CarePasses: 1,
			},
			{
				Repo:        "GoogleCloudPlatform/k8s-config-connector",
				PR:          101,
				State:       "closed",
				CarePasses:  1,
				CloseReason: "stale and buggy",
				Tasks: []tokenusage.TaskOutcomeRef{
					{
						Sandbox: "sb-1",
						Task:    "task-101",
					},
				},
			},
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"outcomes": outcomes})
	})

	// Mock task files
	mux.HandleFunc("/v1/tasks/sb-1/task-101/files/session/stream.ndjson", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"type":"tool_call","tool":"read_file"}`+"\n")
	})
	mux.HandleFunc("/v1/tasks/sb-1/task-101/files/execution.log", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "task executed cleanly\n")
	})
	mux.HandleFunc("/v1/tasks/sb-1/task-101/files/feedback.jsonl", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"author":"reviewer","body":"please fix this"}`+"\n")
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	destDir := t.TempDir()
	cfg := StagingConfig{
		CollectorURL: srv.URL,
		Owner:        "GoogleCloudPlatform",
		Repo:         "k8s-config-connector",
		Since:        time.Now().Add(-7 * 24 * time.Hour),
		DestDir:      destDir,
	}

	res, err := StageInputs(context.Background(), cfg)
	if err != nil {
		t.Fatalf("StageInputs failed: %v", err)
	}

	if len(res.CandidatePRs) != 1 || res.CandidatePRs[0] != 101 {
		t.Errorf("candidate PRs = %v, want [101]", res.CandidatePRs)
	}

	// Verify staged files in DestDir
	filesToCheck := []string{
		"rollups-skill.json",
		"rollups-workflow.json",
		"outcomes.json",
		"prs/101/outcome.json",
		"prs/101/feedback.jsonl",
		"prs/101/tasks/task-101/session/stream.ndjson",
		"prs/101/tasks/task-101/execution.log",
	}

	for _, rel := range filesToCheck {
		full := filepath.Join(destDir, rel)
		data, err := os.ReadFile(full)
		if err != nil {
			t.Errorf("missing expected file %s: %v", rel, err)
			continue
		}
		if len(data) == 0 {
			t.Errorf("file %s is empty", rel)
		}
	}
}
