package watch

import (
	"bytes"
	"context"
	"encoding/base64"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	githubv39 "github.com/google/go-github/v39/github"
)

func stringPtr(s string) *string {
	return &s
}

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
			got := getReferencedIssues(pr)
			if len(got) != len(tc.expected) {
				t.Fatalf("getReferencedIssues() returned %v; want %v", got, tc.expected)
			}
			for num := range tc.expected {
				if !got[num] {
					t.Errorf("getReferencedIssues() missed expected issue %d in %v", num, got)
				}
			}
		})
	}
}

func TestGetMissingLabelsForPR(t *testing.T) {
	tests := []struct {
		name      string
		prLabels  []string
		refIssues [][]string
		expected  []string
	}{
		{
			name:      "All issue labels are missing from PR",
			prLabels:  []string{},
			refIssues: [][]string{{"greenfield", "step/controller"}},
			expected:  []string{"greenfield", "step/controller"},
		},
		{
			name:      "Some labels already exist on PR",
			prLabels:  []string{"greenfield"},
			refIssues: [][]string{{"greenfield", "step/controller", "area/direct"}},
			expected:  []string{"greenfield", "step/controller", "area/direct"},
		},
		{
			name:     "Duplicate labels across multiple issues are deduplicated",
			prLabels: []string{"priority/medium"},
			refIssues: [][]string{
				{"greenfield", "step/controller"},
				{"step/controller", "area/direct"},
			},
			expected: []string{"priority/medium", "greenfield", "step/controller", "area/direct"},
		},
		{
			name:      "No missing labels",
			prLabels:  []string{"greenfield", "step/controller"},
			refIssues: [][]string{{"greenfield"}},
			expected:  []string{"greenfield", "step/controller"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var prLabels []*githubv39.Label
			for _, name := range tc.prLabels {
				prLabels = append(prLabels, &githubv39.Label{Name: stringPtr(name)})
			}

			var refIssues []*githubv39.Issue
			for _, issueLabels := range tc.refIssues {
				var labels []*githubv39.Label
				for _, name := range issueLabels {
					labels = append(labels, &githubv39.Label{Name: stringPtr(name)})
				}
				refIssues = append(refIssues, &githubv39.Issue{Labels: labels})
			}

			got := getMissingLabelsForPR(prLabels, refIssues)

			// Build the final set of labels on the PR (original labels + added labels)
			finalLabelsMap := make(map[string]bool)
			var finalLabels []string
			for _, name := range tc.prLabels {
				if !finalLabelsMap[name] {
					finalLabelsMap[name] = true
					finalLabels = append(finalLabels, name)
				}
			}
			for _, name := range got {
				if !finalLabelsMap[name] {
					finalLabelsMap[name] = true
					finalLabels = append(finalLabels, name)
				}
			}

			if len(finalLabels) != len(tc.expected) {
				t.Fatalf("Final labels list length is %d (%v); want %d (%v)", len(finalLabels), finalLabels, len(tc.expected), tc.expected)
			}
			for i, val := range tc.expected {
				if finalLabels[i] != val {
					t.Errorf("Final label at index %d = %q; want %q", i, finalLabels[i], val)
				}
			}
		})
	}
}

func TestAssignedBotUser(t *testing.T) {
	tests := []struct {
		name     string
		assignee []string
		bots     []string
		expected string
	}{
		{
			name:     "Matches bot user in list",
			assignee: []string{"user-1", "walle-agent-bot"},
			bots:     []string{"walle-agent-bot", "daedalus-agent-bot"},
			expected: "walle-agent-bot",
		},
		{
			name:     "Matches case insensitively",
			assignee: []string{"Walle-Agent-Bot"},
			bots:     []string{"walle-agent-bot"},
			expected: "Walle-Agent-Bot",
		},
		{
			name:     "No match",
			assignee: []string{"human-user"},
			bots:     []string{"walle-agent-bot"},
			expected: "",
		},
		{
			name:     "Empty inputs",
			assignee: []string{},
			bots:     []string{"walle-agent-bot"},
			expected: "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var assignees []*githubv39.User
			for _, login := range tc.assignee {
				assignees = append(assignees, &githubv39.User{Login: stringPtr(login)})
			}
			issue := &githubv39.Issue{Assignees: assignees}
			got := assignedBotUser(issue, tc.bots)
			if got != tc.expected {
				t.Errorf("assignedBotUser() = %q; want %q", got, tc.expected)
			}
		})
	}
}

