// Copyright 2026 The Kubernetes Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// you may obtain a copy of the License at
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
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// DevSandboxOptions holds options for creating a DevSandbox.
type DevSandboxOptions struct {
	Name        string
	Namespace   string
	Labels      map[string]string
	Annotations map[string]string

	// Source
	CloneURL string
	HTMLURL  string

	// Destination
	Branch      string
	Origin      string
	PushEnabled bool
	UserLogin   string
	UserName    string
	UserEmail   string

	// Bot info
	BotLogin string
	BotName  string
	BotEmail string

	// User Config
	DotFilesRepo string

	// LLM
	LLMProvider         string
	LLMConfigdirRef     string
	LLMAPIKeySecretName string
	LLMAPIKey           string
	Prompt              string

	// Infra
	ServiceAccountName    string
	GithubSecretName      string
	DevcontainerConfigRef string
	Image                 string
	OverseerName          string

	// System Images
	RepoSandboxImage string
	ConfigDirImage   string

	// Gateway
	HTTPEnabled bool

	// Scaling
	Replicas int64

	DindSupport string

	// WorkspaceDiskSize specifies the disk size for the workspace PVC.
	WorkspaceDiskSize string

	// Idea Exploration
	IdeaID         string
	Approach       string
	ParentApproach string

	// TraceabilityMetadataEnabled when true will append a metadata footer to GitHub issues, PRs, and comments.
	// This is passed to the sandbox as METADATA_TRACEABILITY_ENABLED env var ("true" or "false").
	TraceabilityMetadataEnabled bool
}

// NewDevSandbox creates a new DevSandbox.
func NewDevSandbox(opt DevSandboxOptions) (*unstructured.Unstructured, *corev1.Service) {
	agentOpt := AgentSandboxOptions{
		DevSandboxOptions: opt,
		DindSupport:       opt.DindSupport,
	}
	return NewAgentSandbox(agentOpt)
}
