package concurrency

import (
	"os"
	"path/filepath"
)

// drainMarkerFiles are the well-known paths whose presence pauses task processing.
func drainMarkerFiles(queueDir string) []string {
	return []string{
		filepath.Join(queueDir, ".do_not_process"),
		filepath.Join(queueDir, "do_not_process"),
		filepath.Join(queueDir, ".drain"),
		filepath.Join(queueDir, "drain"),
		"/workspaces/.do_not_process",
		"/workspaces/do_not_process",
		"/workspaces/.drain",
		"/workspaces/drain",
	}
}

// drainEnvVars are the environment variables that pause task processing when set to "true".
var drainEnvVars = []string{
	"DO_NOT_PROCESS",
	"FACTORY_DO_NOT_PROCESS",
	"DRAIN",
	"FACTORY_DRAIN",
}

// IsDrainMode reports whether task processing is paused, either via a drain
// environment variable or a drain marker file in the queue directory.
func IsDrainMode(queueDir string) bool {
	for _, env := range drainEnvVars {
		if os.Getenv(env) == "true" {
			return true
		}
	}
	for _, p := range drainMarkerFiles(queueDir) {
		if _, err := os.Stat(p); err == nil {
			return true
		}
	}
	return false
}

// IsDrainMode reports whether task processing is paused for this queue.
func (m *TaskQueueManager) IsDrainMode() bool {
	return IsDrainMode(m.queueDir)
}
