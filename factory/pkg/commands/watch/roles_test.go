package watch

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/config"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/k8s"
	githubv39 "github.com/google/go-github/v39/github"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
)

type mockRoundTripper func(req *http.Request) *http.Response

func (f mockRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	resp := f(req)
	if resp != nil && resp.Request == nil {
		resp.Request = req
	}
	return resp, nil
}

func TestSelectUserForTask(t *testing.T) {
	ctx := context.Background()

	// 1. Nil/Empty config case
	got, err := selectUserForTask(ctx, nil, nil, nil, "issue-fix", 0, "owner", "repo", "ns")
	if err != nil || got != "" {
		t.Errorf("selectUserForTask(nil config) = (%q, %v); want (empty, nil)", got, err)
	}

	// Define role config
	cfg := &config.FactoryConfig{
		Roles: map[string]config.RoleConfig{
			"coder": {
				Users: []string{"coder-bot-1", "coder-bot-2"},
				Tasks: []string{"issue-fix"},
			},
			"reviewer": {
				Users: []string{"reviewer-bot"},
				Tasks: []string{"pr-review"},
			},
			"agent": {
				Users: []string{"agent-bot"},
				Tasks: []string{"agent-chore"},
			},
		},
	}

	// Setup mock github client
	httpClient := &http.Client{
		Transport: mockRoundTripper(func(req *http.Request) *http.Response {
			if strings.Contains(req.URL.Path, "/issues/100") {
				return &http.Response{
					StatusCode: 200,
					Body:       io.NopCloser(bytes.NewBufferString(`{"number":100,"assignees":[{"login":"coder-bot-2"}]}`)),
					Header:     make(http.Header),
				}
			}
			if strings.Contains(req.URL.Path, "/pulls/200") {
				return &http.Response{
					StatusCode: 200,
					Body:       io.NopCloser(bytes.NewBufferString(`{"number":200,"user":{"login":"agent-bot"}}`)),
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

	// Scenario 2: Issue assignee fallback
	got, err = selectUserForTask(ctx, ghClient, nil, cfg, "issue-fix", 100, "owner", "repo", "ns")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "coder-bot-2" {
		t.Errorf("expected assignee %q, got %q", "coder-bot-2", got)
	}

	// Scenario 3: PR Author fallback (for PR task like pr-iterate)
	got, err = selectUserForTask(ctx, ghClient, nil, cfg, "pr-iterate", 200, "owner", "repo", "ns")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "agent-bot" {
		t.Errorf("expected assignee %q, got %q", "agent-bot", got)
	}

	// Scenario 4: Pinned user label on K8s Sandbox
	pinnedSandbox := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "agents.x-k8s.io/v1alpha1",
			"kind":       "Sandbox",
			"metadata": map[string]interface{}{
				"name":      "fix-repo-300",
				"namespace": "ns",
				"labels": map[string]interface{}{
					"factory.gemini.google.com/user": "coder-bot-1",
				},
			},
		},
	}
	pinnedSandbox.SetGroupVersionKind(k8s.SandboxGVR.GroupVersion().WithKind("Sandbox"))

	fakeKubeClient := newFakeKubeClient([]runtime.Object{pinnedSandbox}...)

	got, err = selectUserForTask(ctx, ghClient, fakeKubeClient, cfg, "issue-fix", 300, "owner", "repo", "ns")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "coder-bot-1" {
		t.Errorf("expected pinned assignee %q, got %q", "coder-bot-1", got)
	}
}

func TestSelectUserForTaskEdgeCases(t *testing.T) {
	ctx := context.Background()

	// 1. Config with no agent users, checking agent-chore -> falls back to coder
	cfgNoAgent := &config.FactoryConfig{
		Roles: map[string]config.RoleConfig{
			"coder": {
				Users: []string{"coder-bot-1"},
				Tasks: []string{"issue-fix"},
			},
		},
	}

	got, err := selectUserForTask(ctx, nil, nil, cfgNoAgent, "agent-chore", 0, "owner", "repo", "ns")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "coder-bot-1" {
		t.Errorf("expected fallback to coder-bot-1, got %q", got)
	}

	// 2. taskType pr-review -> should select reviewer
	httpClient1 := &http.Client{
		Transport: mockRoundTripper(func(req *http.Request) *http.Response {
			return &http.Response{
				StatusCode: 200,
				Body:       io.NopCloser(bytes.NewBufferString(`{"number":10,"user":{"login":"external-user"}}`)),
				Header:     make(http.Header),
			}
		}),
	}
	ghClient1 := githubv39.NewClient(httpClient1)

	cfgReviewer := &config.FactoryConfig{
		Roles: map[string]config.RoleConfig{
			"reviewer": {
				Users: []string{"reviewer-bot-1"},
				Tasks: []string{"pr-review"},
			},
		},
	}
	got2, err := selectUserForTask(ctx, ghClient1, nil, cfgReviewer, "pr-review", 10, "owner", "repo", "ns")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got2 != "reviewer-bot-1" {
		t.Errorf("expected reviewer-bot-1, got %q", got2)
	}

	// 3. PR author is not in configured bot pool -> returns error
	httpClient2 := &http.Client{
		Transport: mockRoundTripper(func(req *http.Request) *http.Response {
			return &http.Response{
				StatusCode: 200,
				Body:       io.NopCloser(bytes.NewBufferString(`{"number":11,"user":{"login":"external-user"}}`)),
				Header:     make(http.Header),
			}
		}),
	}
	ghClient2 := githubv39.NewClient(httpClient2)

	cfgCoderOnly := &config.FactoryConfig{
		Roles: map[string]config.RoleConfig{
			"coder": {
				Users: []string{"coder-bot-1"},
			},
		},
	}

	_, err = selectUserForTask(ctx, ghClient2, nil, cfgCoderOnly, "pr-iterate", 11, "owner", "repo", "ns")
	if err == nil {
		t.Errorf("expected error when PR author is not in bot pool")
	}
}
