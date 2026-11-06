// Package diskcare keeps a sandbox's workspace disk from filling: it
// removes what earlier tasks left in the temporary directory, trims the
// Go build cache when free space runs low, and says when there is too
// little room for a task to start (KCC #15081: a 40 Gi workspace filled
// mid-task - a 22 GB build cache, 4.5 GB of leftover temporary builds -
// and the task hung).
package diskcare

import (
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

// GB is a gigabyte, as df counts them.
const GB = 1 << 30

// Thresholds a task starts against: below MinFree free, the Go build cache
// is trimmed, least recently used first, until TrimTarget is free; below
// Floor after that, the task does not start.
const (
	MinFree    = 10 * GB
	TrimTarget = 20 * GB
	Floor      = 5 * GB
)

// LeftoverAge is how old a temporary entry must be to be an earlier
// task's leftover: one task runs in a sandbox at a time, but a person's
// terminal, or a build of the task just started, may be using a newer
// one.
const LeftoverAge = 2 * time.Hour

// Usage is a filesystem's space, in bytes.
type Usage struct {
	Used uint64    `json:"used"`
	Size uint64    `json:"size"`
	Free uint64    `json:"free"`
	At   time.Time `json:"at"`
}

// String is Usage as people read it: "33.1/58.8 GB used".
func (u Usage) String() string {
	return fmt.Sprintf("%.1f/%.1f GB used, %.1f GB free", float64(u.Used)/GB, float64(u.Size)/GB, float64(u.Free)/GB)
}

// Of is the space of the filesystem path is on.
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

// leftoverPrefixes name what builds, tests and the agent's shell leave in
// the temporary directory: go build's work directories, envtest's
// (k8s_test_framework_), gemini's shell scratch, mktemp's.
var leftoverPrefixes = []string{"go-build", "k8s_test_framework_", "gemini-shell-", "tmp."}

// CleanTemp removes the leftovers in dir last modified before cutoff, and
// says how many it removed and roughly how many bytes they held.
func CleanTemp(dir string, cutoff time.Time) (int, uint64, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, 0, nil
		}
		return 0, 0, err
	}
	removed, freed := 0, uint64(0)
	for _, e := range entries {
		if !leftover(e.Name()) {
			continue
		}
		info, err := e.Info()
		if err != nil || !info.ModTime().Before(cutoff) {
			continue
		}
		path := filepath.Join(dir, e.Name())
		size := treeSize(path)
		if err := os.RemoveAll(path); err != nil {
			continue
		}
		removed++
		freed += size
	}
	return removed, freed, nil
}

func leftover(name string) bool {
	for _, p := range leftoverPrefixes {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	return false
}

func treeSize(path string) uint64 {
	var n uint64
	_ = filepath.WalkDir(path, func(_ string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			if info, err := d.Info(); err == nil {
				n += uint64(info.Size())
			}
		}
		return nil
	})
	return n
}

// TrimCache removes the Go build cache's entries in cacheDir, least
// recently used first (go refreshes an entry's mtime when it uses it),
// until the filesystem path is on has target bytes free, and says how many
// bytes it removed. The cache stays valid: a removed entry is a miss, built
// again when needed.
func TrimCache(cacheDir, path string, target uint64) (uint64, error) {
	u, err := Of(path)
	if err != nil || u.Free >= target {
		return 0, err
	}
	type entry struct {
		path string
		size uint64
		mod  time.Time
	}
	var entries []entry
	_ = filepath.WalkDir(cacheDir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		// The cache's own bookkeeping stays.
		if filepath.Dir(p) == cacheDir {
			return nil
		}
		if info, err := d.Info(); err == nil {
			entries = append(entries, entry{p, uint64(info.Size()), info.ModTime()})
		}
		return nil
	})
	sort.Slice(entries, func(i, j int) bool { return entries[i].mod.Before(entries[j].mod) })
	need := target - u.Free
	var freed uint64
	for _, e := range entries {
		if freed >= need {
			break
		}
		if os.Remove(e.path) == nil {
			freed += e.size
		}
	}
	return freed, nil
}

// ErrFull is a workspace with too little room for a task even after
// Prepare cleaned it. Its message starts "disk full", which the overseer
// reads as a failure not of the task's making.
type ErrFull struct{ Usage Usage }

func (e *ErrFull) Error() string {
	return fmt.Sprintf("disk full: %s on the workspace after cleaning; a task needs %d GB free", e.Usage, Floor/GB)
}

// Prepare makes room on workspace before a task: the earlier tasks'
// leftovers in tmpDir go; with less than MinFree free the Go build cache
// in cacheDir is trimmed to TrimTarget, and then any leftover older than a
// few minutes goes too. With less than Floor free after all that, it
// returns *ErrFull. It says what it did on out.
func Prepare(workspace, tmpDir, cacheDir string, out io.Writer) (Usage, error) {
	now := time.Now()
	if n, freed, _ := CleanTemp(tmpDir, now.Add(-LeftoverAge)); n > 0 {
		fmt.Fprintf(out, "Removed %d leftover temporary directories of earlier tasks (%.1f GB) from %s\n", n, float64(freed)/GB, tmpDir)
	}
	u, err := Of(workspace)
	if err != nil {
		return Usage{}, err
	}
	if u.Free >= MinFree {
		return u, nil
	}
	fmt.Fprintf(out, "::warning the workspace is low on space: %s\n", u)
	if cacheDir != "" {
		if freed, err := TrimCache(cacheDir, workspace, TrimTarget); err == nil && freed > 0 {
			fmt.Fprintf(out, "Trimmed the Go build cache by %.1f GB, least recently used first\n", float64(freed)/GB)
		}
	}
	if u, err = Of(workspace); err != nil {
		return Usage{}, err
	}
	if u.Free < MinFree {
		if n, freed, _ := CleanTemp(tmpDir, now.Add(-10*time.Minute)); n > 0 {
			fmt.Fprintf(out, "Removed %d more temporary directories (%.1f GB)\n", n, float64(freed)/GB)
		}
		if u, err = Of(workspace); err != nil {
			return Usage{}, err
		}
	}
	if u.Free < Floor {
		return u, &ErrFull{Usage: u}
	}
	return u, nil
}

// UsageFile is where a task records its workspace's space when it ends:
// the overseer copies it to the sandbox (Annotation), for the board and
// for sandboxes paused since.
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
