package github

import (
	"fmt"
	"time"
)

// Metadata represents traceability metadata to be embedded in GitHub content.
type Metadata struct {
	Enabled        bool
	SandboxTask    string
	SandboxTaskUID string
	Sandbox        string
	RepoWatch      string
	TaskType       string
	Timestamp      string
}

// String returns the metadata formatted as an HTML comment footer.
func (m Metadata) String() string {
	if !m.Enabled {
		return ""
	}
	footer := "\n---\n<!-- repo-agent-metadata\n"
	if m.SandboxTask != "" {
		footer += fmt.Sprintf("sandbox-task: %s\n", m.SandboxTask)
	}
	if m.SandboxTaskUID != "" {
		footer += fmt.Sprintf("sandbox-task-uid: %s\n", m.SandboxTaskUID)
	}
	if m.Sandbox != "" {
		footer += fmt.Sprintf("sandbox: %s\n", m.Sandbox)
	}
	if m.RepoWatch != "" {
		footer += fmt.Sprintf("repowatch: %s\n", m.RepoWatch)
	}
	if m.TaskType != "" {
		footer += fmt.Sprintf("task-type: %s\n", m.TaskType)
	}
	timestamp := m.Timestamp
	if timestamp == "" {
		timestamp = time.Now().Format(time.RFC3339)
	}
	footer += fmt.Sprintf("timestamp: %s\n", timestamp)
	footer += "-->\n"
	return footer
}
