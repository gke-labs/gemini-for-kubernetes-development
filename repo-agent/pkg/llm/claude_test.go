// Copyright 2025 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package llm

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ... (MockClient and other test functions remain the same)

type MockClient struct {
	DoFunc func(req *http.Request) (*http.Response, error)
}

func (m *MockClient) Do(req *http.Request) (*http.Response, error) {
	return m.DoFunc(req)
}

type errorReader struct{}

func (er *errorReader) Read(_ []byte) (n int, err error) {
	return 0, fmt.Errorf("simulated read error")
}

func (er *errorReader) Close() error {
	return nil
}

func TestClaudeRun(t *testing.T) {
	// Test case 1: Successful API call
	mockClient := &MockClient{
		DoFunc: func(_ *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(bytes.NewBufferString(`{"content":[{"text":"Hello!"}]}`)),
			}, nil
		},
	}

	c := &Claude{apiKey: "test-key", client: mockClient}
	prompt := "test prompt"

	resp, err := c.Run(prompt)
	if err != nil {
		t.Fatalf("TestClaudeRun (success) failed: %v", err)
	}

	expected := "Hello!"
	if string(resp) != expected {
		t.Errorf("TestClaudeRun (success): Expected %q, got %q", expected, string(resp))
	}

	// Test case 2: API call fails (network error)
	mockClient.DoFunc = func(_ *http.Request) (*http.Response, error) {
		return nil, fmt.Errorf("network error")
	}

	_, err = c.Run(prompt)
	if err == nil || !strings.Contains(err.Error(), "failed to make request: network error") {
		t.Errorf("TestClaudeRun (network error): Expected network error, got %v", err)
	}

	// Test case 3: API returns non-200 status code
	mockClient.DoFunc = func(_ *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusInternalServerError,
			Body:       io.NopCloser(bytes.NewBufferString(`{"error":"internal server error"}`)),
		}, nil
	}

	_, err = c.Run(prompt)
	if err == nil || !strings.Contains(err.Error(), "request failed with status 500: {\"error\":\"internal server error\"}") {
		t.Errorf("TestClaudeRun (non-200 status): Expected status 500 error, got %v", err)
	}

	// Test case 4: API returns invalid JSON response
	mockClient.DoFunc = func(_ *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(bytes.NewBufferString(`invalid json`)),
		}, nil
	}

	_, err = c.Run(prompt)
	if err == nil || !strings.Contains(err.Error(), "failed to unmarshal response body") {
		t.Errorf("TestClaudeRun (invalid JSON): Expected unmarshal error, got %v", err)
	}

	// Test case 5: API returns empty content array
	mockClient.DoFunc = func(_ *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(bytes.NewBufferString(`{"content":[]}`)),
		}, nil
	}

	_, err = c.Run(prompt)
	if err == nil || !strings.Contains(err.Error(), "no content in response") {
		t.Errorf("TestClaudeRun (empty content): Expected 'no content' error, got %v", err)
	}

	// Test case 6: io.ReadAll fails
	mockClient.DoFunc = func(_ *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       &errorReader{},
		}, nil
	}

	_, err = c.Run(prompt)
	if err == nil || !strings.Contains(err.Error(), "failed to read response body: simulated read error") {
		t.Errorf("TestClaudeRun (io.ReadAll error): Expected read error, got %v", err)
	}

	// Test case 7: http.NewRequest fails
	c.URL = "://invalid-url"
	_, err = c.Run(prompt)
	if err == nil || !strings.Contains(err.Error(), "failed to create request") {
		t.Errorf("TestClaudeRun (http.NewRequest error): Expected create request error, got %v", err)
	}
}

