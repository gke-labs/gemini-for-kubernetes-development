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

package metadata

import (
	"strings"
	"testing"
)

func TestGenerateMetadataFooter(t *testing.T) {
	tests := []struct {
		name     string
		metadata Metadata
		contains []string
	}{
		{
			name: "Sanitize newlines",
			metadata: Metadata{
				SandboxTask: "task\nwith\nnewlines",
				Sandbox:     "sandbox",
			},
			contains: []string{
				"sandbox-task: task with newlines",
				"sandbox: sandbox",
			},
		},
		{
			name: "Sanitize HTML comments",
			metadata: Metadata{
				SandboxTask: "task <!-- with --> comment and --!> sneaky closure",
			},
			contains: []string{
				"sandbox-task: task <!** with **> comment and **!> sneaky closure",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := GenerateMetadataFooter(tt.metadata)
			for _, c := range tt.contains {
				if !strings.Contains(got, c) {
					t.Errorf("GenerateMetadataFooter() = %q, missing %q", got, c)
				}
			}
		})
	}
}
