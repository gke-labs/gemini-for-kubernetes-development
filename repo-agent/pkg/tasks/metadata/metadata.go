package metadata

import (
	"fmt"
	"os"
	"strconv"
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
)

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
	return fmt.Sprintf("\n\n---\n\n<!-- repo-agent-metadata\n%s: %s\n%s: %s\n%s: %s\n%s: %s\n%s: %s\n%s: %s\n-->",
		MetadataKeySandboxTask, m.SandboxTask,
		MetadataKeySandboxTaskUID, m.SandboxTaskUID,
		MetadataKeySandbox, m.Sandbox,
		MetadataKeyRepoWatch, m.RepoWatch,
		MetadataKeyTaskType, m.TaskType,
		MetadataKeyTimestamp, m.Timestamp)
}

// GetMetadataEnv returns a map of all metadata environment variables.
func GetMetadataEnv() map[string]string {
	return map[string]string{
		EnvSandboxTaskName:            os.Getenv(EnvSandboxTaskName),
		EnvSandboxTaskUID:             os.Getenv(EnvSandboxTaskUID),
		EnvRepoWatchName:              os.Getenv(EnvRepoWatchName),
		EnvSandboxName:                os.Getenv(EnvSandboxName),
		EnvSandboxTaskType:            os.Getenv(EnvSandboxTaskType),
		EnvMetadataTraceabilityEnable: os.Getenv(EnvMetadataTraceabilityEnable),
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
