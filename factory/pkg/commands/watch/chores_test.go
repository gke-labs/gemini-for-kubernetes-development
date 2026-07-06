package watch

import (
	"bytes"
	"context"
	"encoding/base64"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	githubv39 "github.com/google/go-github/v39/github"
)

func TestShouldRunChoreAt(t *testing.T) {
	// Base mock "now" time: Wednesday, July 1st, 2026 at 9:55 AM UTC
	now := time.Date(2026, 7, 1, 9, 55, 0, 0, time.UTC)

	tests := []struct {
		name     string
		schedule string
		lastRun  time.Time
		expected bool
	}{
		{
			name:     "Never run before (zero lastRun)",
			schedule: "*/30 * * * *",
			lastRun:  time.Time{},
			expected: true,
		},
		{
			name:     "Interval triggers - run at 9:15 AM (40m ago, next was 9:30 AM), now is 9:55 AM",
			schedule: "*/30 * * * *",
			lastRun:  time.Date(2026, 7, 1, 9, 15, 0, 0, time.UTC),
			expected: true,
		},
		{
			name:     "Interval skips - run at 9:40 AM (15m ago, next is 10:00 AM), now is 9:55 AM",
			schedule: "*/30 * * * *",
			lastRun:  time.Date(2026, 7, 1, 9, 40, 0, 0, time.UTC),
			expected: false,
		},
		{
			name:     "Macro descriptor @hourly - run at 8:45 AM (70m ago, next was 9:00 AM), now is 9:55 AM",
			schedule: "@hourly",
			lastRun:  time.Date(2026, 7, 1, 8, 45, 0, 0, time.UTC),
			expected: true,
		},
		{
			name:     "Macro descriptor @hourly - run at 9:15 AM (40m ago, next is 10:00 AM), now is 9:55 AM",
			schedule: "@hourly",
			lastRun:  time.Date(2026, 7, 1, 9, 15, 0, 0, time.UTC),
			expected: false,
		},
		{
			name:     "Complex schedule (9 AM on Monday) - run on Saturday 9 AM (2 days ago), should trigger",
			schedule: "0 9 * * 1",
			lastRun:  time.Date(2026, 7, 4, 9, 0, 0, 0, time.UTC),
			expected: true,
		},
		{
			name:     "Complex schedule (9 AM on Monday) - run on Monday 9:15 AM (40m ago, next is next Monday), now is Monday 9:55 AM",
			schedule: "0 9 * * 1",
			lastRun:  time.Date(2026, 7, 6, 9, 15, 0, 0, time.UTC),
			expected: false,
		},
		{
			name:     "Never schedule",
			schedule: "never",
			lastRun:  time.Date(2026, 7, 1, 8, 0, 0, 0, time.UTC),
			expected: false,
		},
		{
			name:     "Invalid schedule - fallback 24h - triggers",
			schedule: "invalid-cron-schedule",
			lastRun:  now.Add(-25 * time.Hour),
			expected: true,
		},
		{
			name:     "Invalid schedule - fallback 24h - skips",
			schedule: "invalid-cron-schedule",
			lastRun:  now.Add(-23 * time.Hour),
			expected: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			currentNow := now
			// For the Monday test cases, override mock now to Monday, July 6th, 2026, 9:55 AM UTC
			if tc.name == "Complex schedule (9 AM on Monday) - run on Saturday 9 AM (2 days ago), should trigger" ||
				tc.name == "Complex schedule (9 AM on Monday) - run on Monday 9:15 AM (40m ago, next is next Monday), now is Monday 9:55 AM" {
				currentNow = time.Date(2026, 7, 6, 9, 55, 0, 0, time.UTC)
			}

			got := shouldRunChoreAt(tc.schedule, tc.lastRun, currentNow)
			if got != tc.expected {
				t.Errorf("shouldRunChoreAt(%q, %v, %v) = %v; want %v", tc.schedule, tc.lastRun, currentNow, got, tc.expected)
			}
		})
	}
}

