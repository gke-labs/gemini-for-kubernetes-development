/*
Copyright 2026.

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

package tasks

import (
	"bytes"
	"fmt"

	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/tasks/metadata"

	reviewv1alpha1 "github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/api/repowatch/v1alpha1"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/github"
)

var _ Task = &InvestigateFailuresModel{}

type FailedRun struct {
	ID         int64
	Name       string
	URL        string
	HeadSHA    string
	FailedJobs []FailedJob
}

type FailedJob struct {
	ID      int64
	Name    string
	LogPath string // Path to the downloaded log file in the sandbox
}

type InvestigateFailuresModel struct {
	Repo                        *github.Repository
	PullRequest                 *github.PullRequest
	RepositoryCommits           []github.RepositoryCommit
	User                        *github.User
	PromptFile                  string
	Models                      []string
	FailedRuns                  []FailedRun
	Extensions                  []reviewv1alpha1.Extension
	IssueComments               []github.IssueComment
	Metadata                    metadata.Metadata
	TraceabilityMetadataEnabled bool
}

func (m *InvestigateFailuresModel) Name() string {
	return "investigate-failures"
}

func (m *InvestigateFailuresModel) PreScript() ([]byte, error) {
	tmpl, err := getScriptTemplate("investigate_failures.sh")
	if err != nil {
		return nil, err
	}
	var w bytes.Buffer
	if err := tmpl.Execute(&w, m); err != nil {
		return nil, fmt.Errorf("failed to execute script template: %w", err)
	}
	return w.Bytes(), nil
}

func (m *InvestigateFailuresModel) Prompt() ([]byte, error) {
	tmpl, err := getPromptTemplate("investigate_failures.txt")
	if err != nil {
		return nil, err
	}
	var w bytes.Buffer
	if err := tmpl.Execute(&w, m); err != nil {
		return nil, fmt.Errorf("failed to execute prompt template: %w", err)
	}
	return w.Bytes(), nil
}

func (m *InvestigateFailuresModel) PostScript() ([]byte, error) {
	return nil, nil
}

func (m *InvestigateFailuresModel) DraftState() string {
	return "informational"
}
