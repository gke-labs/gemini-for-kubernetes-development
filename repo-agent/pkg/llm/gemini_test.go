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
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestStripYAMLMarkers(t *testing.T) {
	tests := []struct {
		name    string
		input   []byte
		want    []byte
		wantErr bool
	}{
		{
			name:    "with markers",
			input:   []byte("```yaml\nfoo: bar\n```"),
			want:    []byte("foo: bar"),
			wantErr: false,
		},
		{
			name:    "without markers",
			input:   []byte("foo: bar"),
			want:    []byte("foo: bar"),
			wantErr: false,
		},
		{
			name:    "empty input",
			input:   []byte(""),
			want:    []byte(""),
			wantErr: false,
		},
		{
			name:    "only markers",
			input:   []byte("```yaml\n```"),
			want:    []byte(""),
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := stripYAMLMarkers(tt.input)
			if (err != nil) != tt.wantErr {
				t.Errorf("stripYAMLMarkers() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !bytes.Equal(got, tt.want) {
				t.Errorf("stripYAMLMarkers() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestNewProvider(t *testing.T) {
	tests := []struct {
		name         string
		provider     string
		wantErr      bool
		expectedType string
	}{
		{
			name:         "gemini-cli provider",
			provider:     "gemini-cli",
			wantErr:      false,
			expectedType: "*llm.Gemini",
		},
		{
			name:     "unknown provider",
			provider: "unknown",
			wantErr:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			provider, err := NewLLMProvider(tt.provider)
			if (err != nil) != tt.wantErr {
				t.Errorf("NewProvider() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !tt.wantErr {
				providerType := fmt.Sprintf("%T", provider)
				if providerType != tt.expectedType {
					t.Errorf("NewProvider() type = %v, want %v", providerType, tt.expectedType)
				}
			}
		})
	}
}

func TestGemini_Setup(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		// Create a temporary directory for the test
		tmpDir := t.TempDir()

		// Create dummy files and directories
		workspacesDir := filepath.Join(tmpDir, "workspaces")
		geminiDir := filepath.Join(workspacesDir, ".gemini")
		tokensDir := filepath.Join(tmpDir, "tokens")

		if err := os.MkdirAll(geminiDir, 0755); err != nil {
			t.Fatalf("Failed to create .gemini dir: %v", err)
		}
		if err := os.MkdirAll(tokensDir, 0755); err != nil {
			t.Fatalf("Failed to create tokens dir: %v", err)
		}

		geminiTokenFile := filepath.Join(tokensDir, "gemini")
		if err := os.WriteFile(geminiTokenFile, []byte("test-api-key"), 0644); err != nil {
			t.Fatalf("Failed to write gemini token file: %v", err)
		}

		// Change the current working directory to the temporary directory
		wd, err := os.Getwd()
		if err != nil {
			t.Fatalf("Failed to get current working directory: %v", err)
		}
		defer func() {
			_ = os.Chdir(wd)
		}()
		if err := os.Chdir(tmpDir); err != nil {
			t.Fatalf("Failed to change working directory: %v", err)
		}

		// Create a Gemini provider and run Setup
		g := &Gemini{}
		if err := g.Setup(workspacesDir, tokensDir); err != nil {
			t.Fatalf("Gemini.Setup() failed: %v", err)
		}

		// Check if the environment variable is set
		apiKey := os.Getenv("GEMINI_API_KEY")
		if apiKey != "test-api-key" {
			t.Errorf("Expected GEMINI_API_KEY to be 'test-api-key', but got '%s'", apiKey)
		}
	})

	t.Run("read token error", func(t *testing.T) {
		// Create a temporary directory for the test
		tmpDir := t.TempDir()

		// Create dummy files and directories
		workspacesDir := filepath.Join(tmpDir, "workspaces")
		tokensDir := filepath.Join(tmpDir, "tokens")

		// Create a Gemini provider and run Setup
		g := &Gemini{}
		if err := g.Setup(workspacesDir, tokensDir); err == nil {
			t.Fatal("Gemini.Setup() should have failed, but it didn't")
		}
	})
}

// MockCommandExecutor is a mock implementation of CommandExecutor for testing.
type MockCommandExecutor struct {
	Command string
	Args    []string
	Output  []byte
	Err     error
}

func (e *MockCommandExecutor) Run(command string, args ...string) ([]byte, error) {
	e.Command = command
	e.Args = args
	return e.Output, e.Err
}

func TestGemini_Run(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		// Create a mock executor
		mockExecutor := &MockCommandExecutor{
			Output: []byte("```yaml\nfoo: bar\n```"),
			Err:    nil,
		}

		// Create a Gemini provider with the mock executor and add the default post-processor
		g := &Gemini{Executor: mockExecutor}
		g.AddPostProcessor(stripYAMLMarkers)

		// Run the provider
		output, err := g.Run("test prompt")
		if err != nil {
			t.Fatalf("Gemini.Run() failed: %v", err)
		}

		// Check the output
		expectedOutput := []byte("foo: bar")
		if !bytes.Equal(output, expectedOutput) {
			t.Errorf("Expected output %q, but got %q", expectedOutput, output)
		}

		// Check if the command was called correctly
		if mockExecutor.Command != "gemini" {
			t.Errorf("Expected command to be 'gemini', but got '%s'", mockExecutor.Command)
		}
		if len(mockExecutor.Args) != 3 {
			t.Fatalf("Expected 3 arguments, but got %d", len(mockExecutor.Args))
		}
		if mockExecutor.Args[0] != "-y" {
			t.Errorf("Expected first argument to be '-y', but got '%s'", mockExecutor.Args[0])
		}
		if mockExecutor.Args[1] != "-p" {
			t.Errorf("Expected second argument to be '-p', but got '%s'", mockExecutor.Args[1])
		}
		if mockExecutor.Args[2] != "test prompt" {
			t.Errorf("Expected third argument to be 'test prompt', but got '%s'", mockExecutor.Args[2])
		}
	})

	t.Run("error", func(t *testing.T) {
		// Create a mock executor that returns an error
		mockExecutor := &MockCommandExecutor{
			Output: nil,
			Err:    errors.New("command failed"),
		}

		// Create a Gemini provider with the mock executor
		g := &Gemini{Executor: mockExecutor}

		// Run the provider
		_, err := g.Run("test prompt")
		if err == nil {
			t.Fatal("Gemini.Run() should have failed, but it didn't")
		}
	})

	t.Run("post-processor error", func(t *testing.T) {
		// Create a mock executor
		mockExecutor := &MockCommandExecutor{
			Output: []byte("some output"),
			Err:    nil,
		}

		// Create a Gemini provider with the mock executor and a post-processor that returns an error
		g := &Gemini{Executor: mockExecutor}
		g.AddPostProcessor(func(_ []byte) ([]byte, error) {
			return nil, errors.New("post-processing failed")
		})

		// Run the provider
		_, err := g.Run("test prompt")
		if err == nil {
			t.Fatal("Gemini.Run() should have failed due to post-processor error, but it didn't")
		}
		if err.Error() != "post-processing failed" {
			t.Errorf("Expected error 'post-processing failed', but got '%v'", err)
		}
	})
}