func TestShouldIgnoreUser(t *testing.T) {
	tests := []struct {
		name        string
		login       string
		userType    string
		githubLogin string
		allowlist   []string
		expected    bool
	}{
		{
			name:        "Always ignore our own bot user",
			login:       "daedalus-agent-bot",
			userType:    "User",
			githubLogin: "daedalus-agent-bot",
			allowlist:   []string{},
			expected:    true,
		},
		{
			name:        "Ignore non-allowlisted bot type",
			login:       "some-other-bot",
			userType:    "Bot",
			githubLogin: "daedalus-agent-bot",
			allowlist:   []string{"walle-agent-bot"},
			expected:    true,
		},
		{
			name:        "Do not ignore allowlisted bot type",
			login:       "walle-agent-bot",
			userType:    "Bot",
			githubLogin: "daedalus-agent-bot",
			allowlist:   []string{"walle-agent-bot"},
			expected:    false,
		},
		{
			name:        "Ignore user with bot suffix",
			login:       "auto-reviewer-bot",
			userType:    "User",
			githubLogin: "daedalus-agent-bot",
			allowlist:   []string{},
			expected:    true,
		},
		{
			name:        "Ignore user with robot suffix",
			login:       "reviewbot-robot",
			userType:    "User",
			githubLogin: "daedalus-agent-bot",
			allowlist:   []string{},
			expected:    true,
		},
		{
			name:        "Ignore user containing prow",
			login:       "prow-bot-reviewer",
			userType:    "User",
			githubLogin: "daedalus-agent-bot",
			allowlist:   []string{},
			expected:    true,
		},
		{
			name:        "Do not ignore standard human user",
			login:       "human-developer",
			userType:    "User",
			githubLogin: "daedalus-agent-bot",
			allowlist:   []string{},
			expected:    false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			user := &githubv39.User{
				Login: stringPtr(tc.login),
				Type:  stringPtr(tc.userType),
			}
			got := shouldIgnoreUser(user, tc.githubLogin, tc.allowlist)
			if got != tc.expected {
				t.Errorf("shouldIgnoreUser(%q, type: %q) = %v; want %v", tc.login, tc.userType, got, tc.expected)
			}
		})
	}
}

func TestIsPRApprovedOrLGTM(t *testing.T) {
	tests := []struct {
		name    string
		labels  []string
		reviews []struct {
			login string
			state string
		}
		expected bool
	}{
		{
			name:     "Approved via LGTM label",
			labels:   []string{"lgtm"},
			expected: true,
		},
		{
			name:     "Approved via approved label",
			labels:   []string{"approved"},
			expected: true,
		},
		{
			name: "Approved via review state",
			reviews: []struct {
				login string
				state string
			}{
				{login: "user-1", state: "APPROVED"},
			},
			expected: true,
		},
		{
			name:     "Not approved (empty review)",
			expected: false,
		},
		{
			name: "Blocked by changes requested review",
			reviews: []struct {
				login string
				state string
			}{
				{login: "user-1", state: "APPROVED"},
				{login: "user-2", state: "CHANGES_REQUESTED"},
			},
			expected: false,
		},
		{
			name: "Approved review overrides previous review from same user",
			reviews: []struct {
				login string
				state string
			}{
				{login: "user-1", state: "CHANGES_REQUESTED"},
				{login: "user-1", state: "APPROVED"},
			},
			expected: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var labels []*githubv39.Label
			for _, name := range tc.labels {
				labels = append(labels, &githubv39.Label{Name: stringPtr(name)})
			}
			prIssue := &githubv39.Issue{Labels: labels}

			var reviews []*githubv39.PullRequestReview
			for _, r := range tc.reviews {
				reviews = append(reviews, &githubv39.PullRequestReview{
					User:  &githubv39.User{Login: stringPtr(r.login)},
					State: stringPtr(r.state),
				})
			}

			pr := &githubv39.PullRequest{}
			got := isPRApprovedOrLGTM(pr, prIssue, reviews)
			if got != tc.expected {
				t.Errorf("isPRApprovedOrLGTM() = %v; want %v", got, tc.expected)
			}
		})
	}
}

