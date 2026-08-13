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
