package github

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
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
func newTimelineTestClient(t *testing.T, opts timelineServerOpts) (*Client, func(), *int) {
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
			items := "[]"
			if opts.searchTotal > 0 {
				items = `[{"number":12689,"title":"Fixes #9259","body":"This resolves #9259","state":"open"}]`
			}
			_, _ = w.Write([]byte(fmt.Sprintf(`{"total_count":%d,"items":%s}`, opts.searchTotal, items)))

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

	gh := githubv39.NewClient(nil)
	gh.BaseURL, _ = url.Parse(server.URL + "/")

	return ForRepo(gh, "test-owner", "test-repo"), server.Close, &timelineRequests
}

// crossRefIssue is a cross-reference from a plain issue (not a PR).
const crossRefIssue = `{"event":"cross-referenced","source":{"issue":{"number":8439,"state":"open"}}}`

// crossRefClosedPR is a cross-reference from a PR that has since been closed.
const crossRefClosedPR = `{"event":"cross-referenced","source":{"issue":{"number":11169,"state":"closed","pull_request":{"url":"https://api.github.com/repos/test-owner/test-repo/pulls/11169"}}}}`

// crossRefOpenPR is a connected event from the open PR that fixes the issue.
const crossRefOpenPR = `{"event":"connected","source":{"issue":{"number":12689,"state":"open","pull_request":{"url":"https://api.github.com/repos/test-owner/test-repo/pulls/12689"}}}}`

