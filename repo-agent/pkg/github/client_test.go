package github

import (
	"testing"
)

func TestParseHTMLUrl(t *testing.T) {
	tests := []struct {
		url   string
		owner string
		repo  string
		err   bool
	}{
		{"https://github.com/owner/repo", "owner", "repo", false},
		{"https://github.com/owner/repo.git", "owner", "repo", false},
		{"https://github.com/owner/repo/issues/123", "owner", "repo", false},
		{"https://github.com/owner/repo/pull/123", "owner", "repo", false},
		{"https://github.com/owner/repo/tree/branch", "owner", "repo", false},
		{"https://github.com/owner/repo/blob/branch/file", "owner", "repo", false},
		{"https://github.com/owner", "", "", true},
		{"https://google.com/owner/repo", "", "", true},
	}

	for _, tt := range tests {
		owner, repo, err := ParseHTMLUrl(tt.url)
		if (err != nil) != tt.err {
			t.Errorf("parseHTMLUrl(%q) error = %v, wantErr %v", tt.url, err, tt.err)
			continue
		}
		if owner != tt.owner {
			t.Errorf("parseHTMLUrl(%q) owner = %v, want %v", tt.url, owner, tt.owner)
		}
		if repo != tt.repo {
			t.Errorf("parseHTMLUrl(%q) repo = %v, want %v", tt.url, repo, tt.repo)
		}
	}
}

func TestParseIssueURL(t *testing.T) {
	tests := []struct {
		url    string
		owner  string
		repo   string
		number int
		err    bool
	}{
		{"https://github.com/owner/repo/issues/123", "owner", "repo", 123, false},
		{"https://github.com/owner/repo/pull/456", "owner", "repo", 456, false},
		{"https://github.com/owner/repo/pull/invalid", "", "", 0, true},
		{"https://github.com/owner/repo/blob/branch/file", "", "", 0, true},
		{"https://github.com/owner/repo", "", "", 0, true},
		{"https://google.com/owner/repo/issues/1", "", "", 0, true},
	}

	for _, tt := range tests {
		owner, repo, number, err := ParseIssueURL(tt.url)
		if (err != nil) != tt.err {
			t.Errorf("parseIssueURL(%q) error = %v, wantErr %v", tt.url, err, tt.err)
			continue
		}
		if owner != tt.owner {
			t.Errorf("parseIssueURL(%q) owner = %v, want %v", tt.url, owner, tt.owner)
		}
		if repo != tt.repo {
			t.Errorf("parseIssueURL(%q) repo = %v, want %v", tt.url, repo, tt.repo)
		}
		if number != tt.number {
			t.Errorf("parseIssueURL(%q) number = %v, want %v", tt.url, number, tt.number)
		}
	}
}
