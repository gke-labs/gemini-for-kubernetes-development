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

package main

import (
	"fmt"
	"log"
	"os"
	"path/filepath"

	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/llm"
)

// This binary tests the Claude LLM provider implementation found in `pkg/llm/claude.go`.
// It demonstrates how the Claude LLM is set up, including securely handling API keys via
// temporary files to validate the primary Setup function path.
// It takes an optional prompt as a command-line argument, sends it to the
// Claude LLM provider, and prints the response.
func main() {
	// Initialize the Claude LLM client. The implementation for Claude LLM provider
	// is located at `pkg/llm/claude.go`.
	claude := &llm.Claude{}

	// --- Secure API Key Handling for Testing Primary Setup Path ---
	// The Setup function in `pkg/llm/claude.go` prioritizes reading the API key from a file.
	// To test this primary path securely without hardcoding or exposing the API key,
	// we create a temporary directory and a temporary file within it.
	// The API key is read from the ANTHROPIC_API_KEY environment variable (securely provided
	// to this test execution environment) and then written into this temporary file.
	// The path to this temporary directory is then passed to the claude.Setup function.
	// The temporary directory and its contents are deleted when the main function exits.
	tempDir, err := os.MkdirTemp("", "claude-test-")
	if err != nil {
		log.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir) // Clean up the temporary directory on exit

	// Retrieve the API key from environment variable
	apiKey := os.Getenv("ANTHROPIC_API_KEY")
	if apiKey == "" {
		log.Fatal("ANTHROPIC_API_KEY environment variable not set")
	}
	// Write the API key to a file named 'claude' within the temporary directory.
	// The filename 'claude' is what the Setup function expects by default.
	if err := os.WriteFile(filepath.Join(tempDir, "claude"), []byte(apiKey), 0600); err != nil {
		log.Fatalf("failed to write api key file: %v", err)
	}

	// Call the Setup function, passing the temporary directory as the tokens directory.
	// This ensures the file-based API key retrieval path is tested.
	if err := claude.Setup("", tempDir); err != nil {
		log.Fatalf("failed to setup claude: %v", err)
	}

	prompt := "What is the capital of France?"
	if len(os.Args) > 1 {
		prompt = os.Args[1]
	}

	fmt.Printf("Sending prompt to Claude: %q\n", prompt)

	resp, err := claude.Run(prompt)
	if err != nil {
		log.Fatalf("failed to run claude: %v", err)
	}

	fmt.Printf("Response from Claude: %s\n", string(resp))
}
