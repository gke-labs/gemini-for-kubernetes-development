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
	"path/filepath"

	"k8s.io/klog/v2"
)

// ClaudeCLI is an Provider that uses the claude CLI (@anthropic-ai/claude-code).
var _ Provider = &ClaudeCLI{}

type ClaudeCLI struct {
	Executor   CommandExecutor
	processors []PostProcessor
	ProviderConfig
}

type ClaudeJSONOutput struct {
	Result     string                      `json:"result"`
	ModelUsage map[string]ClaudeModelUsage `json:"modelUsage"`
}

type ClaudeModelUsage struct {
	InputTokens              int64 `json:"inputTokens"`
	OutputTokens             int64 `json:"outputTokens"`
	CacheReadInputTokens     int64 `json:"cacheReadInputTokens"`
	CacheCreationInputTokens int64 `json:"cacheCreationInputTokens"`
}

func (c *ClaudeCLI) AddPostProcessor(p PostProcessor) {
	c.processors = append(c.processors, p)
}

func (c *ClaudeCLI) QuotaCheck() bool {
	return true
}

func (c *ClaudeCLI) Setup() error {
	// 1. Setup API Key from /tokens/claude
	apiKeyPath := filepath.Join(c.TokensDir, "claude")
	apiKeyBytes, err := os.ReadFile(apiKeyPath)
	if err != nil {
		klog.Infof("Warning: failed to read API key from %s: %v. Falling back to env.", apiKeyPath, err)
	} else {
		os.Setenv("ANTHROPIC_API_KEY", string(apiKeyBytes))
	}

	// 2. TODO: Handle MCP servers configuration in Phase 3
	// 3. Ensure any one-time consent/config for claude-code is handled if needed.

	return nil
}

func (c *ClaudeCLI) Cleanup() error {
	return nil
}

func (c *ClaudeCLI) ExpandPrompt(prompt string) (string, error) {
	// For now, we don't have a specific command expansion for claude-cli,
	// but we could use the same logic as gemini if needed.
	return prompt, nil
}

func (c *ClaudeCLI) Run(agentPrompt string) ([]byte, *Stats, error) {
	klog.Info("running claude-cli")

	// Execute claude --print --output-format json "prompt"
	stdout, stderr, err := c.Executor.Run("claude", "--print", "--output-format", "json", agentPrompt)

	if err != nil {
		klog.Infof("claude command failed: %v. Stderr: %s", err, string(stderr))
		return nil, nil, fmt.Errorf("claude command failed: %w. Stderr: %s", err, string(stderr))
	}

	// Parse the JSON envelope
	var envelope ClaudeJSONOutput
	// Find first '{' to skip any non-JSON prefix warnings
	idx := bytes.IndexByte(stdout, '{')
	if idx == -1 {
		return nil, nil, fmt.Errorf("claude --output-format json returned no JSON object")
	}
	if err := json.Unmarshal(stdout[idx:], &envelope); err != nil {
		return nil, nil, fmt.Errorf("failed to parse claude JSON output: %w", err)
	}

	output := []byte(envelope.Result)
	for _, p := range c.processors {
		output, err = p(output)
		if err != nil {
			return nil, nil, err
		}
	}

	// Convert stats
	usage := &Stats{
		Models: make(map[string]ModelUsage),
	}
	for model, data := range envelope.ModelUsage {
		usage.Models[model] = ModelUsage{
			Tokens: TokenUsage{
				Input:  data.InputTokens,
				Output: data.OutputTokens,
				Cached: data.CacheReadInputTokens,
				Total:  data.InputTokens + data.OutputTokens,
			},
		}
	}

	return output, usage, nil
}
