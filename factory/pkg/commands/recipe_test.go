package commands

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/recipe"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/spool"
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

// --apply picks up the newest run of the recipe on the issue, whatever
// ran after it on other issues or with other recipes.
func TestLastRun(t *testing.T) {
	entry := func(id, recipeName, url string) spool.Entry {
		return spool.Entry{Task: spool.Task{ID: id, Recipe: recipeName, URL: url}}
	}
	entries := []spool.Entry{ // newest first, as spool.List gives them
		entry("recipe-triage-3", "triage", "https://github.com/o/r/issues/8"),
		entry("recipe-explain-2", "explain", "https://github.com/o/r/issues/7"),
		entry("fix-2", "", ""),
		entry("recipe-triage-2", "triage", "https://github.com/O/r/issues/7/"),
		entry("recipe-triage-1", "triage", "https://github.com/o/r/issues/7"),
	}
	if e, ok := lastRun(entries, "triage", "https://github.com/o/r/issues/7"); !ok || e.ID != "recipe-triage-2" {
		t.Errorf("lastRun = %s, %v; want recipe-triage-2", e.ID, ok)
	}
	if e, ok := lastRun(entries, "triage", "https://github.com/o/r/issues/9"); ok {
		t.Errorf("lastRun on an issue never run = %s; want none", e.ID)
	}
}

func TestRunByClientID(t *testing.T) {
	entry := func(id, clientID, recipeName, url string) spool.Entry {
		return spool.Entry{Task: spool.Task{ID: id, ClientID: clientID, Recipe: recipeName, URL: url}}
	}
	entries := []spool.Entry{ // newest first
		entry("recipe-triage-3", "auto/b/7/2", "triage", "https://github.com/o/r/issues/7"),
		entry("recipe-explain-2", "request/x", "explain", "https://github.com/o/r/issues/7"),
		entry("recipe-triage-1", "request/y", "triage", "https://github.com/O/r/issues/7/"),
	}
	if e, ok, err := runByClientID(entries, "request/y", "triage", "https://github.com/o/r/issues/7"); err != nil || !ok || e.ID != "recipe-triage-1" {
		t.Errorf("runByClientID(request/y) = %s, %v, %v; want recipe-triage-1", e.ID, ok, err)
	}
	if e, ok, err := runByClientID(entries, "request/z", "triage", "https://github.com/o/r/issues/7"); err != nil || ok {
		t.Errorf("runByClientID of an unused id = %s, %v, %v; want none", e.ID, ok, err)
	}
	if _, ok, err := runByClientID(entries, "request/x", "triage", "https://github.com/o/r/issues/7"); err == nil || ok {
		t.Errorf("runByClientID of another recipe's id = %v, %v; want an error", ok, err)
	}
	if _, ok, err := runByClientID(entries, "request/y", "triage", "https://github.com/o/r/issues/8"); err == nil || ok {
		t.Errorf("runByClientID of another issue's id = %v, %v; want an error", ok, err)
	}
}
