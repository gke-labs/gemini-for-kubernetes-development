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
	"io"
	"net/http"
	"os"
	"path/filepath"

	"k8s.io/klog/v2"
)

const (
	defaultClaudeModel     = "claude-sonnet-4-5"
	defaultClaudeMaxTokens = 4096
	defaultClaudeAPIURL    = "https://api.anthropic.com/v1/messages"
	anthropicAPIVersion    = "2023-06-01"

	// AnthropicAPIKeySecretName is the name of the secret containing the Anthropic API key.
	AnthropicAPIKeySecretName = "anthropic-api-key"

	// AnthropicAPIKeySecretKey is the key in the secret containing the Anthropic API key.
	AnthropicAPIKeySecretKey = "claude"

	// AnthropicAPIKeyEnvVar is the environment variable name for the Anthropic API key.
	AnthropicAPIKeyEnvVar = "ANTHROPIC_API_KEY"
)

type HTTPClient interface {
	Do(req *http.Request) (*http.Response, error)
}

type Claude struct {
	apiKey         string
	client         HTTPClient
	postProcessors []PostProcessor
	URL            string
	ProviderConfig
}

func (c *Claude) AddPostProcessor(p PostProcessor) {
	c.postProcessors = append(c.postProcessors, p)
}

func (c *Claude) QuotaCheck() bool {
	return true
}

func (c *Claude) Setup() error {
	tokensDir := c.TokensDir
	// Read API key from the mounted secret file
	apiKeyPath := filepath.Join(tokensDir, AnthropicAPIKeySecretKey)
	apiKeyBytes, err := os.ReadFile(apiKeyPath)
	if err != nil {
		// If reading from file fails, fall back to checking the environment variable
		klog.Infof("Failed to read API key from %s: %v", apiKeyPath, err)
		apiKey, ok := os.LookupEnv(AnthropicAPIKeyEnvVar)
		if !ok {
			return fmt.Errorf("API key not found in %s or %s environment variable", apiKeyPath, AnthropicAPIKeyEnvVar)
		}
		c.apiKey = apiKey
		return nil
	}
	c.apiKey = string(bytes.TrimSpace(apiKeyBytes))
	return nil
}

func (c *Claude) Cleanup() error {
	return nil
}

func (c *Claude) ExpandPrompt(prompt string) (string, error) {
	return prompt, nil
}

func (c *Claude) Run(prompt string) ([]byte, *Stats, error) {
	klog.Infof("Claude provider called with prompt: %s", prompt)

	requestBody, err := json.Marshal(map[string]interface{}{
		"model":      defaultClaudeModel,
		"max_tokens": defaultClaudeMaxTokens,
		"messages": []map[string]string{
			{
				"role":    "user",
				"content": prompt,
			},
		},
	})
	if err != nil {
		return nil, nil, fmt.Errorf("failed to marshal request body: %w", err)
	}

	url := c.URL
	if url == "" {
		url = defaultClaudeAPIURL
	}
	req, err := http.NewRequest("POST", url, bytes.NewBuffer(requestBody))
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", c.apiKey)
	req.Header.Set("anthropic-version", anthropicAPIVersion)

	client := c.client
	if client == nil {
		client = &http.Client{}
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to make request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to read response body: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		klog.Infof("Claude API request failed with status %d: %s", resp.StatusCode, string(body))
		return nil, nil, fmt.Errorf("request failed with status %d: %s", resp.StatusCode, string(body))
	}

	var response struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
		Usage struct {
			InputTokens  int64 `json:"input_tokens"`
			OutputTokens int64 `json:"output_tokens"`
		} `json:"usage"`
		Model string `json:"model"`
	}
	if err := json.Unmarshal(body, &response); err != nil {
		return nil, nil, fmt.Errorf("failed to unmarshal response body: %w", err)
	}

	if len(response.Content) == 0 {
		return nil, nil, fmt.Errorf("no content in response")
	}

	output := []byte(response.Content[0].Text)
	for _, p := range c.postProcessors {
		output, err = p(output)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to apply post-processor: %w", err)
		}
	}

	// Extract usage from Claude's response
	model := response.Model
	if model == "" {
		model = defaultClaudeModel
	}
	usage := &Stats{
		Models: map[string]ModelUsage{
			model: {
				API: APIUsage{TotalRequests: 1},
				Tokens: TokenUsage{
					Input:  response.Usage.InputTokens,
					Output: response.Usage.OutputTokens,
					Total:  response.Usage.InputTokens + response.Usage.OutputTokens,
				},
			},
		},
	}

	return output, usage, nil
}
