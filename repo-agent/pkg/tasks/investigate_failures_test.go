package tasks

import (
	"bytes"
	"os"
	"testing"
	"text/template"
	"time"
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
		URL  string
	}
	data := struct {
		PullRequest   MockPR
		FailedRuns    []MockFailedRun
		IssueComments []MockComment
	}{
		PullRequest: MockPR{
			URL:   "https://github.com/owner/repo/pull/1",
			Title: "Test PR",
			Body:  "PR Body",
		},
		FailedRuns: []MockFailedRun{
			{ID: 123, Name: "Run 1", URL: "https://github.com/owner/repo/actions/runs/123"},
		},
		IssueComments: []MockComment{
			{
				UserLogin: "overseer",
				CreatedAt: time.Now(),
				Body:      "### Investigating Run 1 failure\nStatus: Failed",
			},
		},
	}

	var w bytes.Buffer
	if err := tmpl.Execute(&w, data); err != nil {
		t.Fatalf("Failed to execute template: %v", err)
	}

	output := w.String()

	// Verify that the new instruction is present
	expectedInstruction := "Check for Repeated Failures"
	if !bytes.Contains(w.Bytes(), []byte(expectedInstruction)) {
		t.Errorf("Prompt does not contain expected instruction: %q", expectedInstruction)
	}

	// Verify that existing comments are present
	if !bytes.Contains(w.Bytes(), []byte("### Investigating")) {
		t.Errorf("Prompt does not contain existing investigation report")
	}

	// Verify retry limit instruction
	expectedLimit := "If there are already 3 or more such reports, and you are seeing the same failures, DO NOT attempt to fix them again."
	if !bytes.Contains(w.Bytes(), []byte(expectedLimit)) {
		t.Errorf("Prompt does not contain retry limit instruction: %q", expectedLimit)
	}

	_ = output
}
