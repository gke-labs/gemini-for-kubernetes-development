package watch

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"testing"

	githubv39 "github.com/google/go-github/v39/github"
)

// timelineServerOpts configures the fake GitHub timeline endpoint.
type timelineServerOpts struct {
	// pages holds the raw JSON body for each page, in order.
	pages []string
	// searchTotal is the total_count returned by the search API.
	searchTotal int
	// failSearch makes the search endpoint return a 403.
	failSearch bool
	// failTimeline makes the timeline endpoint return a 403.
	failTimeline bool
}

// newTimelineTestClient spins up a fake GitHub API that paginates the timeline
// endpoint via the Link header, exactly like the real API does.
func newTimelineTestClient(t *testing.T, opts timelineServerOpts) (*githubv39.Client, func(), *int) {
	t.Helper()

	timelineRequests := 0
	var serverURL string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		switch r.URL.Path {
		case "/search/issues":
			if opts.failSearch {
				w.WriteHeader(http.StatusForbidden)
				_, _ = w.Write([]byte(`{"message":"API rate limit exceeded"}`))
				return
			}
			_, _ = w.Write([]byte(fmt.Sprintf(`{"total_count":%d,"items":[]}`, opts.searchTotal)))

		case "/repos/test-owner/test-repo/issues/9259/timeline":
			timelineRequests++
			if opts.failTimeline {
				w.WriteHeader(http.StatusForbidden)
				_, _ = w.Write([]byte(`{"message":"API rate limit exceeded"}`))
				return
			}
			page := 1
			if p := r.URL.Query().Get("page"); p != "" {
				page, _ = strconv.Atoi(p)
			}
			if page < 1 || page > len(opts.pages) {
				_, _ = w.Write([]byte(`[]`))
				return
			}
			if page < len(opts.pages) {
				w.Header().Set("Link", fmt.Sprintf(`<%s/repos/test-owner/test-repo/issues/9259/timeline?page=%d>; rel="next"`, serverURL, page+1))
			}
			_, _ = w.Write([]byte(opts.pages[page-1]))

		default:
			t.Errorf("unexpected request to %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	serverURL = server.URL

	client := githubv39.NewClient(nil)
	client.BaseURL, _ = url.Parse(server.URL + "/")

	return client, server.Close, &timelineRequests
}

// crossRefIssue is a cross-reference from a plain issue (not a PR).
const crossRefIssue = `{"event":"cross-referenced","source":{"issue":{"number":8439,"state":"open"}}}`

// crossRefClosedPR is a cross-reference from a PR that has since been closed.
const crossRefClosedPR = `{"event":"cross-referenced","source":{"issue":{"number":11169,"state":"closed","pull_request":{"url":"https://api.github.com/repos/test-owner/test-repo/pulls/11169"}}}}`

// crossRefOpenPR is a cross-reference from the open PR that fixes the issue.
const crossRefOpenPR = `{"event":"cross-referenced","source":{"issue":{"number":12689,"state":"open","pull_request":{"url":"https://api.github.com/repos/test-owner/test-repo/pulls/12689"}}}}`

func TestListAllIssueTimeline_FollowsPagination(t *testing.T) {
	client, closeFn, requests := newTimelineTestClient(t, timelineServerOpts{
		pages: []string{
			"[" + crossRefIssue + "," + crossRefClosedPR + "]",
			"[" + crossRefOpenPR + "]",
		},
	})
	defer closeFn()

	timeline, complete, err := listAllIssueTimeline(context.Background(), client, "test-owner", "test-repo", 9259)
	if err != nil {
		t.Fatalf("listAllIssueTimeline() returned error: %v", err)
	}
	if !complete {
		t.Errorf("complete = false; want true")
	}
	if *requests != 2 {
		t.Errorf("made %d timeline requests; want 2 (pagination not followed)", *requests)
	}
	if len(timeline) != 3 {
		t.Fatalf("got %d timeline events; want 3", len(timeline))
	}
}

func TestListAllIssueTimeline_ErrorReturnsNilTimeline(t *testing.T) {
	client, closeFn, _ := newTimelineTestClient(t, timelineServerOpts{failTimeline: true})
	defer closeFn()

	timeline, complete, err := listAllIssueTimeline(context.Background(), client, "test-owner", "test-repo", 9259)
	if err == nil {
		t.Fatal("expected an error, got nil")
	}
	if complete {
		t.Errorf("complete = true; want false on error")
	}
	// Callers rely on a nil timeline meaning "unknown"; a partial slice would
	// be mistaken for an authoritative answer.
	if timeline != nil {
		t.Errorf("timeline = %v; want nil on error", timeline)
	}
}

// TestHasLinkedPR_DetectsPROnLaterPage is the regression test for
// GoogleCloudPlatform/k8s-config-connector#9259: the only open linked PR was
// event 45 of 46, so the unpaginated (30 event) timeline check missed it and
// the watcher re-triggered a fix for an issue that already had an open PR.
func TestHasLinkedPR_DetectsPROnLaterPage(t *testing.T) {
	client, closeFn, _ := newTimelineTestClient(t, timelineServerOpts{
		pages: []string{
			// Page 1 mirrors the real issue: a cross-referenced issue (not a
			// PR) and a cross-referenced PR that is already closed.
			"[" + crossRefIssue + "," + crossRefClosedPR + "]",
			"[" + crossRefOpenPR + "]",
		},
		// The search fallback would mask the bug, so make it return nothing.
		searchTotal: 0,
	})
	defer closeFn()

	linked, err := hasLinkedPR(context.Background(), client, "test-owner", "test-repo", 9259)
	if err != nil {
		t.Fatalf("hasLinkedPR() returned error: %v", err)
	}
	if !linked {
		t.Error("hasLinkedPR() = false; want true (open PR #12689 is on timeline page 2)")
	}
}

func TestHasLinkedPR_NoOpenPR(t *testing.T) {
	client, closeFn, _ := newTimelineTestClient(t, timelineServerOpts{
		pages:       []string{"[" + crossRefIssue + "," + crossRefClosedPR + "]"},
		searchTotal: 0,
	})
	defer closeFn()

	linked, err := hasLinkedPR(context.Background(), client, "test-owner", "test-repo", 9259)
	if err != nil {
		t.Fatalf("hasLinkedPR() returned error: %v", err)
	}
	if linked {
		t.Error("hasLinkedPR() = true; want false (only a closed PR and a plain issue reference it)")
	}
}

func TestHasLinkedPR_FallsBackToSearchWhenTimelineFails(t *testing.T) {
	client, closeFn, _ := newTimelineTestClient(t, timelineServerOpts{
		failTimeline: true,
		searchTotal:  1,
	})
	defer closeFn()

	linked, err := hasLinkedPR(context.Background(), client, "test-owner", "test-repo", 9259)
	if err != nil {
		t.Fatalf("hasLinkedPR() returned error: %v", err)
	}
	if !linked {
		t.Error("hasLinkedPR() = false; want true from the search fallback")
	}
}

// TestHasLinkedPR_ErrorsWhenBothChecksFail asserts we fail closed: the caller
// skips the issue on error rather than assuming there is no linked PR.
func TestHasLinkedPR_ErrorsWhenBothChecksFail(t *testing.T) {
	client, closeFn, _ := newTimelineTestClient(t, timelineServerOpts{
		failTimeline: true,
		failSearch:   true,
	})
	defer closeFn()

	if _, err := hasLinkedPR(context.Background(), client, "test-owner", "test-repo", 9259); err == nil {
		t.Error("hasLinkedPR() returned nil error; want an error so the caller skips the issue")
	}
}

func TestHasLinkedPRWithTimeline_NilTimelineFallsBack(t *testing.T) {
	client, closeFn, requests := newTimelineTestClient(t, timelineServerOpts{
		pages: []string{"[" + crossRefOpenPR + "]"},
	})
	defer closeFn()

	// A nil timeline means the caller could not determine the answer, so the
	// helper must fetch it itself instead of reporting "no linked PR".
	linked, err := hasLinkedPRWithTimeline(context.Background(), client, "test-owner", "test-repo", 9259, nil)
	if err != nil {
		t.Fatalf("hasLinkedPRWithTimeline() returned error: %v", err)
	}
	if !linked {
		t.Error("hasLinkedPRWithTimeline() = false; want true")
	}
	if *requests == 0 {
		t.Error("expected a timeline fetch when the supplied timeline is nil")
	}
}

func TestHasLinkedPRWithTimeline_ReusesSuppliedTimeline(t *testing.T) {
	client, closeFn, requests := newTimelineTestClient(t, timelineServerOpts{})
	defer closeFn()

	num := 12689
	state := "open"
	timeline := []*githubv39.Timeline{
		{
			Event: githubv39.String("cross-referenced"),
			Source: &githubv39.Source{
				Issue: &githubv39.Issue{
					Number:           &num,
					State:            &state,
					PullRequestLinks: &githubv39.PullRequestLinks{URL: githubv39.String("https://example.com/pulls/12689")},
				},
			},
		},
	}

	linked, err := hasLinkedPRWithTimeline(context.Background(), client, "test-owner", "test-repo", 9259, timeline)
	if err != nil {
		t.Fatalf("hasLinkedPRWithTimeline() returned error: %v", err)
	}
	if !linked {
		t.Error("hasLinkedPRWithTimeline() = false; want true")
	}
	if *requests != 0 {
		t.Errorf("made %d timeline requests; want 0 (supplied timeline should be reused)", *requests)
	}
}
