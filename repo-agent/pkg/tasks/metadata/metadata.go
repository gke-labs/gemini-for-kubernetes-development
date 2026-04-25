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

package metadata

import (
	"bytes"
	"os"
	"strconv"
	"strings"
	"text/template"
	"time"

	"k8s.io/klog/v2"
)

const (
	EnvSandboxTaskName            = "SANDBOX_TASK_NAME"
	EnvSandboxTaskUID             = "SANDBOX_TASK_UID"
	EnvSandboxName                = "SANDBOX_NAME"
	EnvRepoWatchName              = "REPO_WATCH_NAME"
	EnvSandboxTaskType            = "SANDBOX_TASK_TYPE"
	EnvMetadataTraceabilityEnable = "METADATA_TRACEABILITY_ENABLED"

	// Metadata footer keys
	MetadataKeySandboxTask    = "sandbox-task"
	MetadataKeySandboxTaskUID = "sandbox-task-uid"
	MetadataKeySandbox        = "sandbox"
	MetadataKeyRepoWatch      = "repowatch"
	MetadataKeyTaskType       = "task-type"
	MetadataKeyTimestamp      = "timestamp"

	// Task types
	TaskTypeReview          = "review"
	TaskTypeFixIssue        = "fix-issue"
	TaskTypeAddressFeedback = "address-feedback"
	TaskTypeChore           = "chore"
	TaskTypeDevSetup        = "dev-setup"
	TaskTypeFeedback        = "feedback"
	TaskTypeIssueComment    = "issue-comment"
	TaskTypePRReview        = "pr-review"
	TaskTypeTriageIssue     = "triage-issue"
	TaskTypeIterate         = "iterate"
	TaskTypeRollback        = "rollback"
)

var footerTemplate = template.Must(template.New("metadata-footer").Parse(`

---

<!-- repo-agent-metadata
sandbox-task: {{ .SandboxTask }}
sandbox-task-uid: {{ .SandboxTaskUID }}
sandbox: {{ .Sandbox }}
repowatch: {{ .RepoWatch }}
task-type: {{ .TaskType }}
timestamp: {{ .Timestamp }}
-->`))

// Metadata contains traceability information for bot actions.
type Metadata struct {
	// SandboxTask is the name of the SandboxTask that triggered this action.
	SandboxTask string `json:"sandboxTask,omitempty"`
	// SandboxTaskUID is the UID of the SandboxTask that triggered this action.
	SandboxTaskUID string `json:"sandboxTaskUid,omitempty"`
	// Sandbox is the name of the Sandbox where the action is being performed.
	Sandbox string `json:"sandbox,omitempty"`
	// RepoWatch is the name of the RepoWatch CR associated with this sandbox.
	RepoWatch string `json:"repoWatch,omitempty"`
	// TaskType is the type of task being performed (e.g. fix-issue, pr-review).
	TaskType string `json:"taskType,omitempty"`
	// Timestamp is the time when the action was performed (UTC).
	Timestamp string `json:"timestamp,omitempty"`
}

// GetMetadata returns the current traceability metadata from environment variables.
// It uses "n/a" as a fallback for missing values.
func GetMetadata() Metadata {
	return Metadata{
		SandboxTask:    getEnvWithFallback(EnvSandboxTaskName, "n/a"),
		SandboxTaskUID: getEnvWithFallback(EnvSandboxTaskUID, "n/a"),
		Sandbox:        getEnvWithFallback(EnvSandboxName, "n/a"),
		RepoWatch:      getEnvWithFallback(EnvRepoWatchName, "n/a"),
		TaskType:       getEnvWithFallback(EnvSandboxTaskType, "n/a"),
		Timestamp:      time.Now().UTC().Format(time.RFC3339),
	}
}

func getEnvWithFallback(key, fallback string) string {
	if val := os.Getenv(key); val != "" {
		return val
	}
	return fallback
}

// GenerateMetadataFooter creates a structured HTML comment footer from metadata.
func GenerateMetadataFooter(m Metadata) string {
	var buf bytes.Buffer
	sanitized := Metadata{
		SandboxTask:    sanitize(m.SandboxTask),
		SandboxTaskUID: sanitize(m.SandboxTaskUID),
		Sandbox:        sanitize(m.Sandbox),
		RepoWatch:      sanitize(m.RepoWatch),
		TaskType:       sanitize(m.TaskType),
		Timestamp:      sanitize(m.Timestamp),
	}
	if err := footerTemplate.Execute(&buf, sanitized); err != nil {
		klog.Errorf("failed to execute metadata footer template: %v", err)
		return ""
	}
	return buf.String()
}

func sanitize(s string) string {
	s = strings.ReplaceAll(s, "--", "**")
	return strings.ReplaceAll(s, "\n", " ")
}

// GetMetadataEnv returns a map of all metadata environment variables.
func GetMetadataEnv() map[string]string {
	return map[string]string{
		EnvSandboxTaskName:            os.Getenv(EnvSandboxTaskName),
		EnvSandboxTaskUID:             os.Getenv(EnvSandboxTaskUID),
		EnvRepoWatchName:              os.Getenv(EnvRepoWatchName),
		EnvSandboxName:                os.Getenv(EnvSandboxName),
		EnvSandboxTaskType:            os.Getenv(EnvSandboxTaskType),
		EnvMetadataTraceabilityEnable: strconv.FormatBool(GetTraceabilityMetadataEnabled()),
	}
}

func GetTraceabilityMetadataEnabled() bool {
	val := os.Getenv(EnvMetadataTraceabilityEnable)
	if val == "" {
		return false
	}
	b, err := strconv.ParseBool(val)
	if err != nil {
		klog.Errorf("failed to parse %s=%q as bool: %v", EnvMetadataTraceabilityEnable, val, err)
		return false
	}
	return b
}
