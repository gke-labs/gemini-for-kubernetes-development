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

// ExpandIssueHandlerPrompt expands the issue handler prompt template with the provided issue.
func ExpandIssueHandlerPrompt(templateStr string, issue *github.Issue) (string, error) {
	tmpl, err := template.New("issue_handler").Parse(templateStr)
	if err != nil {
		return "", fmt.Errorf("failed to parse issue handler template: %w", err)
	}

	var buf bytes.Buffer
	err = tmpl.Execute(&buf, issue)
	if err != nil {
		return "", fmt.Errorf("failed to execute issue handler template: %w", err)
	}
	return buf.String(), nil
}
