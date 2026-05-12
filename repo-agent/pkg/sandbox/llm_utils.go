/*
Copyright 2026 The Gemini Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package sandbox

import (
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/k8s"
)

// buildLLMEnvVars returns the standard environment variables for LLM API keys.
func buildLLMEnvVars(opt DevSandboxOptions) []interface{} {
	var env []interface{}

	if opt.LLMAPIKey != "" {
		env = append(env, map[string]interface{}{
			"name":  "GEMINI_API_KEY",
			"value": opt.LLMAPIKey,
		})
	} else {
		env = append(env, map[string]interface{}{
			"name": "GEMINI_API_KEY",
			"valueFrom": map[string]interface{}{
				"secretKeyRef": map[string]interface{}{
					"name":     k8s.GeminiSecretName,
					"key":      "gemini",
					"optional": true,
				},
			},
		})
	}

	env = append(env, map[string]interface{}{
		"name": "ANTHROPIC_API_KEY",
		"valueFrom": map[string]interface{}{
			"secretKeyRef": map[string]interface{}{
				"name":     k8s.ClaudeSecretName,
				"key":      "claude",
				"optional": true,
			},
		},
	})

	return env
}

// buildLLMVolumeSources returns the projected volume sources for LLM API keys.
func buildLLMVolumeSources(opt DevSandboxOptions) []interface{} {
	var sources []interface{}
	if opt.LLMAPIKeySecretName != "" {
		sources = append(sources, map[string]interface{}{
			"secret": map[string]interface{}{
				"name": opt.LLMAPIKeySecretName,
			},
		})
	}
	// Ensure default gemini and claude secrets are also mounted if they are different from LLMAPIKeySecretName
	if opt.LLMAPIKeySecretName != k8s.GeminiSecretName {
		sources = append(sources, map[string]interface{}{
			"secret": map[string]interface{}{
				"name":     k8s.GeminiSecretName,
				"optional": true,
			},
		})
	}
	if opt.LLMAPIKeySecretName != k8s.ClaudeSecretName {
		sources = append(sources, map[string]interface{}{
			"secret": map[string]interface{}{
				"name":     k8s.ClaudeSecretName,
				"optional": true,
			},
		})
	}
	return sources
}
