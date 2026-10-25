package recipe

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// This repository's .agents/recipes are verbatim copies of factory's
// built-in recipes (a repository in replace mode runs them in their
// place): a copy of a built-in must not drift from it.
func TestRepoRecipesAreBuiltinCopies(t *testing.T) {
	dir := filepath.Join("..", "..", "..", ".agents", "recipes")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		builtin, err := builtinFS.ReadFile("recipes/" + e.Name())
		if err != nil {
			continue // the repository's own
		}
		copied, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(copied, builtin) {
			t.Errorf(".agents/recipes/%s differs from the built-in pkg/recipe/recipes/%s", e.Name(), e.Name())
		}
	}
}

// The built-in plan recipe, with its Breakdown instructions, renders.
func TestBuiltinPlanRecipe(t *testing.T) {
	_, rec, err := Builtin("plan")
	if err != nil {
		t.Fatal(err)
	}
	std := map[string]string{"issue_url": "https://github.com/o/r/issues/7", "issue_title": "t", "issue_body": "b", "issue_number": "7", "repo_owner": "o", "repo_name": "r", "disclose": "true"}
	inputs, err := rec.ResolveInputs(std, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := rec.CheckRender(inputs); err != nil {
		t.Fatal(err)
	}
	var breakdown bool
	for _, s := range rec.Start.Steps {
		breakdown = breakdown || strings.Contains(s.Ask, "```yaml graph")
	}
	if !breakdown {
		t.Error("the plan recipe asks for no `yaml graph` Breakdown")
	}
	if d := rec.OutputDecl(); d == nil || d.Comment == nil || d.Comment.Key != "plan" {
		t.Errorf("task output %+v: want comment key plan", d)
	}
}
