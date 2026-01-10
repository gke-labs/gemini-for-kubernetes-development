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
	"os"
	"os/exec"
	"path/filepath"

	"k8s.io/klog/v2"
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
		klog.Info(".gemini directory exists in /workspaces, copying to repo directory")
		// if desitation .gemini directory exists move it to .gemini.bak
		if _, err := os.Stat(".gemini"); err == nil {
			klog.Info(".gemini directory exists in repo directory, moving to .gemini.bak")
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
		klog.Info(".gemini directory does not exist in /workspaces")
	}

	// Ensure root settings.json has previewFeatures
	if err := ensureSettings(".gemini"); err != nil {
		klog.Infof("Warning: failed to ensure .gemini/settings.json: %v", err)
	}

	// Ensure home directory settings.json has previewFeatures
	homeDir, err := os.UserHomeDir()
	if err == nil {
		if err := ensureSettings(filepath.Join(homeDir, ".gemini")); err != nil {
			klog.Infof("Warning: failed to ensure ~/.gemini/settings.json: %v", err)
		}
	}

	geminiTokenFile := filepath.Join(tokensDir, "gemini")
	geminiKey, err := os.ReadFile(geminiTokenFile)
	if err != nil {
		return fmt.Errorf("failed to read %s: %v", geminiTokenFile, err)
	}
	os.Setenv("GEMINI_API_KEY", string(geminiKey))
	return nil
}

func ensureSettings(geminiDir string) error {
	settingsPath := filepath.Join(geminiDir, "settings.json")
	if err := os.MkdirAll(geminiDir, 0755); err != nil {
		return fmt.Errorf("failed to create .gemini directory: %v", err)
	}

	var settings map[string]interface{}
	if _, err := os.Stat(settingsPath); err == nil {
		data, err := os.ReadFile(settingsPath)
		if err == nil {
			if err := json.Unmarshal(data, &settings); err != nil {
				klog.Infof("Warning: failed to unmarshal existing settings.json: %v", err)
			}
		}
	}

	if settings == nil {
		settings = make(map[string]interface{})
	}

	general, ok := settings["general"].(map[string]interface{})
	if !ok {
		// handle case where "general" is not a map
		general = make(map[string]interface{})
		settings["general"] = general
	}
	general["previewFeatures"] = true
	if _, ok := settings["model"]; !ok {
		// if model is unset, set it to gemini-3-pro-preview
		settings["model"] = "gemini-3-pro-preview"
	}

	data, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal settings: %v", err)
	}

	if err := os.WriteFile(settingsPath, data, 0644); err != nil {
		return fmt.Errorf("failed to write settings.json: %v", err)
	}
	return nil
}

func (g *Gemini) Cleanup(workspacesDir string) error {
	geminiBackupDir := filepath.Join(workspacesDir, ".gemini.bak")
	if _, err := os.Stat(geminiBackupDir); err == nil {
		klog.Info("moving .gemini.bak -> .gemini")
		if err := os.RemoveAll(geminiBackupDir); err != nil {
			klog.Infof("failed to remove .gemini directory: %v", err)
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
	klog.Info("running gemini")

	output, err := g.Executor.Run("gemini", "-y", "-p", agentPrompt)
	if err != nil {
		klog.Infof("gemini command failed: %v. Output: %s", err, string(output))
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
