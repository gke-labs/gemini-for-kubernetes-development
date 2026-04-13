package tasks

import (
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/tasks/metadata"
	"bytes"
	"os"
	"strings"
	"testing"
	"text/template"
)

type MockIssue struct{}

func (i MockIssue) HTMLURL() string { return "http://url" }
func (i MockIssue) Number() int     { return 123 }
func (i MockIssue) Title() string   { return "Title" }
func (i MockIssue) Body() string    { return "Body" }

type MockComment struct{}

func (c MockComment) UserLogin() string { return "User" }
func (c MockComment) Body() string      { return "Comment" }

type MockRepo struct{}

func (r MockRepo) CloneURL() string { return "http://clone" }
func (r MockRepo) Name() string     { return "repo" }
func (r MockRepo) Owner() string    { return "owner" }

type MockUser struct {
	UserID string
	Email  string
	Name   string
}

type MockExtension struct {
	Source string
	Ref    string
}

type MockModel struct {
	Issue                       MockIssue
	Repo                        MockRepo
	IssueComments               []MockComment
	Models                      []string
	User                        MockUser
	PromptFile                  string
	Extensions                  []MockExtension
	Branch                      string
	PRLabel                     string
	Metadata                    metadata.Metadata
	TraceabilityMetadataEnabled bool
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
		Issue:                       MockIssue{},
		Repo:                        MockRepo{},
		IssueComments:               []MockComment{{}},
		Models:                      []string{"gemini-test"},
		User:                        MockUser{UserID: "test", Email: "test@test.com", Name: "Test User"},
		Branch:                      "test-branch",
		TraceabilityMetadataEnabled: true,
		Metadata: metadata.Metadata{
			SandboxTask:    "ns/task",
			SandboxTaskUID: "uid",
			Sandbox:        "sb",
			RepoWatch:      "rw",
			TaskType:       "fix-issue",
			Timestamp:      "2026-03-02T12:00:00Z",
		},
	}
	var w bytes.Buffer
	if err := tmpl.Execute(&w, data); err != nil {
		t.Fatalf("Failed to execute template: %v", err)
	}

	// Verify metadata footer
	missing := []string{}
	for _, expected := range []string{
		"sandbox-task: ns/task",
		"sandbox-task-uid: uid",
		"sandbox: sb",
		"repowatch: rw",
		"task-type: fix-issue",
		"timestamp: 2026-03-02T12:00:00Z",
	} {
		if !strings.Contains(w.String(), expected) {
			missing = append(missing, expected)
		}
	}
	if len(missing) > 0 {
		t.Errorf("Prompt missing %d expected metadata strings: %v\nFull prompt:\n%s", len(missing), missing, w.String())
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
	expectedBranch := `branch_name="test-branch"`
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
	expectedBranch := `local branch_name="issue-${ISSUE_NUMBER}"`
	if !bytes.Contains(w.Bytes(), []byte(expectedBranch)) {
		t.Errorf("Script does not contain expected default branch name. Got:\n%s", script)
	}
}
