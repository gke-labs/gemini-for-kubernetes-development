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

var _ Task = &AddressFeedbackModel{}

type AddressFeedbackModel struct {
	Repo                  *github.Repository
	PullRequest           *github.PullRequest
	RepositoryCommits     []github.RepositoryCommit
	IssueComments         []github.IssueComment
	OldIssueComments      []github.IssueComment
	PullRequestReviews    []github.PullRequestReview
	OldPullRequestReviews []github.PullRequestReview
	User                  *github.User
	PromptFile            string
	Models                []string
	Extensions            []reviewv1alpha1.Extension
}

func (m *AddressFeedbackModel) Name() string {
	return "address-feedback"
}

func (m *AddressFeedbackModel) PreScript() ([]byte, error) {
	tmpl, err := getScriptTemplate("address_feedback.sh")
	if err != nil {
		return nil, err
	}
	var w bytes.Buffer
	if err := tmpl.Execute(&w, m); err != nil {
		return nil, fmt.Errorf("failed to execute script template: %w", err)
	}
	return w.Bytes(), nil
}

func (m *AddressFeedbackModel) Prompt() ([]byte, error) {
	tmpl, err := getPromptTemplate("address_feedback.txt")
	if err != nil {
		return nil, err
	}
	var w bytes.Buffer
	if err := tmpl.Execute(&w, m); err != nil {
		return nil, fmt.Errorf("failed to execute prompt template: %w", err)
	}
	return w.Bytes(), nil
}

func (m *AddressFeedbackModel) PostScript() ([]byte, error) {
	return nil, nil
}

func (m *AddressFeedbackModel) DraftState() string {
	return "informational"
}
