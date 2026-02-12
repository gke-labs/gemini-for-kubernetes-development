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
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"k8s.io/klog/v2"
)

// Gemini is an Provider that uses the gemini-cli.
//
// Make sure that the Gemini struct implements the Provider interface.
var _ Provider = &Gemini{}

type Gemini struct {
	Executor   CommandExecutor
	processors []PostProcessor
	ProviderConfig
}

func (g *Gemini) AddPostProcessor(p PostProcessor) {
	g.processors = append(g.processors, p)
}

func (g *Gemini) QuotaCheck() bool {
	return true
}

func (g *Gemini) Setup() error {
	// if .gemini directory exists in /workspaces copy it to repo directory
	// We copy the .gemini folder from the workspace root (if it exists) to the repo directory
	// to make sure the agent running in the repo context has access to the user's configuration.
	// If a .gemini folder already exists in the repo, we back it up to .gemini.bak to restore it later.
	wsGeminiConfigDir := filepath.Join(g.WorkspacesDir, ".gemini")
	repoGeminiConfigDir := filepath.Join(g.RepoDir, ".gemini")
	backupGeminiConfigDir := filepath.Join(g.WorkspacesDir, ".gemini.bak")
	if _, err := os.Stat(wsGeminiConfigDir); err == nil {
		klog.Info(".gemini directory exists in /workspaces, copying to repo directory")
		// if desitation .gemini directory exists move it to .gemini.bak
		if _, err := os.Stat(repoGeminiConfigDir); err == nil {
			klog.Info(".gemini directory exists in repo directory, moving to .gemini.bak")
			err := os.Rename(repoGeminiConfigDir, backupGeminiConfigDir)
			if err != nil {
				return fmt.Errorf("failed to move .gemini to .gemini.bak: %v", err)
			}
		}
		cmd := exec.Command("cp", "-R", wsGeminiConfigDir, repoGeminiConfigDir)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("failed to copy .gemini directory: %v", err)
		}
	} else {
		klog.Info(".gemini directory does not exist in /workspaces")
	}

	// Ensure root settings.json has previewFeatures
	// We force previewFeatures to true to enable experimental capabilities required by the agent.
	if err := ensureSettings(repoGeminiConfigDir); err != nil {
		klog.Infof("Warning: failed to ensure .gemini/settings.json: %v", err)
	}
	// Ensure home directory settings.json has previewFeatures
	homeDir, err := os.UserHomeDir()
	if err == nil {
		if err := ensureSettings(filepath.Join(homeDir, ".gemini")); err != nil {
			klog.Infof("Warning: failed to ensure ~/.gemini/settings.json: %v", err)
		}
	}

	geminiTokenFile := filepath.Join(g.TokensDir, "gemini")
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
	if model, ok := settings["model"]; !ok {
		// if model is unset, set it to gemini-3-pro-preview
		settings["model"] = map[string]interface{}{
			"name": "gemini-3-pro-preview",
		}
	} else if modelStr, ok := model.(string); ok {
		// if model is a string, convert it to an object
		settings["model"] = map[string]interface{}{
			"name": modelStr,
		}
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

func (g *Gemini) Cleanup() error {
	repoGeminiConfigDir := filepath.Join(g.RepoDir, ".gemini")
	backupGeminiConfigDir := filepath.Join(g.WorkspacesDir, ".gemini.bak")
	if _, err := os.Stat(backupGeminiConfigDir); err == nil {
		klog.Info("moving .gemini.bak -> .gemini")
		if err := os.RemoveAll(repoGeminiConfigDir); err != nil {
			klog.Infof("failed to remove .gemini directory: %v", err)
		}
		if err := os.Rename(backupGeminiConfigDir, repoGeminiConfigDir); err != nil {
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

	stdout, stderr, err := g.Executor.Run("gemini", "-y", "-p", agentPrompt)
	if err != nil {
		klog.Infof("gemini command failed: %v. Stderr: %s", err, string(stderr))
		if strings.Contains(string(stderr), "[API Error: You have exhausted your daily quota on this model.]") ||
			strings.Contains(string(stderr), "429") {
			// Fallback to gemini-3-flash-preview
			klog.Info("Quota exhausted, retrying with gemini-3-flash-preview")
			stdout, stderr, err = g.Executor.Run("gemini", "-y", "--model", "gemini-3-flash-preview", "-p", agentPrompt)
			if err != nil {
				klog.Infof("gemini command failed (fallback): %v. Stderr: %s", err, string(stderr))
				if strings.Contains(string(stderr), "[API Error: You have exhausted your daily quota on this model.]") ||
					strings.Contains(string(stderr), "429") {
					return nil, &QuotaError{Err: err}
				}
				return nil, err
			}
		} else {
			return nil, err
		}
	}

	output := stdout
	for _, p := range g.processors {
		output, err = p(output)
		if err != nil {
			return nil, err
		}
	}

	return output, nil
}

func StripUnillStartIndicator(outputStartIndicator string) PostProcessor {
	return func(input []byte) ([]byte, error) {
		if outputStartIndicator == "" {
			return input, nil
		}
		indicator := []byte(outputStartIndicator)
		// Check for indicator at the beginning
		if bytes.HasPrefix(input, indicator) {
			return input, nil
		}

		// Check for "\n" + indicator (indicator at the beginning of a line)
		search := append([]byte("\n"), indicator...)
		if idx := bytes.Index(input, search); idx != -1 {
			return input[idx+1:], nil
		}

		return input, nil
	}
}

func StripIWillStatements() PostProcessor {
	return func(input []byte) ([]byte, error) {
		lines := bytes.SplitAfter(input, []byte("\n"))
		var start int
		foundContent := false
		for i, line := range lines {
			trimmed := bytes.TrimSpace(line)
			if len(trimmed) == 0 {
				continue
			}
			if bytes.HasPrefix(trimmed, []byte("I will")) {
				continue
			}
			start = i
			foundContent = true
			break
		}
		if !foundContent {
			return []byte{}, nil
		}
		return bytes.Join(lines[start:], nil), nil
	}
}
