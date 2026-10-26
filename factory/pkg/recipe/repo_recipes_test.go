package recipe

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/taskoutput"
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
	var all string
	for _, s := range rec.Start.Steps {
		all += s.Ask
	}
	for _, want := range []string{"## Questions", "<!-- factory:question id=", "- [ ] Other: answered in a comment", "Never add a separate step that only asks", "## Baseline", "## PR plan", "### Dependency graph", "| Step | Title | What it adds | Files | After | Verification |", "<details>", "</details>"} {
		if !strings.Contains(all, want) {
			t.Errorf("the plan recipe does not ask for %q", want)
		}
	}
	if d := rec.OutputDecl(); d == nil || d.Comment == nil || d.Comment.Key != "plan" {
		t.Errorf("task output %+v: want comment key plan", d)
	}
}

// TestBuiltinFixWithPlanOpenQuestions pins fix-with-plan's guard: a plan
// whose ## Questions section has a question with no box ticked runs no
// agent and pushes nothing; its Change is a comment naming the open
// questions. A plan with every question answered, or with none, is
// implemented as before.
func TestBuiltinFixWithPlanOpenQuestions(t *testing.T) {
	_, rec, err := Builtin("fix-with-plan")
	if err != nil {
		t.Fatal(err)
	}
	var guard string
	for _, s := range rec.Start.Steps {
		if strings.Contains(s.Run, "plan-open") {
			guard = s.Run
		}
	}
	if guard == "" {
		t.Fatal("no step writes plan-open")
	}
	questions := func(retention, access string) string {
		return "## Summary\nDo it.\n\n## Questions\n\n**How long are task folders kept?**\n" +
			"- [" + retention + "] 180 days (recommended)\n- [ ] Indefinitely\n- [ ] Other: answered in a comment\n\n" +
			"**How do members read traces?**\n- [" + access + "] Collector endpoint\n- [ ] Signed URLs\n\n## Approach\nThe way.\n"
	}
	for _, tc := range []struct {
		name, plan string
		open       []string // the open questions the comment names; none: implemented
	}{
		{"one open", questions("x", " "), []string{"How do members read traces?"}},
		{"both open", questions(" ", " "), []string{"How long are task folders kept?", "How do members read traces?"}},
		{"answered", questions("x", "X"), nil},
		{"no questions", "## Summary\nDo it.\n\n## Approach\nThe way.\n", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			taskDir := t.TempDir()
			if err := os.WriteFile(filepath.Join(taskDir, "plan.md"), []byte(tc.plan), 0o644); err != nil {
				t.Fatal(err)
			}
			e := &SandboxExecutor{TaskDir: taskDir, RepoDir: t.TempDir(), Env: []string{"PATH=" + os.Getenv("PATH")}}
			var out strings.Builder
			if code, err := e.Run(context.Background(), guard, nil, &out); err != nil || code != 0 {
				t.Fatalf("the guard exited %d, %v\n%s", code, err, out.String())
			}
			td := templateData{Inputs: map[string]string{}, Steps: map[string]*StepResult{}}
			var ran []string
			for i, s := range rec.Start.Steps {
				if s.If == "" {
					continue
				}
				ok, err := condition(fmt.Sprint(i), s.If, td, taskDir)
				if err != nil {
					t.Fatal(err)
				}
				if ok {
					ran = append(ran, map[bool]string{true: "ask", false: s.Uses}[s.Ask != ""])
				}
			}
			change, _ := os.ReadFile(filepath.Join(taskDir, "change.yaml"))
			if len(tc.open) == 0 {
				if strings.Join(ran, " ") != "ask ask push" || len(change) != 0 {
					t.Errorf("ran %v, change.yaml %q; want the fix as before", ran, change)
				}
				return
			}
			if len(ran) != 0 {
				t.Errorf("ran %v with open questions", ran)
			}
			for _, q := range tc.open {
				if !strings.Contains(string(change), q) {
					t.Errorf("change.yaml lacks %q:\n%s", q, change)
				}
			}
			if len(tc.open) == 1 && strings.Contains(string(change), "kept?") {
				t.Errorf("change.yaml names the answered question:\n%s", change)
			}
			doc, err := taskoutput.Wrap("Change", string(change), taskoutput.Target{URL: "https://github.com/o/r/issues/7"}, taskoutput.Source{})
			if err != nil {
				t.Fatalf("change.yaml does not read as a Change: %v\n%s", err, change)
			}
			if c, err := doc.ChangeSpec(); err != nil || !strings.Contains(c.Comment, "open questions") {
				t.Errorf("Change %+v, %v; want a comment", c, err)
			}
		})
	}
}
