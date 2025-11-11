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
	"log"
	"os"
	"os/exec"
	"path/filepath"
)

// Gemini is an Provider that uses the gemini-cli.
//
// Make sure that the Gemini struct implements the Provider interface.
var _ Provider = &Gemini{}

type Gemini struct {
	Executor CommandExecutor
}

func (g *Gemini) Setup(workspacesDir, tokensDir string) error {
	// if .gemini directory exists in /workspaces copy it to home directory
	geminiConfigDir := filepath.Join(workspacesDir, ".gemini")
	if _, err := os.Stat(geminiConfigDir); err == nil {
		log.Println(".gemini directory exists in /workspaces, copying to repo directory")
		// if desitation .gemini directory exists move it to .gemini.bak
		if _, err := os.Stat(".gemini"); err == nil {
			log.Println(".gemini directory exists in repo directory, moving to .gemini.bak")
			err := os.Rename(".gemini", ".gemini.bak")
			if err != nil {
				return fmt.Errorf("failed to move .gemini to .gemini.bak: %v", err)
			}
		}
		cmd := exec.Command("cp", "-R", geminiConfigDir, ".gemini")
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("failed to copy .gemini directory: %v", err)
		}
	} else {
		log.Println(".gemini directory does not exist in /workspaces")
	}

	geminiTokenFile := filepath.Join(tokensDir, "gemini")
	geminiKey, err := os.ReadFile(geminiTokenFile)
	if err != nil {
		return fmt.Errorf("failed to read %s: %v", geminiTokenFile, err)
	}
	os.Setenv("GEMINI_API_KEY", string(geminiKey))
	return nil
}

func (g *Gemini) Run(agentPrompt string) ([]byte, error) {
	log.Println("running gemini")

	output, err := g.Executor.Run("gemini", "-y", "-p", agentPrompt)
	if err != nil {
		log.Printf("gemini command failed: %v. Output: %s", err, string(output))
		return nil, err
	}

	return stripYAMLMarkers(output), nil
}

// stripYAMLMarkers looks for ```yaml and ``` markers in the input byte slice.
// If found, it strips these markers and returns the content between them.
// If markers are not found, the original byte slice is returned.
func stripYAMLMarkers(input []byte) []byte {
	startMarker := []byte("```yaml")
	endMarker := []byte("```")

	startIndex := bytes.Index(input, startMarker)
	if startIndex == -1 {
		return input // Start marker not found
	}

	// Adjust startIndex to point after the start marker
	startIndex += len(startMarker)

	endIndex := bytes.Index(input[startIndex:], endMarker)
	if endIndex == -1 {
		return input // End marker not found after start marker
	}

	// Adjust endIndex to be relative to the original input slice
	endIndex += startIndex

	// Extract the content between the markers, trimming any leading/trailing whitespace
	return bytes.TrimSpace(input[startIndex:endIndex])
}
