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

package tasks

import (
	"bytes"
	"fmt"

	reviewv1alpha1 "github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/api/repowatch/v1alpha1"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/github"
)

var _ Task = &IterateModel{}

type IterateModel struct {
	Repo        *github.Repository
	RepoOwner   string
	RepoName    string
	User        *github.User
	AgentPrompt string
	BranchName  string
	PRID        string
	PromptFile  string
	Models      []string
	Extensions  []reviewv1alpha1.Extension
}

func (m *IterateModel) Name() string {
	return "iterate"
}

func (m *IterateModel) PreScript() ([]byte, error) {
	tmpl, err := getScriptTemplate("iterate.sh")
	if err != nil {
		return nil, err
	}
	var w bytes.Buffer
	if err := tmpl.Execute(&w, m); err != nil {
		return nil, fmt.Errorf("failed to execute script template: %w", err)
	}
	return w.Bytes(), nil
}

func (m *IterateModel) Prompt() ([]byte, error) {
	tmpl, err := getPromptTemplate("iterate.txt")
	if err != nil {
		return nil, err
	}
	var w bytes.Buffer
	if err := tmpl.Execute(&w, m); err != nil {
		return nil, fmt.Errorf("failed to execute prompt template: %w", err)
	}
	return w.Bytes(), nil
}

func (m *IterateModel) PostScript() ([]byte, error) {
	return nil, nil
}

func (m *IterateModel) DraftState() string {
	return "informational"
}
