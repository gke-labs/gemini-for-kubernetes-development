package common

import (
	"testing"

	githubv39 "github.com/google/go-github/v39/github"
)

func TestGetReferencedIssues(t *testing.T) {
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
			name:    "Title and body contain issue number references",
			headRef: "my-dev-branch",
			title:   "Fixes #8883 and #10294",
			body:    "Resolves issue #9271 in config-connector",
			expected: map[int]bool{
				8883:  true,
				10294: true,
				9271:  true,
			},
		},
		{
			name:     "No references",
			headRef:  "master",
			title:    "Clean PR without issue link",
			body:     "Just refactoring some code",
			expected: map[int]bool{},
		},
		{
			name:    "Branch with timestamp and keyword issue link without #",
			headRef: "ada-coder-bot:issue-11414-1783386792",
			title:   "Fixes 11414",
			body:    "Resolves 11414 without hash",
			expected: map[int]bool{
				11414: true,
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
			got := GetReferencedIssues(pr)
			if len(got) != len(tc.expected) {
				t.Fatalf("GetReferencedIssues() returned %v; want %v", got, tc.expected)
			}
			for num := range tc.expected {
				if !got[num] {
					t.Errorf("GetReferencedIssues() missed expected issue %d in %v", num, got)
				}
			}
		})
	}
}

func TestGetParentIssuesFromIssue(t *testing.T) {
	tests := []struct {
		name     string
		issueNum int
		title    string
		body     string
		expected map[int]bool
	}{
		{
			name:     "Workflow Issue reference in body",
			issueNum: 101,
			title:    "Greenfield: Implement direct KRM types for Foo",
			body:     "Workflow Issue: #100\n\nPlease follow the skill...",
			expected: map[int]bool{100: true},
		},
		{
			name:     "Part of and parent reference in body",
			issueNum: 102,
			title:    "Implement controller for Bar",
			body:     "Part of #200\nParent: #300",
			expected: map[int]bool{200: true, 300: true},
		},
		{
			name:     "Keyword without hash and case insensitivity",
			issueNum: 103,
			title:    "WORKFLOW 400",
			body:     "tracked in 500",
			expected: map[int]bool{400: true, 500: true},
		},
		{
			name:     "Does not match unrelated numbers or self reference",
			issueNum: 104,
			title:    "Step 1: Direct API Types for #104",
			body:     "Phase 2 has 3 steps. Port 8080. line #123 should not match.",
			expected: map[int]bool{},
		},
		{
			name:     "Nil issue",
			issueNum: 0,
			title:    "",
			body:     "",
			expected: map[int]bool{},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var issue *githubv39.Issue
			if tc.name != "Nil issue" {
				num := tc.issueNum
				issue = &githubv39.Issue{
					Number: &num,
					Title:  &tc.title,
					Body:   &tc.body,
				}
			}
			got := GetParentIssuesFromIssue(issue)
			if len(got) != len(tc.expected) {
				t.Fatalf("GetParentIssuesFromIssue() returned %v; want %v", got, tc.expected)
			}
			for num := range tc.expected {
				if !got[num] {
					t.Errorf("GetParentIssuesFromIssue() missed expected issue %d in %v", num, got)
				}
			}
		})
	}
}

func stringPtr(s string) *string {
	return &s
}

func TestExtractRelatedIssuesAndPRs(t *testing.T) {
	body := "This issue fixes #100 and relates to https://github.com/foo/bar/issues/200."
	comments := []string{
		"Closed by pull request https://github.com/foo/bar/pull/300. Also see GH-400 and resolves #500.",
		"Does not relate to 10000000 (too large) or 999. But wait, we should check pr 600.",
	}

	got := ExtractRelatedIssuesAndPRs(body, comments, 100)
	expected := []int{200, 300, 400, 500, 600}

	if len(got) != len(expected) {
		t.Fatalf("ExtractRelatedIssuesAndPRs returned %v; want %v", got, expected)
	}
	for i, v := range expected {
		if got[i] != v {
			t.Errorf("at index %d: expected %d, got %d", i, v, got[i])
		}
	}
}
