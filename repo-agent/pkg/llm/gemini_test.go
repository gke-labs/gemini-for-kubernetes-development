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

	reviewv1alpha1 "github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/api/repowatch/v1alpha1"
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
	Stdin   string
	Output  []byte
	Stderr  []byte
	Err     error
}

func (e *MockCommandExecutor) Run(command string, args ...string) ([]byte, []byte, error) {
	return e.RunWithStdin(command, "", args...)
}

func (e *MockCommandExecutor) RunWithStdin(command string, stdin string, args ...string) ([]byte, []byte, error) {
	e.Command = command
	e.Args = args
	e.Stdin = stdin
	return e.Output, e.Stderr, e.Err
}

// RecordingCommandExecutor records all calls for verification in tests.
type RecordingCommandExecutor struct {
	Calls []RecordedCall
	// Err is returned for all calls if set
	Err    error
	Stderr []byte
}

type RecordedCall struct {
	Command string
	Args    []string
	Stdin   string
}

func (e *RecordingCommandExecutor) Run(command string, args ...string) ([]byte, []byte, error) {
	return e.RunWithStdin(command, "", args...)
}

func (e *RecordingCommandExecutor) RunWithStdin(command string, stdin string, args ...string) ([]byte, []byte, error) {
	e.Calls = append(e.Calls, RecordedCall{Command: command, Args: args, Stdin: stdin})
	return []byte("ok"), e.Stderr, e.Err
}