func TestFindWorkflowPath(t *testing.T) {
	tests := []struct {
		name     string
		body     string
		expected string
	}{
		{
			name:     "Matches workflow URL in markdown",
			body:     "Please run: https://github.com/GoogleCloudPlatform/k8s-config-connector/blob/master/.agents/greenfield-direct-new-resource-types.md",
			expected: "https://github.com/GoogleCloudPlatform/k8s-config-connector/blob/master/.agents/greenfield-direct-new-resource-types.md",
		},
		{
			name:     "Matches local file path",
			body:     "Chore file: .agents/greenfield-direct-new-resource-types.md",
			expected: ".agents/greenfield-direct-new-resource-types.md",
		},
		{
			name:     "No match",
			body:     "Just some general description",
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

func TestIssuePriority(t *testing.T) {
	tests := []struct {
		name     string
		labels   []string
		expected string
	}{
		{
			name:     "Extracts priority label",
			labels:   []string{"priority/high", "bug"},
			expected: "high",
		},
		{
			name:     "Defaults to medium if priority label is absent",
			labels:   []string{"bug", "step/gen-types"},
			expected: "medium",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var labels []*githubv39.Label
			for _, name := range tc.labels {
				labels = append(labels, &githubv39.Label{Name: stringPtr(name)})
			}
			issue := &githubv39.Issue{Labels: labels}
			got := issuePriority(issue)
			if got != tc.expected {
				t.Errorf("issuePriority() = %q; want %q", got, tc.expected)
			}
		})
	}
}

func TestIsPRTask(t *testing.T) {
	tests := []struct {
		taskType string
		expected bool
	}{
		{"pr-investigate", true},
		{"pr-comments", true},
		{"pr-iterate", true},
		{"issue-fix", false},
		{"agent-chore", false},
	}

	for _, tc := range tests {
		t.Run(tc.taskType, func(t *testing.T) {
			got := isPRTask(tc.taskType)
			if got != tc.expected {
				t.Errorf("isPRTask(%q) = %v; want %v", tc.taskType, got, tc.expected)
			}
		})
	}
}

func TestSlugify(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"Hello World", "hello-world"},
		{"My_Agent_Prompt", "myagentprompt"},
		{"lowercase", "lowercase"},
	}

	for _, tc := range tests {
		t.Run(tc.input, func(t *testing.T) {
			got := Slugify(tc.input)
			if got != tc.expected {
				t.Errorf("Slugify(%q) = %q; want %q", tc.input, got, tc.expected)
			}
		})
	}
}

func TestPRPriority(t *testing.T) {
	tests := []struct {
		name     string
		labels   []string
		expected string
	}{
		{
			name:     "Has priority label",
			labels:   []string{"priority/critical", "bug"},
			expected: "critical",
		},
		{
			name:     "Has no priority label",
			labels:   []string{"bug", "lgtm"},
			expected: "medium",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var labels []*githubv39.Label
			for _, name := range tc.labels {
				labels = append(labels, &githubv39.Label{Name: stringPtr(name)})
			}
			prIssue := &githubv39.Issue{Labels: labels}
			got := prPriority(prIssue)
			if got != tc.expected {
				t.Errorf("prPriority() = %q; want %q", got, tc.expected)
			}
		})
	}
}

