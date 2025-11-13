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

	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/llm"
)

// claude-test is a simple binary to test the Claude LLM provider.
// It takes an optional prompt as a command-line argument, sends it to the
// Claude LLM provider, and prints the response.
func main() {
	claude := &llm.Claude{}

	if err := claude.Setup("", ""); err != nil {
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
