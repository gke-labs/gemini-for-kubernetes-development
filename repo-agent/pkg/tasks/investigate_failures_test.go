package tasks

import (
	"bytes"
	"os"
	"testing"
	"text/template"
	"time"

	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/tasks/metadata"
)

func TestInvestigateFailuresPromptTemplate(t *testing.T) {
	// Read the template file
	content, err := os.ReadFile("investigate_failures.txt")
	if err != nil {
		t.Fatal(err)
	}

	tmpl, err := template.New("test").Parse(string(content))
	if err != nil {
		t.Fatalf("Failed to parse template: %v", err)
	}

	// Mock data
	type MockPR struct {
		URL   string
		Title string
		Body  string
	}
	type MockComment struct {
		UserLogin string
		CreatedAt time.Time
		Body      string
	}
	type MockFailedRun struct {
		ID   int64
		Name string
	}
	data := struct {
		PullRequest                 MockPR
		FailedRuns                  []MockFailedRun
		IssueComments               []MockComment
		Metadata                    metadata.Metadata
		TraceabilityMetadataEnabled bool
	}{
		PullRequest: MockPR{
			URL:   "https://github.com/owner/repo/pull/1",
			Title: "Test PR",
			Body:  "PR Body",
		},
		FailedRuns: []MockFailedRun{
			{ID: 123, Name: "Run 1"},
		},
		IssueComments: []MockComment{
			{
				UserLogin: "overseer",
				CreatedAt: time.Now(),
				Body:      "--- INVESTIGATION REPORT ---\nStatus: Failed",
			},
		},
		Metadata: metadata.Metadata{
			SandboxTask:    "ns/task",
			SandboxTaskUID: "uid",
			Sandbox:        "sb",
			RepoWatch:      "rw",
			TaskType:       "investigate-failures",
			Timestamp:      "2026-03-02T12:00:00Z",
		},
		TraceabilityMetadataEnabled: true,
	}

	var w bytes.Buffer
	if err := tmpl.Execute(&w, data); err != nil {
		t.Fatalf("Failed to execute template: %v", err)
	}

	// Verify that the new instruction is present
	expectedInstruction := "Check for Repeated Failures"
	if !bytes.Contains(w.Bytes(), []byte(expectedInstruction)) {
		t.Errorf("Prompt does not contain expected instruction: %q", expectedInstruction)
	}

	// Verify that existing comments are present
	if !bytes.Contains(w.Bytes(), []byte("--- INVESTIGATION REPORT ---")) {
		t.Errorf("Prompt does not contain existing investigation report")
	}

	// Verify metadata footer
	missing := []string{}
	for _, expected := range []string{
		"sandbox-task: ns/task",
		"sandbox-task-uid: uid",
		"sandbox: sb",
		"repowatch: rw",
		"task-type: investigate-failures",
		"timestamp: 2026-03-02T12:00:00Z",
	} {
		if !bytes.Contains(w.Bytes(), []byte(expected)) {
			missing = append(missing, expected)
		}
	}
	if len(missing) > 0 {
		t.Errorf("Prompt missing %d expected metadata strings: %v\nFull prompt:\n%s", len(missing), missing, w.String())
	}
}
