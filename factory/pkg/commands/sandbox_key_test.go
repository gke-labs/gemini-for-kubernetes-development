package commands

import (
	"context"
	"strings"
	"testing"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/clients"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/recipe"
	factorysandbox "github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/sandbox"
)

// --sandbox-key goes with a recipe on a repository of a keyed kind, and
// only with one; an agent's recipe runs through factory agent create.
func TestCheckSandboxKey(t *testing.T) {
	repo := githubItem{Owner: "o", Repo: "r"}
	issue := githubItem{Owner: "o", Repo: "r", Number: 7}
	kind := func(k string) *recipe.Recipe {
		return &recipe.Recipe{Name: "x", On: []string{recipe.OnRepo}, Sandbox: &recipe.SandboxDecl{Kind: k}}
	}
	for name, tc := range map[string]struct {
		rec  *recipe.Recipe
		it   githubItem
		opts recipeRunOpts
		key  string
		want string
	}{
		"research with a key":   {kind(recipe.SandboxResearch), repo, recipeRunOpts{}, "s1", ""},
		"research without":      {kind(recipe.SandboxResearch), repo, recipeRunOpts{}, "", "give it one with --sandbox-key"},
		"run without":           {kind(recipe.SandboxRun), repo, recipeRunOpts{}, "", "give it one with --sandbox-key"},
		"warm with a key":       {kind(recipe.SandboxWarm), repo, recipeRunOpts{}, "k", "drop --sandbox-key"},
		"warm without":          {kind(recipe.SandboxWarm), repo, recipeRunOpts{}, "", ""},
		"agent by recipe":       {kind(recipe.SandboxAgent), repo, recipeRunOpts{}, "", "factory agent create"},
		"agent by agent create": {kind(recipe.SandboxAgent), repo, recipeRunOpts{ensure: noEnsure}, "", ""},
		"issue with a key":      {&recipe.Recipe{Name: "fix", On: []string{recipe.OnIssue}}, issue, recipeRunOpts{}, "k", "for a recipe on a repository"},
		"issue without":         {&recipe.Recipe{Name: "fix", On: []string{recipe.OnIssue}}, issue, recipeRunOpts{}, "", ""},
	} {
		err := checkSandboxKey(tc.rec, tc.it, tc.opts, tc.key)
		if (tc.want == "") != (err == nil) || (err != nil && !strings.Contains(err.Error(), tc.want)) {
			t.Errorf("%s: %v, want %q", name, err, tc.want)
		}
	}
}

// One name for every keyed kind: research's is what it always was, and a
// run's is hashed like it; a recipe's prefix replaces the kind's.
func TestKeyedSandboxName(t *testing.T) {
	if got, want := factorysandbox.KeyedSandboxName("rsch", "k8s-config-connector", "s1"), factorysandbox.ResearchSandboxName("k8s-config-connector", "s1"); got != want {
		t.Errorf("research = %s, want %s", got, want)
	}
	run := factorysandbox.RunSandboxName("k8s-config-connector", "deploy-gke-k8s1")
	if !strings.HasPrefix(run, "run-k8s-config-connector-") || strings.Contains(run, "deploy") {
		t.Errorf("run = %s, want run-<repo>-<hash>", run)
	}
	if got := factorysandbox.KeyedSandboxName("kcc", "repo", "s1"); !strings.HasPrefix(got, "kcc-repo-") || len(got) > 60 {
		t.Errorf("prefixed = %s", got)
	}
	long := factorysandbox.KeyedSandboxName("rsch", strings.Repeat("very-long-repository-name-", 5), "s1")
	if len(long) > 60 {
		t.Errorf("long repo = %s (%d), want at most 60", long, len(long))
	}
}

// noEnsure stands for factory agent create's sandbox.
var noEnsure ensureSandbox = func(context.Context, *clients.KubernetesClient, string, string, string) (string, error) {
	return "agent-x", nil
}