func TestScanChores(t *testing.T) {
	ctx := context.Background()
	tempDir := t.TempDir()

	incomingDir := filepath.Join(tempDir, "incoming")
	processingDir := filepath.Join(tempDir, "processing")
	_ = os.MkdirAll(incomingDir, 0755)
	_ = os.MkdirAll(processingDir, 0755)

	httpClient := &http.Client{
		Transport: mockRoundTripper(func(req *http.Request) *http.Response {
			if strings.Contains(req.URL.Path, "/repos/owner/repo/contents/.agents") && !strings.Contains(req.URL.Path, "chore-agent.yaml") {
				return &http.Response{
					StatusCode: 200,
					Body:       io.NopCloser(bytes.NewBufferString(`[{"name":"chore-agent.yaml","type":"file"}]`)),
					Header:     make(http.Header),
				}
			}
			if strings.Contains(req.URL.Path, "/repos/owner/repo/contents/.agents/chore-agent.yaml") {
				agentYAML := `---
name: "chore-agent"
description: "A test chore agent"
schedule: "*/30 * * * *"
---
Please run this chore.`
				encoded := base64.StdEncoding.EncodeToString([]byte(agentYAML))
				return &http.Response{
					StatusCode: 200,
					Body:       io.NopCloser(bytes.NewBufferString(`{"name":"chore-agent.yaml","type":"file","content":"` + encoded + `","encoding":"base64"}`)),
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

	scanChores(ctx, ghClient, "owner", "repo", incomingDir, processingDir, tempDir, false)

	// Verify that the task file was created
	filename := "task-chore-chore-agent.yaml"
	if _, err := os.Stat(filepath.Join(incomingDir, filename)); os.IsNotExist(err) {
		t.Errorf("expected chore task file %s to be created in incoming", filename)
	}
}

func TestScanChoresErrors(t *testing.T) {
	ctx := context.Background()
	tempDir := t.TempDir()

	incomingDir := filepath.Join(tempDir, "incoming")
	processingDir := filepath.Join(tempDir, "processing")
	_ = os.MkdirAll(incomingDir, 0755)
	_ = os.MkdirAll(processingDir, 0755)

	httpClient := &http.Client{
		Transport: mockRoundTripper(func(req *http.Request) *http.Response {
			if strings.Contains(req.URL.Path, "/repos/owner/repo/contents/.agents") && !strings.Contains(req.URL.Path, "bad-") {
				// Return two chore files: one malformed, one decode error
				filesJSON := `[
					{"name":"bad-yaml.yaml","type":"file"},
					{"name":"bad-encoding.yaml","type":"file"}
				]`
				return &http.Response{
					StatusCode: 200,
					Body:       io.NopCloser(bytes.NewBufferString(filesJSON)),
					Header:     make(http.Header),
				}
			}
			if strings.Contains(req.URL.Path, "/repos/owner/repo/contents/.agents/bad-yaml.yaml") {
				// Invalid/malformed YAML
				badYAML := `---
name: : malformed
---`
				encoded := base64.StdEncoding.EncodeToString([]byte(badYAML))
				return &http.Response{
					StatusCode: 200,
					Body:       io.NopCloser(bytes.NewBufferString(`{"name":"bad-yaml.yaml","type":"file","content":"` + encoded + `","encoding":"base64"}`)),
					Header:     make(http.Header),
				}
			}
			if strings.Contains(req.URL.Path, "/repos/owner/repo/contents/.agents/bad-encoding.yaml") {
				// Return status 200 but content string is not base64 encoded
				return &http.Response{
					StatusCode: 200,
					Body:       io.NopCloser(bytes.NewBufferString(`{"name":"bad-encoding.yaml","type":"file","content":"invalid_base64_!!@#$","encoding":"base64"}`)),
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

	// This should run without panicking or creating any task files
	scanChores(ctx, ghClient, "owner", "repo", incomingDir, processingDir, tempDir, false)

	// Verify no tasks were created in incoming Dir
	files, _ := os.ReadDir(incomingDir)
	if len(files) > 0 {
		t.Errorf("expected no task files to be created on error paths, got %d", len(files))
	}
}
