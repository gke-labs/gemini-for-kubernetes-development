// Package diskcare is what factory knows about a sandbox's workspace disk
// (KCC #15081: a 40 Gi workspace filled mid-task, and the task hung): how
// to measure it, the thresholds a task starts against, and the error that
// says there is no room. It decides nothing: the overseer measures through
// the sandbox's daemon (GET /v1/disk), and when to run the repository's
// workspace-cleanup recipe (recipe.WorkspaceCleanup) is the overseer's.
package diskcare

import (
	"fmt"
	"time"

	"golang.org/x/sys/unix"
)

// GB is a gigabyte, as df counts them.
const GB = 1 << 30

// Thresholds: below MinFree free a workspace is cleaned, asked for
// TargetFree; below Floor no task starts on it.
const (
	MinFree    = 10 * GB
	TargetFree = 20 * GB
	Floor      = 5 * GB
)

// Workspace is a sandbox's workspace mount.
const Workspace = "/workspaces"

// Usage is a filesystem's space, in bytes.
type Usage struct {
	Used uint64    `json:"used"`
	Size uint64    `json:"size"`
	Free uint64    `json:"free"`
	At   time.Time `json:"at"`
}

// String is Usage as people read it: "33.1/58.8 GB used, 25.7 GB free".
func (u Usage) String() string {
	return fmt.Sprintf("%.1f/%.1f GB used, %.1f GB free", float64(u.Used)/GB, float64(u.Size)/GB, float64(u.Free)/GB)
}

// Low is whether u is below MinFree free: time to clean.
func (u Usage) Low() bool { return u.Size > 0 && u.Free < MinFree }

// Full is whether u is below Floor free: no task starts.
func (u Usage) Full() bool { return u.Size > 0 && u.Free < Floor }

// Of is the space of the filesystem path is on. A variable, for tests.
var Of = func(path string) (Usage, error) {
	var st unix.Statfs_t
	if err := unix.Statfs(path, &st); err != nil {
		return Usage{}, fmt.Errorf("statfs %s: %w", path, err)
	}
	bs := uint64(st.Bsize)
	size, free := st.Blocks*bs, st.Bavail*bs
	used := size - st.Bfree*bs
	return Usage{Used: used, Size: size, Free: free, At: time.Now().UTC().Truncate(time.Second)}, nil
}

// ErrFull is a workspace with too little room for a task. Its message
// starts "disk full", which the overseer's dispatcher reads as a failure
// not of the task's making (no attempt of its own).
type ErrFull struct{ Usage Usage }

func (e *ErrFull) Error() string {
	return fmt.Sprintf("disk full: %s on the workspace; a task needs %d GB free", e.Usage, Floor/GB)
}

// Annotation is the sandbox annotation that carries the workspace's space
// as last measured: Usage, as JSON.
const Annotation = "factory.gemini.google.com/disk"

// CleanupInputs are the workspace-cleanup recipe's inputs for a workspace
// with space u, asked for target bytes free.
func CleanupInputs(u Usage, target uint64) map[string]string {
	return map[string]string{
		"workspace":    Workspace,
		"free_bytes":   fmt.Sprint(u.Free),
		"target_bytes": fmt.Sprint(target),
	}
}