func TestParseGitHubURL(t *testing.T) {
	tests := []struct {
		urlStr       string
		expectOk     bool
		expectOwner  string
		expectRepo   string
		expectBranch string
		expectPath   string
	}{
		{
			urlStr:       "https://github.com/google/go-github/blob/main/README.md",
			expectOk:     true,
			expectOwner:  "google",
			expectRepo:   "go-github",
			expectBranch: "main",
			expectPath:   "README.md",
		},
		{
			urlStr:       "https://github.com/google/go-github/raw/master/pkg/file.go",
			expectOk:     true,
			expectOwner:  "google",
			expectRepo:   "go-github",
			expectBranch: "master",
			expectPath:   "pkg/file.go",
		},
		{
			urlStr:   "https://github.com/google/go-github/pull/123",
			expectOk: false,
		},
		{
			urlStr:   "invalid-url",
			expectOk: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.urlStr, func(t *testing.T) {
			owner, repo, branch, path, ok := parseGitHubURL(tc.urlStr)
			if ok != tc.expectOk {
				t.Fatalf("expected parseGitHubURL(%q) ok = %v, got %v", tc.urlStr, tc.expectOk, ok)
			}
			if ok {
				if owner != tc.expectOwner || repo != tc.expectRepo || branch != tc.expectBranch || path != tc.expectPath {
					t.Errorf("parseGitHubURL(%q) = (%q, %q, %q, %q); want (%q, %q, %q, %q)", tc.urlStr, owner, repo, branch, path, tc.expectOwner, tc.expectRepo, tc.expectBranch, tc.expectPath)
				}
			}
		})
	}
}

func TestWorkflowCooldown(t *testing.T) {
	ctx := context.Background()

	httpClient := &http.Client{
		Transport: mockRoundTripper(func(req *http.Request) *http.Response {
			if strings.Contains(req.URL.Path, "/repos/owner/repo/contents/workflow.yaml") {
				agentYAML := `---
cooldown: "10m"
---
Prompt`
				return &http.Response{
					StatusCode: 200,
					Body:       io.NopCloser(bytes.NewBufferString(`{"name":"workflow.yaml","type":"file","content":"` + strings.ReplaceAll(agentYAML, "\n", "\\n") + `","encoding":"base64"}`)),
					Header:     make(http.Header),
				}
			}
			if strings.Contains(req.URL.Path, "/repos/owner/repo/contents/invalid.yaml") {
				// Invalid YAML structure
				return &http.Response{
					StatusCode: 200,
					Body:       io.NopCloser(bytes.NewBufferString(`{"name":"invalid.yaml","type":"file","content":"aW52YWxpZCB5YW1s","encoding":"base64"}`)), // base64 for "invalid yaml"
					Header:     make(http.Header),
				}
			}
			if strings.Contains(req.URL.Path, "/repos/owner/repo/contents/invalid-duration.yaml") {
				agentYAML := `---
cooldown: "invalid-duration"
---
Prompt`
				return &http.Response{
					StatusCode: 200,
					Body:       io.NopCloser(bytes.NewBufferString(`{"name":"invalid-duration.yaml","type":"file","content":"` + base64.StdEncoding.EncodeToString([]byte(agentYAML)) + `","encoding":"base64"}`)),
					Header:     make(http.Header),
				}
			}
			return &http.Response{
				StatusCode: 404,
				Body:       io.NopCloser(bytes.NewBufferString("")),
				Header:     make(http.Header),
			}
		}),
	}
	ghClient := githubv39.NewClient(httpClient)

	// Scenario A: file found with cooldown
	got := workflowCooldown(ctx, ghClient, "owner", "repo", "workflow.yaml")
	if got != 10*time.Minute {
		t.Errorf("expected cooldown 10m, got %v", got)
	}

	// Scenario B: file not found (fallback to 10m)
	got = workflowCooldown(ctx, ghClient, "owner", "repo", "nonexistent.yaml")
	if got != 10*time.Minute {
		t.Errorf("expected fallback cooldown 10m, got %v", got)
	}

	// Scenario C: invalid YAML file (fallback to 10m)
	got = workflowCooldown(ctx, ghClient, "owner", "repo", "invalid.yaml")
	if got != 10*time.Minute {
		t.Errorf("expected fallback cooldown for invalid YAML to be 10m, got %v", got)
	}

	// Scenario D: invalid duration syntax (fallback to 10m)
	got = workflowCooldown(ctx, ghClient, "owner", "repo", "invalid-duration.yaml")
	if got != 10*time.Minute {
		t.Errorf("expected fallback cooldown for invalid duration format to be 10m, got %v", got)
	}

	// Scenario E: empty path (fallback to 10m)
	got = workflowCooldown(ctx, ghClient, "owner", "repo", "")
	if got != 10*time.Minute {
		t.Errorf("expected fallback cooldown for empty path to be 10m, got %v", got)
	}
}

