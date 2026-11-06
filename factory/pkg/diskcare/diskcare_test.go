package diskcare

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func write(t *testing.T, path string, size int, mod time.Time) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, make([]byte, size), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, mod, mod); err != nil {
		t.Fatal(err)
	}
}

// fakeDisk is a workspace whose free space is base plus what was removed
// from the directories watched.
func fakeDisk(t *testing.T, size, free uint64, dirs ...string) {
	t.Helper()
	start := map[string]uint64{}
	for _, d := range dirs {
		start[d] = treeSize(d)
	}
	old := Of
	Of = func(string) (Usage, error) {
		f := free
		for _, d := range dirs {
			f += start[d] - treeSize(d)
		}
		return Usage{Used: size - f, Size: size, Free: f}, nil
	}
	t.Cleanup(func() { Of = old })
}

// Earlier tasks' temporary builds go; what is newer (a build running now)
// and what is not a leftover stay.
func TestCleanTempRemovesOldLeftoversOnly(t *testing.T) {
	dir := t.TempDir()
	old, recent := time.Now().Add(-3*time.Hour), time.Now()
	write(t, filepath.Join(dir, "go-build123", "a"), 100, old)
	write(t, filepath.Join(dir, "k8s_test_framework_9", "b"), 50, old)
	write(t, filepath.Join(dir, "go-build456", "c"), 10, recent)
	write(t, filepath.Join(dir, "notes.txt"), 10, old)
	for _, d := range []string{"go-build123", "k8s_test_framework_9"} {
		_ = os.Chtimes(filepath.Join(dir, d), old, old)
	}
	n, freed, err := CleanTemp(dir, time.Now().Add(-LeftoverAge))
	if err != nil || n != 2 || freed != 150 {
		t.Errorf("CleanTemp = %d, %d, %v; want 2, 150", n, freed, err)
	}
	for name, kept := range map[string]bool{"go-build123": false, "k8s_test_framework_9": false, "go-build456": true, "notes.txt": true} {
		if _, err := os.Stat(filepath.Join(dir, name)); (err == nil) != kept {
			t.Errorf("%s kept %v, want %v", name, err == nil, kept)
		}
	}
}

// Trimming takes the least recently used entries first, and stops once
// the target is free; the cache's own files stay.
func TestTrimCacheLeastRecentlyUsedFirst(t *testing.T) {
	cache := t.TempDir()
	now := time.Now()
	write(t, filepath.Join(cache, "00", "oldest-a"), 400, now.Add(-72*time.Hour))
	write(t, filepath.Join(cache, "01", "older-a"), 400, now.Add(-48*time.Hour))
	write(t, filepath.Join(cache, "02", "newest-a"), 400, now.Add(-time.Hour))
	write(t, filepath.Join(cache, "trim.txt"), 1, now.Add(-100*time.Hour))
	fakeDisk(t, 10000, 100, cache)
	freed, err := TrimCache(cache, "/ws", 700)
	if err != nil || freed != 800 {
		t.Errorf("TrimCache = %d, %v; want 800 (the two oldest)", freed, err)
	}
	for name, kept := range map[string]bool{"00/oldest-a": false, "01/older-a": false, "02/newest-a": true, "trim.txt": true} {
		if _, err := os.Stat(filepath.Join(cache, name)); (err == nil) != kept {
			t.Errorf("%s kept %v, want %v", name, err == nil, kept)
		}
	}
}

// Prepare: plenty of room, nothing beyond the leftovers; low, the cache is
// trimmed; still too low, the task does not start (disk full).
func TestPrepare(t *testing.T) {
	t.Run("room enough", func(t *testing.T) {
		fakeDisk(t, 60*GB, 30*GB)
		var out bytes.Buffer
		if _, err := Prepare("/ws", t.TempDir(), t.TempDir(), &out); err != nil || strings.Contains(out.String(), "Trimmed") {
			t.Errorf("err %v:\n%s", err, out.String())
		}
	})
	t.Run("too little even after cleaning: disk full", func(t *testing.T) {
		fakeDisk(t, 40*GB, 2*GB)
		var out bytes.Buffer
		_, err := Prepare("/ws", t.TempDir(), t.TempDir(), &out)
		var full *ErrFull
		if !errors.As(err, &full) || !strings.HasPrefix(err.Error(), "disk full: ") || !strings.Contains(out.String(), "low on space") {
			t.Errorf("err %v:\n%s", err, out.String())
		}
	})
}

func TestRecord(t *testing.T) {
	dir := t.TempDir()
	if err := Record(dir, Usage{Used: 1, Size: 2, Free: 1}); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(dir, UsageFile))
	if !strings.Contains(string(b), `"size":2`) {
		t.Errorf("%s", b)
	}
}
