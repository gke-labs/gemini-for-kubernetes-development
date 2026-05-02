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

package sandbox

import (
	"os"
	"testing"
)

func TestNewAgentSandbox_Defaulting(t *testing.T) {
	// Set env vars
	os.Setenv("REPO_SANDBOX_IMAGE", "env-repo-sandbox-image")
	os.Setenv("CONFIGDIR_CLI_IMAGE", "env-configdir-cli-image")
	defer func() {
		os.Unsetenv("REPO_SANDBOX_IMAGE")
		os.Unsetenv("CONFIGDIR_CLI_IMAGE")
	}()

	tests := []struct {
		name                string
		optRepoSandboxImage string
		optConfigDirImage   string
		expectedRepoSandbox string
		expectedConfigDir   string
	}{
		{
			name:                "DefaultFromEnv",
			optRepoSandboxImage: "",
			optConfigDirImage:   "",
			expectedRepoSandbox: "env-repo-sandbox-image",
			expectedConfigDir:   "env-configdir-cli-image",
		},
		{
			name:                "OverrideEnv",
			optRepoSandboxImage: "override-repo-image",
			optConfigDirImage:   "override-config-image",
			expectedRepoSandbox: "override-repo-image",
			expectedConfigDir:   "override-config-image",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			opt := AgentSandboxOptions{
				DevSandboxOptions: DevSandboxOptions{
					Name:             "test",
					Namespace:        "default",
					RepoSandboxImage: tt.optRepoSandboxImage,
					ConfigDirImage:   tt.optConfigDirImage,
					LLMConfigdirRef:  "some-ref", // To trigger init container check
				},
			}
			sandboxObj, _ := NewAgentSandbox(opt)

			spec := sandboxObj.Object["spec"].(map[string]interface{})
			podTemplate := spec["podTemplate"].(map[string]interface{})
			podSpec := podTemplate["spec"].(map[string]interface{})
			initContainers := podSpec["initContainers"].([]interface{})

			// Check gemini-configs init container
			foundConfigDir := false
			foundRepoSandbox := false
			for _, ic := range initContainers {
				icMap := ic.(map[string]interface{})
				if icMap["name"] == "gemini-configs" {
					foundConfigDir = true
					if icMap["image"] != tt.expectedConfigDir {
						t.Errorf("expected configdir image %s, got %s", tt.expectedConfigDir, icMap["image"])
					}
				}
				if icMap["name"] == "inject-agent" {
					foundRepoSandbox = true
					if icMap["image"] != tt.expectedRepoSandbox {
						t.Errorf("expected reposandbox image %s, got %s", tt.expectedRepoSandbox, icMap["image"])
					}
				}
			}

			if !foundConfigDir {
				t.Errorf("gemini-configs init container not found")
			}
			if !foundRepoSandbox {
				t.Errorf("inject-agent init container not found")
			}
		})
	}
}

func TestNewReviewSandbox_Defaulting(t *testing.T) {
	// Set env vars
	os.Setenv("REPO_SANDBOX_IMAGE", "env-repo-sandbox-image")
	os.Setenv("CONFIGDIR_CLI_IMAGE", "env-configdir-cli-image")
	defer func() {
		os.Unsetenv("REPO_SANDBOX_IMAGE")
		os.Unsetenv("CONFIGDIR_CLI_IMAGE")
	}()

	tests := []struct {
		name                string
		optRepoSandboxImage string
		optConfigDirImage   string
		expectedRepoSandbox string
		expectedConfigDir   string
	}{
		{
			name:                "DefaultFromEnv",
			optRepoSandboxImage: "",
			optConfigDirImage:   "",
			expectedRepoSandbox: "env-repo-sandbox-image",
			expectedConfigDir:   "env-configdir-cli-image",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			opt := ReviewSandboxOptions{
				DevSandboxOptions: DevSandboxOptions{
					Name:             "test",
					Namespace:        "default",
					RepoSandboxImage: tt.optRepoSandboxImage,
					ConfigDirImage:   tt.optConfigDirImage,
					LLMConfigdirRef:  "some-ref", // To trigger init container check
				},
			}
			sandboxObj, _ := NewReviewSandbox(opt)

			spec := sandboxObj.Object["spec"].(map[string]interface{})
			podTemplate := spec["podTemplate"].(map[string]interface{})
			podSpec := podTemplate["spec"].(map[string]interface{})
			initContainers := podSpec["initContainers"].([]interface{})

			foundConfigDir := false
			for _, ic := range initContainers {
				icMap := ic.(map[string]interface{})
				if icMap["name"] == "gemini-configs" {
					foundConfigDir = true
					if icMap["image"] != tt.expectedConfigDir {
						t.Errorf("expected configdir image %s, got %s", tt.expectedConfigDir, icMap["image"])
					}
				}
			}

			if !foundConfigDir {
				t.Errorf("gemini-configs init container not found")
			}
		})
	}
}