func TestIsWorkflowDefinition(t *testing.T) {
	ctx := context.Background()

	// Backup and mock http.DefaultTransport for the HTTP URL fetch inside IsWorkflowDefinition
	origTransport := http.DefaultTransport
	defer func() { http.DefaultTransport = origTransport }()

	http.DefaultTransport = mockRoundTripper(func(req *http.Request) *http.Response {
		if strings.Contains(req.URL.Host, "domain.com") {
			if strings.Contains(req.URL.Path, "/valid-http-workflow.yaml") {
				return &http.Response{
					StatusCode: 200,
					Body:       io.NopCloser(bytes.NewBufferString(`mode: workflow`)),
					Header:     make(http.Header),
				}
			}
			if strings.Contains(req.URL.Path, "/invalid-http-workflow.yaml") {
				return &http.Response{
					StatusCode: 200,
					Body:       io.NopCloser(bytes.NewBufferString(`ordinary text`)),
					Header:     make(http.Header),
				}
			}
		}
		return &http.Response{
			StatusCode: 404,
			Body:       io.NopCloser(bytes.NewBufferString("")),
			Header:     make(http.Header),
		}
	})

	httpClient := &http.Client{
		Transport: mockRoundTripper(func(req *http.Request) *http.Response {
			if strings.Contains(req.URL.Path, "/repos/owner/repo/contents/valid.yaml") {
				agentYAML := `---
name: "valid-agent"
schedule: "0 9 * * 1"
mode: workflow
---
Prompt`
				encoded := base64.StdEncoding.EncodeToString([]byte(agentYAML))
				return &http.Response{
					StatusCode: 200,
					Body:       io.NopCloser(bytes.NewBufferString(`{"name":"valid.yaml","type":"file","content":"` + encoded + `","encoding":"base64"}`)),
					Header:     make(http.Header),
				}
			}
			if strings.Contains(req.URL.Path, "/repos/owner/repo/contents/invalid-base64.yaml") {
				// Returns invalid base64 encoding content
				return &http.Response{
					StatusCode: 200,
					Body:       io.NopCloser(bytes.NewBufferString(`{"name":"invalid-base64.yaml","type":"file","content":"not_base64_encoded!","encoding":"base64"}`)),
					Header:     make(http.Header),
				}
			}
			if strings.Contains(req.URL.Path, "/repos/owner/repo/contents/nil-file.yaml") {
				// Returns null content response
				return &http.Response{
					StatusCode: 200,
					Body:       io.NopCloser(bytes.NewBufferString(`null`)),
					Header:     make(http.Header),
				}
			}
			return &http.Response{
				StatusCode: 404,
				Body:       io.NopCloser(bytes.NewBufferString("")),
				Header:     make(http.Header),
			}
		}),
	}
	ghClient := githubv39.NewClient(httpClient)

	// Local file scenarios
	if !IsWorkflowDefinition(ctx, ghClient, "owner", "repo", "valid.yaml") {
		t.Errorf("expected valid.yaml to be a valid workflow definition")
	}

	if IsWorkflowDefinition(ctx, ghClient, "owner", "repo", "invalid.yaml") {
		t.Errorf("expected invalid.yaml to not be a valid workflow definition")
	}

	if IsWorkflowDefinition(ctx, ghClient, "owner", "repo", "invalid-base64.yaml") {
		t.Errorf("expected invalid-base64.yaml to fail content decoding check")
	}

	if IsWorkflowDefinition(ctx, ghClient, "owner", "repo", "nil-file.yaml") {
		t.Errorf("expected nil-file.yaml to fail content presence check")
	}

	// Local path conventions
	if !IsWorkflowDefinition(ctx, ghClient, "owner", "repo", "path/to/workflows/agent.yaml") {
		t.Errorf("expected path containing /workflows/ to be treated as workflow path convention")
	}

	// HTTP URL conventions
	if !IsWorkflowDefinition(ctx, ghClient, "owner", "repo", "http://domain.com/workflows/agent.yaml") {
		t.Errorf("expected URL containing /workflows/ to be treated as workflow URL convention")
	}
	if !IsWorkflowDefinition(ctx, ghClient, "owner", "repo", "https://domain.com/agents/agent.yaml") {
		t.Errorf("expected URL containing /agents/ to be treated as workflow URL convention")
	}

	// HTTP URL non-convention headers search
	if !IsWorkflowDefinition(ctx, ghClient, "owner", "repo", "http://domain.com/valid-http-workflow.yaml") {
		t.Errorf("expected URL with workflow headers to be a workflow definition")
	}
	if IsWorkflowDefinition(ctx, ghClient, "owner", "repo", "http://domain.com/invalid-http-workflow.yaml") {
		t.Errorf("expected URL without workflow headers to not be a workflow definition")
	}
	if IsWorkflowDefinition(ctx, ghClient, "owner", "repo", "http://domain.com/nonexistent-http-workflow.yaml") {
		t.Errorf("expected failed HTTP fetch to not be a workflow definition")
	}
}