func TestGemini_Setup_Extensions(t *testing.T) {
	// Helper to set up a valid Gemini Setup environment (tokens, workspace)
	setupEnv := func(t *testing.T) (string, string) {
		t.Helper()
		tmpDir := t.TempDir()
		workspacesDir := filepath.Join(tmpDir, "workspaces")
		tokensDir := filepath.Join(tmpDir, "tokens")
		if err := os.MkdirAll(workspacesDir, 0755); err != nil {
			t.Fatalf("Failed to create workspaces dir: %v", err)
		}
		if err := os.MkdirAll(tokensDir, 0755); err != nil {
			t.Fatalf("Failed to create tokens dir: %v", err)
		}
		if err := os.WriteFile(filepath.Join(tokensDir, "gemini"), []byte("test-key"), 0644); err != nil {
			t.Fatalf("Failed to write gemini token: %v", err)
		}
		return workspacesDir, tokensDir
	}

	t.Run("no extensions", func(t *testing.T) {
		workspacesDir, tokensDir := setupEnv(t)
		executor := &RecordingCommandExecutor{}
		g := &Gemini{
			Executor:       executor,
			ProviderConfig: ProviderConfig{WorkspacesDir: workspacesDir, TokensDir: tokensDir},
		}
		if err := g.Setup(); err != nil {
			t.Fatalf("Setup() failed: %v", err)
		}
		if len(executor.Calls) != 0 {
			t.Errorf("Expected 0 executor calls, got %d", len(executor.Calls))
		}
	})

	t.Run("single extension without ref", func(t *testing.T) {
		workspacesDir, tokensDir := setupEnv(t)
		executor := &RecordingCommandExecutor{}
		g := &Gemini{
			Executor: executor,
			ProviderConfig: ProviderConfig{
				WorkspacesDir: workspacesDir,
				TokensDir:     tokensDir,
				Extensions: []reviewv1alpha1.Extension{
					{Source: "https://github.com/example/ext1"},
				},
			},
		}
		if err := g.Setup(); err != nil {
			t.Fatalf("Setup() failed: %v", err)
		}
		if len(executor.Calls) != 1 {
			t.Fatalf("Expected 1 executor call, got %d", len(executor.Calls))
		}
		call := executor.Calls[0]
		if call.Command != "gemini" {
			t.Errorf("Expected command 'gemini', got %q", call.Command)
		}
		expectedArgs := []string{"extensions", "install", "https://github.com/example/ext1", "--consent"}
		if len(call.Args) != len(expectedArgs) {
			t.Fatalf("Expected %d args, got %d: %v", len(expectedArgs), len(call.Args), call.Args)
		}
		for i, arg := range expectedArgs {
			if call.Args[i] != arg {
				t.Errorf("Arg[%d]: expected %q, got %q", i, arg, call.Args[i])
			}
		}
	})

	t.Run("single extension with ref", func(t *testing.T) {
		workspacesDir, tokensDir := setupEnv(t)
		executor := &RecordingCommandExecutor{}
		g := &Gemini{
			Executor: executor,
			ProviderConfig: ProviderConfig{
				WorkspacesDir: workspacesDir,
				TokensDir:     tokensDir,
				Extensions: []reviewv1alpha1.Extension{
					{Source: "https://github.com/example/ext1", Ref: "v1.0.0"},
				},
			},
		}
		if err := g.Setup(); err != nil {
			t.Fatalf("Setup() failed: %v", err)
		}
		if len(executor.Calls) != 1 {
			t.Fatalf("Expected 1 executor call, got %d", len(executor.Calls))
		}
		call := executor.Calls[0]
		expectedArgs := []string{"extensions", "install", "https://github.com/example/ext1", "--consent", "--ref", "v1.0.0"}
		if len(call.Args) != len(expectedArgs) {
			t.Fatalf("Expected %d args, got %d: %v", len(expectedArgs), len(call.Args), call.Args)
		}
		for i, arg := range expectedArgs {
			if call.Args[i] != arg {
				t.Errorf("Arg[%d]: expected %q, got %q", i, arg, call.Args[i])
			}
		}
	})

	t.Run("multiple extensions", func(t *testing.T) {
		workspacesDir, tokensDir := setupEnv(t)
		executor := &RecordingCommandExecutor{}
		g := &Gemini{
			Executor: executor,
			ProviderConfig: ProviderConfig{
				WorkspacesDir: workspacesDir,
				TokensDir:     tokensDir,
				Extensions: []reviewv1alpha1.Extension{
					{Source: "https://github.com/example/ext1"},
					{Source: "https://github.com/example/ext2", Ref: "main"},
					{Source: "https://github.com/example/ext3", Ref: "abc123"},
				},
			},
		}
		if err := g.Setup(); err != nil {
			t.Fatalf("Setup() failed: %v", err)
		}
		if len(executor.Calls) != 3 {
			t.Fatalf("Expected 3 executor calls, got %d", len(executor.Calls))
		}

		// Verify first call (no ref)
		if executor.Calls[0].Args[2] != "https://github.com/example/ext1" {
			t.Errorf("Call 0: expected source 'ext1', got %q", executor.Calls[0].Args[2])
		}
		if len(executor.Calls[0].Args) != 4 {
			t.Errorf("Call 0: expected 4 args (no ref), got %d", len(executor.Calls[0].Args))
		}

		// Verify second call (with ref "main")
		if executor.Calls[1].Args[2] != "https://github.com/example/ext2" {
			t.Errorf("Call 1: expected source 'ext2', got %q", executor.Calls[1].Args[2])
		}
		if len(executor.Calls[1].Args) != 6 || executor.Calls[1].Args[5] != "main" {
			t.Errorf("Call 1: expected ref 'main', got args %v", executor.Calls[1].Args)
		}

		// Verify third call (with ref "abc123")
		if executor.Calls[2].Args[2] != "https://github.com/example/ext3" {
			t.Errorf("Call 2: expected source 'ext3', got %q", executor.Calls[2].Args[2])
		}
		if len(executor.Calls[2].Args) != 6 || executor.Calls[2].Args[5] != "abc123" {
			t.Errorf("Call 2: expected ref 'abc123', got args %v", executor.Calls[2].Args)
		}
	})

	t.Run("extension install failure", func(t *testing.T) {
		workspacesDir, tokensDir := setupEnv(t)
		executor := &RecordingCommandExecutor{
			Err:    errors.New("install failed"),
			Stderr: []byte("permission denied"),
		}
		g := &Gemini{
			Executor: executor,
			ProviderConfig: ProviderConfig{
				WorkspacesDir: workspacesDir,
				TokensDir:     tokensDir,
				Extensions: []reviewv1alpha1.Extension{
					{Source: "https://github.com/example/bad-ext"},
				},
			},
		}
		err := g.Setup()
		if err == nil {
			t.Fatal("Setup() should have failed when extension install fails")
		}
		if !bytes.Contains([]byte(err.Error()), []byte("failed to install extension")) {
			t.Errorf("Error should mention 'failed to install extension', got: %v", err)
		}
		if !bytes.Contains([]byte(err.Error()), []byte("permission denied")) {
			t.Errorf("Error should include stderr content, got: %v", err)
		}
	})

	t.Run("second extension fails stops early", func(t *testing.T) {
		workspacesDir, tokensDir := setupEnv(t)
		callCount := 0
		// Use a custom executor that fails on the second call
		executor := &FailOnNthCallExecutor{FailOnCall: 2}
		g := &Gemini{
			Executor: executor,
			ProviderConfig: ProviderConfig{
				WorkspacesDir: workspacesDir,
				TokensDir:     tokensDir,
				Extensions: []reviewv1alpha1.Extension{
					{Source: "https://github.com/example/ext1"},
					{Source: "https://github.com/example/ext2"},
					{Source: "https://github.com/example/ext3"},
				},
			},
		}
		err := g.Setup()
		if err == nil {
			t.Fatal("Setup() should have failed")
		}
		callCount = len(executor.Calls)
		if callCount != 2 {
			t.Errorf("Expected 2 calls (stop at failure), got %d", callCount)
		}
	})
}

