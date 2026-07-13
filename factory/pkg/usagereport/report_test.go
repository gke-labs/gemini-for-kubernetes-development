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

package usagereport

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPublishAndMarkSummarized(t *testing.T) {
	var received UsageRecord
	markCalls := 0
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/usage":
			if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			w.WriteHeader(http.StatusOK)
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/mark-summarized"):
			markCalls++
			_ = json.NewEncoder(w).Encode(map[string]bool{"alreadyPosted": markCalls > 1})
		default:
			http.NotFound(w, r)
		}
	}))
	defer ts.Close()
	t.Setenv("COLLECTOR_URL", ts.URL)

	if !Enabled() {
		t.Fatal("expected Enabled() with COLLECTOR_URL set")
	}

	rec := UsageRecord{
		Key: "sb:/workspaces/tasks/fix-1", Repo: "org/repo", Issue: 5,
		Stats: Stats{Models: map[string]ModelUsage{"m": {Tokens: TokenUsage{Total: 10}}}},
	}
	if err := Publish(context.Background(), rec); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if received.Key != rec.Key || received.Issue != 5 {
		t.Errorf("collector received %+v", received)
	}

	already, err := markSummarized(context.Background(), "issue-5")
	if err != nil || already {
		t.Fatalf("first markSummarized: (%v, %v)", already, err)
	}
	already, err = markSummarized(context.Background(), "issue-5")
	if err != nil || !already {
		t.Fatalf("second markSummarized: (%v, %v)", already, err)
	}
}

func TestPublishDisabled(t *testing.T) {
	t.Setenv("COLLECTOR_URL", "")
	if Enabled() {
		t.Fatal("expected disabled without COLLECTOR_URL")
	}
	// No-op, no error, no network.
	if err := Publish(context.Background(), UsageRecord{Key: "x"}); err != nil {
		t.Fatalf("Publish while disabled: %v", err)
	}
}

func TestTaskTypeFromDir(t *testing.T) {
	cases := map[string]string{
		"/workspaces/tasks/fix-20260713-101500":         "fix",
		"/workspaces/tasks/agent-chore-20260713-101500": "agent",
		"/workspaces/tasks/investigate-20260713-101500": "investigate",
		"/workspaces/tasks/weird":                       "weird",
	}
	for dir, want := range cases {
		if got := taskTypeFromDir(dir); got != want {
			t.Errorf("taskTypeFromDir(%s) = %s, want %s", dir, got, want)
		}
	}
}

func TestFormatWorkflowSummary(t *testing.T) {
	r := &Rollup{
		Key: "issue-42", WorkflowName: "greenfield", TaskCount: 3, PRs: []int{101, 102},
		Stats: Stats{Models: map[string]ModelUsage{
			"gemini-2.5-pro": {
				API:    APIUsage{TotalRequests: 4, TotalErrors: 1},
				Tokens: TokenUsage{Input: 100, Output: 50, Cached: 20, Thoughts: 10, Total: 180},
			},
			"gemini-2.5-flash": {
				API:    APIUsage{TotalRequests: 2},
				Tokens: TokenUsage{Input: 10, Output: 5, Total: 15},
			},
		}},
	}
	body := FormatWorkflowSummary(r)
	for _, want := range []string{
		"Gemini token usage summary",
		"**greenfield**",
		"Tasks: 3",
		"#101, #102",
		"| gemini-2.5-pro | 4 | 1 | 100 | 50 | 20 | 10 | 180 |",
		"| gemini-2.5-flash | 2 | 0 | 10 | 5 | 0 | 0 | 15 |",
		"| **Total** | 6 | 1 | 110 | 55 | 20 | 10 | 195 |",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("summary missing %q in:\n%s", want, body)
		}
	}
}
