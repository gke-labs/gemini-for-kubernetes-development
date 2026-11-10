package recipe

import (
	"strings"
	"testing"
)

// A merger recipe runs on pull requests, with its full token, and asks
// nothing; it runs in a sandbox of its own.
func TestMergerRecipe(t *testing.T) {
	ok := "name: m\non: [pr]\nas: merger\nstart:\n  steps:\n    - uses: setup-git\n    - run: gh pr merge 1 --auto\n"
	r, err := Parse([]byte(ok))
	if err != nil {
		t.Fatal(err)
	}
	if !r.OwnSandbox() {
		t.Error("a merger recipe does not run in a sandbox of its own")
	}
	for name, bad := range map[string]string{
		"asks":      "name: m\non: [pr]\nas: merger\nstart:\n  steps:\n    - uses: setup-git\n    - ask: merge it\n",
		"on issues": "name: m\non: [issue]\nas: merger\nstart:\n  steps:\n    - run: true\n",
		"no on":     "name: m\nas: merger\nstart:\n  steps:\n    - run: true\n",
		"clone":     "name: m\non: [pr]\nas: merger\ncredentials: clone\nstart:\n  steps:\n    - uses: clone\n    - run: true\n",
	} {
		if _, err := Parse([]byte(bad)); err == nil || !strings.Contains(err.Error(), "merger") {
			t.Errorf("%s: err = %v, want a merger error", name, err)
		}
	}
}

func TestOwnSandbox(t *testing.T) {
	for _, c := range []struct {
		r    Recipe
		want bool
	}{
		{Recipe{As: AsCoder}, false},
		{Recipe{As: AsReviewer, Credentials: CredentialsClone}, true},
		{Recipe{As: AsMerger}, true},
	} {
		if got := c.r.OwnSandbox(); got != c.want {
			t.Errorf("%+v: OwnSandbox = %v", c.r, got)
		}
	}
}

// The built-in merge recipe is a merger's: on PRs, asking nothing.
func TestBuiltinMergeIsAMerger(t *testing.T) {
	_, r, err := Builtin("merge")
	if err != nil {
		t.Fatal(err)
	}
	if r.As != AsMerger || !r.OwnSandbox() {
		t.Errorf("merge: as %q, own sandbox %v", r.As, r.OwnSandbox())
	}
	for _, s := range r.Start.Steps {
		if s.Ask != "" {
			t.Error("the built-in merge recipe asks")
		}
	}
}
