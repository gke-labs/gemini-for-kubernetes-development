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
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

// newClientsetFunc is a variable to allow mocking in tests.
var newClientsetFunc func(c *rest.Config) (kubernetes.Interface, error) = func(c *rest.Config) (kubernetes.Interface, error) {
	return kubernetes.NewForConfig(c)
}
var inClusterConfigFunc = rest.InClusterConfig

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
}

func (c *Claude) AddPostProcessor(p PostProcessor) {
	c.postProcessors = append(c.postProcessors, p)
}

func (c *Claude) Setup(_, _ string) error {
	// First, try to get the config from in-cluster config
	config, err := inClusterConfigFunc()
	if err != nil {
		// If that fails, fall back to checking the environment variable
		log.Printf("Not running in a Kubernetes cluster, checking for ANTHROPIC_API_KEY environment variable: %v", err)
		apiKey, ok := os.LookupEnv(AnthropicAPIKeyEnvVar)
		if !ok {
			return fmt.Errorf("%s environment variable not set and not in a Kubernetes cluster", AnthropicAPIKeyEnvVar)
		}
		c.apiKey = apiKey
		return nil
	}

	clientset, err := newClientsetFunc(config)
	if err != nil {
		return fmt.Errorf("failed to create clientset: %w", err)
	}

	secret, err := clientset.CoreV1().Secrets(RepoAgentSystemNamespace).Get(context.TODO(), AnthropicAPIKeySecretName, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("failed to get secret %s: %w", AnthropicAPIKeySecretName, err)
	}

	apiKey, ok := secret.Data[AnthropicAPIKeySecretKey]
	if !ok {
		return fmt.Errorf("secret %s does not contain key '%s'", AnthropicAPIKeySecretName, AnthropicAPIKeySecretKey)
	}

	c.apiKey = string(apiKey)
	return nil
}

func (c *Claude) Cleanup(_ string) error {
	return nil
}

func (c *Claude) ExpandPrompt(prompt string) (string, error) {
	return prompt, nil
}

func (c *Claude) Run(prompt string) ([]byte, error) {
	log.Printf("Claude provider called with prompt: %s", prompt)

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
		return nil, fmt.Errorf("failed to marshal request body: %w", err)
	}

	url := c.URL
	if url == "" {
		url = defaultClaudeAPIURL
	}
	req, err := http.NewRequest("POST", url, bytes.NewBuffer(requestBody))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
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
		return nil, fmt.Errorf("failed to make request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response body: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		log.Printf("Claude API request failed with status %d: %s", resp.StatusCode, string(body))
		return nil, fmt.Errorf("request failed with status %d: %s", resp.StatusCode, string(body))
	}

	var response struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(body, &response); err != nil {
		return nil, fmt.Errorf("failed to unmarshal response body: %w", err)
	}

	if len(response.Content) == 0 {
		return nil, fmt.Errorf("no content in response")
	}

	output := []byte(response.Content[0].Text)
	for _, p := range c.postProcessors {
		output, err = p(output)
		if err != nil {
			return nil, fmt.Errorf("failed to apply post-processor: %w", err)
		}
	}

	return output, nil
}
