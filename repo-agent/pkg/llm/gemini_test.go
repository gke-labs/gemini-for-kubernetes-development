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
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

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
		g := &Gemini{ProviderConfig: ProviderConfig{WorkspacesDir: workspacesDir, TokensDir: tokensDir}}
		if err := g.Setup(); err != nil {
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
		g := &Gemini{ProviderConfig: ProviderConfig{WorkspacesDir: workspacesDir, TokensDir: tokensDir}}
		if err := g.Setup(); err == nil {
			t.Fatal("Gemini.Setup() should have failed, but it didn't")
		}
	})
}

// MockCommandExecutor is a mock implementation of CommandExecutor for testing.
type MockCommandExecutor struct {
	Command string
	Args    []string
	Output  []byte
	Stderr  []byte
	Err     error
}

func (e *MockCommandExecutor) Run(command string, args ...string) ([]byte, []byte, error) {
	e.Command = command
	e.Args = args
	return e.Output, e.Stderr, e.Err
}

func TestGemini_Run(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		// Create a mock executor
		mockExecutor := &MockCommandExecutor{
			Output: []byte("```yaml\nfoo: bar\n```"),
			Err:    nil,
		}

		// Create a Gemini provider with the mock executor
		tg := &Gemini{Executor: mockExecutor}
		tg.AddPostProcessor(StripYAMLMarkers)

		// Run the provider
		output, err := tg.Run("test prompt")
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
		if len(mockExecutor.Stderr) > 0 {
			t.Errorf("Expected empty stderr, but got %q", string(mockExecutor.Stderr))
		}
	})

	t.Run("error", func(t *testing.T) {
		// Create a mock executor that returns an error
		mockExecutor := &MockCommandExecutor{
			Output: nil,
			Stderr: nil,
			Err:    errors.New("command failed"),
		}

		// Create a Gemini provider with the mock executor
		g := &Gemini{Executor: mockExecutor}

		// Run the provider
		_, err := g.Run("test prompt")
		if err == nil {
			t.Fatal("Gemini.Run() should have failed, but it didn't")
		}
		if errors.Is(err, &QuotaError{}) {
			t.Errorf("Expected generic error, but got QuotaError: %v", err)
		}
	})

	t.Run("quota error", func(t *testing.T) {
		// Create a mock executor that returns a quota error in stderr
		mockExecutor := &MockCommandExecutor{
			Output: []byte("some output"),
			Stderr: []byte("[API Error: You have exhausted your daily quota on this model.]"),
			Err:    errors.New("command failed due to quota"),
		}

		// Create a Gemini provider with the mock executor
		g := &Gemini{Executor: mockExecutor}

		// Run the provider
		_, err := g.Run("test prompt")
		if err == nil {
			t.Fatal("Gemini.Run() should have failed with quota error, but it didn't")
		}
		var quotaErr *QuotaError
		if !errors.As(err, &quotaErr) {
			t.Errorf("Expected QuotaError, but got %T: %v", err, err)
		}
	})

	t.Run("post-processor error", func(t *testing.T) {
		// Create a mock executor
		mockExecutor := &MockCommandExecutor{
			Output: []byte("some output"),
			Stderr: nil,
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

func TestGeminiCleanup(t *testing.T) {
	// Create a temporary directory for the test
	tmpDir := t.TempDir()

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

	g := &Gemini{}
	if err := g.Cleanup(); err != nil {
		t.Errorf("Cleanup() error = %v, want nil", err)
	}
}

func TestEnsureSettings(t *testing.T) {
	t.Run("creates new settings", func(t *testing.T) {
		tmpDir := t.TempDir()
		geminiDir := filepath.Join(tmpDir, ".gemini")

		if err := ensureSettings(geminiDir); err != nil {
			t.Fatalf("ensureSettings failed: %v", err)
		}

		settingsPath := filepath.Join(geminiDir, "settings.json")
		data, err := os.ReadFile(settingsPath)
		if err != nil {
			t.Fatalf("failed to read settings.json: %v", err)
		}

		expected := `{
  "general": {
    "previewFeatures": true
  },
  "model": "gemini-3-pro-preview"
}`
		if string(data) != expected {
			t.Errorf("Expected settings:\n%s\nGot:\n%s", expected, string(data))
		}
	})

	t.Run("updates existing settings", func(t *testing.T) {
		tmpDir := t.TempDir()
		geminiDir := filepath.Join(tmpDir, ".gemini")
		if err := os.MkdirAll(geminiDir, 0755); err != nil {
			t.Fatalf("failed to create gemini dir: %v", err)
		}

		initialSettings := `{"other": "value", "general": {"old": "feature"}}`
		settingsPath := filepath.Join(geminiDir, "settings.json")
		if err := os.WriteFile(settingsPath, []byte(initialSettings), 0644); err != nil {
			t.Fatalf("failed to write initial settings: %v", err)
		}

		if err := ensureSettings(geminiDir); err != nil {
			t.Fatalf("ensureSettings failed: %v", err)
		}

		data, err := os.ReadFile(settingsPath)
		if err != nil {
			t.Fatalf("failed to read settings.json: %v", err)
		}

		// Check if it contains both old and new values
		var settings map[string]interface{}
		if err := json.Unmarshal(data, &settings); err != nil {
			t.Fatalf("failed to unmarshal settings: %v", err)
		}

		if settings["other"] != "value" {
			t.Errorf("Expected 'other' to be 'value', got %v", settings["other"])
		}

		general := settings["general"].(map[string]interface{})
		if general["old"] != "feature" {
			t.Errorf("Expected 'general.old' to be 'feature', got %v", general["old"])
		}
		if general["previewFeatures"] != true {
			t.Errorf("Expected 'general.previewFeatures' to be true, got %v", general["previewFeatures"])
		}
		if settings["model"] != "gemini-3-pro-preview" {
			t.Errorf("Expected 'model' to be 'gemini-3-pro-preview', got %v", settings["model"])
		}
	})

	t.Run("does not override existing model", func(t *testing.T) {
		tmpDir := t.TempDir()
		geminiDir := filepath.Join(tmpDir, ".gemini")
		if err := os.MkdirAll(geminiDir, 0755); err != nil {
			t.Fatalf("failed to create gemini dir: %v", err)
		}

		initialSettings := `{"model": "my-custom-model"}`
		settingsPath := filepath.Join(geminiDir, "settings.json")
		if err := os.WriteFile(settingsPath, []byte(initialSettings), 0644); err != nil {
			t.Fatalf("failed to write initial settings: %v", err)
		}

		if err := ensureSettings(geminiDir); err != nil {
			t.Fatalf("ensureSettings failed: %v", err)
		}

		data, err := os.ReadFile(settingsPath)
		if err != nil {
			t.Fatalf("failed to read settings.json: %v", err)
		}

		var settings map[string]interface{}
		if err := json.Unmarshal(data, &settings); err != nil {
			t.Fatalf("failed to unmarshal settings: %v", err)
		}

		if settings["model"] != "my-custom-model" {
			t.Errorf("Expected 'model' to be 'my-custom-model', got %v", settings["model"])
		}
	})
}

func TestStripUnillStartIndicator(t *testing.T) {
	processor := StripUnillStartIndicator("note:")

	t.Run("with note at beginning", func(t *testing.T) {
		input := []byte("note: content")
		expected := []byte("note: content")
		result, err := processor(input)
		if err != nil {
			t.Fatalf("StripUnillStartIndicator failed: %v", err)
		}
		if !bytes.Equal(result, expected) {
			t.Errorf("Expected %q, got %q", expected, result)
		}
	})

	t.Run("with note after thoughts", func(t *testing.T) {
		input := []byte("thoughts\nmore thoughts\nnote: content")
		expected := []byte("note: content")
		result, err := processor(input)
		if err != nil {
			t.Fatalf("StripUnillStartIndicator failed: %v", err)
		}
		if !bytes.Equal(result, expected) {
			t.Errorf("Expected %q, got %q", expected, result)
		}
	})

	t.Run("without note", func(t *testing.T) {
		input := []byte("just content")
		expected := []byte("just content")
		result, err := processor(input)
		if err != nil {
			t.Fatalf("StripUnillStartIndicator failed: %v", err)
		}
		if !bytes.Equal(result, expected) {
			t.Errorf("Expected %q, got %q", expected, result)
		}
	})

	t.Run("with note inside line (not start)", func(t *testing.T) {
		input := []byte("This is a note: content")
		expected := []byte("This is a note: content")
		result, err := processor(input)
		if err != nil {
			t.Fatalf("StripUnillStartIndicator failed: %v", err)
		}
		if !bytes.Equal(result, expected) {
			t.Errorf("Expected %q, got %q", expected, result)
		}
	})
}