// FailOnNthCallExecutor fails on the Nth call.
type FailOnNthCallExecutor struct {
	FailOnCall int
	Calls      []RecordedCall
}

func (e *FailOnNthCallExecutor) Run(command string, args ...string) ([]byte, []byte, error) {
	return e.RunWithStdin(command, "", args...)
}

func (e *FailOnNthCallExecutor) RunWithStdin(command string, stdin string, args ...string) ([]byte, []byte, error) {
	e.Calls = append(e.Calls, RecordedCall{Command: command, Args: args, Stdin: stdin})
	if len(e.Calls) == e.FailOnCall {
		return nil, []byte("error on call"), errors.New("command failed")
	}
	return []byte("ok"), nil, nil
}

func makeGeminiJSONOutput(response string) []byte {
	envelope := GeminiJSONOutput{
		SessionID: "test-session",
		Response:  response,
		Stats: GeminiStatsJSON{
			Models: map[string]GeminiModelStatsJSON{
				"gemini-2.5-pro": {
					API: GeminiAPIStatsJSON{
						TotalRequests:  3,
						TotalErrors:    0,
						TotalLatencyMs: 5000,
					},
					Tokens: GeminiTokenStatsJSON{
						Input:      100,
						Candidates: 50,
						Total:      150,
					},
				},
			},
		},
	}
	data, _ := json.Marshal(envelope)
	return data
}

