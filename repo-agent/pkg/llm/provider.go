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

	reviewv1alpha1 "github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/api/repowatch/v1alpha1"
)

const (
	// TODO(seans): Find a more appropriate location for this constant.
	RepoAgentSystemNamespace = "repo-agent-system"
)

// PostProcessor defines the signature for functions that can post-process the LLM's raw output.
type PostProcessor func([]byte) ([]byte, error)

// ProviderConfig holds the configuration for an LLM provider.
type ProviderConfig struct {
	Name                 string
	WorkspacesDir        string
	RepoDir              string
	TokensDir            string
	OutputStartIndicator string
	Extensions           []reviewv1alpha1.Extension
}

// Provider defines the interface for interacting with an LLM.
type Provider interface {
	Setup() error
	Cleanup() error
	ExpandPrompt(prompt string) (string, error)
	Run(prompt string) ([]byte, *Stats, error)
	// AddPostProcessor adds a post-processing function to the provider.
	// These functions are applied sequentially to the LLM's raw output.
	AddPostProcessor(p PostProcessor)
	QuotaCheck() bool
}

// Stats captures usage statistics from an LLM invocation.
type Stats struct {
	Models map[string]ModelUsage `json:"models,omitempty"`
}

// ModelUsage captures per-model usage statistics.
type ModelUsage struct {
	API    APIUsage   `json:"api"`
	Tokens TokenUsage `json:"tokens"`
}

// APIUsage captures API call statistics for a model.
type APIUsage struct {
	TotalRequests  int64 `json:"totalRequests"`
	TotalErrors    int64 `json:"totalErrors"`
	TotalLatencyMs int64 `json:"totalLatencyMs"`
}

// TokenUsage captures token consumption for a model.
type TokenUsage struct {
	Input    int64 `json:"input"`
	Output   int64 `json:"output"`
	Total    int64 `json:"total"`
	Cached   int64 `json:"cached"`
	Thoughts int64 `json:"thoughts"`
}

// QuotaError is returned by Run() when the LLM API returns an "Out of Quota" error.
type QuotaError struct {
	Err error
}

func (e *QuotaError) Error() string {
	return fmt.Sprintf("quota exceeded: %v", e.Err)
}

func (e *QuotaError) Unwrap() error {
	return e.Err
}

func NewLLMProvider(cfg ProviderConfig) (Provider, error) {
	switch cfg.Name {
	case "gemini-cli":
		g := &Gemini{
			Executor:       &RealCommandExecutor{},
			ProviderConfig: cfg,
		}
		g.AddPostProcessor(StripYAMLMarkers)
		if cfg.OutputStartIndicator != "" {
			g.AddPostProcessor(StripUnillStartIndicator(cfg.OutputStartIndicator))
			g.AddPostProcessor(StripIWillStatements())
		}
		return g, nil
	case "claude":
		c := &Claude{
			ProviderConfig: cfg,
		}
		c.AddPostProcessor(StripYAMLMarkers)
		return c, nil
	case "claude-cli":
		c := &ClaudeCLI{
			Executor:       &RealCommandExecutor{},
			ProviderConfig: cfg,
		}
		c.AddPostProcessor(StripYAMLMarkers)
		return c, nil
	case "dummy":
		d := &Dummy{
			ProviderConfig: cfg,
		}
		d.AddPostProcessor(StripYAMLMarkers)
		return d, nil
	default:
		return nil, fmt.Errorf("unknown provider: %s", cfg.Name)
	}
}

// StripYAMLMarkers looks for ```yaml and ``` markers in the input byte slice.
// If found, it strips these markers and returns the content between them.
// If markers are not found, the original byte slice is returned.
func StripYAMLMarkers(input []byte) ([]byte, error) {
	startMarker := []byte("```yaml")
	endMarker := []byte("```")

	startIndex := bytes.Index(input, startMarker)
	if startIndex == -1 {
		return input, nil // Start marker not found
	}

	// Adjust startIndex to point after the start marker
	startIndex += len(startMarker)

	endIndex := bytes.Index(input[startIndex:], endMarker)
	if endIndex == -1 {
		return input, nil // End marker not found after start marker
	}

	// Adjust endIndex to be relative to the original input slice
	endIndex += startIndex

	// Extract the content between the markers, trimming any leading/trailing whitespace
	return bytes.TrimSpace(input[startIndex:endIndex]), nil
}