func TestClaudeSetup(t *testing.T) {
	// Save original functions and restore them after the test

	testCases := []struct {
		name             string
		setupFunc        func(tokensDir string) func() // returns a cleanup function
		envVarSet        bool
		envVarValue      string
		expectedAPIKey   string
		expectError      bool
		expectedErrorMsg string
	}{
		{
			name: "API key from file",
			setupFunc: func(tokensDir string) func() {
				// Create a temporary file to simulate the mounted secret
				filePath := filepath.Join(tokensDir, AnthropicAPIKeySecretKey)
				if err := os.WriteFile(filePath, []byte("file-api-key"), 0600); err != nil {
					t.Fatalf("Failed to write API key file: %v", err)
				}
				return func() { os.Remove(filePath) }
			},
			expectedAPIKey: "file-api-key",
			expectError:    false,
		},
		{
			name: "API key from environment variable (file not found)",
			setupFunc: func(_ string) func() {
				// We don't create the file, so it's not found
				return func() {}
			},
			envVarSet:        true,
			envVarValue:      "env-api-key",
			expectedAPIKey:   "env-api-key",
			expectError:      false,
			expectedErrorMsg: "", // No error expected, as env var is fallback
		},
		{
			name: "No API key (file not found and env var not set)",
			setupFunc: func(_ string) func() {
				// We don't create the file, so it's not found
				return func() {}
			},
			envVarSet:        false,
			expectedAPIKey:   "",
			expectError:      true,
			expectedErrorMsg: "API key not found in",
		},

		{
			name: "API key from file with leading/trailing whitespace",
			setupFunc: func(tokensDir string) func() {
				filePath := filepath.Join(tokensDir, AnthropicAPIKeySecretKey)
				if err := os.WriteFile(filePath, []byte("  file-api-key  "), 0600); err != nil {
					t.Fatalf("Failed to write API key file: %v", err)
				}
				return func() { os.Remove(filePath) }
			},
			expectedAPIKey: "file-api-key",
			expectError:    false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Create a temporary directory for tokens
			tokensDir, err := os.MkdirTemp("", "claude-test-tokens")
			if err != nil {
				t.Fatalf("Failed to create temp dir: %v", err)
			}
			defer os.RemoveAll(tokensDir)

			// Set up the environment as per the test case
			cleanupFunc := tc.setupFunc(tokensDir)
			defer cleanupFunc()

			if tc.envVarSet {
				os.Setenv(AnthropicAPIKeyEnvVar, tc.envVarValue)
				defer os.Unsetenv(AnthropicAPIKeyEnvVar)
			} else {
				os.Unsetenv(AnthropicAPIKeyEnvVar)
			}

			// Run the test
			c := &Claude{ProviderConfig: ProviderConfig{WorkspacesDir: "", TokensDir: tokensDir}}
			err = c.Setup()

			// Assertions
			if tc.expectError {
				if err == nil {
					t.Fatalf("Expected an error, but got none")
				}
				if !strings.Contains(err.Error(), tc.expectedErrorMsg) {
					t.Errorf("Expected error message to contain %q, but got %q", tc.expectedErrorMsg, err.Error())
				}
			} else {
				if err != nil {
					t.Fatalf("Expected no error, but got: %v", err)
				}
				if c.apiKey != tc.expectedAPIKey {
					t.Errorf("Expected apiKey %q, but got %q", tc.expectedAPIKey, c.apiKey)
				}
			}
		})
	}
}

func TestClaudeAddPostProcessor(t *testing.T) {
	c := &Claude{}
	c.AddPostProcessor(func(_ []byte) ([]byte, error) { return nil, nil })
	if len(c.postProcessors) != 1 {
		t.Errorf("TestClaudeAddPostProcessor: Expected 1 post-processor, got %d", len(c.postProcessors))
	}
}

func TestClaudeRunWithPostProcessor(t *testing.T) {
	mockClient := &MockClient{
		DoFunc: func(_ *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(bytes.NewBufferString(`{"content":[{"text":"Hello!"}]}`)),
			}, nil
		},
	}

	c := &Claude{apiKey: "test-key", client: mockClient}
	prompt := "test prompt"

	// Add a post-processor that appends " World!"
	c.AddPostProcessor(func(originalInput []byte) ([]byte, error) {
		return []byte(string(originalInput) + " World!"), nil
	})

	resp, err := c.Run(prompt)
	if err != nil {
		t.Fatalf("TestClaudeRunWithPostProcessor failed: %v", err)
	}

	expected := "Hello! World!"
	if string(resp) != expected {
		t.Errorf("TestClaudeRunWithPostProcessor: Expected %q, got %q", expected, string(resp))
	}
}

func TestClaudeRunWithFailingPostProcessor(t *testing.T) {
	mockClient := &MockClient{
		DoFunc: func(_ *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(bytes.NewBufferString(`{"content":[{"text":"Hello!"}]}`)),
			}, nil
		},
	}

	c := &Claude{apiKey: "test-key", client: mockClient}
	prompt := "test prompt"

	// Add a post-processor that returns an error
	c.AddPostProcessor(func(_ []byte) ([]byte, error) {
		return nil, fmt.Errorf("post-processor error")
	})

	_, err := c.Run(prompt)
	if err == nil || !strings.Contains(err.Error(), "failed to apply post-processor: post-processor error") {
		t.Errorf("TestClaudeRunWithFailingPostProcessor: Expected post-processor error, got %v", err)
	}
}

func TestClaudeRunWithStripYAMLMarkers(t *testing.T) {
	mockClient := &MockClient{
		DoFunc: func(_ *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(bytes.NewBufferString("{\"content\":[{\"text\":\"```yaml\\nfoo: bar\\n```\"}]}")),
			}, nil
		},
	}

	c := &Claude{apiKey: "test-key", client: mockClient}
	prompt := "test prompt"

	// Add the StripYAMLMarkers post-processor
	c.AddPostProcessor(StripYAMLMarkers)

	resp, err := c.Run(prompt)
	if err != nil {
		t.Fatalf("TestClaudeRunWithStripYAMLMarkers failed: %v", err)
	}

	expected := "foo: bar"
	if string(resp) != expected {
		t.Errorf("TestClaudeRunWithStripYAMLMarkers: Expected %q, got %q", expected, string(resp))
	}
}

func TestClaudeCleanup(t *testing.T) {
	c := &Claude{}
	if err := c.Cleanup(); err != nil {
		t.Errorf("Cleanup() error = %v, want nil", err)
	}
}
