package diskcare

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeDisk is a workspace with size bytes whose free space free() says.
func fakeDisk(t *testing.T, size uint64, free func() uint64) {
	t.Helper()
	old := Of
	Of = func(string) (Usage, error) {
		f := free()
		return Usage{Used: size - f, Size: size, Free: f}, nil
	}
	t.Cleanup(func() { Of = old })
}

func repoWithCleanup(t *testing.T, yaml string) string {
	t.Helper()
	repo := t.TempDir()
	if yaml != "" {
		p := filepath.Join(repo, CleanupPath)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(yaml), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return repo
}

// The repository's cleanup recipe when it has one, else the built-in, which
// deletes nothing; a cleanup recipe runs commands only.
func TestLoadCleanup(t *testing.T) {
	if _, source, err := LoadCleanup(repoWithCleanup(t, "")); err != nil || source != "built-in" {
		t.Errorf("no repository recipe: %q, %v", source, err)
	}
	if _, source, err := LoadCleanup(repoWithCleanup(t, "name: workspace-cleanup\nstart:\n  steps:\n    - run: echo hi\n")); err != nil || source != CleanupPath {
		t.Errorf("the repository's: %q, %v", source, err)
	}
	if _, _, err := LoadCleanup(repoWithCleanup(t, "name: workspace-cleanup\nstart:\n  steps:\n    - ask: delete what you like\n")); err == nil || !strings.Contains(err.Error(), "runs commands only") {
		t.Errorf("an ask step: %v", err)
	}
}

// Prepare: room enough, no cleanup; low, the repository's cleanup runs with
// the workspace's state and the task starts; still too low, disk full.
func TestPrepare(t *testing.T) {
	ctx := context.Background()
	t.Run("room enough: no cleanup", func(t *testing.T) {
		fakeDisk(t, 60*GB, func() uint64 { return 30 * GB })
		repo := repoWithCleanup(t, "name: workspace-cleanup\nstart:\n  steps:\n    - run: touch ran\n")
		var out bytes.Buffer
		if _, err := Prepare(ctx, "/ws", repo, t.TempDir(), &out); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(filepath.Join(repo, "ran")); err == nil {
			t.Error("the cleanup ran with room enough")
		}
	})
	t.Run("low: the repository's cleanup runs, told the target", func(t *testing.T) {
		repo := repoWithCleanup(t, "name: workspace-cleanup\nstart:\n  steps:\n    - run: echo \"$WORKSPACE $TARGET_BYTES\" > ran\n")
		fakeDisk(t, 60*GB, func() uint64 {
			if _, err := os.Stat(filepath.Join(repo, "ran")); err == nil {
				return 25 * GB // it made room
			}
			return 3 * GB
		})
		var out bytes.Buffer
		u, err := Prepare(ctx, "/ws", repo, t.TempDir(), &out)
		if err != nil || u.Free != 25*GB {
			t.Fatalf("err %v, free %d:\n%s", err, u.Free, out.String())
		}
		got, _ := os.ReadFile(filepath.Join(repo, "ran"))
		if strings.TrimSpace(string(got)) != "/ws 21474836480" || !strings.Contains(out.String(), "Running workspace-cleanup ("+CleanupPath+")") {
			t.Errorf("ran with %q:\n%s", got, out.String())
		}
	})
	t.Run("still too low: disk full", func(t *testing.T) {
		fakeDisk(t, 40*GB, func() uint64 { return 2 * GB })
		var out bytes.Buffer
		_, err := Prepare(ctx, "/ws", repoWithCleanup(t, ""), t.TempDir(), &out)
		var full *ErrFull
		if !errors.As(err, &full) || !strings.HasPrefix(err.Error(), "disk full: ") || !strings.Contains(out.String(), "nothing deleted") {
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
