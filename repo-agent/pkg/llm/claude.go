package llm

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
)

const (
	defaultClaudeModel     = "claude-sonnet-4-5"
	defaultClaudeMaxTokens = 4096
	defaultClaudeAPIURL    = "https://api.anthropic.com/v1/messages"
	anthropicAPIVersion    = "2023-06-01"
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
	apiKey, ok := os.LookupEnv("ANTHROPIC_API_KEY")
	if !ok {
		return fmt.Errorf("ANTHROPIC_API_KEY environment variable not set")
	}
	c.apiKey = apiKey
	return nil
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