func TestListIssueTimeline_FollowsPagination(t *testing.T) {
	client, closeFn, requests := newTimelineTestClient(t, timelineServerOpts{
		pages: []string{
			"[" + crossRefIssue + "," + crossRefClosedPR + "]",
			"[" + crossRefOpenPR + "]",
		},
	})
	defer closeFn()

	timeline, complete, err := client.ListIssueTimeline(context.Background(), 9259)
	if err != nil {
		t.Fatalf("ListIssueTimeline() returned error: %v", err)
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

func TestListIssueTimeline_ErrorReturnsNilTimeline(t *testing.T) {
	client, closeFn, _ := newTimelineTestClient(t, timelineServerOpts{failTimeline: true})
	defer closeFn()

	timeline, complete, err := client.ListIssueTimeline(context.Background(), 9259)
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

	linked, err := client.HasLinkedPR(context.Background(), 9259)
	if err != nil {
		t.Fatalf("HasLinkedPR() returned error: %v", err)
	}
	if !linked {
		t.Error("HasLinkedPR() = false; want true (open PR #12689 is on timeline page 2)")
	}
}

func TestHasLinkedPR_NoOpenPR(t *testing.T) {
	client, closeFn, _ := newTimelineTestClient(t, timelineServerOpts{
		pages:       []string{"[" + crossRefIssue + "," + crossRefClosedPR + "]"},
		searchTotal: 0,
	})
	defer closeFn()

	linked, err := client.HasLinkedPR(context.Background(), 9259)
	if err != nil {
		t.Fatalf("HasLinkedPR() returned error: %v", err)
	}
	if linked {
		t.Error("HasLinkedPR() = true; want false (only a closed PR and a plain issue reference it)")
	}
}

func TestHasLinkedPR_FallsBackToSearchWhenTimelineFails(t *testing.T) {
	client, closeFn, _ := newTimelineTestClient(t, timelineServerOpts{
		failTimeline: true,
		searchTotal:  1,
	})
	defer closeFn()

	linked, err := client.HasLinkedPR(context.Background(), 9259)
	if err != nil {
		t.Fatalf("HasLinkedPR() returned error: %v", err)
	}
	if !linked {
		t.Error("HasLinkedPR() = false; want true from the search fallback")
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

	if _, err := client.HasLinkedPR(context.Background(), 9259); err == nil {
		t.Error("HasLinkedPR() returned nil error; want an error so the caller skips the issue")
	}
}

func TestHasLinkedPRWithTimeline_NilTimelineFallsBack(t *testing.T) {
	client, closeFn, requests := newTimelineTestClient(t, timelineServerOpts{
		pages: []string{"[" + crossRefOpenPR + "]"},
	})
	defer closeFn()

	// A nil timeline means the caller could not determine the answer, so the
	// helper must fetch it itself instead of reporting "no linked PR".
	linked, err := client.HasLinkedPRWithTimeline(context.Background(), 9259, nil)
	if err != nil {
		t.Fatalf("HasLinkedPRWithTimeline() returned error: %v", err)
	}
	if !linked {
		t.Error("HasLinkedPRWithTimeline() = false; want true")
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
			Event: githubv39.String("connected"),
			Source: &githubv39.Source{
				Issue: &githubv39.Issue{
					Number:           &num,
					State:            &state,
					PullRequestLinks: &githubv39.PullRequestLinks{URL: githubv39.String("https://example.com/pulls/12689")},
				},
			},
		},
	}

	linked, err := client.HasLinkedPRWithTimeline(context.Background(), 9259, timeline)
	if err != nil {
		t.Fatalf("HasLinkedPRWithTimeline() returned error: %v", err)
	}
	if !linked {
		t.Error("HasLinkedPRWithTimeline() = false; want true")
	}
	if *requests != 0 {
		t.Errorf("made %d timeline requests; want 0 (supplied timeline should be reused)", *requests)
	}
}

// A PR opened with "Fixes #N" leaves only a cross-reference on the issue
// (KCC 13173: PR 14055 opened by factory, then a second fix queued because
// the timeline showed no connected PR). It counts when it is open, in the
// issue's repository and closes the issue; a mention does not.
func TestTimelineHasOpenLinkedPR_ClosingCrossReference(t *testing.T) {
	ref := func(state, repo, body string) *githubv39.Timeline {
		src := &githubv39.Issue{
			Number:           githubv39.Int(14055),
			State:            githubv39.String(state),
			Title:            githubv39.String("Greenfield: ConfigDeployment"),
			Body:             githubv39.String(body),
			PullRequestLinks: &githubv39.PullRequestLinks{URL: githubv39.String("https://api.github.com/repos/" + repo + "/pulls/14055")},
		}
		if repo != "" {
			src.Repository = &githubv39.Repository{FullName: githubv39.String(repo)}
		}
		return &githubv39.Timeline{Event: githubv39.String("cross-referenced"), Source: &githubv39.Source{Issue: src}}
	}
	for _, tc := range []struct {
		name string
		ev   *githubv39.Timeline
		want bool
	}{
		{"open, closes it", ref("open", "o/r", "Implements it.\n\nFixes #13173\n"), true},
		{"open, closes it, no repository given", ref("open", "", "Fixes #13173"), true},
		{"open, repository by URL", func() *githubv39.Timeline {
			e := ref("open", "", "Closes #13173")
			e.Source.Issue.RepositoryURL = githubv39.String("https://api.github.com/repos/O/R")
			return e
		}(), true},
		{"closed", ref("closed", "o/r", "Fixes #13173"), false},
		{"another repository's #13173", ref("open", "fork/r", "Fixes #13173"), false},
		{"mentions it only", ref("open", "o/r", "Related to #13173"), false},
		{"closes another issue", ref("open", "o/r", "Fixes #13174"), false},
	} {
		if got := TimelineHasOpenLinkedPR([]*githubv39.Timeline{tc.ev}, "o", "r", 13173); got != tc.want {
			t.Errorf("%s: TimelineHasOpenLinkedPR = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestGetClosingIssues(t *testing.T) {
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
			name:    "Title has closing keyword and body has non-closing references",
			headRef: "my-dev-branch",
			title:   "Fixes #8883 and #10294",
			body:    "This relates to issue #9271 in config-connector",
			expected: map[int]bool{
				8883:  true,
				10294: true,
			},
		},
		{
			name:     "No closing keywords, only references",
			headRef:  "master",
			title:    "Clean PR mentioning #8883",
			body:     "Discussed in issue #9271, but no fix here.",
			expected: map[int]bool{},
		},
		{
			name:    "Closing keyword with URL",
			headRef: "master",
			title:   "Closes the issue https://github.com/GoogleCloudPlatform/k8s-config-connector/issues/12875",
			body:    "Resolves pr #9271 with full url https://github.com/foo/bar/issues/1122",
			expected: map[int]bool{
				12875: true,
				9271:  true,
				1122:  true,
			},
		},
		{
			name:     "Branch name with greedy numbers like v2-refactor or phase-3",
			headRef:  "v2-refactor-phase-3-changes-fix-multibot-4",
			title:    "Some PR title",
			body:     "Some PR body",
			expected: map[int]bool{},
		},
		{
			name:    "Branch name with strict format issue-1234",
			headRef: "issue-1234",
			title:   "Some PR title",
			body:    "Some PR body",
			expected: map[int]bool{
				1234: true,
			},
		},
		{
			name:    "Branch name with strict format factory-issue_5678",
			headRef: "factory-issue_5678",
			title:   "Some PR title",
			body:    "Some PR body",
			expected: map[int]bool{
				5678: true,
			},
		},
		{
			name:    "Non-ASCII multi-byte UTF-8 boundary slicing",
			headRef: "master",
			title:   "Closes #1",
			body:    "Fixes #123 " + strings.Repeat("こんにちは", 30),
			expected: map[int]bool{
				1:   true,
				123: true,
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
			got := GetClosingIssues(pr)
			if len(got) != len(tc.expected) {
				t.Fatalf("GetClosingIssues() returned %v; want %v", got, tc.expected)
			}
			for num := range tc.expected {
				if !got[num] {
					t.Errorf("GetClosingIssues() missed expected issue %d in %v", num, got)
				}
			}
		})
	}
}
