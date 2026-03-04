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

var _ Task = &FixIssueModel{}

type FixIssueModel struct {
	Issue         *github.Issue
	Repo          *github.Repository
	IssueComments []github.IssueComment
	User          *github.User
	PromptFile    string
	Models        []string
	Extensions    []reviewv1alpha1.Extension
}

func (m *FixIssueModel) Name() string {
	return "fix-issue"
}

func (m *FixIssueModel) PreScript() ([]byte, error) {
	tmpl, err := getScriptTemplate("fix_issue.sh")
	if err != nil {
		return nil, err
	}
	var w bytes.Buffer
	if err := tmpl.Execute(&w, m); err != nil {
		return nil, fmt.Errorf("failed to execute prompt template: %w", err)
	}
	return w.Bytes(), nil
}

func (m *FixIssueModel) Prompt() ([]byte, error) {
	tmpl, err := getPromptTemplate("fix_issue.txt")
	if err != nil {
		return nil, err
	}
	var w bytes.Buffer
	if err := tmpl.Execute(&w, m); err != nil {
		return nil, fmt.Errorf("failed to execute prompt template: %w", err)
	}
	return w.Bytes(), nil
}

func (m *FixIssueModel) PostScript() ([]byte, error) {
	return nil, nil
}

func (m *FixIssueModel) DraftState() string {
	return "informational"
}
