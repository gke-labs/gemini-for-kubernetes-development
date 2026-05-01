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
	"os"
	"path/filepath"
	"testing"
)

func TestClaudeCLI_Setup(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		tmpDir := t.TempDir()
		tokensDir := filepath.Join(tmpDir, "tokens")
		if err := os.MkdirAll(tokensDir, 0755); err != nil {
			t.Fatalf("Failed to create tokens dir: %v", err)
		}

		apiKeyFile := filepath.Join(tokensDir, "claude")
		if err := os.WriteFile(apiKeyFile, []byte("test-claude-key"), 0644); err != nil {
			t.Fatalf("Failed to write claude token file: %v", err)
		}

		c := &ClaudeCLI{ProviderConfig: ProviderConfig{TokensDir: tokensDir}}
		if err := c.Setup(); err != nil {
			t.Fatalf("ClaudeCLI.Setup() failed: %v", err)
		}

		apiKey := os.Getenv("ANTHROPIC_API_KEY")
		if apiKey != "test-claude-key" {
			t.Errorf("Expected ANTHROPIC_API_KEY to be 'test-claude-key', but got '%s'", apiKey)
		}
	})
}

func TestClaudeCLI_Run(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		mockOutput := `{"result": "Claude response", "modelUsage": {"claude-sonnet-4-6": {"inputTokens": 10, "outputTokens": 20, "cacheReadInputTokens": 5}}}`
		mockExecutor := &MockCommandExecutor{
			Output: []byte(mockOutput),
			Err:    nil,
		}

		c := &ClaudeCLI{Executor: mockExecutor}

		output, usage, err := c.Run("fix this bug")
		if err != nil {
			t.Fatalf("ClaudeCLI.Run() failed: %v", err)
		}

		if string(output) != "Claude response" {
			t.Errorf("Expected output 'Claude response', got %q", string(output))
		}

		if usage == nil {
			t.Fatal("Expected non-nil usage")
		}
		if usage.Models["claude-sonnet-4-6"].Tokens.Input != 10 {
			t.Errorf("Expected 10 input tokens, got %d", usage.Models["claude-sonnet-4-6"].Tokens.Input)
		}

		if mockExecutor.Command != "claude" {
			t.Errorf("Expected command 'claude', got %q", mockExecutor.Command)
		}
		if len(mockExecutor.Args) != 4 {
			t.Fatalf("Expected 4 args, got %d", len(mockExecutor.Args))
		}
		if mockExecutor.Args[0] != "--print" {
			t.Errorf("Expected arg[0] '--print', got %q", mockExecutor.Args[0])
		}
		if mockExecutor.Args[1] != "--output-format" {
			t.Errorf("Expected arg[1] '--output-format', got %q", mockExecutor.Args[1])
		}
		if mockExecutor.Args[2] != "json" {
			t.Errorf("Expected arg[2] 'json', got %q", mockExecutor.Args[2])
		}
		if mockExecutor.Args[3] != "fix this bug" {
			t.Errorf("Expected arg[3] 'fix this bug', got %q", mockExecutor.Args[3])
		}
	})
}
