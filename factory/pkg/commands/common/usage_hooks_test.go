package common

import (
	"reflect"
	"sort"
	"testing"

	githubv39 "github.com/google/go-github/v39/github"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestReferencedIssueList(t *testing.T) {
	title := "Fixes #100 and #200"
	body := "Resolves #300"
	pr := &githubv39.PullRequest{
		Title: &title,
		Body:  &body,
	}

	got := ReferencedIssueList(pr)
	sort.Ints(got)
	expected := []int{100, 200, 300}
	if !reflect.DeepEqual(got, expected) {
		t.Errorf("ReferencedIssueList() = %v, want %v", got, expected)
	}
}

func TestSandboxUsageMeta(t *testing.T) {
	item := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"metadata": map[string]interface{}{
				"name": "wf-issue-123",
				"labels": map[string]interface{}{
					"factory.gemini.google.com/workflow": "kcc-greenfield",
					"factory.gemini.google.com/pr":       "42",
				},
			},
		},
	}

	meta := SandboxUsageMeta(item, "owner/repo")
	if meta.Repo != "owner/repo" {
		t.Errorf("Repo = %q, want %q", meta.Repo, "owner/repo")
	}
	if meta.Issue != 123 {
		t.Errorf("Issue = %d, want %d", meta.Issue, 123)
	}
	if meta.Workflow != "issue-123" {
		t.Errorf("Workflow = %q, want %q", meta.Workflow, "issue-123")
	}
	if meta.PR != 42 {
		t.Errorf("PR = %d, want %d", meta.PR, 42)
	}
	if meta.WorkflowName != "kcc-greenfield" {
		t.Errorf("WorkflowName = %q, want %q", meta.WorkflowName, "kcc-greenfield")
	}
}
