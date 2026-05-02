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
	"strings"
	"testing"

	"github.com/google/go-github/v39/github"
)

func TestExpandReviewPrompt_IgnoreFiles(t *testing.T) {
	model := ReviewPromptModel{
		PullRequest: github.PullRequest{},
		IgnoreFiles: []string{"*.pb.go", "vendor/**"},
	}

	result, err := ExpandReviewPrompt(model)
	if err != nil {
		t.Fatalf("ExpandReviewPrompt failed: %v", err)
	}

	expectedStrs := []string{
		"Do not review files matching the following patterns:",
		"- *.pb.go",
		"- vendor/**",
	}

	for _, str := range expectedStrs {
		if !strings.Contains(result, str) {
			t.Errorf("Result does not contain expected string %q", str)
		}
	}
}

func TestExpandReviewPrompt_NoIgnoreFiles(t *testing.T) {
	model := ReviewPromptModel{
		PullRequest: github.PullRequest{},
		IgnoreFiles: nil,
	}

	result, err := ExpandReviewPrompt(model)
	if err != nil {
		t.Fatalf("ExpandReviewPrompt failed: %v", err)
	}

	unexpectedStr := "Do not review files matching the following patterns:"
	if strings.Contains(result, unexpectedStr) {
		t.Errorf("Result should not contain %q when IgnoreFiles is nil", unexpectedStr)
	}
}