func TestFetchWorkflowContent(t *testing.T) {
	ctx := context.Background()

	// Backup and mock http.DefaultTransport
	origTransport := http.DefaultTransport
	defer func() { http.DefaultTransport = origTransport }()

	http.DefaultTransport = mockRoundTripper(func(req *http.Request) *http.Response {
		if strings.Contains(req.URL.Host, "my-agents.com") {
			return &http.Response{
				StatusCode: 200,
				Body:       io.NopCloser(bytes.NewBufferString("agent prompt from http")),
				Header:     make(http.Header),
			}
		}
		return &http.Response{
			StatusCode: 404,
			Body:       io.NopCloser(bytes.NewBufferString("")),
			Header:     make(http.Header),
		}
	})

	httpClient := &http.Client{
		Transport: mockRoundTripper(func(req *http.Request) *http.Response {
			if strings.Contains(req.URL.Path, "/repos/owner/repo/contents/workflow.yaml") {
				return &http.Response{
					StatusCode: 200,
					Body:       io.NopCloser(bytes.NewBufferString(`{"name":"workflow.yaml","type":"file","content":"YWdlbnQgcHJvbXB0IGZyb20gZ2l0aHVi","encoding":"base64"}`)), // YWdlbnQgcHJvbXB0IGZyb20gZ2l0aHVi is base64 for "agent prompt from github"
					Header:     make(http.Header),
				}
			}
			return &http.Response{
				StatusCode: 404,
				Body:       io.NopCloser(bytes.NewBufferString("")),
				Header:     make(http.Header),
			}
		}),
	}
	ghClient := githubv39.NewClient(httpClient)

	// Scenario 1: GitHub URL
	got, err := fetchWorkflowContent(ctx, ghClient, "https://github.com/owner/repo/blob/main/workflow.yaml")
	if err != nil {
		t.Fatalf("unexpected error fetching from github URL: %v", err)
	}
	if string(got) != "agent prompt from github" {
		t.Errorf("expected 'agent prompt from github', got %q", string(got))
	}

	// Scenario 2: Public HTTP URL
	got, err = fetchWorkflowContent(ctx, ghClient, "http://my-agents.com/agent.yaml")
	if err != nil {
		t.Fatalf("unexpected error fetching from http URL: %v", err)
	}
	if string(got) != "agent prompt from http" {
		t.Errorf("expected 'agent prompt from http', got %q", string(got))
	}
}

