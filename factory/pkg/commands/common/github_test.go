package common

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	githubv39 "github.com/google/go-github/v39/github"
)

func TestGetReferencedIssues(t *testing.T) {
	tests := []struct {
		name     string
		headRef  string
		title    string
		body     string
		expected map[int]bool
	}{
		{
			name:    "Branch name contains issue number",
			headRef: "issue_8883",
			title:   "Some PR title",
			body:    "Some PR body",
			expected: map[int]bool{
				8883: true,
			},
		},
		{
			name:    "Title and body contain issue number references",
			headRef: "my-dev-branch",
			title:   "Fixes #8883 and #10294",
			body:    "Resolves issue #9271 in config-connector",
			expected: map[int]bool{
				8883:  true,
				10294: true,
				9271:  true,
			},
		},
		{
			name:     "No references",
			headRef:  "master",
			title:    "Clean PR without issue link",
			body:     "Just refactoring some code",
			expected: map[int]bool{},
		},
		{
			name:    "Branch with timestamp and keyword issue link without #",
			headRef: "ada-coder-bot:issue-11414-1783386792",
			title:   "Fixes 11414",
			body:    "Resolves 11414 without hash",
			expected: map[int]bool{
				11414: true,
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			pr := &githubv39.PullRequest{
				Head: &githubv39.PullRequestBranch{
					Ref: &tc.headRef,
				},
				Title: &tc.title,
				Body:  &tc.body,
			}
			got := GetReferencedIssues(pr)
			if len(got) != len(tc.expected) {
				t.Fatalf("GetReferencedIssues() returned %v; want %v", got, tc.expected)
			}
			for num := range tc.expected {
				if !got[num] {
					t.Errorf("GetReferencedIssues() missed expected issue %d in %v", num, got)
				}
			}
		})
	}
}

func stringPtr(s string) *string {
	return &s
}

func int64Ptr(i int64) *int64 {
	return &i
}

func TestListAllCheckRuns(t *testing.T) {
	// Simulated check runs with duplicate names where higher ID is newer
	runs := []*githubv39.CheckRun{
		{
			ID:         int64Ptr(10),
			Name:       stringPtr("unit-tests"),
			Status:     stringPtr("completed"),
			Conclusion: stringPtr("failure"),
		},
		{
			ID:         int64Ptr(20),
			Name:       stringPtr("unit-tests"),
			Status:     stringPtr("completed"),
			Conclusion: stringPtr("success"),
		},
		{
			ID:         int64Ptr(15),
			Name:       stringPtr("lint"),
			Status:     stringPtr("completed"),
			Conclusion: stringPtr("success"),
		},
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"check_runs": runs})
	}))
	defer server.Close()

	client := githubv39.NewClient(nil)
	client.BaseURL, _ = url.Parse(server.URL + "/")

	result, err := ListAllCheckRuns(context.Background(), client, "owner", "repo", "sha123")
	if err != nil {
		t.Fatalf("ListAllCheckRuns returned error: %v", err)
	}

	if len(result) != 2 {
		t.Fatalf("expected 2 deduplicated check runs, got %d", len(result))
	}

	var unitTestRun *githubv39.CheckRun
	for _, r := range result {
		if r.GetName() == "unit-tests" {
			unitTestRun = r
		}
	}

	if unitTestRun == nil {
		t.Fatalf("expected unit-tests check run in results")
	}
	if unitTestRun.GetID() != 20 || unitTestRun.GetConclusion() != "success" {
		t.Errorf("expected unit-tests check run with ID 20 and conclusion success, got ID %d conclusion %s", unitTestRun.GetID(), unitTestRun.GetConclusion())
	}
}

func TestListAllStatuses(t *testing.T) {
	// Simulated statuses returned newest first by GitHub API
	statuses := []*githubv39.RepoStatus{
		{
			Context: stringPtr("ci/prow"),
			State:   stringPtr("success"),
		},
		{
			Context: stringPtr("cla/google"),
			State:   stringPtr("pending"),
		},
		{
			Context: stringPtr("ci/prow"),
			State:   stringPtr("pending"),
		},
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(statuses)
	}))
	defer server.Close()

	client := githubv39.NewClient(nil)
	client.BaseURL, _ = url.Parse(server.URL + "/")

	result, err := ListAllStatuses(context.Background(), client, "owner", "repo", "sha123")
	if err != nil {
		t.Fatalf("ListAllStatuses returned error: %v", err)
	}

	if len(result) != 2 {
		t.Fatalf("expected 2 deduplicated statuses, got %d", len(result))
	}

	var prowStatus *githubv39.RepoStatus
	for _, s := range result {
		if s.GetContext() == "ci/prow" {
			prowStatus = s
		}
	}

	if prowStatus == nil {
		t.Fatalf("expected ci/prow status in results")
	}
	if prowStatus.GetState() != "success" {
		t.Errorf("expected ci/prow status to have state success (newest), got %s", prowStatus.GetState())
	}
}
