package common

import (
	"strings"
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

func TestGetClosingIssues(t *testing.T) {
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
			name:    "Title has closing keyword and body has non-closing references",
			headRef: "my-dev-branch",
			title:   "Fixes #8883 and #10294",
			body:    "This relates to issue #9271 in config-connector",
			expected: map[int]bool{
				8883:  true,
				10294: true,
			},
		},
		{
			name:     "No closing keywords, only references",
			headRef:  "master",
			title:    "Clean PR mentioning #8883",
			body:     "Discussed in issue #9271, but no fix here.",
			expected: map[int]bool{},
		},
		{
			name:    "Closing keyword with URL",
			headRef: "master",
			title:   "Closes the issue https://github.com/GoogleCloudPlatform/k8s-config-connector/issues/12875",
			body:    "Resolves pr #9271 with full url https://github.com/foo/bar/issues/1122",
			expected: map[int]bool{
				12875: true,
				9271:  true,
				1122:  true,
			},
		},
		{
			name:     "Branch name with greedy numbers like v2-refactor or phase-3",
			headRef:  "v2-refactor-phase-3-changes-fix-multibot-4",
			title:    "Some PR title",
			body:     "Some PR body",
			expected: map[int]bool{},
		},
		{
			name:    "Branch name with strict format issue-1234",
			headRef: "issue-1234",
			title:   "Some PR title",
			body:    "Some PR body",
			expected: map[int]bool{
				1234: true,
			},
		},
		{
			name:    "Branch name with strict format factory-issue_5678",
			headRef: "factory-issue_5678",
			title:   "Some PR title",
			body:    "Some PR body",
			expected: map[int]bool{
				5678: true,
			},
		},
		{
			name:    "Non-ASCII multi-byte UTF-8 boundary slicing",
			headRef: "master",
			title:   "Closes #1",
			body:    "Fixes #123 " + strings.Repeat("こんにちは", 30),
			expected: map[int]bool{
				1:   true,
				123: true,
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
			got := GetClosingIssues(pr)
			if len(got) != len(tc.expected) {
				t.Fatalf("GetClosingIssues() returned %v; want %v", got, tc.expected)
			}
			for num := range tc.expected {
				if !got[num] {
					t.Errorf("GetClosingIssues() missed expected issue %d in %v", num, got)
				}
			}
		})
	}
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
