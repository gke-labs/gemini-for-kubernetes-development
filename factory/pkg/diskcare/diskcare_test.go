package diskcare

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/recipe"
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
// deletes nothing; a cleanup recipe runs no agent.
func TestLoadCleanup(t *testing.T) {
	if _, source, err := LoadCleanup(repoWithCleanup(t, "")); err != nil || source != "built-in" {
		t.Errorf("no repository recipe: %q, %v", source, err)
	}
	if _, source, err := LoadCleanup(repoWithCleanup(t, "name: workspace-cleanup\nstart:\n  steps:\n    - run: echo hi\n")); err != nil || source != CleanupPath {
		t.Errorf("the repository's: %q, %v", source, err)
	}
	if _, _, err := LoadCleanup(repoWithCleanup(t, "name: workspace-cleanup\nstart:\n  steps:\n    - ask: delete what you like\n")); err == nil || !strings.Contains(err.Error(), "runs no agent") {
		t.Errorf("an ask step: %v", err)
	}
}

// runSteps is a Cleanup for tests: the recipe's run: steps with bash in
// repo, its inputs as INPUT_<NAME>, as factory's recipe runner gives them.
func runSteps(repo string) Cleanup {
	return func(ctx context.Context, rec *recipe.Recipe, inputs map[string]string) error {
		for _, st := range rec.Start.Steps {
			cmd := exec.CommandContext(ctx, "bash", "-c", st.Run)
			cmd.Dir = repo
			cmd.Env = os.Environ()
			for k, v := range inputs {
				cmd.Env = append(cmd.Env, "INPUT_"+strings.ToUpper(k)+"="+v)
			}
			if err := cmd.Run(); err != nil {
				return err
			}
		}
		return nil
	}
}

// Prepare: room enough, no cleanup; low, the repository's cleanup runs with
// the workspace's state as its inputs and the task starts; still too low,
// disk full.
func TestPrepare(t *testing.T) {
	ctx := context.Background()
	t.Run("room enough: no cleanup", func(t *testing.T) {
		fakeDisk(t, 60*GB, func() uint64 { return 30 * GB })
		called := false
		if _, err := Prepare(ctx, "/ws", repoWithCleanup(t, ""), func(context.Context, *recipe.Recipe, map[string]string) error { called = true; return nil }, &bytes.Buffer{}); err != nil || called {
			t.Errorf("err %v, cleanup ran %v", err, called)
		}
	})
	t.Run("low: the repository's cleanup runs, told the target", func(t *testing.T) {
		repo := repoWithCleanup(t, "name: workspace-cleanup\nstart:\n  steps:\n    - run: echo \"$INPUT_WORKSPACE $INPUT_FREE_BYTES $INPUT_TARGET_BYTES\" > ran\n")
		fakeDisk(t, 60*GB, func() uint64 {
			if _, err := os.Stat(filepath.Join(repo, "ran")); err == nil {
				return 25 * GB // it made room
			}
			return 3 * GB
		})
		var out bytes.Buffer
		u, err := Prepare(ctx, "/ws", repo, runSteps(repo), &out)
		if err != nil || u.Free != 25*GB {
			t.Fatalf("err %v, free %d:\n%s", err, u.Free, out.String())
		}
		got, _ := os.ReadFile(filepath.Join(repo, "ran"))
		if strings.TrimSpace(string(got)) != "/ws 3221225472 21474836480" || !strings.Contains(out.String(), "Running workspace-cleanup ("+CleanupPath+")") {
			t.Errorf("ran with %q:\n%s", got, out.String())
		}
	})
	t.Run("the cleanup fails: reported, the floor decides", func(t *testing.T) {
		fakeDisk(t, 60*GB, func() uint64 { return 8 * GB })
		var out bytes.Buffer
		if _, err := Prepare(ctx, "/ws", repoWithCleanup(t, ""), func(context.Context, *recipe.Recipe, map[string]string) error { return errors.New("step 1 failed") }, &out); err != nil || !strings.Contains(out.String(), "step 1 failed") {
			t.Errorf("err %v:\n%s", err, out.String())
		}
	})
	t.Run("still too low: disk full", func(t *testing.T) {
		fakeDisk(t, 40*GB, func() uint64 { return 2 * GB })
		repo := repoWithCleanup(t, "")
		var out bytes.Buffer
		_, err := Prepare(ctx, "/ws", repo, runSteps(repo), &out)
		var full *ErrFull
		if !errors.As(err, &full) || !strings.HasPrefix(err.Error(), "disk full: ") {
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
