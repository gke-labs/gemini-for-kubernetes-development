/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package main

import (
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/k8s"
)

func TestParseRepoURL(t *testing.T) {
	tests := []struct {
		url     string
		owner   string
		repo    string
		wantErr bool
	}{
		{
			url:     "https://github.com/owner/repo",
			owner:   "owner",
			repo:    "repo",
			wantErr: false,
		},
		{
			url:     "https://github.com/owner/repo.git",
			owner:   "owner",
			repo:    "repo",
			wantErr: false,
		},
		{
			url:     "https://github.com/owner/repo/",
			owner:   "owner",
			repo:    "repo",
			wantErr: false,
		},
		{
			url:     "github.com/owner/repo.git/",
			owner:   "owner",
			repo:    "repo",
			wantErr: false,
		},
		{
			url:     "github.com/owner/repo",
			owner:   "owner",
			repo:    "repo",
			wantErr: false,
		},
		{
			url:     "git@github.com:owner/repo.git",
			owner:   "owner",
			repo:    "repo",
			wantErr: false,
		},
		{
			url:     "github.com:owner/repo.git",
			owner:   "owner",
			repo:    "repo",
			wantErr: false,
		},
		{
			url:     "github.com:owner/repo",
			owner:   "owner",
			repo:    "repo",
			wantErr: false,
		},

		{
			url:     "https://gitlab.com/group/subgroup/repo",
			owner:   "group/subgroup",
			repo:    "repo",
			wantErr: false,
		},
		{
			url:     "git@gitlab.com:group/subgroup/repo.git",
			owner:   "group/subgroup",
			repo:    "repo",
			wantErr: false,
		},
		{
			url:     "https://user:pass@github.com/owner/repo",
			owner:   "owner",
			repo:    "repo",
			wantErr: false,
		},
		{
			url:     "/local/path/to/repo",
			owner:   "local/path/to",
			repo:    "repo",
			wantErr: false,
		},
		{
			url:     "https://github.com/owner/repo.git?ref=main",
			owner:   "owner",
			repo:    "repo",
			wantErr: false,
		},
		{
			url:     "https://github.com/owner/repo#readme",
			owner:   "owner",
			repo:    "repo",
			wantErr: false,
		},
		{
			url:     "invalid-url",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.url, func(t *testing.T) {
			owner, repo, err := parseRepoURL(tt.url)
			if (err != nil) != tt.wantErr {
				t.Errorf("parseRepoURL(%q) error = %v, wantErr %v", tt.url, err, tt.wantErr)
				return
			}
			if !tt.wantErr {
				if owner != tt.owner || repo != tt.repo {
					t.Errorf("parseRepoURL(%q) = (%q, %q), want (%q, %q)", tt.url, owner, repo, tt.owner, tt.repo)
				}
			}
		})
	}
}

func TestGetMode(t *testing.T) {
	tests := []struct {
		name   string
		envVar string
		val    string
		want   string
	}{
		{name: "enabled", envVar: "MODE1", val: "enabled", want: "enabled"},
		{name: "enable alias", envVar: "MODE1_ALIAS", val: "enable", want: "enabled"},
		{name: "DISABLED uppercase", envVar: "MODE2", val: "DISABLED", want: "disabled"},
		{name: "disable alias", envVar: "MODE2_ALIAS", val: "disable", want: "disabled"},
		{name: "dryrun", envVar: "MODE3", val: "dryrun", want: "dryrun"},
		{name: "dry-run", envVar: "MODE4", val: "dry-run", want: "dryrun"},
		{name: "dry_run", envVar: "MODE5", val: "dry_run", want: "dryrun"},
		{name: "dry run space", envVar: "MODE6", val: "dry run", want: "dryrun"},
		{name: "true", envVar: "MODE7", val: "true", want: "enabled"},
		{name: "t shorthand", envVar: "MODE7_SHORT", val: "t", want: "enabled"},
		{name: "yes shorthand", envVar: "MODE7_YES", val: "y", want: "enabled"},
		{name: "false", envVar: "MODE8", val: "false", want: "disabled"},
		{name: "none", envVar: "MODE8_NONE", val: "none", want: "disabled"},
		{name: "f shorthand", envVar: "MODE8_SHORT", val: "f", want: "disabled"},
		{name: "no shorthand", envVar: "MODE8_NO", val: "n", want: "disabled"},
		{name: "quoted enabled", envVar: "MODE9", val: "\"enabled\"", want: "enabled"},
		{name: "single quoted dryrun", envVar: "MODE10", val: "'dryrun'", want: "dryrun"},
		{name: "on with spaces", envVar: "MODE11", val: "  on  ", want: "enabled"},
		{name: "empty", envVar: "MODE12", val: "", want: "enabled"},
		{name: "invalid defaults to enabled", envVar: "MODE13", val: "invalid", want: "enabled"},
		{name: "long utf8", envVar: "MODE14", val: "👋👋👋👋👋👋👋👋👋👋👋👋👋👋👋👋👋👋👋👋👋👋👋👋👋👋👋👋👋👋👋👋👋👋👋👋👋👋👋👋👋👋👋👋👋👋👋👋👋👋👋👋👋👋👋👋👋👋👋👋", want: "enabled"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv(tt.envVar, tt.val)
			got := getMode(tt.envVar)
			if got != tt.want {
				t.Errorf("getMode(%q=%q) = %q, want %q", tt.envVar, tt.val, got, tt.want)
			}
		})
	}
}