func TestSyncReferencedIssueLabels(t *testing.T) {
	ctx := context.Background()

	httpClient := &http.Client{
		Transport: mockRoundTripper(func(req *http.Request) *http.Response {
			if strings.Contains(req.URL.Path, "/repos/owner/repo/issues/8883") {
				return &http.Response{
					StatusCode: 200,
					Body:       io.NopCloser(bytes.NewBufferString(`{"number":8883,"labels":[{"name":"greenfield"},{"name":"area/direct"}]}`)),
					Header:     make(http.Header),
				}
			}
			if strings.Contains(req.URL.Path, "/repos/owner/repo/issues/100/labels") {
				return &http.Response{
					StatusCode: 200,
					Body:       io.NopCloser(bytes.NewBufferString(`[]`)),
					Header:     make(http.Header),
				}
			}
			return &http.Response{
				StatusCode: 404,
				Body:       io.NopCloser(bytes.NewBufferString("")),
				Header:     make(http.Header),
			}
		}),
	}
	ghClient := githubv39.NewClient(httpClient)

	pr := &githubv39.PullRequest{
		Number: githubv39.Int(100),
		Head: &githubv39.PullRequestBranch{
			Ref: githubv39.String("fix-issue-8883"),
		},
	}
	prIssue := &githubv39.Issue{
		Number: githubv39.Int(100),
		Labels: []*githubv39.Label{
			{Name: githubv39.String("greenfield")},
		},
	}

	syncReferencedIssueLabels(ctx, ghClient, "owner", "repo", pr, prIssue)
}

