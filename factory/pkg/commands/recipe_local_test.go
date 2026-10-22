package commands

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// git runs git in dir and returns its trimmed output.
func gitIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// The overseer's clone answers for the repository's recipes from its git
// objects: latest at upstream/HEAD as last fetched; a pinned commit when
// it has it, else the API; never the working tree, and never written. A
// file the commit lacks is os.ErrNotExist, and a directory that is no
// clone leaves it to the API.
func TestLocalRepoFile(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git")
	}
	ctx := context.Background()
	root := t.TempDir()
	up, work, clone := filepath.Join(root, "up.git"), filepath.Join(root, "work"), filepath.Join(root, "clone")
	gitIn(t, root, "init", "-q", "--bare", "-b", "main", up)
	gitIn(t, root, "clone", "-q", up, work)
	commit := func(content string) string {
		if err := os.MkdirAll(filepath.Join(work, ".agents/recipes"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(work, ".agents/recipes/fix.yaml"), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		gitIn(t, work, "add", "-A")
		gitIn(t, work, "commit", "-q", "-m", content)
		gitIn(t, work, "push", "-q", "origin", "HEAD:main")
		return gitIn(t, work, "rev-parse", "HEAD")
	}
	first := commit("v1")
	gitIn(t, root, "clone", "-q", up, clone)
	gitIn(t, clone, "remote", "add", "upstream", up)
	gitIn(t, clone, "fetch", "-q", "upstream")
	gitIn(t, clone, "remote", "set-head", "upstream", "main")
	// The working tree is the overseer's state branch: never read.
	if err := os.WriteFile(filepath.Join(clone, ".agents/recipes/fix.yaml"), []byte("dirty"), 0o644); err != nil {
		t.Fatal(err)
	}
	apiCalls := 0
	api := func(context.Context, string) ([]byte, error) { apiCalls++; return []byte("api"), nil }
	read := func(commit, path string) (string, error) {
		b, err := localRepoFile(clone, commit, api)(ctx, path)
		return string(b), err
	}

	if got, err := read("latest", ".agents/recipes/fix.yaml"); err != nil || got != "v1" {
		t.Errorf("latest = %q, %v; want v1, not the working tree", got, err)
	}
	if _, err := read("latest", ".agents/recipes/none.yaml"); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("missing file: %v", err)
	}

	// A merge upstream: latest stays at what the clone last fetched (the
	// watch's job, not a task's), until it fetches.
	second := commit("v2")
	if got, _ := read("latest", ".agents/recipes/fix.yaml"); got != "v1" {
		t.Errorf("latest before a fetch = %q, want v1", got)
	}
	gitIn(t, clone, "fetch", "-q", "upstream")
	if got, _ := read("latest", ".agents/recipes/fix.yaml"); got != "v2" {
		t.Errorf("latest after a fetch = %q, want v2", got)
	}

	// A pinned commit: an older one is read as it was; one the clone
	// lacks is fetched.
	if got, _ := read(first, ".agents/recipes/fix.yaml"); got != "v1" {
		t.Errorf("pinned %s = %q, want v1", first[:7], got)
	}
	_ = second
	if apiCalls != 0 {
		t.Errorf("the API was asked %d times", apiCalls)
	}

	// A pinned commit the clone lacks is the API's; the clone is not
	// fetched into.
	third := commit("v3")
	before := gitIn(t, clone, "for-each-ref")
	if got, _ := read(third, ".agents/recipes/fix.yaml"); got != "api" || apiCalls != 1 {
		t.Errorf("pinned %s, not in the clone = %q, %d API calls; want the API's", third[:7], got, apiCalls)
	}
	if gitIn(t, clone, "for-each-ref") != before {
		t.Error("reading changed the clone's refs")
	}
	if got, _ := localRepoFile(t.TempDir(), "latest", api)(ctx, ".agents/recipes/fix.yaml"); string(got) != "api" || apiCalls != 2 {
		t.Errorf("not a clone: %q, %d API calls; want the API's", got, apiCalls)
	}
}
