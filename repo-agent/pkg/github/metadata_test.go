package github

import (
	"strings"
	"testing"
)

func TestMetadata_String(t *testing.T) {
	tests := []struct {
		name     string
		metadata Metadata
		want     []string
		notWant  []string
	}{
		{
			name: "Disabled",
			metadata: Metadata{
				Enabled: false,
			},
			want:    []string{""},
			notWant: []string{"repo-agent-metadata"},
		},
		{
			name: "Enabled with some fields",
			metadata: Metadata{
				Enabled:     true,
				SandboxTask: "task-1",
				Sandbox:     "sandbox-1",
				Timestamp:   "2026-03-19T10:00:00Z",
			},
			want: []string{
				"<!-- repo-agent-metadata",
				"sandbox-task: task-1",
				"sandbox: sandbox-1",
				"timestamp: 2026-03-19T10:00:00Z",
				"-->",
			},
			notWant: []string{
				"sandbox-task-uid:",
				"repowatch:",
				"task-type:",
			},
		},
		{
			name: "All fields set",
			metadata: Metadata{
				Enabled:        true,
				SandboxTask:    "task-1",
				SandboxTaskUID: "uid-1",
				Sandbox:        "sandbox-1",
				RepoWatch:      "repowatch-1",
				TaskType:       "triage",
				Timestamp:      "2026-03-19T10:00:00Z",
			},
			want: []string{
				"sandbox-task: task-1",
				"sandbox-task-uid: uid-1",
				"sandbox: sandbox-1",
				"repowatch: repowatch-1",
				"task-type: triage",
				"timestamp: 2026-03-19T10:00:00Z",
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.metadata.String()
			if len(tt.want) == 1 && tt.want[0] == "" {
				if got != "" {
					t.Errorf("Metadata.String() = %v, want empty string", got)
				}
				return
			}
			for _, w := range tt.want {
				if !strings.Contains(got, w) {
					t.Errorf("Metadata.String() expected to contain %q, but got:\n%s", w, got)
				}
			}
			for _, nw := range tt.notWant {
				if strings.Contains(got, nw) {
					t.Errorf("Metadata.String() expected NOT to contain %q, but got:\n%s", nw, got)
				}
			}
		})
	}
}
