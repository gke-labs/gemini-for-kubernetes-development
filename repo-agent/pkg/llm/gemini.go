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
	"encoding/json"
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

	// Ensure .gemini directory exists
	if _, err := os.Stat(".gemini"); os.IsNotExist(err) {
		if err := os.Mkdir(".gemini", 0755); err != nil {
			return fmt.Errorf("failed to create .gemini directory: %v", err)
		}
	}

	settingsPath := filepath.Join(".gemini", "settings.json")
	var settings map[string]interface{}
	if _, err := os.Stat(settingsPath); err == nil {
		content, err := os.ReadFile(settingsPath)
		if err != nil {
			return fmt.Errorf("failed to read settings.json: %v", err)
		}
		if err := json.Unmarshal(content, &settings); err != nil {
			log.Printf("failed to unmarshal settings.json, starting fresh: %v", err)
			settings = make(map[string]interface{})
		}
	} else {
		settings = make(map[string]interface{})
	}

	if settings["general"] == nil {
		settings["general"] = make(map[string]interface{})
	}

	if general, ok := settings["general"].(map[string]interface{}); ok {
		general["previewFeatures"] = true
		settings["general"] = general
	} else {
		// Fallback if general is not a map (unexpected)
		settings["general"] = map[string]interface{}{"previewFeatures": true}
	}

	newContent, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal settings.json: %v", err)
	}

	if err := os.WriteFile(settingsPath, newContent, 0644); err != nil {
		return fmt.Errorf("failed to write settings.json: %v", err)
	}

	geminiTokenFile := filepath.Join(tokensDir, "gemini")
	geminiKey, err := os.ReadFile(geminiTokenFile)
	if err != nil {
		return fmt.Errorf("failed to read %s: %v", geminiTokenFile, err)
	}
	os.Setenv("GEMINI_API_KEY", string(geminiKey))
	return nil
}

func (g *Gemini) Cleanup(workspacesDir string) error {
	geminiBackupDir := filepath.Join(workspacesDir, ".gemini.bak")
	if _, err := os.Stat(geminiBackupDir); err == nil {
		log.Println("moving .gemini.bak -> .gemini")
		if err := os.RemoveAll(geminiBackupDir); err != nil {
			log.Printf("failed to remove .gemini directory: %v", err)
		}
		if err := os.Rename(".gemini.bak", ".gemini"); err != nil {
			return fmt.Errorf("failed to move .gemini.bak to .gemini: %w", err)
		}
	}
	return nil
}

func (g *Gemini) ExpandPrompt(prompt string) (string, error) {
	return expandCommands(prompt, ".gemini")
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
