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
	"strings"
	"testing"
)

// ... (MockClient and TestClaudeRun function remain the same, but with ioutil.NopCloser replaced with io.NopCloser)

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
	// Test case 1: ANTHROPIC_API_KEY is set
	os.Setenv("ANTHROPIC_API_KEY", "test-api-key")
	defer os.Unsetenv("ANTHROPIC_API_KEY")

	c := &Claude{}
	err := c.Setup("", "")
	if err != nil {
		t.Fatalf("TestClaudeSetup (API key set) failed: %v", err)
	}

	if c.apiKey != "test-api-key" {
		t.Errorf("TestClaudeSetup (API key set): Expected apiKey 'test-api-key', got %q", c.apiKey)
	}

	// Test case 2: ANTHROPIC_API_KEY is not set
	os.Unsetenv("ANTHROPIC_API_KEY")

	c = &Claude{}
	err = c.Setup("", "")
	if err == nil || !strings.Contains(err.Error(), "ANTHROPIC_API_KEY environment variable not set") {
		t.Errorf("TestClaudeSetup (API key not set): Expected 'not set' error, got %v", err)
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
