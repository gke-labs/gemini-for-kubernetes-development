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

	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/github"
)

var _ Task = &ChoreModel{}

type ChoreModel struct {
	Repo        *github.Repository
	AgentPrompt string
	ChoreName   string
	ChoreFile   string
	RepoName    string
	CloneURL    string
	RepoOwner   string
	PromptFile  string
	SkipPR      bool
}

func (m *ChoreModel) Name() string {
	return "chore"
}

func (m *ChoreModel) PreScript() ([]byte, error) {
	tmpl, err := getScriptTemplate("chore.sh")
	if err != nil {
		return nil, err
	}
	var w bytes.Buffer
	if err := tmpl.Execute(&w, m); err != nil {
		return nil, fmt.Errorf("failed to execute script template: %w", err)
	}
	return w.Bytes(), nil
}

func (m *ChoreModel) Prompt() ([]byte, error) {
	tmpl, err := getPromptTemplate("chore.txt")
	if err != nil {
		return nil, err
	}
	var w bytes.Buffer
	if err := tmpl.Execute(&w, m); err != nil {
		return nil, fmt.Errorf("failed to execute prompt template: %w", err)
	}
	return w.Bytes(), nil
}

func (m *ChoreModel) PostScript() ([]byte, error) {
	return nil, nil
}

func (m *ChoreModel) DraftState() string {
	return "informational"
}
