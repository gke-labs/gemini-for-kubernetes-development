package watch

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	githubv39 "github.com/google/go-github/v39/github"
)

func TestProcessPRs(t *testing.T) {
	ctx := context.Background()
	tempDir := t.TempDir()

	incomingDir := filepath.Join(tempDir, "incoming")
	processingDir := filepath.Join(tempDir, "processing")
	processedDir := filepath.Join(tempDir, "processed")
	_ = os.MkdirAll(incomingDir, 0755)
	_ = os.MkdirAll(processingDir, 0755)
	_ = os.MkdirAll(processedDir, 0755)

	httpClient := &http.Client{
		Transport: mockRoundTripper(func(req *http.Request) *http.Response {
			// PR details requests
			if strings.Contains(req.URL.Path, "/repos/owner/repo/pulls/100") {
				return &http.Response{
					StatusCode: 200,
					Body:       io.NopCloser(bytes.NewBufferString(`{"number":100,"user":{"login":"external-user"}}`)),
					Header:     make(http.Header),
				}
			}
			if strings.Contains(req.URL.Path, "/repos/owner/repo/pulls/101") {
				return &http.Response{
					StatusCode: 200,
					Body:       io.NopCloser(bytes.NewBufferString(`{"number":101,"user":{"login":"factory-bot"},"head":{"sha":"sha101"},"mergeable":false}`)),
					Header:     make(http.Header),
				}
			}
			if strings.Contains(req.URL.Path, "/repos/owner/repo/pulls/102") {
				return &http.Response{
					StatusCode: 200,
					Body:       io.NopCloser(bytes.NewBufferString(`{"number":102,"user":{"login":"factory-bot"},"head":{"sha":"sha102"},"mergeable":true}`)),
					Header:     make(http.Header),
				}
			}
			if strings.Contains(req.URL.Path, "/repos/owner/repo/pulls/103") {
				return &http.Response{
					StatusCode: 200,
					Body:       io.NopCloser(bytes.NewBufferString(`{"number":103,"user":{"login":"factory-bot"},"head":{"sha":"sha103"},"mergeable":true}`)),
					Header:     make(http.Header),
				}
			}
			if strings.Contains(req.URL.Path, "/repos/owner/repo/pulls/104") {
				return &http.Response{
					StatusCode: 200,
					Body:       io.NopCloser(bytes.NewBufferString(`{"number":104,"user":{"login":"factory-bot"},"head":{"sha":"sha104"},"mergeable":true}`)),
					Header:     make(http.Header),
				}
			}
			if strings.Contains(req.URL.Path, "/repos/owner/repo/pulls/105") {
				return &http.Response{
					StatusCode: 200,
					Body:       io.NopCloser(bytes.NewBufferString(`{"number":105,"user":{"login":"factory-bot"},"head":{"sha":"sha105"},"mergeable":true}`)),
					Header:     make(http.Header),
				}
			}
			if strings.Contains(req.URL.Path, "/repos/owner/repo/pulls/106") {
				return &http.Response{
					StatusCode: 500,
					Body:       io.NopCloser(bytes.NewBufferString("Internal Error")),
					Header:     make(http.Header),
				}
			}

			// Issue timeline / cross-referenced PRs fallback check
			if strings.Contains(req.URL.Path, "/timeline") {
				return &http.Response{
					StatusCode: 200,
					Body:       io.NopCloser(bytes.NewBufferString(`[]`)),
					Header:     make(http.Header),
				}
			}

			// Commits requests (empty commits is fine)
			if strings.Contains(req.URL.Path, "/commits") && !strings.Contains(req.URL.Path, "check-runs") {
				return &http.Response{
					StatusCode: 200,
					Body:       io.NopCloser(bytes.NewBufferString(`[]`)),
					Header:     make(http.Header),
				}
			}

			// Check Runs
			if strings.Contains(req.URL.Path, "/commits/sha102/check-runs") {
				return &http.Response{
					StatusCode: 200,
					Body:       io.NopCloser(bytes.NewBufferString(`{"check_runs":[{"id":1,"name":"ci","conclusion":"failure"}]}`)),
					Header:     make(http.Header),
				}
			}
			if strings.Contains(req.URL.Path, "/commits/sha103/check-runs") {
				return &http.Response{
					StatusCode: 200,
					Body:       io.NopCloser(bytes.NewBufferString(`{"check_runs":[{"id":2,"name":"ci","conclusion":"success"}]}`)),
					Header:     make(http.Header),
				}
			}
			if strings.Contains(req.URL.Path, "/commits/sha104/check-runs") {
				return &http.Response{
					StatusCode: 200,
					Body:       io.NopCloser(bytes.NewBufferString(`{"check_runs":[{"id":3,"name":"ci","conclusion":"success"}]}`)),
					Header:     make(http.Header),
				}
			}
			if strings.Contains(req.URL.Path, "/commits/sha105/check-runs") {
				return &http.Response{
					StatusCode: 200,
					Body:       io.NopCloser(bytes.NewBufferString(`{"check_runs":[{"id":4,"name":"ci","conclusion":"failure"}]}`)),
					Header:     make(http.Header),
				}
			}

			// Statuses
			if strings.Contains(req.URL.Path, "/statuses") {
				return &http.Response{
					StatusCode: 200,
					Body:       io.NopCloser(bytes.NewBufferString(`[]`)),
					Header:     make(http.Header),
				}
			}

			// Comments
			if strings.Contains(req.URL.Path, "/issues/103/comments") {
				return &http.Response{
					StatusCode: 200,
					Body:       io.NopCloser(bytes.NewBufferString(`[{"id":1,"user":{"login":"reviewer-user"},"body":"please fix this code","created_at":"2026-07-03T12:00:00Z"}]`)),
					Header:     make(http.Header),
				}
			}
			if strings.Contains(req.URL.Path, "/issues/104/comments") {
				return &http.Response{
					StatusCode: 200,
					Body:       io.NopCloser(bytes.NewBufferString(`[{"id":2,"user":{"login":"reviewer-user"},"body":"looks great","created_at":"2026-07-03T12:00:00Z"}]`)),
					Header:     make(http.Header),
				}
			}
			if strings.Contains(req.URL.Path, "/issues/105/comments") {
				// Return 3 comments containing "started investigating CI check failures"
				commentsJSON := `[
					{"id":3,"user":{"login":"factory-bot"},"body":"started investigating CI check failures","created_at":"2026-07-03T12:00:00Z"},
					{"id":4,"user":{"login":"factory-bot"},"body":"started investigating CI check failures","created_at":"2026-07-03T12:05:00Z"},
					{"id":5,"user":{"login":"factory-bot"},"body":"started investigating CI check failures","created_at":"2026-07-03T12:10:00Z"}
				]`
				return &http.Response{
					StatusCode: 200,
					Body:       io.NopCloser(bytes.NewBufferString(commentsJSON)),
					Header:     make(http.Header),
				}
			}

			// Reviews
			if strings.Contains(req.URL.Path, "/pulls/103/reviews") && !strings.Contains(req.URL.Path, "/comments") {
				return &http.Response{
					StatusCode: 200,
					Body:       io.NopCloser(bytes.NewBufferString(`[{"id":10,"user":{"login":"reviewer-user"},"state":"CHANGES_REQUESTED","submitted_at":"2026-07-03T11:00:00Z"}]`)),
					Header:     make(http.Header),
				}
			}
			if strings.Contains(req.URL.Path, "/pulls/103/reviews/10/comments") {
				return &http.Response{
					StatusCode: 200,
					Body:       io.NopCloser(bytes.NewBufferString(`[{"id":20,"user":{"login":"reviewer-user"},"body":"please fix this line","created_at":"2026-07-03T12:00:00Z"}]`)),
					Header:     make(http.Header),
				}
			}
			if strings.Contains(req.URL.Path, "/pulls/104/reviews") {
				return &http.Response{
					StatusCode: 200,
					Body:       io.NopCloser(bytes.NewBufferString(`[{"id":10,"user":{"login":"reviewer-user"},"state":"APPROVED"}]`)),
					Header:     make(http.Header),
				}
			}
			if strings.Contains(req.URL.Path, "/pulls/105/reviews") && !strings.Contains(req.URL.Path, "/comments") {
				return &http.Response{
					StatusCode: 200,
					Body:       io.NopCloser(bytes.NewBufferString(`[{"id":11,"user":{"login":"reviewer-user"},"state":"CHANGES_REQUESTED"}]`)),
					Header:     make(http.Header),
				}
			}
			if strings.Contains(req.URL.Path, "/pulls/105/reviews/11/comments") {
				return &http.Response{
					StatusCode: 500,
					Body:       io.NopCloser(bytes.NewBufferString("Internal Error")),
					Header:     make(http.Header),
				}
			}

			return &http.Response{
				StatusCode: 200,
				Body:       io.NopCloser(bytes.NewBufferString("")),
				Header:     make(http.Header),
			}
		}),
	}
	ghClient := githubv39.NewClient(httpClient)

	w := &watchContext{
		ctx:             ctx,
		opts:            Options{Owner: "owner", Repo: "repo", QueueDir: tempDir, Namespace: "ns"},
		ghClient:        ghClient,
		kubeClient:      newFakeKubeClient(),
		allBotUsers:     []string{"factory-bot"},
		processedIssues: make(map[int]time.Time),
		processedPRs:    make(map[int]prWatchState),
		incomingDir:     incomingDir,
		processingDir:   processingDir,
		processedDir:    processedDir,
		queueDir:        tempDir,
	}

	w.processedPRs[102] = prWatchState{
		lastSHA: "old-sha102",
	}

	failedTask := &QueueTask{
		Type:   "pr-investigate",
		Status: "Failed",
	}
	_ = writeTaskAtomically(processedDir, "task-pr-102-investigate.yaml", failedTask)

	prIssues := []*githubv39.Issue{
		{Number: githubv39.Int(100)}, // External author -> skipped
		{Number: githubv39.Int(101)}, // Merge conflict -> pr-iterate queued
		{
			Number:   githubv39.Int(102),
			Assignee: &githubv39.User{Login: githubv39.String("factory-bot")},
			Labels: []*githubv39.Label{
				{Name: githubv39.String("overseer/giving-up")},
			},
		}, // CI failed -> pr-investigate queued, unassigns stale bot, removes label
		{Number: githubv39.Int(103)}, // Labeled/review comments -> pr-comments queued
		{
			Number: githubv39.Int(104),
			Labels: []*githubv39.Label{
				{Name: githubv39.String("lgtm")},
			},
		}, // Approved -> ignored / no comments task queued
		{Number: githubv39.Int(105)}, // Failed retry limit (3 times) -> no task queued
		{Number: githubv39.Int(106)}, // PR fetch fails (500) -> early continue
	}

	w.processPRs(ctx, prIssues)

	// Verify that pr-iterate task file is created for PR 101
	if _, err := os.Stat(filepath.Join(incomingDir, "task-pr-101-iterate.yaml")); os.IsNotExist(err) {
		t.Errorf("expected task-pr-101-iterate.yaml to be queued")
	}

	// Verify that pr-investigate task file is created for PR 102
	if _, err := os.Stat(filepath.Join(incomingDir, "task-pr-102-investigate.yaml")); os.IsNotExist(err) {
		t.Errorf("expected task-pr-102-investigate.yaml to be queued")
	}

	// Verify that pr-comments task file is created for PR 103
	if _, err := os.Stat(filepath.Join(incomingDir, "task-pr-103-comments.yaml")); os.IsNotExist(err) {
		t.Errorf("expected task-pr-103-comments.yaml to be queued")
	}

	// Verify that NO task file is created for PR 104 (approved)
	if _, err := os.Stat(filepath.Join(incomingDir, "task-pr-104-comments.yaml")); err == nil {
		t.Errorf("expected no comments task for approved PR 104")
	}

	// Verify that NO task file is created for PR 105 (retry limit exceeded)
	if _, err := os.Stat(filepath.Join(incomingDir, "task-pr-105-investigate.yaml")); err == nil {
		t.Errorf("expected no investigate task for PR 105 (retry limit exceeded)")
	}
}
