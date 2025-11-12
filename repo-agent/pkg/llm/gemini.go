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
	Executor   CommandExecutor
	processors []PostProcessor
}

func (g *Gemini) AddPostProcessor(p PostProcessor) {
	g.processors = append(g.processors, p)
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

	for _, p := range g.processors {
		output, err = p(output)
		if err != nil {
			return nil, err
		}
	}

	return output, nil
}
