// Copyright 2026 Google LLC

package github

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

type mockHTTPClient struct {
	response *http.Response
	err      error
}

func (m *mockHTTPClient) Do(req *http.Request) (*http.Response, error) {
	return m.response, m.err
}

func TestIsPRInMergeQueue(t *testing.T) {
	// Save the original client and restore after tests
	origClient := DefaultHTTPClient
	defer func() {
		DefaultHTTPClient = origClient
	}()

	tests := []struct {
		name       string
		token      string
		owner      string
		repo       string
		number     int
		mockResp   string
		mockStatus int
		mockErr    error
		expected   bool
		expectErr  bool
	}{
		{
			name:   "PR is enqueued",
			token:  "test-token",
			owner:  "test-owner",
			repo:   "test-repo",
			number: 1,
			mockResp: `{
				"data": {
					"repository": {
						"pullRequest": {
							"mergeQueueEntry": {
								"position": 1
							}
						}
					}
				}
			}`,
			mockStatus: http.StatusOK,
			expected:   true,
			expectErr:  false,
		},
		{
			name:   "PR is not enqueued",
			token:  "test-token",
			owner:  "test-owner",
			repo:   "test-repo",
			number: 2,
			mockResp: `{
				"data": {
					"repository": {
						"pullRequest": {
							"mergeQueueEntry": null
						}
					}
				}
			}`,
			mockStatus: http.StatusOK,
			expected:   false,
			expectErr:  false,
		},
		{
			name:       "GraphQL error response",
			token:      "test-token",
			owner:      "test-owner",
			repo:       "test-repo",
			number:     3,
			mockResp:   `{"errors": [{"message": "Some internal error"}]}`,
			mockStatus: http.StatusOK,
			expected:   false,
			expectErr:  true,
		},
		{
			name:       "HTTP 500 error",
			token:      "test-token",
			owner:      "test-owner",
			repo:       "test-repo",
			number:     4,
			mockResp:   "Internal Server Error",
			mockStatus: http.StatusInternalServerError,
			expected:   false,
			expectErr:  true,
		},
		{
			name:      "Empty token error",
			token:     "",
			owner:     "test-owner",
			repo:      "test-repo",
			number:    5,
			expected:  false,
			expectErr: true,
		},
		{
			name:   "PR not found",
			token:  "test-token",
			owner:  "test-owner",
			repo:   "test-repo",
			number: 6,
			mockResp: `{
				"data": {
					"repository": {
						"pullRequest": null
					}
				}
			}`,
			mockStatus: http.StatusOK,
			expected:   false,
			expectErr:  true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if tc.token != "" {
				client := &mockHTTPClient{
					err: tc.mockErr,
				}
				if tc.mockErr == nil {
					client.response = &http.Response{
						StatusCode: tc.mockStatus,
						Body:       io.NopCloser(strings.NewReader(tc.mockResp)),
					}
				}
				DefaultHTTPClient = client
			}

			result, err := IsPRInMergeQueue(context.Background(), tc.token, tc.owner, tc.repo, tc.number)
			if tc.expectErr {
				if err == nil {
					t.Errorf("expected error, got nil")
				}
			} else {
				if err != nil {
					t.Errorf("unexpected error: %v", err)
				}
				if result != tc.expected {
					t.Errorf("expected %v, got %v", tc.expected, result)
				}
			}
		})
	}
}