func TestHasLinkedPR(t *testing.T) {
	ctx := context.Background()

	httpClient := &http.Client{
		Transport: mockRoundTripper(func(req *http.Request) *http.Response {
			if strings.Contains(req.URL.Path, "/issues/1/timeline") {
				// Returns a timeline with a cross-referenced open PR event
				timelineJSON := `[
					{
						"event": "cross-referenced",
						"source": {
							"issue": {
								"number": 101,
								"state": "open",
								"pull_request": {
									"url": "https://api.github.com/repos/owner/repo/pulls/101"
								}
							}
						}
					}
				]`
				return &http.Response{
					StatusCode: 200,
					Body:       io.NopCloser(bytes.NewBufferString(timelineJSON)),
					Header:     make(http.Header),
				}
			}
			if strings.Contains(req.URL.Path, "/issues/2/timeline") {
				// Returns 404 to trigger fallback search
				return &http.Response{
					StatusCode: 404,
					Body:       io.NopCloser(bytes.NewBufferString("")),
					Header:     make(http.Header),
				}
			}
			if strings.Contains(req.URL.Path, "/search/issues") {
				if strings.Contains(req.URL.RawQuery, "%222%22") {
					// Search returns 1 result
					return &http.Response{
						StatusCode: 200,
						Body:       io.NopCloser(bytes.NewBufferString(`{"total_count": 1, "items": [{"number": 102}]}`)),
						Header:     make(http.Header),
					}
				}
				// Default search returns 0 results
				return &http.Response{
					StatusCode: 200,
					Body:       io.NopCloser(bytes.NewBufferString(`{"total_count": 0, "items": []}`)),
					Header:     make(http.Header),
				}
			}
			return &http.Response{
				StatusCode: 404,
				Body:       io.NopCloser(bytes.NewBufferString("")),
				Header:     make(http.Header),
			}
		}),
	}
	ghClient := githubv39.NewClient(httpClient)

	// Scenario 1: Linked PR found via timeline
	got, err := hasLinkedPR(ctx, ghClient, "owner", "repo", 1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !got {
		t.Errorf("expected hasLinkedPR to return true via timeline")
	}

	// Scenario 2: Linked PR found via search API fallback
	got, err = hasLinkedPR(ctx, ghClient, "owner", "repo", 2)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !got {
		t.Errorf("expected hasLinkedPR to return true via search fallback")
	}

	// Scenario 3: No linked PR found
	got, err = hasLinkedPR(ctx, ghClient, "owner", "repo", 3)
	t.Logf("Scenario 3: got = %v, err = %v", got, err)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got {
		t.Errorf("expected hasLinkedPR to return false")
	}
}

func TestSyncReferencedIssueLabelsErrors(t *testing.T) {
	ctx := context.Background()

	// 1. Fetching referenced issue returns error
	httpClient1 := &http.Client{
		Transport: mockRoundTripper(func(req *http.Request) *http.Response {
			return &http.Response{
				StatusCode: 500,
				Body:       io.NopCloser(bytes.NewBufferString("Internal Error")),
				Header:     make(http.Header),
			}
		}),
	}
	ghClient1 := githubv39.NewClient(httpClient1)

	pr1 := &githubv39.PullRequest{
		Number: githubv39.Int(100),
		Head: &githubv39.PullRequestBranch{
			Ref: githubv39.String("fix-issue-8884"),
		},
	}
	prIssue1 := &githubv39.Issue{
		Number: githubv39.Int(100),
		Labels: []*githubv39.Label{},
	}

	// Should continue without panicking
	syncReferencedIssueLabels(ctx, ghClient1, "owner", "repo", pr1, prIssue1)

	// 2. Adding labels to issue fails
	httpClient2 := &http.Client{
		Transport: mockRoundTripper(func(req *http.Request) *http.Response {
			if strings.Contains(req.URL.Path, "/repos/owner/repo/issues/8883") {
				return &http.Response{
					StatusCode: 200,
					Body:       io.NopCloser(bytes.NewBufferString(`{"number":8883,"labels":[{"name":"greenfield"}]}`)),
					Header:     make(http.Header),
				}
			}
			if strings.Contains(req.URL.Path, "/repos/owner/repo/issues/100/labels") {
				return &http.Response{
					StatusCode: 500,
					Body:       io.NopCloser(bytes.NewBufferString("Failed to add labels")),
					Header:     make(http.Header),
				}
			}
			return &http.Response{
				StatusCode: 404,
				Body:       io.NopCloser(bytes.NewBufferString("")),
				Header:     make(http.Header),
			}
		}),
	}
	ghClient2 := githubv39.NewClient(httpClient2)

	pr2 := &githubv39.PullRequest{
		Number: githubv39.Int(100),
		Head: &githubv39.PullRequestBranch{
			Ref: githubv39.String("fix-issue-8883"),
		},
	}
	prIssue2 := &githubv39.Issue{
		Number: githubv39.Int(100),
		Labels: []*githubv39.Label{},
	}

	// Should log error and return without panicking
	syncReferencedIssueLabels(ctx, ghClient2, "owner", "repo", pr2, prIssue2)
}

func TestFetchWorkflowContentErrors(t *testing.T) {
	ctx := context.Background()

	// 1. GitHub repo returns empty content response (fileContent is nil on github side)
	httpClient1 := &http.Client{
		Transport: mockRoundTripper(func(req *http.Request) *http.Response {
			return &http.Response{
				StatusCode: 200,
				Body:       io.NopCloser(bytes.NewBufferString("null")), // nil unstructured content
				Header:     make(http.Header),
			}
		}),
	}
	ghClient1 := githubv39.NewClient(httpClient1)
	_, err := fetchWorkflowContent(ctx, ghClient1, "https://github.com/owner/repo/blob/main/workflow.yaml")
	if err == nil {
		t.Errorf("expected error when GitHub API returns null content object")
	}

	// 2. Decoding content fails (malformed base64 content field)
	httpClient2 := &http.Client{
		Transport: mockRoundTripper(func(req *http.Request) *http.Response {
			return &http.Response{
				StatusCode: 200,
				Body:       io.NopCloser(bytes.NewBufferString(`{"name":"workflow.yaml","type":"file","content":"invalid_base64_@#$","encoding":"base64"}`)),
				Header:     make(http.Header),
			}
		}),
	}
	ghClient2 := githubv39.NewClient(httpClient2)
	_, err = fetchWorkflowContent(ctx, ghClient2, "https://github.com/owner/repo/blob/main/workflow.yaml")
	if err == nil {
		t.Errorf("expected error when GitHub API returns malformed base64 content")
	}

	// 3. HTTP GET returns status code 500
	http.DefaultTransport = mockRoundTripper(func(req *http.Request) *http.Response {
		return &http.Response{
			StatusCode: 500,
			Body:       io.NopCloser(bytes.NewBufferString("Internal Error")),
			Header:     make(http.Header),
		}
	})
	_, err = fetchWorkflowContent(ctx, nil, "http://my-agents.com/agent.yaml")
	if err == nil {
		t.Errorf("expected error when HTTP server returns status 500")
	}
}
