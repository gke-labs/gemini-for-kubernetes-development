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

package tasks

import (
	"bytes"
	"fmt"

	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/tasks/metadata"

	reviewv1alpha1 "github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/api/repowatch/v1alpha1"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/github"
)

var _ Task = &DevSetupModel{}

type DevSetupModel struct {
	Repo                        *github.Repository
	User                        *github.User
	BranchName                  string
	SourceBranch                string
	PromptFile                  string
	AgentPrompt                 string
	Models                      []string
	Extensions                  []reviewv1alpha1.Extension
	Metadata                    metadata.Metadata
	TraceabilityMetadataEnabled bool
}

func (m *DevSetupModel) Name() string {
	return "dev-setup"
}

func (m *DevSetupModel) PreScript() ([]byte, error) {
	tmpl, err := getScriptTemplate("dev_setup.sh")
	if err != nil {
		return nil, err
	}
	var w bytes.Buffer
	if err := tmpl.Execute(&w, m); err != nil {
		return nil, fmt.Errorf("failed to execute script template: %w", err)
	}
	return w.Bytes(), nil
}

func (m *DevSetupModel) Prompt() ([]byte, error) {
	// If no prompt template is needed, we can return nil or empty
	// But tasks.RunTask writes this to agent-prompt.txt
	tmpl, err := getPromptTemplate("dev_setup.txt")
	if err != nil {
		return nil, err
	}
	var w bytes.Buffer
	if err := tmpl.Execute(&w, m); err != nil {
		return nil, fmt.Errorf("failed to execute prompt template: %w", err)
	}
	return w.Bytes(), nil
}

func (m *DevSetupModel) PostScript() ([]byte, error) {
	return nil, nil
}

func (m *DevSetupModel) DraftState() string {
	return "informational"
}
