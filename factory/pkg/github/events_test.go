package github

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	githubv39 "github.com/google/go-github/v39/github"
)

// newEventsTestClient spins up a fake GitHub API that serves totalPages pages of
// issue events, and returns the number of requests it received.
func newEventsTestClient(t *testing.T, totalPages int, fail bool) (*Client, func(), *int) {
	t.Helper()

	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if fail {
			w.WriteHeader(http.StatusForbidden)
			return
		}

		page := 1
		if p := r.URL.Query().Get("page"); p != "" {
			_, _ = fmt.Sscanf(p, "%d", &page)
		}
		if page < totalPages {
			w.Header().Set("Link", fmt.Sprintf(`<%s?page=%d>; rel="next"`, r.URL.Path, page+1))
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode([]*githubv39.IssueEvent{
			{
				Event: githubv39.String("unlabeled"),
				Label: &githubv39.Label{Name: githubv39.String(fmt.Sprintf("page-%d", page))},
			},
		})
	}))

	ghClient := githubv39.NewClient(nil)
	ghClient.BaseURL, _ = url.Parse(server.URL + "/")

	return ForRepo(ghClient, "test-owner", "test-repo"), server.Close, &requests
}

func TestListIssueEvents_FollowsPagination(t *testing.T) {
	client, closeFn, requests := newEventsTestClient(t, 3, false)
	defer closeFn()

	events, complete, err := client.ListIssueEvents(context.Background(), 100)
	if err != nil {
		t.Fatalf("ListIssueEvents() returned error: %v", err)
	}
	if !complete {
		t.Error("ListIssueEvents() complete = false; want true")
	}
	if len(events) != 3 {
		t.Errorf("ListIssueEvents() returned %d events; want 3 (one per page)", len(events))
	}
	if *requests != 3 {
		t.Errorf("ListIssueEvents() made %d requests; want 3", *requests)
	}
}

func TestListIssueEvents_ErrorReturnsNilEvents(t *testing.T) {
	client, closeFn, _ := newEventsTestClient(t, 1, true)
	defer closeFn()

	events, complete, err := client.ListIssueEvents(context.Background(), 100)
	if err == nil {
		t.Fatal("ListIssueEvents() returned nil error; want an error")
	}
	// A nil slice is how callers tell "could not be determined" apart from
	// "there were no events", so a failure must not hand back an empty slice.
	if events != nil {
		t.Errorf("ListIssueEvents() = %v; want nil on error", events)
	}
	if complete {
		t.Error("ListIssueEvents() complete = true; want false on error")
	}
}

func TestListIssueEvents_NoClientIsNotReady(t *testing.T) {
	client := ForRepo(nil, "test-owner", "test-repo")

	if _, _, err := client.ListIssueEvents(context.Background(), 100); err == nil {
		t.Fatal("ListIssueEvents() with no client returned nil error; want an error")
	}
}
