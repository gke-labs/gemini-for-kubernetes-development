package rules

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// This repository's .agents/watch is a verbatim copy of factory's built-in
// rules (an overseer in replace mode runs it in their place): a copy of a
// built-in must not drift from it.
func TestRepoWatchRulesAreBuiltinCopies(t *testing.T) {
	dir := filepath.Join("..", "..", "..", "..", ".agents", "watch")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		builtin, err := builtinFS.ReadFile(e.Name())
		if err != nil {
			continue // the repository's own
		}
		copied, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(copied, builtin) {
			t.Errorf(".agents/watch/%s differs from the built-in pkg/watch/rules/%s", e.Name(), e.Name())
		}
	}
}
