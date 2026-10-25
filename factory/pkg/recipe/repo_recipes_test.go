package recipe

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestRepoPlanRecipe pins that this repository's plan recipe
// (.agents/recipes/plan.yaml), with its Breakdown instructions, parses,
// validates and renders.
func TestRepoPlanRecipe(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "..", ".agents", "recipes", "plan.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	rec, err := Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	if err := rec.Validate(); err != nil {
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
}
