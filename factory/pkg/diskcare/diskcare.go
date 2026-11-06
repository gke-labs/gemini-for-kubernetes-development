// Package diskcare keeps a sandbox's workspace disk from filling mid-task
// (KCC #15081: a 40 Gi workspace filled, and the task hung). Factory does
// the generic part: it measures the workspace and records what it finds,
// runs the repository's workspace-cleanup recipe when space runs low, and
// refuses to start a task with too little room. What to delete - a
// toolchain's temporary builds, a cache - is the cleanup recipe's to say:
// the built-in one deletes nothing, and a repository puts its own at
// .agents/maintenance/workspace-cleanup.yaml.
package diskcare

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"time"

	"golang.org/x/sys/unix"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/recipe"
)

// GB is a gigabyte, as df counts them.
const GB = 1 << 30

// Thresholds a task starts against: below MinFree free, the cleanup recipe
// runs, asked to bring free space to TargetFree; below Floor after that,
// the task does not start.
const (
	MinFree    = 10 * GB
	TargetFree = 20 * GB
	Floor      = 5 * GB
)

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

// CleanupRecipe is the recipe that makes room on a workspace.
const CleanupRecipe = "workspace-cleanup"

// CleanupPath is where a repository keeps its own cleanup recipe, outside
// .agents/recipes: it is maintenance, not a recipe the board offers on
// issues and pull requests.
const CleanupPath = ".agents/maintenance/" + CleanupRecipe + ".yaml"

//go:embed workspace-cleanup.yaml
var builtinCleanup []byte

// LoadCleanup is the cleanup recipe for the checkout at repoDir: the
// repository's (CleanupPath) when it has one, else the built-in, which
// deletes nothing. It says where it came from. Its steps must all be run:
// steps - no agent, no uses: - since it runs before a task, outside it.
func LoadCleanup(repoDir string) (*recipe.Recipe, string, error) {
	data, source := builtinCleanup, "built-in"
	if repoDir != "" {
		if b, err := os.ReadFile(filepath.Join(repoDir, CleanupPath)); err == nil {
			data, source = b, CleanupPath
		} else if !errors.Is(err, os.ErrNotExist) {
			return nil, "", err
		}
	}
	r, err := recipe.Parse(data)
	if err != nil {
		return nil, "", fmt.Errorf("%s: %w", source, err)
	}
	for i, st := range r.Start.Steps {
		if st.Kind() != "run" {
			return nil, "", fmt.Errorf("%s: step %s is a %s step; a cleanup recipe runs commands only (run:)", source, st.Label(i), st.Kind())
		}
	}
	return r, source, nil
}

// RunCleanup runs rec's steps, each with bash in repoDir, with the
// workspace's state in its environment: WORKSPACE, FREE_BYTES (free now),
// TARGET_BYTES (free space wanted), and LOG_DIR, a directory for anything
// it wants to keep. A step that fails is reported and the next one runs:
// cleaning up is best effort.
func RunCleanup(ctx context.Context, rec *recipe.Recipe, repoDir, workspace, logDir string, target uint64, out io.Writer) {
	_ = os.MkdirAll(logDir, 0o755)
	for i, st := range rec.Start.Steps {
		u, err := Of(workspace)
		if err != nil {
			fmt.Fprintf(out, "::warning %s: %v\n", CleanupRecipe, err)
			return
		}
		cmd := exec.CommandContext(ctx, "bash", "-euo", "pipefail", "-c", st.Run)
		cmd.Dir = repoDir
		cmd.Env = append(os.Environ(),
			"WORKSPACE="+workspace,
			"FREE_BYTES="+strconv.FormatUint(u.Free, 10),
			"TARGET_BYTES="+strconv.FormatUint(target, 10),
			"LOG_DIR="+logDir,
		)
		cmd.Stdout, cmd.Stderr = out, out
		if err := cmd.Run(); err != nil {
			fmt.Fprintf(out, "::warning %s step %s failed: %v\n", CleanupRecipe, st.Label(i), err)
		}
	}
}

// ErrFull is a workspace with too little room for a task even after the
// cleanup recipe ran. Its message starts "disk full", which the overseer
// reads as a failure not of the task's making.
type ErrFull struct{ Usage Usage }

func (e *ErrFull) Error() string {
	return fmt.Sprintf("disk full: %s on the workspace after cleaning; a task needs %d GB free", e.Usage, Floor/GB)
}

// Prepare makes room on workspace before a task: with less than MinFree
// free it runs the cleanup recipe of the checkout at repoDir (LoadCleanup),
// asked for TargetFree, and with less than Floor free after that it
// returns *ErrFull. It says what it did on out, and returns the
// workspace's space as the task starts.
func Prepare(ctx context.Context, workspace, repoDir, logDir string, out io.Writer) (Usage, error) {
	u, err := Of(workspace)
	if err != nil || u.Free >= MinFree {
		return u, err
	}
	fmt.Fprintf(out, "::warning the workspace is low on space: %s\n", u)
	rec, source, err := LoadCleanup(repoDir)
	if err != nil {
		fmt.Fprintf(out, "::warning no cleanup: %v\n", err)
	} else {
		fmt.Fprintf(out, "Running %s (%s)\n", CleanupRecipe, source)
		RunCleanup(ctx, rec, repoDir, workspace, logDir, TargetFree, out)
		if u, err = Of(workspace); err != nil {
			return Usage{}, err
		}
		fmt.Fprintf(out, "After %s: %s\n", CleanupRecipe, u)
	}
	if u.Free < Floor {
		return u, &ErrFull{Usage: u}
	}
	return u, nil
}

// UsageFile is where a task records its workspace's space: the overseer
// copies it to the sandbox (Annotation), for the board and for sandboxes
// paused since.
const UsageFile = "disk.json"

// Annotation is the sandbox annotation that carries the workspace's space
// as its last task left it: Usage, as JSON.
const Annotation = "factory.gemini.google.com/disk"

// Record writes u to taskDir's UsageFile.
func Record(taskDir string, u Usage) error {
	b, err := json.Marshal(u)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(taskDir, UsageFile), b, 0o644)
}