func TestSlugify(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"My Chore", "my-chore"},
		{"My Chore!", "my-chore"},
		{"  My Chore  ", "my-chore"},
		{"My-Chore-", "my-chore"},
		{"My -- Chore", "my-chore"},
		{"My ! Chore", "my-chore"},
		{"", "fallback-e3b0c442"},
		{"My_(Chore)_Test", "my-chore-test"},
		{"👋_My_Chore", "my-chore"},
		{"👋👋👋", "fallback-3656d98a"}, // Only non-alphanumeric
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			if got := k8s.Slugify(tt.input); got != tt.expected {
				t.Errorf("k8s.Slugify(%q) = %q, want %q", tt.input, got, tt.expected)
			}
		})
	}
}

func TestTruncateLabel(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{name: "short", input: "short", expected: "short"},
		{name: "exactly 63", input: strings.Repeat("a", 63), expected: strings.Repeat("a", 63)},
		{name: "long triggers hash", input: strings.Repeat("a", 70), expected: strings.Repeat("a", 56) + "-6bd5e5"},
		{name: "unicode start", input: "👋hello-world", expected: "hello-world"},
		{name: "unicode middle", input: "hello👋world", expected: "hello-world"},
		{name: "invalid middle", input: "abc!@#def", expected: "abc-def"},
		{name: "trim dash", input: "-abc-", expected: "abc"},
		{name: "trim dot", input: ".abc.", expected: "abc"},
		{name: "trim mixed", input: ".-_abc_-.", expected: "abc"},
		{name: "empty input", input: "", expected: "fallback-e3b0c442"},
		{name: "only non-alphanumeric triggers fallback hash", input: ".-_", expected: "fallback-3f77d544"},
		{name: "dash dash triggers fallback hash", input: "--", expected: "fallback-d8156bae"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := k8s.TruncateLabel(tt.input); got != tt.expected {
				t.Errorf("k8s.TruncateLabel(%q) = %q, want %q", tt.input, got, tt.expected)
			}
		})
	}
}

func TestGetNumber(t *testing.T) {
	tests := []struct {
		name         string
		labels       map[string]string
		sandboxName  string
		overseerName string
		wantIssue    int
		wantPR       int
	}{
		{
			name: "labels only",
			labels: map[string]string{
				"issue.gemini.google.com/number": "123",
				"pr.gemini.google.com/number":    "456",
			},
			sandboxName:  "myoverseer-issue-999",
			overseerName: "myoverseer",
			wantIssue:    123,
			wantPR:       456,
		},
		{
			name: "fallback to name",
			labels: map[string]string{
				"issue.gemini.google.com/number": "invalid",
				"pr.gemini.google.com/number":    "invalid",
			},
			sandboxName:  "myoverseer-issue-789",
			overseerName: "myoverseer",
			wantIssue:    789,
			wantPR:       0,
		},
		{
			name:         "name only",
			labels:       map[string]string{},
			sandboxName:  "myoverseer-pr-321",
			overseerName: "myoverseer",
			wantIssue:    0,
			wantPR:       321,
		},
		{
			name:         "mismatched overseer",
			labels:       map[string]string{},
			sandboxName:  "other-issue-123",
			overseerName: "myoverseer",
			wantIssue:    0,
			wantPR:       0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			issueRe := regexp.MustCompile(fmt.Sprintf(`^%s-issue-(\d+)`, regexp.QuoteMeta(tt.overseerName)))
			prRe := regexp.MustCompile(fmt.Sprintf(`^%s-pr-(\d+)`, regexp.QuoteMeta(tt.overseerName)))

			gotIssue := getIssueNumber(tt.labels, tt.sandboxName, issueRe)
			if gotIssue != tt.wantIssue {
				t.Errorf("getIssueNumber() = %d, want %d", gotIssue, tt.wantIssue)
			}
			gotPR := getPRNumber(tt.labels, tt.sandboxName, prRe)
			if gotPR != tt.wantPR {
				t.Errorf("getPRNumber() = %d, want %d", gotPR, tt.wantPR)
			}
		})
	}
}
