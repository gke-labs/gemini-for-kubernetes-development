package tasks

import (
	"bytes"
	"os"
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

type MockUser struct {
	UserID string
	Email  string
	Name   string
}

type MockModel struct {
	Issue         MockIssue
	Repo          MockRepo
	IssueComments []MockComment
	Model         string
	User          MockUser
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
		Model:         "gemini-test",
		User:          MockUser{UserID: "test", Email: "test@test.com", Name: "Test User"},
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
}
