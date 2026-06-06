/*
Copyright 2026 The Kubernetes Authors.

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

package commands

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/clients"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/github"
	"k8s.io/apimachinery/pkg/types"
)

type mockRoundTripper struct {
	responses map[string]func() *http.Response
}

func (m *mockRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	respFunc, ok := m.responses[req.URL.String()]
	if !ok {
		return &http.Response{StatusCode: http.StatusNotFound, Body: http.NoBody, Request: req}, nil
	}
	resp := respFunc()
	resp.Request = req
	return resp, nil
}

func TestFindSandboxForPR(t *testing.T) {
	repo := &github.Repo{
		Owner: "test-owner",
		Name:  "test-repo",
	}

	tests := []struct {
		name               string
		prID               int
		sandboxPodMapping  map[string]*types.NamespacedName
		timelineResponse   string
		timelineStatusCode int
		wantSandbox        string
		wantErrContains    string
	}{
		{
			name: "sandbox exists with PR number",
			prID: 101,
			sandboxPodMapping: map[string]*types.NamespacedName{
				"github-test-owner-test-repo-101": {
					Namespace: "default",
					Name:      "github-test-owner-test-repo-101",
				},
			},
			wantSandbox: "github-test-owner-test-repo-101",
		},
		{
			name: "sandbox with PR number doesn't exist, but sandbox with linked issue does",
			prID: 102,
			sandboxPodMapping: map[string]*types.NamespacedName{
				"github-test-owner-test-repo-50": {
					Namespace: "default",
					Name:      "github-test-owner-test-repo-50",
				},
			},
			timelineStatusCode: http.StatusOK,
			timelineResponse: `[
				{
					"event": "cross-referenced",
					"source": {
						"type": "issue",
						"issue": {
							"number": 50
						}
					}
				}
			]`,
			wantSandbox: "github-test-owner-test-repo-50",
		},
		{
			name:               "no sandbox exists",
			prID:               103,
			sandboxPodMapping:  map[string]*types.NamespacedName{},
			timelineStatusCode: http.StatusOK,
			timelineResponse:   `[]`,
			wantErrContains:    "no existing sandbox found for pull request #103",
		},
		{
			name:               "timeline API fails",
			prID:               104,
			sandboxPodMapping:  map[string]*types.NamespacedName{},
			timelineStatusCode: http.StatusInternalServerError,
			timelineResponse:   `{"message": "Internal Server Error"}`,
			wantErrContains:    "failed to get pull request timeline",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Set up mock HTTP client for GitHub API
			mockHTTPClient := &http.Client{
				Transport: &mockRoundTripper{
					responses: map[string]func() *http.Response{
						fmt.Sprintf("https://api.github.com/repos/test-owner/test-repo/issues/%d/timeline", tt.prID): func() *http.Response {
							return &http.Response{
								StatusCode: tt.timelineStatusCode,
								Body:       io.NopCloser(strings.NewReader(tt.timelineResponse)),
							}
						},
					},
				},
			}
			ghClient := clients.NewGitHubClientFromHTTP(mockHTTPClient)
			githubAPI := &github.Client{Client: ghClient}

			p := &AutoPoller{
				githubAPI: githubAPI,
				findSandboxPodFn: func(ctx context.Context, name string) (*types.NamespacedName, error) {
					if podID, ok := tt.sandboxPodMapping[name]; ok {
						return podID, nil
					}
					return nil, nil
				},
			}

			gotSandbox, err := p.findSandboxForPR(context.Background(), repo, tt.prID)
			if tt.wantErrContains != "" {
				if err == nil {
					t.Fatalf("expected error containing %q, got nil", tt.wantErrContains)
				}
				if !strings.Contains(err.Error(), tt.wantErrContains) {
					t.Errorf("error = %v, want error containing %q", err, tt.wantErrContains)
				}
			} else {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if gotSandbox != tt.wantSandbox {
					t.Errorf("findSandboxForPR() = %q, want %q", gotSandbox, tt.wantSandbox)
				}
			}
		})
	}
}
