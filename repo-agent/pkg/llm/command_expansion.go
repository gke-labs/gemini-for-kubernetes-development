// Copyright 2026 The Kubernetes Authors.
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
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Regex for finding commands: /namespace:command "args" or /namespace:command args
// We'll support quoted args with simple quote handling.
var commandRegex = regexp.MustCompile(`(?m)/([\w-]+):([\w-]+)(?:\s+(".*?"|[^\s]+))?`)

func expandCommands(prompt string, geminiDir string) (string, error) {
	return replaceCommands(prompt, geminiDir)
}

func replaceCommands(text string, geminiDir string) (string, error) {
	// We iterate through all matches and replace them.
	// Since we might have nested commands (unlikely but possible if a command prompt contains another command?),
	// we'll just do one pass for now.

	var errs []error

	// FindAllStringSubmatchIndex is better if we want to replace ranges, but ReplaceAllStringFunc is easier.
	result := commandRegex.ReplaceAllStringFunc(text, func(match string) string {
		submatches := commandRegex.FindStringSubmatch(match)
		namespace := submatches[1]
		command := submatches[2]
		args := ""
		if len(submatches) > 3 {
			args = submatches[3]
		}

		// Unquote args if quoted
		if strings.HasPrefix(args, "\"") && strings.HasSuffix(args, "\"") {
			args = args[1 : len(args)-1]
			// TODO: handle escaped quotes if necessary
		}

		// Look for command file
		cmdPath := filepath.Join(geminiDir, "commands", namespace, command+".toml")
		if _, err := os.Stat(cmdPath); os.IsNotExist(err) {
			// Try .json? Or maybe the user didn't define it.
			// If not found, we return the original match (maybe it's not a command).
			return match
		}

		content, err := os.ReadFile(cmdPath)
		if err != nil {
			errs = append(errs, fmt.Errorf("failed to read command file %s: %v", cmdPath, err))
			return match
		}

		promptTemplate, err := extractPrompt(content, filepath.Ext(cmdPath))
		if err != nil {
			errs = append(errs, fmt.Errorf("failed to extract prompt from %s: %v", cmdPath, err))
			return match
		}

		promptTemplate = strings.TrimSpace(promptTemplate)

		// Substitute {{args}}
		replacement := strings.ReplaceAll(promptTemplate, "{{args}}", args)
		return replacement
	})

	if len(errs) > 0 {
		return result, fmt.Errorf("errors expanding commands: %v", errs)
	}
	return result, nil
}

func extractPrompt(content []byte, ext string) (string, error) {
	s := string(content)
	if ext == ".toml" {
		// Regex for prompt = """...""" (multiline)
		// We use (?s) to let . match newlines
		reMultiline := regexp.MustCompile(`(?s)prompt\s*=\s*"""(.*?)"""`)
		m := reMultiline.FindStringSubmatch(s)
		if len(m) > 1 {
			return m[1], nil
		}

		// Regex for prompt = "..." (single line)
		reSingle := regexp.MustCompile(`prompt\s*=\s*"(.*?)"`)
		m2 := reSingle.FindStringSubmatch(s)
		if len(m2) > 1 {
			return m2[1], nil
		}
		return "", fmt.Errorf("prompt field not found in TOML")
	}
	// Add JSON support if needed
	return "", fmt.Errorf("unsupported command file extension: %s", ext)
}
