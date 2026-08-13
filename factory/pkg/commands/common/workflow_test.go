package common

import (
	"testing"
)

func TestFindWorkflowPath(t *testing.T) {
	tests := []struct {
		name     string
		body     string
		expected string
	}{
		{
			name:     "clean URL",
			body:     "Workflow: https://raw.githubusercontent.com/gke-labs/gemini-for-kubernetes-development/main/.agents/workflows/kcc-greenfield.txt",
			expected: "https://raw.githubusercontent.com/gke-labs/gemini-for-kubernetes-development/main/.agents/workflows/kcc-greenfield.txt",
		},
		{
			name:     "URL with literal escaped newline \\n",
			body:     "This issue is to track Greenfield.\n\nWorkflow: https://raw.githubusercontent.com/gke-labs/gemini-for-kubernetes-development/main/.agents/workflows/kcc-greenfield.txt\\n",
			expected: "https://raw.githubusercontent.com/gke-labs/gemini-for-kubernetes-development/main/.agents/workflows/kcc-greenfield.txt",
		},
		{
			name:     "quoted double-quote URL should be ignored",
			body:     "Follow workflow at \"https://raw.githubusercontent.com/gke-labs/gemini-for-kubernetes-development/main/.agents/workflows/kcc-greenfield.txt\", please.",
			expected: "",
		},
		{
			name:     "quoted single-quote URL should be ignored",
			body:     "Check 'https://raw.githubusercontent.com/gke-labs/gemini-for-kubernetes-development/main/.agents/workflows/kcc-greenfield.txt'",
			expected: "",
		},
		{
			name:     "backticked URL should be ignored",
			body:     "See `https://raw.githubusercontent.com/gke-labs/gemini-for-kubernetes-development/main/.agents/workflows/kcc-greenfield.txt`",
			expected: "",
		},
		{
			name:     "local relative workflow file path",
			body:     "Please use .agents/workflows/kcc-greenfield.txt for this issue",
			expected: ".agents/workflows/kcc-greenfield.txt",
		},
		{
			name:     "local workflow path with escaped newline",
			body:     "Workflow: .agents/workflows/kcc-greenfield.txt\\n",
			expected: ".agents/workflows/kcc-greenfield.txt",
		},
		{
			name:     "backticked local workflow path should be ignored",
			body:     "Reference `.agents/workflows/kcc-greenfield.txt` in docs",
			expected: "",
		},
		{
			name:     "no workflow referenced",
			body:     "Regular bug report with some code snippets",
			expected: "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := FindWorkflowPath(tc.body)
			if got != tc.expected {
				t.Errorf("FindWorkflowPath(%q) = %q; want %q", tc.body, got, tc.expected)
			}
		})
	}
}

func TestSanitizeWorkflowPath(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{
			input:    "  https://example.com/workflow.yaml\\n  ",
			expected: "https://example.com/workflow.yaml",
		},
		{
			input:    "path/to/workflow.yaml\\r\\n",
			expected: "path/to/workflow.yaml",
		},
		{
			input:    "normal/path.yaml",
			expected: "normal/path.yaml",
		},
	}

	for _, tc := range tests {
		got := SanitizeWorkflowPath(tc.input)
		if got != tc.expected {
			t.Errorf("SanitizeWorkflowPath(%q) = %q; want %q", tc.input, got, tc.expected)
		}
	}
}

func TestSlugify(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{
			input:    "My Awesome Workflow!",
			expected: "my-awesome-workflow",
		},
		{
			input:    "agent_name-123",
			expected: "agentname-123",
		},
		{
			input:    "KCC Greenfield",
			expected: "kcc-greenfield",
		},
	}

	for _, tc := range tests {
		got := Slugify(tc.input)
		if got != tc.expected {
			t.Errorf("Slugify(%q) = %q; want %q", tc.input, got, tc.expected)
		}
	}
}

func TestParseGitHubURL(t *testing.T) {
	tests := []struct {
		url            string
		expectedOwner  string
		expectedRepo   string
		expectedBranch string
		expectedPath   string
		expectedOK     bool
	}{
		{
			url:            "https://github.com/owner/repo/blob/main/.agents/chore.yaml",
			expectedOwner:  "owner",
			expectedRepo:   "repo",
			expectedBranch: "main",
			expectedPath:   ".agents/chore.yaml",
			expectedOK:     true,
		},
		{
			url:            "https://github.com/owner/repo/raw/feature-branch/path/to/file.txt",
			expectedOwner:  "owner",
			expectedRepo:   "repo",
			expectedBranch: "feature-branch",
			expectedPath:   "path/to/file.txt",
			expectedOK:     true,
		},
		{
			url:        "https://example.com/not-github",
			expectedOK: false,
		},
		{
			url:        "https://github.com/short/path",
			expectedOK: false,
		},
	}

	for _, tc := range tests {
		owner, repo, branch, path, ok := ParseGitHubURL(tc.url)
		if ok != tc.expectedOK {
			t.Errorf("ParseGitHubURL(%q) ok = %v, want %v", tc.url, ok, tc.expectedOK)
			continue
		}
		if ok {
			if owner != tc.expectedOwner || repo != tc.expectedRepo || branch != tc.expectedBranch || path != tc.expectedPath {
				t.Errorf("ParseGitHubURL(%q) = (%s, %s, %s, %s); want (%s, %s, %s, %s)",
					tc.url, owner, repo, branch, path, tc.expectedOwner, tc.expectedRepo, tc.expectedBranch, tc.expectedPath)
			}
		}
	}
}

func TestParseAgent(t *testing.T) {
	content := []byte(`---
name: test-agent
description: A test agent
schedule: "0 * * * *"
cooldown: 15m
---
You are a test assistant.
`)

	def, err := ParseAgent(content)
	if err != nil {
		t.Fatalf("ParseAgent failed: %v", err)
	}

	if def.Name != "test-agent" {
		t.Errorf("Name = %q; want %q", def.Name, "test-agent")
	}
	if def.Description != "A test agent" {
		t.Errorf("Description = %q; want %q", def.Description, "A test agent")
	}
	if def.Schedule != "0 * * * *" {
		t.Errorf("Schedule = %q; want %q", def.Schedule, "0 * * * *")
	}
	if def.Cooldown != "15m" {
		t.Errorf("Cooldown = %q; want %q", def.Cooldown, "15m")
	}
	if def.Prompt != "You are a test assistant." {
		t.Errorf("Prompt = %q; want %q", def.Prompt, "You are a test assistant.")
	}
}
