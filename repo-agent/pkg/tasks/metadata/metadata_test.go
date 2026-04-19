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
				SandboxTask: "task <!-- with --> comment",
			},
			contains: []string{
				"sandbox-task: task  with  comment",
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
