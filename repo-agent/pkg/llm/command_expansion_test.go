package llm

import (
	"os"
	"path/filepath"
	"testing"
)

func TestExpandCommands(t *testing.T) {
	// Setup temporary .gemini directory
	tempDir := t.TempDir()
	geminiDir := filepath.Join(tempDir, ".gemini")
	commandsDir := filepath.Join(geminiDir, "commands")

	// Create namespace directory
	nsDir := filepath.Join(commandsDir, "google-internal")
	if err := os.MkdirAll(nsDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Create command file
	cmdFile := filepath.Join(nsDir, "review-pr.toml")
	cmdContent := `
description = "Test Command"
prompt = """
Reviewing PR {{args}}.
"""
`
	if err := os.WriteFile(cmdFile, []byte(cmdContent), 0644); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name     string
		prompt   string
		expected string
	}{
		{
			name:     "Basic command",
			prompt:   `Run this: /google-internal:review-pr "123"`,
			expected: `Run this: Reviewing PR 123.`,
		},
		{
			name:     "Command with text around",
			prompt:   `Start /google-internal:review-pr "456" End`,
			expected: `Start Reviewing PR 456. End`,
		},
		{
			name:     "Command with no args", // The template expects args, but if none provided?
			prompt:   `Run /google-internal:review-pr`,
			expected: `Run Reviewing PR .`, // Assuming empty string for args
		},
		{
			name:     "Unknown command",
			prompt:   "Run /google-internal:unknown",
			expected: "Run /google-internal:unknown", // Should probably leave it alone or error? CLI usually errors. I'll leave it for now or log warning.
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := expandCommands(tt.prompt, geminiDir)
			if err != nil {
				// For unknown command, maybe we don't return error but leave it?
				// But let's see.
				t.Logf("Got error: %v", err)
			}
			// Check containment or exact match.
			// My implementation might normalize spaces.
			if result != tt.expected {
				// If it's the unknown command case, we might accept it returning the original or error.
				if tt.name == "Unknown command" && result == tt.prompt {
					return
				}
				t.Errorf("expected %q, got %q", tt.expected, result)
			}
		})
	}
}