func TestGemini_Run(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		// Create a mock executor that returns JSON envelope with yaml content
		mockExecutor := &MockCommandExecutor{
			Output: makeGeminiJSONOutput("```yaml\nfoo: bar\n```"),
			Err:    nil,
		}

		// Create a Gemini provider with the mock executor
		tg := &Gemini{Executor: mockExecutor}
		tg.AddPostProcessor(StripYAMLMarkers)

		// Run the provider
		output, usage, err := tg.Run("test prompt")
		if err != nil {
			t.Fatalf("Gemini.Run() failed: %v", err)
		}

		// Check the output (post-processor strips YAML markers from the response field)
		expectedOutput := []byte("foo: bar")
		if !bytes.Equal(output, expectedOutput) {
			t.Errorf("Expected output %q, but got %q", expectedOutput, output)
		}

		// Check usage was extracted
		if usage == nil {
			t.Fatal("Expected non-nil usage")
		}
		if len(usage.Models) != 1 {
			t.Fatalf("Expected 1 model in usage, got %d", len(usage.Models))
		}
		modelUsage, ok := usage.Models["gemini-2.5-pro"]
		if !ok {
			t.Fatal("Expected usage for model 'gemini-2.5-pro'")
		}
		if modelUsage.API.TotalRequests != 3 {
			t.Errorf("Expected 3 total requests, got %d", modelUsage.API.TotalRequests)
		}
		if modelUsage.Tokens.Input != 100 {
			t.Errorf("Expected 100 input tokens, got %d", modelUsage.Tokens.Input)
		}

		// Check if the command was called correctly
		if mockExecutor.Command != "gemini" {
			t.Errorf("Expected command to be 'gemini', but got '%s'", mockExecutor.Command)
		}
		if len(mockExecutor.Args) != 5 {
			t.Fatalf("Expected 5 arguments, but got %d: %v", len(mockExecutor.Args), mockExecutor.Args)
		}
		if mockExecutor.Args[0] != "-y" {
			t.Errorf("Expected first argument to be '-y', but got '%s'", mockExecutor.Args[0])
		}
		if mockExecutor.Args[1] != "--output-format" {
			t.Errorf("Expected second argument to be '--output-format', but got '%s'", mockExecutor.Args[1])
		}
		if mockExecutor.Args[2] != "json" {
			t.Errorf("Expected third argument to be 'json', but got '%s'", mockExecutor.Args[2])
		}
		if mockExecutor.Args[3] != "-p" {
			t.Errorf("Expected fourth argument to be '-p', but got '%s'", mockExecutor.Args[3])
		}
		if mockExecutor.Args[4] != "" {
			t.Errorf("Expected fifth argument to be empty string, but got '%s'", mockExecutor.Args[4])
		}
		// Check if the prompt was passed via stdin
		if mockExecutor.Stdin != "test prompt" {
			t.Errorf("Expected stdin to be 'test prompt', but got '%q'", mockExecutor.Stdin)
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
		_, _, err := g.Run("test prompt")
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
		_, _, err := g.Run("test prompt")
		if err == nil {
			t.Fatal("Gemini.Run() should have failed with quota error, but it didn't")
		}
		var quotaErr *QuotaError
		if !errors.As(err, &quotaErr) {
			t.Errorf("Expected QuotaError, but got %T: %v", err, err)
		}
	})

	t.Run("post-processor error", func(t *testing.T) {
		// Create a mock executor that returns valid JSON envelope
		mockExecutor := &MockCommandExecutor{
			Output: makeGeminiJSONOutput("some output"),
			Stderr: nil,
			Err:    nil,
		}

		// Create a Gemini provider with the mock executor and a post-processor that returns an error
		g := &Gemini{Executor: mockExecutor}
		g.AddPostProcessor(func(_ []byte) ([]byte, error) {
			return nil, errors.New("post-processing failed")
		})

		// Run the provider
		_, _, err := g.Run("test prompt")
		if err == nil {
			t.Fatal("Gemini.Run() should have failed due to post-processor error, but it didn't")
		}
		if err.Error() != "post-processing failed" {
			t.Errorf("Expected error 'post-processing failed', but got '%v'", err)
		}
	})

	t.Run("no JSON in output", func(t *testing.T) {
		mockExecutor := &MockCommandExecutor{
			Output: []byte("not json at all"),
			Err:    nil,
		}
		g := &Gemini{Executor: mockExecutor}
		_, _, err := g.Run("test prompt")
		if err == nil {
			t.Fatal("Gemini.Run() should have failed when no JSON in output")
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
  "model": {
    "name": "gemini-3.1-pro-preview"
  }
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
		model := settings["model"].(map[string]interface{})
		if model["name"] != "gemini-3.1-pro-preview" {
			t.Errorf("Expected 'model.name' to be 'gemini-3.1-pro-preview', got %v", model["name"])
		}

	})

	t.Run("does not override existing model", func(t *testing.T) {
		tmpDir := t.TempDir()
		geminiDir := filepath.Join(tmpDir, ".gemini")
		if err := os.MkdirAll(geminiDir, 0755); err != nil {
			t.Fatalf("failed to create gemini dir: %v", err)
		}

		initialSettings := `{"model": {"name": "my-custom-model"}}`
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

		model := settings["model"].(map[string]interface{})
		if model["name"] != "my-custom-model" {
			t.Errorf("Expected 'model.name' to be 'my-custom-model', got %v", model["name"])
		}
	})

	t.Run("converts string model to object", func(t *testing.T) {
		tmpDir := t.TempDir()
		geminiDir := filepath.Join(tmpDir, ".gemini")
		if err := os.MkdirAll(geminiDir, 0755); err != nil {
			t.Fatalf("failed to create gemini dir: %v", err)
		}

		initialSettings := `{"model": "string-model"}`
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

		model, ok := settings["model"].(map[string]interface{})
		if !ok {
			t.Fatalf("Expected 'model' to be an object, but got %T", settings["model"])
		}
		if model["name"] != "string-model" {
			t.Errorf("Expected 'model.name' to be 'string-model', got %v", model["name"])
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
