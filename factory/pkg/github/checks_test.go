package github

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	githubv39 "github.com/google/go-github/v39/github"
)

func stringPtr(s string) *string {
	return &s
}

func int64Ptr(i int64) *int64 {
	return &i
}

func TestListCheckRuns(t *testing.T) {
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

	gh := githubv39.NewClient(nil)
	gh.BaseURL, _ = url.Parse(server.URL + "/")

	result, err := ForRepo(gh, "owner", "repo").ListCheckRuns(context.Background(), "sha123")
	if err != nil {
		t.Fatalf("ListCheckRuns returned error: %v", err)
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

func TestListStatuses(t *testing.T) {
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

	gh := githubv39.NewClient(nil)
	gh.BaseURL, _ = url.Parse(server.URL + "/")

	result, err := ForRepo(gh, "owner", "repo").ListStatuses(context.Background(), "sha123")
	if err != nil {
		t.Fatalf("ListStatuses returned error: %v", err)
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
