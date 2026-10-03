package commands

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/recipe"
)

func TestParseGitHubItemURL(t *testing.T) {
	for raw, want := range map[string]githubItem{
		"https://github.com/o/r/issues/12":         {Owner: "o", Repo: "r", Number: 12},
		"https://github.com/o/r/pull/34":           {Owner: "o", Repo: "r", Number: 34, IsPR: true},
		"https://github.com/o/r/pull/34/files":     {Owner: "o", Repo: "r", Number: 34, IsPR: true},
		"https://github.com/o/r/issues/12?foo=1#x": {Owner: "o", Repo: "r", Number: 12},
	} {
		got, err := parseGitHubItemURL(raw)
		if err != nil || got != want {
			t.Errorf("%s = %+v, %v; want %+v", raw, got, err, want)
		}
	}
	for _, raw := range []string{
		"https://github.com/o/r",
		"https://github.com/o/r/discussions/1",
		"https://github.com/o/r/issues/x",
		"https://example.com/o/r/issues/1",
	} {
		if _, err := parseGitHubItemURL(raw); err == nil {
			t.Errorf("%s parsed, want an error", raw)
		}
	}
}

func TestParseInputArgs(t *testing.T) {
	got, err := parseInputArgs([]string{"a=1", "b=x=y", "c="})
	if err != nil {
		t.Fatal(err)
	}
	if got["a"] != "1" || got["b"] != "x=y" || got["c"] != "" || len(got) != 3 {
		t.Errorf("inputs = %v", got)
	}
	if _, err := parseInputArgs([]string{"novalue"}); err == nil {
		t.Error("an input without = parsed")
	}
}

func TestLoadRecipe(t *testing.T) {
	if _, r, err := loadRecipe("triage"); err != nil || r.Name != "triage" {
		t.Errorf("built-in: %v, %v", r, err)
	}
	if _, _, err := loadRecipe("no-such-recipe"); err == nil {
		t.Error("unknown built-in loaded")
	}
	path := filepath.Join(t.TempDir(), "mine.yaml")
	if err := os.WriteFile(path, []byte("name: mine\nsteps: [{run: 'true'}]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, r, err := loadRecipe(path); err != nil || r.Name != "mine" {
		t.Errorf("file: %v, %v", r, err)
	}
}

// Every built-in recipe is a command of its own, `factory recipe <name>`,
// with a flag per input that shadows no other flag.
func TestBuiltinRecipeCommands(t *testing.T) {
	root := NewRootCommand(context.Background())
	recipeCmd, _, err := root.Find([]string{"recipe"})
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range recipe.BuiltinNames() {
		if name == "run" || name == "exec" {
			t.Errorf("built-in recipe %q takes the name of a recipe command", name)
			continue
		}
		cmd, _, err := recipeCmd.Find([]string{name})
		if err != nil || cmd.Name() != name {
			t.Errorf("factory recipe %s: no such command (%v)", name, err)
			continue
		}
		_, rec, err := recipe.Builtin(name)
		if err != nil {
			t.Fatal(err)
		}
		for in, decl := range rec.Inputs {
			flag := inputFlagName(in, decl)
			if root.PersistentFlags().Lookup(flag) != nil || flag == "url" || flag == "input" || flag == "client-id" {
				t.Errorf("recipe %s: input %s's flag --%s is taken", name, in, flag)
			}
			if cmd.Flags().Lookup(flag) == nil {
				t.Errorf("factory recipe %s has no --%s", name, flag)
			}
		}
	}
	if len(recipe.BuiltinNames()) == 0 {
		t.Error("no built-in recipes")
	}
}
