// Copyright 2026 The Kubernetes Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package tasks

import (
	"bytes"
	"os"
	"testing"
	"text/template"
)

func TestResolveConflictsScriptTemplate(t *testing.T) {
	// Read the template file
	content, err := os.ReadFile("resolve_conflicts.sh")
	if err != nil {
		t.Fatal(err)
	}

	tmpl, err := template.New("test").Parse(string(content))
	if err != nil {
		t.Fatalf("Failed to parse template: %v", err)
	}

	data := MockModel{
		PullRequest: MockPullRequest{},
		Repo:        MockRepo{},
		RepoName:    "repo",
		RepoOwner:   "owner",
		Models:      []string{"gemini-test-1", "gemini-test-2"},
		User:        MockUser{UserID: "test", Email: "test@test.com", Name: "Test User"},
		BaseRef:     "main",
		HeadRef:     "feature",
		PromptFile:  "/tmp/prompt.txt",
	}
	var w bytes.Buffer
	if err := tmpl.Execute(&w, data); err != nil {
		t.Fatalf("Failed to execute template: %v", err)
	}

	script := w.String()
	// Verify that the host is correctly populated
	expectedHost := "github.com:"
	if !bytes.Contains(w.Bytes(), []byte(expectedHost)) {
		t.Errorf("Script does not contain expected host. Got:\n%s", script)
	}

	// Verify that models are populated
	expectedModels := `MODELS=( "gemini-test-1" "gemini-test-2"  )`
	if !bytes.Contains(w.Bytes(), []byte(expectedModels)) {
		t.Errorf("Script does not contain expected models definition. Got:\n%s", script)
	}
}

func TestFixIssuePromptTemplate(t *testing.T) {
	// Read the template file
	content, err := os.ReadFile("fix_issue.txt")
	if err != nil {
		t.Fatal(err)
	}

	tmpl, err := template.New("test").Parse(string(content))
	if err != nil {
		t.Fatalf("Failed to parse template: %v", err)
	}

	data := MockModel{
		Issue:         MockIssue{},
		Repo:          MockRepo{},
		IssueComments: []MockComment{{}},
		Models:        []string{"gemini-test"},
		User:          MockUser{UserID: "test", Email: "test@test.com", Name: "Test User"},
		Branch:        "test-branch",
	}
	var w bytes.Buffer
	if err := tmpl.Execute(&w, data); err != nil {
		t.Fatalf("Failed to execute template: %v", err)
	}

	// Verify that the new instruction is present
	expected := `Final Steps:
* After you have created the PR, you MUST summarize the design decisions you made.`
	if !bytes.Contains(w.Bytes(), []byte(expected)) {
		t.Errorf("Prompt does not contain expected instruction. Got:\n%s", w.String())
	}

	// Verify that it uses the provided branch name
	expectedPush := `git push --force --set-upstream origin test-branch`
	if !bytes.Contains(w.Bytes(), []byte(expectedPush)) {
		t.Errorf("Prompt does not contain expected push command with branch. Got:\n%s", w.String())
	}
}

func TestFixIssuePromptTemplate_NoBranch(t *testing.T) {
	// Read the template file
	content, err := os.ReadFile("fix_issue.txt")
	if err != nil {
		t.Fatal(err)
	}

	tmpl, err := template.New("test").Parse(string(content))
	if err != nil {
		t.Fatalf("Failed to parse template: %v", err)
	}

	data := MockModel{
		Issue:         MockIssue{},
		Repo:          MockRepo{},
		IssueComments: []MockComment{{}},
		Models:        []string{"gemini-test"},
		User:          MockUser{UserID: "test", Email: "test@test.com", Name: "Test User"},
		// Branch left empty
	}
	var w bytes.Buffer
	if err := tmpl.Execute(&w, data); err != nil {
		t.Fatalf("Failed to execute template: %v", err)
	}

	// Verify that it uses the default branch name pattern
	expected := `git push --force --set-upstream origin issue-123`
	if !bytes.Contains(w.Bytes(), []byte(expected)) {
		t.Errorf("Prompt does not contain expected default push command. Got:\n%s", w.String())
	}
}

func TestFixIssueScriptTemplate(t *testing.T) {
	// Read the template file
	content, err := os.ReadFile("fix_issue.sh")
	if err != nil {
		t.Fatal(err)
	}

	tmpl, err := template.New("test").Parse(string(content))
	if err != nil {
		t.Fatalf("Failed to parse template: %v", err)
	}

	data := MockModel{
		Issue:         MockIssue{},
		Repo:          MockRepo{},
		IssueComments: []MockComment{{}},
		Models:        []string{"gemini-test-1", "gemini-test-2"},
		User:          MockUser{UserID: "test", Email: "test@test.com", Name: "Test User"},
		Branch:        "test-branch",
	}
	var w bytes.Buffer
	if err := tmpl.Execute(&w, data); err != nil {
		t.Fatalf("Failed to execute template: %v", err)
	}

	script := w.String()
	// Verify that the loop is present and models are populated
	expectedModels := `MODELS=( "gemini-test-1" "gemini-test-2"  )`
	if !bytes.Contains(w.Bytes(), []byte(expectedModels)) {
		t.Errorf("Script does not contain expected models definition. Got:\n%s", script)
	}

	expectedLoop := `for MODEL in "${MODELS[@]}"; do`
	if !bytes.Contains(w.Bytes(), []byte(expectedLoop)) {
		t.Errorf("Script does not contain expected loop. Got:\n%s", script)
	}

	// Verify that it uses the provided branch name
	expectedBranch := `export BRANCH_NAME="test-branch"`
	if !bytes.Contains(w.Bytes(), []byte(expectedBranch)) {
		t.Errorf("Script does not contain expected branch name. Got:\n%s", script)
	}
}

func TestFixIssueScriptTemplate_NoBranch(t *testing.T) {
	// Read the template file
	content, err := os.ReadFile("fix_issue.sh")
	if err != nil {
		t.Fatal(err)
	}

	tmpl, err := template.New("test").Parse(string(content))
	if err != nil {
		t.Fatalf("Failed to parse template: %v", err)
	}

	data := MockModel{
		Issue:         MockIssue{},
		Repo:          MockRepo{},
		IssueComments: []MockComment{{}},
		Models:        []string{"gemini-test-1", "gemini-test-2"},
		User:          MockUser{UserID: "test", Email: "test@test.com", Name: "Test User"},
		// Branch left empty
	}
	var w bytes.Buffer
	if err := tmpl.Execute(&w, data); err != nil {
		t.Fatalf("Failed to execute template: %v", err)
	}

	script := w.String()
	// Verify that it uses the default branch name with bash variable
	expectedBranch := `export BRANCH_NAME="fix-issue-${ISSUE_NUMBER}"`
	if !bytes.Contains(w.Bytes(), []byte(expectedBranch)) {
		t.Errorf("Script does not contain expected default branch name. Got:\n%s", script)
	}
}
