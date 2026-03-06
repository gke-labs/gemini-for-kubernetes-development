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

package github

import (
	"fmt"
	"time"
)

type TraceabilityMetadata struct {
	Enabled        bool
	SandboxTask    string
	SandboxTaskUID string
	Sandbox        string
	RepoWatch      string
	TaskType       string
	Timestamp      string
}

func (m *TraceabilityMetadata) FormatHTMLComment() string {
	if !m.Enabled {
		return ""
	}

	timestamp := m.Timestamp
	if timestamp == "" {
		timestamp = time.Now().Format(time.RFC3339)
	}

	metadata := "\n---\n<!-- repo-agent-metadata\n"
	if m.SandboxTask != "" {
		metadata += fmt.Sprintf("sandbox-task: %s\n", m.SandboxTask)
	}
	if m.SandboxTaskUID != "" {
		metadata += fmt.Sprintf("sandbox-task-uid: %s\n", m.SandboxTaskUID)
	}
	if m.Sandbox != "" {
		metadata += fmt.Sprintf("sandbox: %s\n", m.Sandbox)
	}
	if m.RepoWatch != "" {
		metadata += fmt.Sprintf("repowatch: %s\n", m.RepoWatch)
	}
	if m.TaskType != "" {
		metadata += fmt.Sprintf("task-type: %s\n", m.TaskType)
	}
	metadata += fmt.Sprintf("timestamp: %s\n", timestamp)
	metadata += "-->"
	return metadata
}
