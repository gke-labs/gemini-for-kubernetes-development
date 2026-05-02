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

package prompts

import (
	"bytes"
	"fmt"
	"text/template"

	"github.com/google/go-github/v39/github"
)

type ReviewPromptModel struct {
	github.PullRequest
	Prompt      string
	IgnoreFiles []string
}

// ExpandReviewPrompt expands the review prompt template with the provided model.
// It performs a two-level substitution to allow the user-provided prompt to also use template variables from the PR.
func ExpandReviewPrompt(model ReviewPromptModel) (string, error) {
	// Level 1 substitution
	lvl1, err := getTemplate("review_system.txt")
	if err != nil {
		return "", err
	}

	var level1 bytes.Buffer
	err = lvl1.Execute(&level1, model)
	if err != nil {
		return "", fmt.Errorf("failed to execute level 1 template: %w", err)
	}

	// Level 2 substitution
	// This allows the user provided Prompt to contain template variables that are substituted with PR details.
	tmpl, err := template.New("lvl2").Parse(level1.String())
	if err != nil {
		return "", fmt.Errorf("failed to parse level 2 template: %w", err)
	}

	var buf bytes.Buffer
	err = tmpl.Execute(&buf, model.PullRequest)
	if err != nil {
		return "", fmt.Errorf("failed to execute level 2 template: %w", err)
	}
	return buf.String(), nil
}
