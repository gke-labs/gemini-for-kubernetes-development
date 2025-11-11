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

import "fmt"

// PostProcessor defines the signature for functions that can post-process the LLM's raw output.
type PostProcessor func([]byte) ([]byte, error)

// Provider defines the interface for interacting with an LLM.
type Provider interface {
	Setup(workspacesDir, tokensDir string) error
	Run(prompt string) ([]byte, error)
	// AddPostProcessor adds a post-processing function to the provider.
	// These functions are applied sequentially to the LLM's raw output.
	AddPostProcessor(p PostProcessor)
}

func NewLLMProvider(name string) (Provider, error) {
	switch name {
	case "gemini-cli":
		g := &Gemini{Executor: &RealCommandExecutor{}}
		g.AddPostProcessor(stripYAMLMarkers)
		return g, nil
	default:
		return nil, fmt.Errorf("unknown provider: %s", name)
	}
}
