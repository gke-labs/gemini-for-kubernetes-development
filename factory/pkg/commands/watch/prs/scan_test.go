package prs

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	githubv39 "github.com/google/go-github/v39/github"
)

// TestScanCandidates_Dedupes covers the deduplication guarantee: a pull request
// that is both assigned and labelled is handed out once, so it is evaluated at
// most once in a cycle.
func TestScanCandidates_Dedupes(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// The same pull request comes back from the assignee query and the
		// label query, alongside an issue that must be dropped entirely.
		_ = json.NewEncoder(w).Encode([]*githubv39.Issue{
			{Number: githubv39.Int(7), PullRequestLinks: &githubv39.PullRequestLinks{}},
			{Number: githubv39.Int(8)},
		})
	}))
	defer server.Close()

	gh := githubv39.NewClient(nil)
	gh.BaseURL, _ = url.Parse(server.URL + "/")

	s, _ := newTestScanner(t, t.TempDir(), testOpts{
		GitHub:       gh,
		BotUsers:     []string{"bot1", "bot2"},
		TriggerLabel: "factory",
	})

	candidates, err := s.scanCandidates(context.Background())
	if err != nil {
		t.Fatalf("scanCandidates() error = %v", err)
	}
	if len(candidates) != 1 {
		t.Fatalf("scanCandidates() returned %d candidates, want 1", len(candidates))
	}
	if candidates[0].GetNumber() != 7 {
		t.Errorf("candidate = #%d, want #7 (the pull request, not the issue)", candidates[0].GetNumber())
	}
}
