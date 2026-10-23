package commands

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/watch/rules"
)

// A repository's watch rules are laid over factory's as the repository
// recipe mode says: replace turns a factory rule off, replaces it, or adds
// one of a new name; augment adds new names only; off leaves factory's. A
// rule may run the repository's own recipe; one whose recipe is neither
// factory's nor the repository's is left out.
func TestComposeRepoWatchRules(t *testing.T) {
	base, err := rules.Builtin()
	if err != nil {
		t.Fatal(err)
	}
	ci, _ := os.ReadFile("../watch/rules/ci.yaml")
	files := map[string][]byte{
		"review.yaml":  []byte("name: review\nenabled: false\n"),
		"ci.yaml":      []byte(strings.Replace(string(ci), "recipe: care-ci", "recipe: care", 1)),
		"new-one.yaml": []byte("name: new-one\non: pr\norder: 90\nwhen: {checks: green}\nrun: {recipe: deploy-preview}\n"),
		"typo.yaml":    []byte("name: typo\non: pr\norder: 91\nwhen: {checks: green}\nrun: {recipe: deploy-previw}\n"),
		"README.md":    []byte("not a rule"),
	}
	recipes := []string{"deploy-preview"}
	set, skipped, n, err := composeRepoWatchRules(base, files, recipes, "replace")
	if err != nil {
		t.Fatal(err)
	}
	if n != 4 || set.Named("new-one") == nil || set.Named("typo") != nil || len(skipped) != 1 || !strings.Contains(skipped[0], "typo: runs recipe deploy-previw") {
		t.Errorf("replace: %d rules, new-one %v, typo %v, skipped %v", n, set.Named("new-one"), set.Named("typo"), skipped)
	}
	if r := set.Named("review"); r == nil || r.IsEnabled() || r.EnabledBy != rules.SourceRepo {
		t.Errorf("replace: review = %+v, want off by the repository", r)
	}
	if r := set.Named("ci"); r == nil || r.Source != rules.SourceRepo || r.Run.Recipe != "care" {
		t.Errorf("replace: ci = %+v, want the repository's", r)
	}

	for mode, added := range map[string]bool{"augment": true, "off": false} {
		set, _, _, err := composeRepoWatchRules(base, files, recipes, mode)
		if err != nil {
			t.Fatal(err)
		}
		if r := set.Named("review"); r == nil || !r.IsEnabled() || set.Named("ci").Run.Recipe != "care-ci" {
			t.Errorf("%s changed factory's rules", mode)
		}
		if (set.Named("new-one") != nil) != added {
			t.Errorf("%s: new-one added %v, want %v", mode, set.Named("new-one") != nil, added)
		}
	}
	if _, _, _, err := composeRepoWatchRules(base, map[string][]byte{"ci.yaml": []byte("name: other\n")}, nil, "replace"); err == nil || !strings.Contains(err.Error(), "named after it") {
		t.Errorf("a misnamed file: %v", err)
	}
}

// The overseer's clone answers for .agents/watch from its git objects at
// the pinned commit, never its working tree.
func TestLocalDirFiles(t *testing.T) {
	ctx := context.Background()
	clone := t.TempDir()
	gitIn(t, clone, "init", "-q")
	dir := filepath.Join(clone, ".agents", "watch")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "review.yaml"), []byte("name: review\nenabled: false\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitIn(t, clone, "add", ".")
	gitIn(t, clone, "commit", "-q", "-m", "rules")
	rev := gitIn(t, clone, "rev-parse", "HEAD")
	if err := os.WriteFile(filepath.Join(dir, "review.yaml"), []byte("dirty"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv(EnvRepoRecipeDir, clone)
	t.Setenv(EnvRepoRecipeCommit, rev)
	files, got, err := repoDirFiles(ctx, nil, "o", "r", repoWatchDir)
	if err != nil || got != rev || string(files["review.yaml"]) != "name: review\nenabled: false\n" || len(files) != 1 {
		t.Errorf("files %q at %s, %v; want review.yaml at %s", files, got, err, rev)
	}
	none, err := localDirFiles(ctx, clone, rev, ".agents/missing")
	if err != nil || len(none) != 0 {
		t.Errorf("a missing directory = %v, %v", none, err)
	}
}

// A repository's recipe says who runs it: augment lets in author or coder
// for names of its own; replace any role, a built-in's name included. A
// recipe that says nothing, or names a pool augment does not allow, is
// left out, and its tasks run as factory's say.
func TestRecipeAsOf(t *testing.T) {
	files := map[string][]byte{
		"deploy-preview.yaml": []byte("name: deploy-preview\non: [pr]\nas: author\nstart: {steps: [{ask: a}]}\n"),
		"lint.yaml":           []byte("name: lint\non: [issue]\nas: coder\nstart: {steps: [{ask: a}]}\n"),
		"approve.yaml":        []byte("name: approve\non: [pr]\nas: reviewer\nstart: {steps: [{ask: a}]}\n"),
		"silent.yaml":         []byte("name: silent\non: [pr]\nstart: {steps: [{ask: a}]}\n"),
		"review.yaml":         []byte("name: review\non: [pr]\nas: coder\nstart: {steps: [{ask: a}]}\n"),
	}
	got := recipeAsOf(files, "augment", "abc")
	if len(got) != 2 || got["deploy-preview"] != "author" || got["lint"] != "coder" {
		t.Errorf("augment = %v, want deploy-preview author and lint coder only", got)
	}
	got = recipeAsOf(files, "replace", "abc")
	if got["approve"] != "reviewer" || got["review"] != "coder" || len(got) != 4 {
		t.Errorf("replace = %v, want every recipe that says, reviewer and a built-in's name included", got)
	}
}
